[CmdletBinding()]
param(
    [switch] $LocalRdp,
    [switch] $ValidateOnly,
    [string] $RdpProfileTemplatePath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$operatorTempRoot = [System.IO.Path]::GetTempPath()
$tempRoot = if (-not [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) {
    $env:RUNNER_TEMP
} else {
    # The interactive operator's profile temp directory is not necessarily
    # traversable by the disposable managed user that runs the smoke. Use the
    # machine temp directory for the shared runtime/data tree instead.
    Join-Path $env:windir 'Temp'
}
$runID = [Guid]::NewGuid().ToString('N')
$runRoot = Join-Path $tempRoot ('chuzi-job-pool-smoke-' + $runID)
$userPrefix = 'Cz' + $runID.Substring(0, 9)
$currentUserPattern = '^' + [regex]::Escape($userPrefix) + '0001$'
$smokeUserPattern = '^Cz[0-9a-f]{9}0001$'
$marker = 'CHUZI-MANAGED:smoke-001:1'
$ownershipMarker = 'CHUZI-SMOKE-OWNERSHIP:' + $runID
$userOwnershipMarker = 'CHUZI-SMOKE-USER-OWNERSHIP:' + $runID + ':' + $userPrefix
$cleanupErrors = [System.Collections.Generic.List[string]]::new()
$smokePassed = $false
$script:rootPreserved = $false
$script:unownedSmokeUsers = 0
$script:remainingSmokeUsers = -1
$script:rdpCredentialOwned = $false
$script:rdpCredentialTarget = $null
$script:rdpCredentialUserName = $null
$script:rdpProfilePath = $null
$script:rdpDebugSummary = @()
$script:rdpClientProcess = $null
$script:nativeSmokeTaskName = $null
$script:runnerUserName = $null
$script:runnerSessionID = -1
$script:runnerSID = $null
$script:failureDetail = $null
$script:rdpDiagnosticsPath = $null
$script:rdpFailurePhase = 'not_started'
$script:rdpFailureExceptionType = 'unavailable'
$script:rdpFailureHResult = 'unavailable'
$script:rdpFailureCategory = 'unavailable'
$script:rdpFailureOperation = 'unavailable'
$script:rdpFailureDetailsCaptured = $false
$script:rdpFailureLine = 'unavailable'
$script:rdpFailureUnloadExitCode = 'unavailable'
$script:rdpFailureUnloadHiveMounted = 'unavailable'
$script:rdpFailureUnloadAttempts = 'unavailable'
$script:rdpStartTime = $null
$script:sessionShellSigningThumbprint = $null
$script:lastRdpDiagnosticAt = [DateTime]::MinValue
$script:preserveRootOnFailure = $false
$failureStage = 'setup'
$testLog = Join-Path $runRoot 'test-output.log'
$nativeTokenDiagnosticsPath = Join-Path $runRoot 'native-token-diagnostics.log'
$preservedLog = Join-Path $operatorTempRoot 'chuzi-job-pool-smoke-test-output.log'
Remove-Item -LiteralPath $preservedLog -Force -ErrorAction SilentlyContinue

function Invoke-SmokeRetry([scriptblock] $Action, [int] $Attempts = 6) {
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        try {
            & $Action
            return $true
        } catch {
            if ($attempt -eq $Attempts) {
                return $false
            }
            Start-Sleep -Milliseconds ([Math]::Min(2000, 250 * [Math]::Pow(2, $attempt - 1)))
        }
    }
    return $false
}

function Invoke-SmokeHiveUnload([string] $Hive, [int] $Attempts = 6) {
    $result = [pscustomobject]@{
        Succeeded = $false
        Attempts = 0
        ExitCode = 'unavailable'
        HiveMounted = 'unavailable'
        ExceptionType = 'none'
        HResult = 'unavailable'
    }
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        $result.Attempts = $attempt
        $result.ExceptionType = 'none'
        $result.HResult = 'unavailable'
        try {
            & reg.exe unload $Hive 1>$null 2>$null
            $result.ExitCode = [string]$LASTEXITCODE
        } catch {
            $result.ExitCode = [string]$LASTEXITCODE
            $cause = $_.Exception
            while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
            $result.ExceptionType = $cause.GetType().FullName
            $result.HResult = '0x{0:X8}' -f $cause.HResult
        }
        try {
            $result.HiveMounted = [string](Test-Path -LiteralPath ('Registry::' + $Hive) -ErrorAction Stop)
        } catch {
            $cause = $_.Exception
            while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
            $result.HiveMounted = 'unknown'
            if ($result.ExceptionType -eq 'none') {
                $result.ExceptionType = $cause.GetType().FullName
                $result.HResult = '0x{0:X8}' -f $cause.HResult
            }
        }
        Write-RdpDiagnostic ('HIVE_UNLOAD_ATTEMPT=' + $attempt +
            ' EXIT_CODE=' + $result.ExitCode +
            ' MOUNTED=' + $result.HiveMounted +
            ' EXCEPTION_TYPE=' + $result.ExceptionType +
            ' HRESULT=' + $result.HResult)
        if ($result.ExitCode -eq '0' -and $result.HiveMounted -eq 'False' -and $result.ExceptionType -eq 'none') {
            $result.Succeeded = $true
            return $result
        }
        if ($attempt -lt $Attempts) {
            Start-Sleep -Milliseconds ([Math]::Min(2000, 250 * [Math]::Pow(2, $attempt - 1)))
        }
    }
    return $result
}

function New-SmokePassword {
    $alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%+='
    $secure = [System.Security.SecureString]::new()
    $random = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    [byte[]] $sample = @(0)
    $limit = [int]([Math]::Floor(256.0 / $alphabet.Length) * $alphabet.Length)
    try {
        $position = 0
        while ($position -lt 48) {
            $random.GetBytes($sample)
            if ([int]$sample[0] -ge $limit) {
                continue
            }
            $index = [int]$sample[0] % $alphabet.Length
            $secure.AppendChar([char]$alphabet[$index])
            $position++
        }
        $secure.MakeReadOnly()
        return $secure
    } catch {
        $secure.Dispose()
        throw
    } finally {
        [Array]::Clear($sample, 0, $sample.Length)
        $random.Dispose()
    }
}

function Convert-SmokeSecureStringToPlainText([System.Security.SecureString] $Value) {
    $buffer = [IntPtr]::Zero
    try {
        $buffer = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($Value)
        return [System.Runtime.InteropServices.Marshal]::PtrToStringBSTR($buffer)
    } finally {
        if ($buffer -ne [IntPtr]::Zero) {
            [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($buffer)
        }
    }
}

function Initialize-SmokeSessionShellRegistryHelper {
    if ('ChuziSmokeSessionShellRegistryV2' -as [type]) {
        return
    }
    # Add-Type classes persist in a PowerShell process; version this helper so an older session cannot reuse stale code.
    Add-Type -TypeDefinition @'
using System;
using System.Security.Principal;
using Microsoft.Win32;

public static class ChuziSmokeSessionShellRegistryV2
{
    public static string LastOperation { get; private set; }

    public static void SetShell(string sid, string command)
    {
        LastOperation = "validate_command";
        if (String.IsNullOrWhiteSpace(command))
            throw new ArgumentException("session shell command is required");
        LastOperation = "normalize_sid";
        string userSid = new SecurityIdentifier(sid).Value;
        string path = userSid + @"\Software\Microsoft\Windows NT\CurrentVersion\Winlogon";
        LastOperation = "open_users";
        using (RegistryKey users = RegistryKey.OpenBaseKey(RegistryHive.Users, RegistryView.Default))
        {
            LastOperation = "create_winlogon_key";
            using (RegistryKey winlogon = users.CreateSubKey(path, RegistryKeyPermissionCheck.ReadWriteSubTree))
            {
                if (winlogon == null)
                    throw new InvalidOperationException("session shell registry key is unavailable");
                LastOperation = "set_shell";
                winlogon.SetValue("Shell", command, RegistryValueKind.String);
                LastOperation = "read_shell";
                object actual = winlogon.GetValue("Shell", null, RegistryValueOptions.DoNotExpandEnvironmentNames);
                LastOperation = "verify_shell";
                if (!(actual is string) || !String.Equals((string)actual, command, StringComparison.Ordinal) ||
                    command.IndexOf("-Command", StringComparison.OrdinalIgnoreCase) >= 0 ||
                    command.IndexOf("Bypass", StringComparison.OrdinalIgnoreCase) >= 0)
                    throw new InvalidOperationException("session shell registry value mismatch");
                LastOperation = "complete";
            }
        }
    }
}
'@
}

function Initialize-SmokeRdpCredentialOverrideHelper {
    if ('ChuziSmokeRdpCredentialOverrideV3' -as [type]) {
        return
    }
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security;

public static class ChuziSmokeRdpCredentialOverrideV3
{
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct NativeCredential
    {
        public uint Flags;
        public uint Type;
        public IntPtr TargetName;
        public IntPtr Comment;
        public long LastWritten;
        public uint CredentialBlobSize;
        public IntPtr CredentialBlob;
        public uint Persist;
        public uint AttributeCount;
        public IntPtr Attributes;
        public IntPtr TargetAlias;
        public IntPtr UserName;
    }

    [DllImport("advapi32.dll", EntryPoint = "CredReadW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredRead(string targetName, uint type, uint flags, out IntPtr credential);

    [DllImport("advapi32.dll", EntryPoint = "CredWriteW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredWrite(ref NativeCredential credential, uint flags);

    [DllImport("advapi32.dll", EntryPoint = "CredDeleteW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredDelete(string targetName, uint type, uint flags);

    [DllImport("advapi32.dll")]
    private static extern void CredFree(IntPtr credential);

    private static IntPtr previousCredential = IntPtr.Zero;
    private static string activeTarget;
    private static bool replacementWritten;
    private static uint replacementType = 2;

    private static bool TryRead(string targetName, out IntPtr credential)
    {
        if (CredRead(targetName, 2, 0, out credential))
            return true;
        int error = Marshal.GetLastWin32Error();
        if (error != 1168) throw new Win32Exception(error);
        if (CredRead(targetName, 1, 0, out credential))
            return true;
        error = Marshal.GetLastWin32Error();
        if (error == 1168) return false;
        throw new Win32Exception(error);
    }

    public static bool UserNameMatches(string targetName, string expectedUserName)
    {
        IntPtr credential;
        if (!TryRead(targetName, out credential)) return false;
        try
        {
            NativeCredential value = (NativeCredential)Marshal.PtrToStructure(credential, typeof(NativeCredential));
            string actualUserName = Marshal.PtrToStringUni(value.UserName) ?? String.Empty;
            return String.Equals(actualUserName, expectedUserName, StringComparison.OrdinalIgnoreCase);
        }
        finally
        {
            CredFree(credential);
        }
    }

    public static string ReadUserName(string targetName)
    {
        IntPtr credential;
        if (!TryRead(targetName, out credential)) return String.Empty;
        try
        {
            NativeCredential value = (NativeCredential)Marshal.PtrToStructure(credential, typeof(NativeCredential));
            return Marshal.PtrToStringUni(value.UserName) ?? String.Empty;
        }
        finally
        {
            CredFree(credential);
        }
    }

    public static void Write(string targetName, string userName, SecureString password)
    {
        if (activeTarget != null) throw new InvalidOperationException("credential override is already active");
        IntPtr existing;
        if (TryRead(targetName, out existing))
        {
            previousCredential = existing;
            NativeCredential previous = (NativeCredential)Marshal.PtrToStructure(existing, typeof(NativeCredential));
            replacementType = previous.Type;
        }
        else
        {
            int error = Marshal.GetLastWin32Error();
            if (error != 1168) throw new Win32Exception(error);
        }
        activeTarget = targetName;

        IntPtr target = IntPtr.Zero;
        IntPtr user = IntPtr.Zero;
        IntPtr blob = IntPtr.Zero;
        try
        {
            target = Marshal.StringToHGlobalUni(targetName);
            user = Marshal.StringToHGlobalUni(userName);
            blob = Marshal.SecureStringToGlobalAllocUnicode(password);
            NativeCredential credential = new NativeCredential
            {
                Type = replacementType,
                TargetName = target,
                CredentialBlob = blob,
                CredentialBlobSize = checked((uint)(password.Length * 2)),
                Persist = 1,
                UserName = user
            };
            if (!CredWrite(ref credential, 0))
                throw new Win32Exception(Marshal.GetLastWin32Error());
            replacementWritten = true;
        }
        finally
        {
            if (blob != IntPtr.Zero) Marshal.ZeroFreeGlobalAllocUnicode(blob);
            if (user != IntPtr.Zero) Marshal.FreeHGlobal(user);
            if (target != IntPtr.Zero) Marshal.FreeHGlobal(target);
        }
    }

    public static void Restore(string targetName)
    {
        if (activeTarget == null) return;
        if (!String.Equals(activeTarget, targetName, StringComparison.OrdinalIgnoreCase))
            throw new InvalidOperationException("credential override target mismatch");

        if (previousCredential != IntPtr.Zero)
        {
            NativeCredential previous = (NativeCredential)Marshal.PtrToStructure(previousCredential, typeof(NativeCredential));
            if (!CredWrite(ref previous, 0))
                throw new Win32Exception(Marshal.GetLastWin32Error());
        }
        else if (replacementWritten && !CredDelete(targetName, 2, 0))
        {
            int error = Marshal.GetLastWin32Error();
            if (error != 1168) throw new Win32Exception(error);
        }

        if (previousCredential != IntPtr.Zero)
        {
            CredFree(previousCredential);
            previousCredential = IntPtr.Zero;
        }
        activeTarget = null;
        replacementWritten = false;
        replacementType = 2;
    }
}
'@
}

function Initialize-SmokeNativeHelpers {
    if ('ChuziSmokeCredentialStoreV2' -as [type]) {
        Initialize-SmokeSessionShellRegistryHelper
        Initialize-SmokeRdpCredentialOverrideHelper
        return
    }
    Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security;
using System.Text;

public static class ChuziSmokeCredentialStoreV2
{
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct NativeCredential
    {
        public uint Flags;
        public uint Type;
        public IntPtr TargetName;
        public IntPtr Comment;
        public long LastWritten;
        public uint CredentialBlobSize;
        public IntPtr CredentialBlob;
        public uint Persist;
        public uint AttributeCount;
        public IntPtr Attributes;
        public IntPtr TargetAlias;
        public IntPtr UserName;
    }

    [DllImport("advapi32.dll", EntryPoint = "CredReadW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredRead(string targetName, uint type, uint flags, out IntPtr credential);

    [DllImport("advapi32.dll", EntryPoint = "CredWriteW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredWrite(ref NativeCredential credential, uint flags);

    [DllImport("advapi32.dll", EntryPoint = "CredDeleteW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredDelete(string targetName, uint type, uint flags);

    [DllImport("advapi32.dll")]
    private static extern void CredFree(IntPtr credential);

    public static bool Exists(string targetName)
    {
        IntPtr credential;
        if (CredRead(targetName, 2, 0, out credential))
        {
            CredFree(credential);
            return true;
        }
        int error = Marshal.GetLastWin32Error();
        if (error != 1168) throw new Win32Exception(error);
        if (CredRead(targetName, 1, 0, out credential))
        {
            CredFree(credential);
            return true;
        }
        error = Marshal.GetLastWin32Error();
        if (error == 1168) return false;
        throw new Win32Exception(error);
    }

    public static void Write(string targetName, string userName, SecureString password)
    {
        IntPtr target = IntPtr.Zero;
        IntPtr user = IntPtr.Zero;
        IntPtr blob = IntPtr.Zero;
        try
        {
            target = Marshal.StringToHGlobalUni(targetName);
            user = Marshal.StringToHGlobalUni(userName);
            blob = Marshal.SecureStringToGlobalAllocUnicode(password);
            NativeCredential credential = new NativeCredential
            {
                Type = 2,
                TargetName = target,
                CredentialBlob = blob,
                CredentialBlobSize = checked((uint)(password.Length * 2)),
                Persist = 1,
                UserName = user
            };
            if (!CredWrite(ref credential, 0))
                throw new Win32Exception(Marshal.GetLastWin32Error());
        }
        finally
        {
            if (blob != IntPtr.Zero) Marshal.ZeroFreeGlobalAllocUnicode(blob);
            if (user != IntPtr.Zero) Marshal.FreeHGlobal(user);
            if (target != IntPtr.Zero) Marshal.FreeHGlobal(target);
        }
    }

    public static void Delete(string targetName)
    {
        if (!CredDelete(targetName, 2, 0))
        {
            int error = Marshal.GetLastWin32Error();
            if (error != 1168)
                throw new Win32Exception(error);
        }
    }
}

public static class ChuziSmokeProfileBootstrap
{
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct StartupInfo
    {
        public int cb;
        public string lpReserved;
        public string lpDesktop;
        public string lpTitle;
        public int dwX;
        public int dwY;
        public int dwXSize;
        public int dwYSize;
        public int dwXCountChars;
        public int dwYCountChars;
        public int dwFillAttribute;
        public int dwFlags;
        public short wShowWindow;
        public short cbReserved2;
        public IntPtr lpReserved2;
        public IntPtr hStdInput;
        public IntPtr hStdOutput;
        public IntPtr hStdError;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct ProcessInformation
    {
        public IntPtr hProcess;
        public IntPtr hThread;
        public int processId;
        public int threadId;
    }

    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CreateProcessWithLogonW(
        string userName,
        string domain,
        string password,
        uint logonFlags,
        string applicationName,
        StringBuilder commandLine,
        uint creationFlags,
        IntPtr environment,
        string currentDirectory,
        ref StartupInfo startupInfo,
        out ProcessInformation processInformation);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CloseHandle(IntPtr handle);

    private const uint LogonWithProfile = 0x00000001;
    private const uint CreateUnicodeEnvironment = 0x00000400;
    private const uint CreateNoWindow = 0x08000000;
    private const uint Infinite = 0xFFFFFFFF;

    public static void Run(string userName, string domain, string password)
    {
        string systemDirectory = Environment.GetFolderPath(Environment.SpecialFolder.System);
        string applicationName = System.IO.Path.Combine(systemDirectory, "cmd.exe");
        StartupInfo startupInfo = new StartupInfo
        {
            cb = Marshal.SizeOf(typeof(StartupInfo))
        };
        ProcessInformation processInformation;
        StringBuilder commandLine = new StringBuilder("cmd.exe /c exit");
        bool created = CreateProcessWithLogonW(
            userName,
            domain,
            password,
            LogonWithProfile,
            applicationName,
            commandLine,
            CreateUnicodeEnvironment | CreateNoWindow,
            IntPtr.Zero,
            systemDirectory,
            ref startupInfo,
            out processInformation);
        int error = created ? 0 : Marshal.GetLastWin32Error();
        if (!created) throw new Win32Exception(error);

        try
        {
            WaitForSingleObject(processInformation.hProcess, Infinite);
        }
        finally
        {
            if (processInformation.hThread != IntPtr.Zero)
                CloseHandle(processInformation.hThread);
            if (processInformation.hProcess != IntPtr.Zero)
                CloseHandle(processInformation.hProcess);
        }
    }
}

public sealed class ChuziSmokeSession
{
    public int SessionId { get; set; }
    public string UserName { get; set; }
    public int State { get; set; }
}

public static class ChuziSmokeSessionQuery
{
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct NativeSessionInfo
    {
        public int SessionId;
        public IntPtr StationName;
        public int State;
    }

    [DllImport("wtsapi32.dll", EntryPoint = "WTSEnumerateSessionsW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool WTSEnumerateSessions(IntPtr server, int reserved, int version, out IntPtr sessions, out int count);

    [DllImport("wtsapi32.dll", EntryPoint = "WTSQuerySessionInformationW", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool WTSQuerySessionInformation(IntPtr server, int sessionId, int infoClass, out IntPtr buffer, out int bytesReturned);

    [DllImport("wtsapi32.dll")]
    private static extern void WTSFreeMemory(IntPtr memory);

    public static ChuziSmokeSession[] GetSessions()
    {
        IntPtr buffer;
        int count;
        if (!WTSEnumerateSessions(IntPtr.Zero, 0, 1, out buffer, out count))
            throw new Win32Exception(Marshal.GetLastWin32Error());
        try
        {
            List<ChuziSmokeSession> result = new List<ChuziSmokeSession>();
            int itemSize = Marshal.SizeOf(typeof(NativeSessionInfo));
            for (int i = 0; i < count; i++)
            {
                IntPtr item = new IntPtr(buffer.ToInt64() + ((long)i * itemSize));
                NativeSessionInfo session = (NativeSessionInfo)Marshal.PtrToStructure(item, typeof(NativeSessionInfo));
                if (session.SessionId == 0 || session.State == 6 || session.State == 8 || session.State == 9)
                    continue;
                string userName = QueryString(session.SessionId, 5);
                if (!String.IsNullOrWhiteSpace(userName))
                    result.Add(new ChuziSmokeSession { SessionId = session.SessionId, UserName = userName, State = session.State });
            }
            return result.ToArray();
        }
        finally
        {
            WTSFreeMemory(buffer);
        }
    }

    private static string QueryString(int sessionId, int infoClass)
    {
        IntPtr buffer;
        int bytesReturned;
        if (!WTSQuerySessionInformation(IntPtr.Zero, sessionId, infoClass, out buffer, out bytesReturned))
            throw new Win32Exception(Marshal.GetLastWin32Error());
        try
        {
            return buffer == IntPtr.Zero ? String.Empty : (Marshal.PtrToStringUni(buffer) ?? String.Empty);
        }
        finally
        {
            if (buffer != IntPtr.Zero)
                WTSFreeMemory(buffer);
        }
    }
}
'@
    Initialize-SmokeSessionShellRegistryHelper
    Initialize-SmokeRdpCredentialOverrideHelper
}

function Sign-SmokeSessionShell([string] $ScriptPath) {
    $certificate = New-SelfSignedCertificate `
        -Type CodeSigningCert `
        -Subject ('CN=Chuzi smoke ' + $runID) `
        -CertStoreLocation 'Cert:\LocalMachine\My' `
        -KeyExportPolicy Exportable `
        -NotAfter (Get-Date).AddHours(4) `
        -ErrorAction Stop
    $script:sessionShellSigningThumbprint = $certificate.Thumbprint
    $publicCertificate = Join-Path $runRoot 'smoke-code-signing.cer'
    Export-Certificate -Cert $certificate -FilePath $publicCertificate -Force -ErrorAction Stop | Out-Null
    Import-Certificate -FilePath $publicCertificate -CertStoreLocation 'Cert:\LocalMachine\Root' -ErrorAction Stop | Out-Null
    Import-Certificate -FilePath $publicCertificate -CertStoreLocation 'Cert:\LocalMachine\TrustedPublisher' -ErrorAction Stop | Out-Null
    Remove-Item -LiteralPath $publicCertificate -Force -ErrorAction Stop
    $signature = Set-AuthenticodeSignature -FilePath $ScriptPath -Certificate $certificate -HashAlgorithm SHA256 -ErrorAction Stop
    if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid) {
        throw 'session shell Authenticode signature validation failed'
    }
}

function Remove-SmokeSessionShellSigner {
    if ([string]::IsNullOrWhiteSpace($script:sessionShellSigningThumbprint)) { return }
    foreach ($store in @('Cert:\LocalMachine\My\', 'Cert:\LocalMachine\Root\', 'Cert:\LocalMachine\TrustedPublisher\')) {
        $certificatePath = $store + $script:sessionShellSigningThumbprint
        if (Test-Path -LiteralPath $certificatePath) {
            Remove-Item -LiteralPath $certificatePath -Force -ErrorAction Stop
        }
    }
    $script:sessionShellSigningThumbprint = $null
}

function Test-ActiveManagedSession([string] $Name) {
    foreach ($line in @(Get-MarkedUserSessions $Name)) {
        if ($line.State -eq 0) {
            return $true
        }
    }
    return $false
}

function Wait-SmokeUserProfile([string] $Sid, [int] $TimeoutSeconds = 30) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $profiles = @(Get-CimInstance -ClassName Win32_UserProfile -Filter ("SID='" + $Sid + "'") -ErrorAction SilentlyContinue)
        if ($profiles.Count -eq 1 -and
            -not [string]::IsNullOrWhiteSpace($profiles[0].LocalPath) -and
            (Test-Path -LiteralPath (Join-Path $profiles[0].LocalPath 'NTUSER.DAT') -PathType Leaf)) {
            return $profiles[0]
        }
        Start-Sleep -Seconds 1
    }
    return $null
}

function Wait-SmokeUserProfileReleased([string] $Sid, [int] $TimeoutSeconds = 30) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $profiles = @(Get-CimInstance -ClassName Win32_UserProfile -Filter ("SID='" + $Sid + "'") -ErrorAction SilentlyContinue)
        if ($profiles.Count -eq 1 -and -not $profiles[0].Loaded) {
            return $profiles[0]
        }
        Start-Sleep -Milliseconds 250
    }
    return $null
}

function Initialize-SmokeUserProfile([string] $Name, [System.Security.SecureString] $Password) {
    $plainPassword = Convert-SmokeSecureStringToPlainText $Password
    try {
        [ChuziSmokeProfileBootstrap]::Run($Name, '.', $plainPassword)
    } finally {
        $plainPassword = $null
    }
    $targetUser = Get-LocalUser -Name $Name -ErrorAction Stop
    $profile = Wait-SmokeUserProfile $targetUser.SID.Value
    if ($null -eq $profile) {
        throw 'local RDP user profile did not initialize'
    }
    if ($null -eq (Wait-SmokeUserProfileReleased $targetUser.SID.Value)) {
        throw 'local RDP user profile remained loaded after bootstrap'
    }
    Start-Sleep -Milliseconds 500
}

function Get-SmokeSessionShellCommand([string] $RuntimeRoot) {
    $root = [System.IO.Path]::GetFullPath($RuntimeRoot)
    $scriptPath = [System.IO.Path]::GetFullPath((Join-Path $root 'session-shell.ps1'))
    if (-not [System.IO.Path]::IsPathRooted($scriptPath) -or
        [System.IO.Path]::GetFileName($scriptPath) -cne 'session-shell.ps1' -or
        -not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
        throw 'fixed session shell script is unavailable'
    }
    return 'powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy AllSigned -File "' + $scriptPath + '"'
}

function Apply-SmokeSessionShellPolicy([string] $Name, [string] $RuntimeRoot) {
    Set-RdpFailurePhase 'session_shell_policy_profile_lookup'
    $targetUser = Get-LocalUser -Name $Name -ErrorAction Stop
    $profile = Wait-SmokeUserProfile $targetUser.SID.Value
    if ($null -eq $profile) {
        throw 'local RDP user profile did not initialize'
    }
    $hive = 'HKEY_USERS\' + $targetUser.SID.Value
    $hivePath = Join-Path $profile.LocalPath 'NTUSER.DAT'
    $loaded = $false
    $operationFailed = $false
    $operationFailurePhase = $null
    try {
        Set-RdpFailurePhase 'session_shell_policy_hive_check'
        if (-not (Test-Path -LiteralPath ('Registry::' + $hive))) {
            Set-RdpFailurePhase 'session_shell_policy_hive_load'
            & reg.exe load $hive $hivePath 1>$null 2>$null
            if ($LASTEXITCODE -ne 0) { throw 'target user hive load failed' }
            $loaded = $true
        }
        Set-RdpFailurePhase 'session_shell_policy_command_validation'
        $command = Get-SmokeSessionShellCommand $RuntimeRoot
        Set-RdpFailurePhase 'session_shell_policy_write_verify'
        [ChuziSmokeSessionShellRegistryV2]::SetShell($targetUser.SID.Value, $command)
    } catch {
        $operationFailed = $true
        $operationFailurePhase = $script:rdpFailurePhase
        $cause = $_.Exception
        while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
        $script:rdpFailureExceptionType = $cause.GetType().FullName
        $script:rdpFailureHResult = '0x{0:X8}' -f $cause.HResult
        $script:rdpFailureCategory = Get-RdpFailureCategory $_.Exception
        try {
            $script:rdpFailureOperation = [ChuziSmokeSessionShellRegistryV2]::LastOperation
        } catch {
            $script:rdpFailureOperation = 'registry_operation_unavailable'
        }
        $script:rdpFailureDetailsCaptured = $true
    } finally {
        if ($loaded) {
            Set-RdpFailurePhase 'session_shell_policy_hive_unload'
            try {
                $unload = Invoke-SmokeHiveUnload $hive
            } catch {
                Set-RdpFailurePhase 'session_shell_policy_hive_unload_failed'
                $cause = $_.Exception
                while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
                $script:rdpFailureOperation = 'reg_unload_diagnostic'
                $script:rdpFailureExceptionType = $cause.GetType().FullName
                $script:rdpFailureHResult = '0x{0:X8}' -f $cause.HResult
                $script:rdpFailureCategory = Get-RdpFailureCategory $_.Exception
                $script:rdpFailureDetailsCaptured = $true
                throw 'target user hive unload failed'
            }
            $script:rdpFailureUnloadAttempts = [string]$unload.Attempts
            $script:rdpFailureUnloadExitCode = [string]$unload.ExitCode
            $script:rdpFailureUnloadHiveMounted = [string]$unload.HiveMounted
            if (-not $unload.Succeeded) {
                Set-RdpFailurePhase 'session_shell_policy_hive_unload_failed'
                $script:rdpFailureOperation = 'reg_unload'
                $script:rdpFailureExceptionType = $unload.ExceptionType
                $script:rdpFailureHResult = $unload.HResult
                $script:rdpFailureCategory = 'registry_unload_failed'
                $script:rdpFailureDetailsCaptured = $true
                throw 'target user hive unload failed'
            }
            Set-RdpFailurePhase 'session_shell_policy_hive_unload_complete'
        }
    }
    if ($operationFailed) {
        Set-RdpFailurePhase $operationFailurePhase
        throw 'target user shell policy setup failed'
    }
}

function Test-SmokeSessionShellReady([string] $Sid, [int] $SessionId, [int] $TimeoutSeconds = 30) {
    $name = 'Global\ChuziSessionShell-' + $Sid + '-' + $SessionId + '-ready'
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastError = 'not_found'
    while ([DateTime]::UtcNow -lt $deadline) {
        $event = $null
        try {
            $event = [System.Threading.EventWaitHandle]::OpenExisting($name)
            $lastError = 'opened_not_signaled'
            if ($event.WaitOne(0)) {
                Write-RdpDiagnostic 'SESSION_SHELL_READY_LAST_ERROR=none'
                return $true
            }
        } catch [System.Threading.WaitHandleCannotBeOpenedException] {
            $lastError = 'not_found'
        } catch {
            $lastError = Get-RdpFailureCategory $_.Exception
        } finally {
            if ($null -ne $event) { $event.Dispose() }
        }
        Start-Sleep -Milliseconds 250
    }
    Write-RdpDiagnostic ('SESSION_SHELL_READY_LAST_ERROR=' + $lastError)
    return $false
}

function Capture-SmokeSessionShellDiagnostics([string] $Sid, [int] $SessionId) {
    $winlogonPath = 'Registry::HKEY_USERS\' + $Sid + '\Software\Microsoft\Windows NT\CurrentVersion\Winlogon'
    try {
        $shell = [string](Get-ItemProperty -LiteralPath $winlogonPath -Name Shell -ErrorAction Stop).Shell
        Write-RdpDiagnostic ('SESSION_SHELL_REGISTRY_PRESENT=' + $true)
        Write-RdpDiagnostic ('SESSION_SHELL_REGISTRY_VALUE_IS_FIXED=' + ($shell -match '(?i)session-shell\.ps1'))
    } catch {
        Write-RdpDiagnostic 'SESSION_SHELL_REGISTRY_PRESENT=False'
        Write-RdpDiagnostic ('SESSION_SHELL_REGISTRY_ERROR=' + (Get-RdpFailureCategory $_.Exception))
    }
    try {
        $processes = @(Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" -ErrorAction Stop | Where-Object {
            $_.SessionId -eq $SessionId -and
            ([string]$_.CommandLine) -match '(?i)(^|[\\/\"\s])session-shell\.ps1([\"\s]|$)'
        })
        Write-RdpDiagnostic ('SESSION_SHELL_PROCESS_COUNT=' + $processes.Count)
    } catch {
        Write-RdpDiagnostic 'SESSION_SHELL_PROCESS_COUNT=unavailable'
        Write-RdpDiagnostic ('SESSION_SHELL_PROCESS_ERROR=' + (Get-RdpFailureCategory $_.Exception))
    }
}

function Write-RdpDiagnostic([string] $Line) {
    if ([string]::IsNullOrWhiteSpace($script:rdpDiagnosticsPath)) {
        return
    }
    try {
        Add-Content -LiteralPath $script:rdpDiagnosticsPath -Value $Line -Encoding UTF8 -ErrorAction Stop
    } catch {
        return
    }
}

function Set-RdpFailurePhase([string] $Phase) {
    $script:rdpFailurePhase = $Phase
    Write-RdpDiagnostic ('PHASE=' + $Phase)
}

function Get-RdpFailureCategory([System.Exception] $Exception) {
    $current = $Exception
    while ($null -ne $current) {
        if ($current -is [System.UnauthorizedAccessException]) {
            return 'access_denied'
        }
        if ($current -is [System.Security.SecurityException]) {
            return 'security_denied'
        }
        if ($current -is [System.IO.IOException]) {
            return 'io_error'
        }
        if ($current -is [System.ArgumentException]) {
            return 'invalid_argument'
        }
        if ($current -is [System.ComponentModel.Win32Exception]) {
            switch ($current.NativeErrorCode) {
                2 { return 'system_file_missing' }
                5 { return 'access_denied' }
                87 { return 'invalid_parameter' }
                1314 { return 'privilege_not_held' }
                1326 { return 'logon_rejected' }
                1327 { return 'account_restricted' }
                1328 { return 'logon_hours_restricted' }
                1329 { return 'workstation_restricted' }
                1330 { return 'password_expired' }
                1331 { return 'account_disabled' }
                1385 { return 'logon_policy_denied' }
                1816 { return 'resource_quota_exhausted' }
                1907 { return 'password_change_required' }
                default { return 'other_win32_error' }
            }
        }
        $current = $current.InnerException
    }
    return 'non_win32_error'
}

function Capture-RdpDiagnostics([string] $Label, [string] $Name) {
    if ([string]::IsNullOrWhiteSpace($script:rdpDiagnosticsPath)) {
        return
    }
    Write-RdpDiagnostic ("`n=== " + $Label + ' @ ' + (Get-Date -Format o) + ' ===')
    try {
        Write-RdpDiagnostic 'MSTSC_PROCESSES:'
        $mstscCount = @(Get-CimInstance Win32_Process -Filter "Name='mstsc.exe'" -ErrorAction SilentlyContinue).Count
        Write-RdpDiagnostic ('MSTSC_PROCESS_COUNT=' + $mstscCount)
    } catch {
        Write-RdpDiagnostic 'MSTSC_QUERY_ERROR=unavailable'
    }
    try {
        Write-RdpDiagnostic 'WTS_SESSIONS:'
        $managedSessions = @(Get-MarkedUserSessions $Name)
        Write-RdpDiagnostic ('TARGET_WTS_COUNT=' + $managedSessions.Count)
        Write-RdpDiagnostic ('TARGET_ACTIVE_COUNT=' + @($managedSessions | Where-Object { $_.State -eq 0 }).Count)
    } catch {
        Write-RdpDiagnostic 'WTS_QUERY_ERROR=unavailable'
    }
    try {
        Write-RdpDiagnostic ('RDP_CREDENTIAL_PRESENT=' + [ChuziSmokeCredentialStoreV2]::Exists($script:rdpCredentialTarget))
        if (-not [string]::IsNullOrWhiteSpace($script:rdpCredentialUserName)) {
            Write-RdpDiagnostic ('RDP_CREDENTIAL_USERNAME_MATCHES=' + [ChuziSmokeRdpCredentialOverrideV3]::UserNameMatches($script:rdpCredentialTarget, $script:rdpCredentialUserName))
        }
    } catch {
        Write-RdpDiagnostic 'CREDENTIAL_QUERY_ERROR=unavailable'
    }
    $since = if ($null -ne $script:rdpStartTime) { $script:rdpStartTime } else { (Get-Date).AddMinutes(-2) }
    $channels = @(
        'Microsoft-Windows-TerminalServices-RDPClient/Operational',
        'Microsoft-Windows-TerminalServices-LocalSessionManager/Operational',
        'Microsoft-Windows-TerminalServices-RemoteConnectionManager/Operational',
        'Microsoft-Windows-RemoteDesktopServices-RdpCoreTS/Operational'
    )
    foreach ($channel in $channels) {
        $eventKey = ($channel -replace '[^A-Za-z0-9]+', '_').Trim('_').ToUpperInvariant()
        try {
            $events = @(Get-WinEvent -FilterHashtable @{ LogName = $channel; StartTime = $since } -ErrorAction SilentlyContinue |
                Select-Object -First 20 TimeCreated, Id)
            if ($events.Count -gt 0) {
                $ids = @($events | ForEach-Object { [string]$_.Id } | Sort-Object -Unique) -join ','
                $latest = $events | Select-Object -First 1
                $latestTime = if ($null -ne $latest.TimeCreated) { $latest.TimeCreated.ToString('o') } else { 'unknown' }
                Write-RdpDiagnostic ('EVENTS_' + $eventKey + '=sample_count:' + $events.Count + ';ids:' + $ids + ';latest:' + $latestTime)
            } else {
                Write-RdpDiagnostic ('EVENTS_' + $eventKey + '=sample_count:0')
            }
        } catch {
            Write-RdpDiagnostic ('EVENT_QUERY_ERROR=' + $eventKey + ':unavailable')
        }
    }
}

function Get-SmokeRdpCredentialListing {
    $cmdkey = (Get-Command cmdkey.exe -CommandType Application -ErrorAction Stop).Source
    return (& $cmdkey '/list' 2>&1 | Out-String)
}

function Test-SmokeRdpCredential([string] $Target) {
    $cmdkey = (Get-Command cmdkey.exe -CommandType Application -ErrorAction Stop).Source
    $output = (& $cmdkey (('/list:' + $Target)) 2>&1 | Out-String)
    return $output.IndexOf($Target, [System.StringComparison]::OrdinalIgnoreCase) -ge 0
}

function Get-LocalRdpTarget([string] $ProfileTemplatePath) {
    if (-not [string]::IsNullOrWhiteSpace($ProfileTemplatePath)) {
        $resolvedPath = (Resolve-Path -LiteralPath $ProfileTemplatePath -ErrorAction Stop).Path
        $addresses = @()
        $portValues = @()
        foreach ($line in Get-Content -LiteralPath $resolvedPath -ErrorAction Stop) {
            if ($line -match '^\s*full address:s:(.*?)\s*$') {
                $addresses += $Matches[1]
            } elseif ($line -match '^\s*server port:i:(\d+)\s*$') {
                $portValues += [int]$Matches[1]
            }
        }
        if ($addresses.Count -ne 1) {
            throw 'verified profile must contain exactly one full address'
        }
        if ($portValues.Count -gt 1) {
            throw 'verified profile contains multiple server ports'
        }
        $addressValue = $addresses[0].Trim()
        if ($addressValue -notmatch '^(?<host>\d{1,3}(?:\.\d{1,3}){3})(?::(?<port>\d+))?$') {
            throw 'verified profile address must be an IPv4 loopback endpoint'
        }
        $targetAddress = $Matches['host']
        $port = if ($Matches['port']) { [int]$Matches['port'] } elseif ($portValues.Count -eq 1) { $portValues[0] } else { 3389 }
        if ($Matches['port'] -and $portValues.Count -eq 1 -and $portValues[0] -ne $port) {
            throw 'verified profile contains conflicting server ports'
        }
        $parsedAddress = $null
        if (-not [System.Net.IPAddress]::TryParse($targetAddress, [ref]$parsedAddress) -or
            $parsedAddress.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork -or
            $parsedAddress.GetAddressBytes()[0] -ne 127) {
            throw 'verified profile address must be an IPv4 loopback endpoint'
        }
        if ($port -ne 3389) {
            throw 'verified profile must use the standard RDP port'
        }
        return $parsedAddress.ToString()
    }

    $listing = Get-SmokeRdpCredentialListing
    $occupiedOctets = [System.Collections.Generic.HashSet[int]]::new()
    foreach ($match in [regex]::Matches($listing, '(?i)TERMSRV/127\.0\.0\.(\d{1,3})(?::\d+)?')) {
        $octet = [int]$match.Groups[1].Value
        if ($octet -ge 2 -and $octet -le 254) {
            [void]$occupiedOctets.Add($octet)
        }
    }
    Write-Host ('RDP DEBUG: occupied_loopback_targets=' + $occupiedOctets.Count)
    foreach ($octet in 2..254) {
        if (-not $occupiedOctets.Contains($octet)) {
            Write-Host 'RDP DEBUG: rdp_target_selection=lowest_unoccupied_loopback'
            return '127.0.0.' + $octet
        }
    }
    throw 'no unoccupied loopback RDP endpoint is available'
}

# Evidence from the operator-verified MiniSession profile. The endpoint and
# username stay run-scoped; only these transport options are fixed.
function Get-SmokeRdpProfileLines([string] $TargetHost, [string] $TargetUsername) {
    if ([string]::IsNullOrWhiteSpace($TargetHost) -or [string]::IsNullOrWhiteSpace($TargetUsername)) {
        throw 'smoke RDP profile requires a target and username'
    }
    return @(
        "full address:s:${TargetHost}:3389",
        "username:s:$TargetUsername",
        'prompt for credentials:i:0',
        'administrative session:i:0',
        'screen mode id:i:2',
        'session bpp:i:32',
        'compression:i:1',
        'redirectclipboard:i:1',
        'autoreconnection enabled:i:1',
        'authentication level:i:2',
        'negotiate security layer:i:1'
    )
}

function Set-SmokeRdpCredential([string] $Target, [string] $UserName, [System.Security.SecureString] $Password) {
    $cmdkey = (Get-Command cmdkey.exe -CommandType Application -ErrorAction Stop).Source
    $plainPassword = Convert-SmokeSecureStringToPlainText $Password
    try {
        $argumentList = @(
            ('/generic:' + $Target),
            ('/user:' + $UserName),
            ('/pass:"' + $plainPassword + '"')
        )
        $process = Start-Process -FilePath $cmdkey -ArgumentList $argumentList -Wait -PassThru -WindowStyle Hidden -ErrorAction Stop
        if ($process.ExitCode -ne 0) {
            throw ('cmdkey credential write failed with exit code ' + $process.ExitCode)
        }
    } finally {
        $plainPassword = $null
    }
}

function Remove-SmokeRdpCredential([string] $Target) {
    $cmdkey = (Get-Command cmdkey.exe -CommandType Application -ErrorAction Stop).Source
    $process = Start-Process -FilePath $cmdkey -ArgumentList @('/delete:' + $Target) -Wait -PassThru -WindowStyle Hidden -ErrorAction Stop
    if ($process.ExitCode -ne 0 -and (Test-SmokeRdpCredential $Target)) {
        throw ('cmdkey credential cleanup failed with exit code ' + $process.ExitCode)
    }
}

function Start-LocalRdpSession([string] $Name, [string] $RuntimeRoot, [string] $TargetHost) {
    $script:rdpFailurePhase = 'diagnostics_initialize'
    $script:rdpDiagnosticsPath = Join-Path $runRoot 'rdp-diagnostics.log'
    New-Item -ItemType File -Path $script:rdpDiagnosticsPath -Force | Out-Null
    if (-not [Environment]::UserInteractive) {
        Set-RdpFailurePhase 'interactive_check'
        throw 'interactive smoke console required'
    }
    Set-RdpFailurePhase 'native_helpers'
    Initialize-SmokeNativeHelpers
    Set-RdpFailurePhase 'target_selection'
    if ([string]::IsNullOrWhiteSpace($TargetHost)) {
        throw 'verified RDP profile target was not initialized'
    }
    $targetHost = $TargetHost
    $script:rdpCredentialTarget = 'TERMSRV/' + $targetHost
    Set-RdpFailurePhase 'credential_precheck'

    # Credential Manager stores the canonical computer-qualified identity. Keep
    # the same form in the RDP profile so mstsc does not fall back to the
    # interactive runner when the saved credential username differs.
    $targetUsername = $env:COMPUTERNAME + '\' + $Name
    $credentialUsername = $targetUsername
    Set-RdpFailurePhase 'password_generation'
    $password = New-SmokePassword
    try {
        Set-RdpFailurePhase 'user_create'
        if (Get-LocalUser -Name $Name -ErrorAction SilentlyContinue) {
            throw 'temporary smoke user already exists'
        }
        New-LocalUser -Name $Name -Password $password -Description $marker -PasswordNeverExpires -ErrorAction Stop | Out-Null
        Set-RdpFailurePhase 'rdp_group_add'
        $rdpGroup = Get-LocalGroup -SID ([System.Security.Principal.SecurityIdentifier]::new('S-1-5-32-555')) -ErrorAction Stop
        Add-LocalGroupMember -Group $rdpGroup.Name -Member $Name -ErrorAction Stop
        Set-RdpFailurePhase 'credential_write'
        $script:rdpCredentialOwned = $true
        $script:rdpCredentialUserName = $credentialUsername
        Set-SmokeRdpCredential $script:rdpCredentialTarget $credentialUsername $password
        $credentialUsernameMatches = [ChuziSmokeRdpCredentialOverrideV3]::UserNameMatches($script:rdpCredentialTarget, $credentialUsername)
        Write-RdpDiagnostic ('RDP_CREDENTIAL_USERNAME_MATCHES=' + $credentialUsernameMatches)
        Write-Host ('RDP DEBUG: credential_username_matches=' + $credentialUsernameMatches)
        if (-not $credentialUsernameMatches) {
            throw 'RDP credential identity did not match the target user'
        }
        Set-RdpFailurePhase 'profile_initialize'
        Initialize-SmokeUserProfile $Name $password
        Set-RdpFailurePhase 'session_shell_policy'
        Apply-SmokeSessionShellPolicy $Name $RuntimeRoot
        Set-RdpFailurePhase 'session_shell_policy_applied'
    } finally {
        $password.Dispose()
    }

    $rdpProfile = Join-Path $runRoot 'local-rdp.rdp'
    Set-RdpFailurePhase 'runner_identity_check'
    $targetUser = Get-LocalUser -Name $Name -ErrorAction Stop
    $runnerIdentityObject = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $runnerIdentity = $runnerIdentityObject.Name
    $script:runnerUserName = ($runnerIdentity -split '\\')[-1]
    $script:runnerSID = $runnerIdentityObject.User.Value
    $script:runnerSessionID = (Get-Process -Id $PID -ErrorAction Stop).SessionId
    if ($targetUser.SID.Value -eq $script:runnerSID) {
        throw 'local RDP target resolves to the interactive runner identity'
    }
    $script:rdpDebugSummary = @(
        'rdp_target=loopback-redacted',
        'rdp_identity=run-scoped',
        'rdp_credential_target=redacted',
        'rdp_profile=run-scoped-and-redacted',
        'rdp_password=redacted'
    )
    Set-RdpFailurePhase 'rdp_profile_setup'
    Write-Host ('RDP DEBUG: rdp_diagnostics=' + $script:rdpDiagnosticsPath)
    Get-SmokeRdpProfileLines $targetHost $targetUsername |
        Set-Content -LiteralPath $rdpProfile -Encoding ASCII
    $script:rdpProfilePath = $rdpProfile

    Write-Host 'Opening the one-run local RDP session.'
    Write-Host 'If Windows shows a first-connection certificate prompt, verify the local target and accept it.'
    Capture-RdpDiagnostics 'before_mstsc' $Name
    $mstsc = Join-Path $env:SystemRoot 'System32\mstsc.exe'
    $script:rdpStartTime = Get-Date
    $mstscArguments = @('"' + $rdpProfile + '"')
    Set-RdpFailurePhase 'mstsc_launch'
    Write-RdpDiagnostic 'MSTSC_LAUNCH=started'
    $client = Start-Process -FilePath $mstsc -ArgumentList $mstscArguments -PassThru -ErrorAction Stop
    $script:rdpClientProcess = $client
    Set-RdpFailurePhase 'rdp_session_wait'
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    $activeSince = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        if ((Get-Date) - $script:lastRdpDiagnosticAt -gt [TimeSpan]::FromSeconds(5)) {
            Capture-RdpDiagnostics 'rdp_poll' $Name
            $script:lastRdpDiagnosticAt = Get-Date
        }
        if (Test-UnexpectedRunnerSession) {
            throw 'local RDP authenticated as the interactive runner identity'
        }
        if ($client.HasExited) {
            Capture-RdpDiagnostics 'mstsc_exited' $Name
            throw 'local RDP client exited before the managed session stabilized'
        }
        if (Test-ActiveManagedSession $Name) {
            if ($null -eq $activeSince) {
                $activeSince = [DateTime]::UtcNow
            }
            if (([DateTime]::UtcNow - $activeSince).TotalSeconds -lt 3) {
                Start-Sleep -Milliseconds 250
                continue
            }
            $userProfile = Wait-SmokeUserProfile $targetUser.SID.Value
            if ($null -eq $userProfile) {
                throw 'local RDP user profile did not initialize'
            }
            Set-RdpFailurePhase 'rdp_session_identity_verify'
            $activeSessions = @(Get-MarkedUserSessions $Name | Where-Object { $_.State -eq 0 })
            $sessionUsernameMatches = $activeSessions.Count -eq 1 -and
                $activeSessions[0].UserName -ieq $Name
            Write-RdpDiagnostic ('RDP_WTS_USERNAME_MATCHES=' + $sessionUsernameMatches)
            Write-Host ('RDP DEBUG: wts_username_matches=' + $sessionUsernameMatches)
            if (-not $sessionUsernameMatches) {
                throw 'local RDP session identity did not match the managed user'
            }
            Set-RdpFailurePhase 'rdp_session_shell_readiness'
            $sessionShellReady = Test-SmokeSessionShellReady $targetUser.SID.Value $activeSessions[0].SessionId
            Write-RdpDiagnostic ('SESSION_SHELL_READY=' + $sessionShellReady)
            Write-Host ('RDP DEBUG: session_shell_ready=' + $sessionShellReady)
            if (-not $sessionShellReady) {
                Capture-SmokeSessionShellDiagnostics $targetUser.SID.Value $activeSessions[0].SessionId
                Capture-RdpDiagnostics 'session_shell_not_ready' $Name
                throw 'PowerShell session shell did not report readiness'
            }
            Remove-SmokeRdpCredential $script:rdpCredentialTarget
            $script:rdpCredentialOwned = $false
            Set-RdpFailurePhase 'complete'
            return
        }
        $activeSince = $null
        Start-Sleep -Seconds 1
    }
    Capture-RdpDiagnostics 'rdp_timeout' $Name
    throw 'local RDP session did not become active'
}

function Get-MarkedUsers([switch] $CurrentRunOnly) {
    $pattern = $smokeUserPattern
    if ($CurrentRunOnly) {
        $pattern = $currentUserPattern
    }
    @(Get-LocalUser -ErrorAction Stop | Where-Object {
        $_.Name -match $pattern -and
        $_.Description -eq $marker
    })
}

function Get-MarkedUserSessions([string] $Name) {
    Initialize-SmokeNativeHelpers
    @([ChuziSmokeSessionQuery]::GetSessions() | Where-Object { $_.UserName -ieq $Name })
}

function Test-UnexpectedRunnerSession {
    if ([string]::IsNullOrWhiteSpace($script:runnerUserName) -or
        $script:runnerSessionID -lt 0) {
        return $false
    }
    $runnerSessions = @(Get-MarkedUserSessions $script:runnerUserName)
    return @($runnerSessions | Where-Object {
        $_.SessionId -ne $script:runnerSessionID
    }).Count -gt 0
}

function Remove-MarkedUserProfile([object] $user) {
    $sid = $user.SID.Value
    if ([string]::IsNullOrWhiteSpace($sid)) {
        throw 'managed profile identity unavailable'
    }
    $profiles = @(Get-CimInstance -ClassName Win32_UserProfile -Filter ("SID='" + $sid + "'") -ErrorAction Stop)
    foreach ($profile in $profiles) {
        if ($profile.Loaded) {
            throw 'managed profile still loaded'
        }
        Remove-CimInstance -InputObject $profile -ErrorAction Stop
    }
}

function Stop-MarkedUserSessions([string] $name) {
    $lines = @(Get-MarkedUserSessions $name)
    foreach ($line in $lines) {
        $sessionID = $line.SessionId
        if (-not (Invoke-SmokeRetry {
            & logoff.exe $sessionID 2>$null 1>$null
            if ($LASTEXITCODE -ne 0) { throw 'session logoff failed' }
        })) {
            throw 'session logoff failed'
        }
    }
    if (@(Get-MarkedUserSessions $name).Count -ne 0) {
        throw 'managed session remained after logoff'
    }
}

function Test-RunOwnership {
    if (-not (Test-Path -LiteralPath $runRoot -PathType Container)) {
        return $false
    }
    $ownershipPath = Join-Path $runRoot '.chuzi-smoke-ownership'
    if (-not (Test-Path -LiteralPath $ownershipPath -PathType Leaf)) {
        return $false
    }
    try {
        return ((Get-Content -LiteralPath $ownershipPath -Raw -ErrorAction Stop).Trim() -eq $ownershipMarker)
    } catch {
        return $false
    }
}

function Test-RunUserOwnership {
    if (-not (Test-RunOwnership)) {
        return $false
    }
    $ownershipPath = Join-Path $runRoot '.chuzi-smoke-user-ownership'
    if (-not (Test-Path -LiteralPath $ownershipPath -PathType Leaf)) {
        return $false
    }
    try {
        return ((Get-Content -LiteralPath $ownershipPath -Raw -ErrorAction Stop).Trim() -eq $userOwnershipMarker)
    } catch {
        return $false
    }
}

function Stop-SmokeResources {
    $usersClean = $true
    $rootClean = $true
    if (Test-RunUserOwnership) {
        try {
            $markedUsers = @(Get-MarkedUsers -CurrentRunOnly)
            foreach ($user in $markedUsers) {
                $profileClean = Invoke-SmokeRetry {
                    Stop-MarkedUserSessions $user.Name
                    Remove-MarkedUserProfile $user
                }
                if (-not $profileClean) {
                    $usersClean = $false
                    continue
                }
                if (-not (Invoke-SmokeRetry {
                    Remove-LocalUser -Name $user.Name -Confirm:$false -ErrorAction Stop
                })) {
                    $usersClean = $false
                }
            }
            if (@(Get-MarkedUsers -CurrentRunOnly).Count -ne 0) {
                $usersClean = $false
            }
        } catch {
            $usersClean = $false
        }
    } elseif (@(Get-MarkedUsers -CurrentRunOnly).Count -ne 0) {
        # Never delete a matching account when the run-owned marker is absent
        # or unreadable. Leave it for explicit operator review.
        $usersClean = $false
    }
    try {
        $script:remainingSmokeUsers = @(Get-MarkedUsers -CurrentRunOnly).Count
        $script:unownedSmokeUsers = @(Get-MarkedUsers | Where-Object {
            $_.Name -notmatch $currentUserPattern
        }).Count
    } catch {
        $script:remainingSmokeUsers = -1
        $usersClean = $false
    }
    if (-not $usersClean) {
        $cleanupErrors.Add('user_cleanup_failed')
    }

    if ($env:CHUZI_PRESERVE_WINDOWS_JOB_POOL_SMOKE_ROOT -eq '1') {
        $script:rootPreserved = $true
    } elseif ($script:preserveRootOnFailure) {
        $script:rootPreserved = $true
    } elseif (-not $usersClean -and (Test-RunOwnership)) {
        # Keep the ownership marker and diagnostics when a managed account or
        # session could not be removed.
        $script:rootPreserved = $true
    } elseif (Test-RunOwnership) {
        if (-not (Invoke-SmokeRetry {
            Remove-Item -LiteralPath $runRoot -Recurse -Force -ErrorAction Stop
        })) {
            $rootClean = $false
        }
    }
    try {
        Remove-SmokeSessionShellSigner
    } catch {
        $cleanupErrors.Add('session_shell_signer_cleanup_failed')
    }
    if (-not $rootClean -and $env:CHUZI_PRESERVE_WINDOWS_JOB_POOL_SMOKE_ROOT -ne '1') {
        $cleanupErrors.Add('root_cleanup_failed')
    }
    return @{ Users = $usersClean; Root = $rootClean }
}

function Invoke-Icacls([string] $path) {
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $system = 'NT AUTHORITY\SYSTEM:(OI)(CI)(F)'
    $administrators = 'BUILTIN\Administrators:(OI)(CI)(F)'
    $users = 'BUILTIN\Users:(OI)(CI)(RX)'
    & icacls.exe $path /inheritance:r /grant:r ($identity + ':(OI)(CI)(F)') $system $administrators $users 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'runtime ACL preparation failed'
    }
    $usersSid = 'S-1-5-32-545'
    $requiredRights = [int][System.Security.AccessControl.FileSystemRights]::ReadAndExecute
    $allowedRights = [int]([System.Security.AccessControl.FileSystemRights]::ReadAndExecute -bor [System.Security.AccessControl.FileSystemRights]::Synchronize)
    foreach ($entry in @($path, (Join-Path $path 'session-shell.ps1'))) {
        $acl = Get-Acl -LiteralPath $entry -ErrorAction Stop
        $hasReadExecute = $false
        foreach ($rule in $acl.Access) {
            try {
                $ruleSid = $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
            } catch {
                continue
            }
            $rights = [int]$rule.FileSystemRights
            if ($ruleSid -eq $usersSid -and
                $rule.AccessControlType -eq [System.Security.AccessControl.AccessControlType]::Allow -and
                ($rights -band $requiredRights) -eq $requiredRights -and
                ($rights -band (-bnot $allowedRights)) -eq 0) {
                $hasReadExecute = $true
            }
        }
        if (-not $hasReadExecute) {
            throw 'runtime ACL does not grant BUILTIN Users read and execute access'
        }
    }
}

function Grant-NativeSmokeSystemAccess {
    & icacls.exe $runRoot /grant:r 'NT AUTHORITY\SYSTEM:(OI)(CI)(F)' 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'LocalSystem smoke workspace access could not be prepared'
    }
    # Node resolves the worker entry point with lstat on every parent. Grant
    # the disposable managed user traverse/read access to this run root only;
    # runtime and data children retain their narrower ACLs and do not inherit.
    & icacls.exe $runRoot /grant:r 'BUILTIN\Users:(RX)' 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'managed smoke user could not traverse the smoke workspace'
    }
}

function Assert-NativeSmokeTaskSupport {
    Import-Module ScheduledTasks -ErrorAction Stop
    foreach ($commandName in @(
        'New-ScheduledTaskAction', 'New-ScheduledTaskPrincipal',
        'New-ScheduledTaskSettingsSet', 'Register-ScheduledTask',
        'Start-ScheduledTask', 'Get-ScheduledTask', 'Get-ScheduledTaskInfo',
        'Stop-ScheduledTask', 'Unregister-ScheduledTask'
    )) {
        if ($null -eq (Get-Command -Name $commandName -ErrorAction SilentlyContinue)) {
            throw 'LocalSystem smoke task support is unavailable'
        }
    }
}

function ConvertTo-PowerShellLiteral([string] $Value) {
    return "'" + $Value.Replace("'", "''") + "'"
}

function Remove-NativeSmokeSystemTask {
    if ([string]::IsNullOrWhiteSpace($script:nativeSmokeTaskName)) {
        return
    }
    $task = Get-ScheduledTask -TaskName $script:nativeSmokeTaskName -ErrorAction SilentlyContinue
    if ($null -ne $task) {
        if ($task.State -eq 'Running') {
            Stop-ScheduledTask -TaskName $script:nativeSmokeTaskName -ErrorAction SilentlyContinue
            $deadline = [DateTime]::UtcNow.AddSeconds(10)
            while ([DateTime]::UtcNow -lt $deadline) {
                $task = Get-ScheduledTask -TaskName $script:nativeSmokeTaskName -ErrorAction SilentlyContinue
                if ($null -eq $task -or $task.State -ne 'Running') {
                    break
                }
                Start-Sleep -Milliseconds 250
            }
        }
        Unregister-ScheduledTask -TaskName $script:nativeSmokeTaskName -Confirm:$false -ErrorAction Stop
    }
    $script:nativeSmokeTaskName = $null
}

function Invoke-NativeSmokeAsSystem([string] $TestBinary) {
    Assert-NativeSmokeTaskSupport
    $taskName = 'ChuziJobPoolSmoke-' + $runID
    $script:nativeSmokeTaskName = $taskName
    $exitPath = Join-Path $runRoot 'native-smoke.exit-code'
    $localRdpValue = if ($LocalRdp) { '1' } else { '0' }
    $runnerScript = @'
$ErrorActionPreference = 'Stop'
$exitCode = 1
$testLog = __TEST_LOG__
$exitPath = __EXIT_PATH__
$env:CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE = '1'
$env:CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT = __RUN_ROOT__
$env:CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT = __AGENT_PATH__
$env:CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE = __NODE_PATH__
$env:CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER = __WORKER_PATH__
$env:CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX = __USER_PREFIX__
$env:CHUZI_WINDOWS_JOB_POOL_SMOKE_RUN_ID = __RUN_ID__
$env:CHUZI_WINDOWS_JOB_POOL_SMOKE_LOCAL_RDP = __LOCAL_RDP__
$stdoutPath = $testLog + '.stdout'
$stderrPath = $testLog + '.stderr'
try {
    $process = Start-Process -FilePath __TEST_BINARY__ -ArgumentList @(
        '-test.run=TestWindowsJobPoolNativeSmoke', '-test.count=1'
    ) -WorkingDirectory __RUN_ROOT__ -Wait -PassThru `
        -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -ErrorAction Stop
    $exitCode = $process.ExitCode
    $stdout = if (Test-Path -LiteralPath $stdoutPath -PathType Leaf) { [System.IO.File]::ReadAllText($stdoutPath) } else { '' }
    $stderr = if (Test-Path -LiteralPath $stderrPath -PathType Leaf) { [System.IO.File]::ReadAllText($stderrPath) } else { '' }
    [System.IO.File]::WriteAllText($testLog, $stdout + [Environment]::NewLine + $stderr)
} catch {
    $runnerException = $_.Exception
    while ($null -ne $runnerException.InnerException) {
        $runnerException = $runnerException.InnerException
    }
    $runnerFailure = ('native smoke runner failed; exception_type=' + $runnerException.GetType().FullName +
        '; hresult=0x{0:X8}' -f $runnerException.HResult)
    [System.IO.File]::WriteAllText($testLog, $runnerFailure)
    $exitCode = 1
}
[System.IO.File]::WriteAllText($exitPath, [string]$exitCode)
exit $exitCode
'@
    $replacements = @{
        '__TEST_LOG__' = (ConvertTo-PowerShellLiteral $testLog)
        '__EXIT_PATH__' = (ConvertTo-PowerShellLiteral $exitPath)
        '__RUN_ROOT__' = (ConvertTo-PowerShellLiteral $runRoot)
        '__AGENT_PATH__' = (ConvertTo-PowerShellLiteral $agentPath)
        '__NODE_PATH__' = (ConvertTo-PowerShellLiteral $nodePath)
        '__WORKER_PATH__' = (ConvertTo-PowerShellLiteral $workerPath)
        '__USER_PREFIX__' = (ConvertTo-PowerShellLiteral $userPrefix)
        '__RUN_ID__' = (ConvertTo-PowerShellLiteral $runID)
        '__LOCAL_RDP__' = (ConvertTo-PowerShellLiteral $localRdpValue)
        '__TEST_BINARY__' = (ConvertTo-PowerShellLiteral $TestBinary)
    }
    foreach ($token in $replacements.Keys) {
        $runnerScript = $runnerScript.Replace($token, [string]$replacements[$token])
    }
    $encodedCommand = [Convert]::ToBase64String([System.Text.Encoding]::Unicode.GetBytes($runnerScript))
    if ($encodedCommand.Length -gt 7000) {
        throw 'LocalSystem smoke runner command exceeded the supported task limit'
    }

    $powerShell = Join-Path $PSHOME 'powershell.exe'
    $action = New-ScheduledTaskAction -Execute $powerShell -Argument ('-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand ' + $encodedCommand) -WorkingDirectory $runRoot
    $principal = New-ScheduledTaskPrincipal -UserId 'NT AUTHORITY\SYSTEM' -LogonType ServiceAccount
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::FromMinutes(6)) -StartWhenAvailable -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName $taskName -Action $action -Principal $principal -Settings $settings -Force | Out-Null
    Start-ScheduledTask -TaskName $taskName -ErrorAction Stop

    $deadline = [DateTime]::UtcNow.AddSeconds(375)
    while ([DateTime]::UtcNow -lt $deadline -and -not (Test-Path -LiteralPath $exitPath -PathType Leaf)) {
        Start-Sleep -Milliseconds 500
    }
    if (-not (Test-Path -LiteralPath $exitPath -PathType Leaf)) {
        $task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        $taskInfo = Get-ScheduledTaskInfo -TaskName $taskName -ErrorAction SilentlyContinue
        $state = if ($null -ne $task) { [string]$task.State } else { 'unavailable' }
        $result = if ($null -ne $taskInfo) { [string]$taskInfo.LastTaskResult } else { 'unavailable' }
        throw ('LocalSystem native smoke task timed out (state=' + $state + ', result=' + $result + ')')
    }

    $exitText = (Get-Content -LiteralPath $exitPath -Raw -ErrorAction Stop).Trim()
    $exitCode = 0
    if (-not [int]::TryParse($exitText, [ref]$exitCode)) {
        throw 'LocalSystem native smoke task returned an invalid exit code'
    }
    $taskExitDeadline = [DateTime]::UtcNow.AddSeconds(10)
    while ([DateTime]::UtcNow -lt $taskExitDeadline) {
        $task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        if ($null -eq $task -or $task.State -ne 'Running') {
            break
        }
        Start-Sleep -Milliseconds 250
    }
    if ($exitCode -ne 0) {
        if (Select-String -LiteralPath $testLog -Pattern 'session_unavailable' -Quiet -ErrorAction SilentlyContinue) {
            $script:failureStage = 'session_unavailable'
        }
        throw 'Windows job-pool native smoke failed'
    }
}

if ($ValidateOnly) {
    if ($env:OS -ne 'Windows_NT') {
        throw 'Windows smoke validation requires Windows'
    }
    $emptyResults = @(& {})
    $singleResults = @(& { 'one' })
    $multipleResults = @(& { 'one'; 'two'; 'three' })
    if ($emptyResults.Count -ne 0 -or $singleResults.Count -ne 1 -or $multipleResults.Count -ne 3) {
        throw 'PowerShell result collection validation failed'
    }
    Initialize-SmokeNativeHelpers
    $registryHelperType = 'ChuziSmokeSessionShellRegistryV2' -as [type]
    if ($null -eq $registryHelperType) {
        throw 'session shell registry helper validation failed'
    }
    if ($null -eq $registryHelperType.GetProperty('LastOperation') -or
        $null -eq $registryHelperType.GetMethod('SetShell')) {
        throw 'session shell registry helper contract validation failed'
    }
    $credentialStoreType = 'ChuziSmokeCredentialStoreV2' -as [type]
    if ($null -eq $credentialStoreType -or $null -eq $credentialStoreType.GetMethod('Exists')) {
        throw 'RDP credential store helper contract validation failed'
    }
    $credentialHelperType = 'ChuziSmokeRdpCredentialOverrideV3' -as [type]
    if ($null -eq $credentialHelperType -or
        $null -eq $credentialHelperType.GetMethod('UserNameMatches') -or
        $null -eq $credentialHelperType.GetMethod('ReadUserName') -or
        $null -eq $credentialHelperType.GetMethod('Write') -or
        $null -eq $credentialHelperType.GetMethod('Restore')) {
        throw 'RDP credential override helper contract validation failed'
    }
    Assert-NativeSmokeTaskSupport
    $profileEvidence = @(Get-SmokeRdpProfileLines 'loopback-target' 'smoke-user')
    if ($profileEvidence.Count -ne 11 -or
        $profileEvidence[0] -ne 'full address:s:loopback-target:3389' -or
        $profileEvidence[2] -ne 'prompt for credentials:i:0' -or
        $profileEvidence[3] -ne 'administrative session:i:0' -or
        @($profileEvidence | Where-Object { $_ -match '^password(?: 51)?:' }).Count -ne 0) {
        throw 'RDP profile evidence contract validation failed'
    }
    if (-not [string]::IsNullOrWhiteSpace($RdpProfileTemplatePath)) {
        [void](Get-LocalRdpTarget $RdpProfileTemplatePath)
    }
    Write-Host 'Windows job-pool smoke script validation passed'
    return
}

try {
    if ($env:OS -ne 'Windows_NT') {
        $failureStage = 'windows_required'
        throw 'Windows native smoke requires Windows'
    }
    $failureStage = 'administrator_required'
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [System.Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Windows native smoke requires an administrator runner'
    }
    if ($LocalRdp) {
        $failureStage = 'rdp_endpoint_discovery'
        $script:rdpTargetHost = Get-LocalRdpTarget $RdpProfileTemplatePath
    }
    $failureStage = 'root_setup'
    New-Item -ItemType Directory -Path $runRoot -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $runRoot '.chuzi-smoke-ownership') -Value $ownershipMarker -NoNewline -Encoding ASCII
    Set-Content -LiteralPath (Join-Path $runRoot '.chuzi-smoke-user-ownership') -Value $userOwnershipMarker -NoNewline -Encoding ASCII
    $failureStage = 'root_acl'
    Grant-NativeSmokeSystemAccess
    $failureStage = 'runtime_setup'
    $runtimeRoot = Join-Path $runRoot 'runtime'
    $workerRoot = Join-Path $runtimeRoot 'browser-worker/src'
    $dataRoot = Join-Path $runRoot 'data'
    New-Item -ItemType Directory -Path $workerRoot -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $dataRoot 'profiles') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $dataRoot 'backups') -Force | Out-Null

    $failureStage = 'toolchain_required'
    $go = (Get-Command go.exe -CommandType Application -ErrorAction Stop).Source
    $node = (Get-Command node.exe -CommandType Application -ErrorAction Stop).Source
    $agentPath = Join-Path $runtimeRoot 'chuzi-user-agent.exe'
    $nodePath = Join-Path $runtimeRoot 'node.exe'
    $workerPath = Join-Path $workerRoot 'worker.mjs'
    $sessionShellPath = Join-Path $runtimeRoot 'session-shell.ps1'

    $failureStage = 'build_agent'
    & $go build -trimpath -o $agentPath (Join-Path $repoRoot 'cmd/user-agent') 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'user-agent build failed'
    }
    $nativeSmokePath = Join-Path $runRoot 'native-smoke.test.exe'
    $failureStage = 'build_smoke_test'
    & $go test -c -o $nativeSmokePath ./internal/slotwindows 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'native smoke test build failed'
    }
    Copy-Item -LiteralPath $node -Destination $nodePath -Force
    Copy-Item -LiteralPath (Join-Path $repoRoot 'browser-worker/src/worker.mjs') -Destination $workerPath -Force
    Copy-Item -LiteralPath (Join-Path $repoRoot 'scripts/session-shell.ps1') -Destination $sessionShellPath -Force
    Sign-SmokeSessionShell $sessionShellPath
    $failureStage = 'runtime_acl'
    Invoke-Icacls $runtimeRoot

    $env:CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE = '1'
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT = $runRoot
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT = $agentPath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE = $nodePath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER = $workerPath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX = $userPrefix
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_RUN_ID = $runID
    if ($LocalRdp) {
        $failureStage = 'local_rdp_session'
        Start-LocalRdpSession ($userPrefix + '0001') $runtimeRoot $script:rdpTargetHost
        $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_LOCAL_RDP = '1'
    }

    $failureStage = 'native_smoke'
    New-Item -ItemType File -Path $testLog -Force | Out-Null
    Invoke-NativeSmokeAsSystem $nativeSmokePath
    $smokePassed = $true
    } catch {
        $script:failureDetail = 'redacted'
        if ($failureStage -eq 'local_rdp_session') {
            if ($null -ne $_.InvocationInfo) {
                $script:rdpFailureLine = [string]$_.InvocationInfo.ScriptLineNumber
            }
            if (-not $script:rdpFailureDetailsCaptured) {
                $cause = $_.Exception
                while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
                $script:rdpFailureExceptionType = $cause.GetType().FullName
                $script:rdpFailureHResult = '0x{0:X8}' -f $cause.HResult
                $script:rdpFailureCategory = Get-RdpFailureCategory $_.Exception
            }
            Write-RdpDiagnostic ('FAILURE_PHASE=' + $script:rdpFailurePhase)
            Write-RdpDiagnostic ('FAILURE_SCRIPT_LINE=' + $script:rdpFailureLine)
            Write-RdpDiagnostic ('FAILURE_OPERATION=' + $script:rdpFailureOperation)
            Write-RdpDiagnostic ('FAILURE_EXCEPTION_TYPE=' + $script:rdpFailureExceptionType)
            Write-RdpDiagnostic ('FAILURE_HRESULT=' + $script:rdpFailureHResult)
            Write-RdpDiagnostic ('FAILURE_CATEGORY=' + $script:rdpFailureCategory)
            Write-RdpDiagnostic ('FAILURE_UNLOAD_ATTEMPTS=' + $script:rdpFailureUnloadAttempts)
            Write-RdpDiagnostic ('FAILURE_UNLOAD_EXIT_CODE=' + $script:rdpFailureUnloadExitCode)
            Write-RdpDiagnostic ('FAILURE_UNLOAD_HIVE_MOUNTED=' + $script:rdpFailureUnloadHiveMounted)
        }
        if (Test-Path -LiteralPath $testLog -PathType Leaf) {
        try {
            Copy-Item -LiteralPath $testLog -Destination $preservedLog -Force
        } catch {
            $cleanupErrors.Add('test_log_preservation_failed')
        }
    } else {
        try {
            $failureRecord = @('failure_stage=' + $failureStage) + @($script:rdpDebugSummary)
            if ($failureStage -eq 'local_rdp_session') {
                $failureRecord += 'local_rdp_phase=' + $script:rdpFailurePhase
                $failureRecord += 'local_rdp_failure_line=' + $script:rdpFailureLine
                $failureRecord += 'local_rdp_exception_type=' + $script:rdpFailureExceptionType
                $failureRecord += 'local_rdp_hresult=' + $script:rdpFailureHResult
                $failureRecord += 'local_rdp_failure_category=' + $script:rdpFailureCategory
                $failureRecord += 'local_rdp_failure_operation=' + $script:rdpFailureOperation
                $failureRecord += 'local_rdp_unload_attempts=' + $script:rdpFailureUnloadAttempts
                $failureRecord += 'local_rdp_unload_exit_code=' + $script:rdpFailureUnloadExitCode
                $failureRecord += 'local_rdp_unload_hive_mounted=' + $script:rdpFailureUnloadHiveMounted
            }
            if (-not [string]::IsNullOrWhiteSpace($script:failureDetail)) {
                $failureRecord += 'failure_detail=' + $script:failureDetail
            }
            Set-Content -LiteralPath $preservedLog -Value $failureRecord -Encoding ASCII
        } catch {
            $cleanupErrors.Add('test_log_preservation_failed')
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($script:rdpDiagnosticsPath)) {
        try {
            Add-Content -LiteralPath $preservedLog -Value ('rdp_diagnostics=' + $script:rdpDiagnosticsPath) -Encoding ASCII
        } catch {
            $cleanupErrors.Add('rdp_diagnostics_reference_failed')
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($nativeTokenDiagnosticsPath)) {
        try {
            Add-Content -LiteralPath $preservedLog -Value ('native_token_diagnostics=' + $nativeTokenDiagnosticsPath) -Encoding ASCII
        } catch {
            $cleanupErrors.Add('native_token_diagnostics_reference_failed')
        }
    }
} finally {
    Remove-Item Env:CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_RUN_ID -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_LOCAL_RDP -ErrorAction SilentlyContinue
    if (-not [string]::IsNullOrWhiteSpace($script:nativeSmokeTaskName)) {
        try {
            Remove-NativeSmokeSystemTask
        } catch {
            $cleanupErrors.Add('native_smoke_task_cleanup_failed')
        }
    }
    if ($null -ne $script:rdpClientProcess -and -not $script:rdpClientProcess.HasExited) {
        Stop-Process -InputObject $script:rdpClientProcess -Force -ErrorAction SilentlyContinue
    }
    if ($script:rdpCredentialOwned) {
        try {
            Remove-SmokeRdpCredential $script:rdpCredentialTarget
            $script:rdpCredentialOwned = $false
        } catch {
            $cleanupErrors.Add('rdp_credential_cleanup_failed')
        }
    }
    if ($null -ne $script:rdpProfilePath) {
        Remove-Item -LiteralPath $script:rdpProfilePath -Force -ErrorAction SilentlyContinue
        $script:rdpProfilePath = $null
    }
    if (-not $smokePassed -and $failureStage -eq 'local_rdp_session') {
        $script:preserveRootOnFailure = $true
    }
    $cleanupResult = Stop-SmokeResources
    $summaryPassed = $smokePassed -and $script:remainingSmokeUsers -eq 0 -and $cleanupErrors.Count -eq 0 -and $cleanupResult.Users -and $cleanupResult.Root
    if ($summaryPassed) {
        try {
            $successRecord = @(
                'status=passed',
                ('smoke_root=' + $runRoot),
                ('remaining_smoke_users=' + $script:remainingSmokeUsers),
                ('unowned_smoke_users=' + $script:unownedSmokeUsers),
                ('rdp_diagnostics=' + $script:rdpDiagnosticsPath),
                ('native_token_diagnostics=' + $nativeTokenDiagnosticsPath)
            )
            Set-Content -LiteralPath $preservedLog -Value $successRecord -Encoding ASCII
        } catch {
            $cleanupErrors.Add('success_summary_write_failed')
            $summaryPassed = $false
        }
    }
    Write-Host ('RemainingSmokeUsers = ' + $script:remainingSmokeUsers)
    Write-Host ('UnownedSmokeUsers = ' + $script:unownedSmokeUsers)
    if (Test-RunOwnership) {
        Write-Host ('SmokeRoot = ' + $runRoot)
    }
    if ($summaryPassed) {
        if ($script:rootPreserved) {
            Write-Host 'RemainingSmokeRoots = preserved'
        } else {
            Write-Host 'RemainingSmokeRoots = 0'
        }
        Write-Host 'Windows job-pool native smoke passed'
        exit 0
    }
    if ($smokePassed) {
        [Console]::Error.WriteLine('Windows job-pool native smoke failed: cleanup_failed')
    } else {
        [Console]::Error.WriteLine('Windows job-pool native smoke failed: ' + $failureStage)
        if (-not [string]::IsNullOrWhiteSpace($script:failureDetail)) {
            [Console]::Error.WriteLine('Failure detail: ' + $script:failureDetail)
        }
    }
    foreach ($cleanupError in $cleanupErrors) {
        [Console]::Error.WriteLine($cleanupError)
    }
    exit 1
}
