[CmdletBinding()]
param(
    [switch] $LocalRdp,
    [switch] $ValidateOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$tempRoot = if (-not [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) {
    $env:RUNNER_TEMP
} else {
    [System.IO.Path]::GetTempPath()
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
$script:rdpProfilePath = $null
$script:rdpDebugSummary = @()
$script:rdpClientProcess = $null
$script:runnerUserName = $null
$script:runnerSessionID = -1
$script:runnerSID = $null
$script:failureDetail = $null
$script:rdpDiagnosticsPath = $null
$script:rdpStartTime = $null
$script:lastRdpDiagnosticAt = [DateTime]::MinValue
$script:preserveRootOnFailure = $false
$failureStage = 'setup'
$testLog = Join-Path $runRoot 'test-output.log'
$preservedLog = Join-Path $tempRoot 'chuzi-job-pool-smoke-test-output.log'

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

function Initialize-SmokeNativeHelpers {
    if ('ChuziSmokeCredentialStore' -as [type]) {
        return
    }
    Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security;
using System.Text;

public static class ChuziSmokeCredentialStore
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
        if (error == 1168)
            return false;
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
        if (!created)
            throw new Win32Exception(Marshal.GetLastWin32Error());

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

function Initialize-SmokeUserProfile([string] $Name, [System.Security.SecureString] $Password) {
    $plainPassword = Convert-SmokeSecureStringToPlainText $Password
    try {
        [ChuziSmokeProfileBootstrap]::Run($Name, $env:COMPUTERNAME, $plainPassword)
    } finally {
        $plainPassword = $null
    }
    $targetUser = Get-LocalUser -Name $Name -ErrorAction Stop
    $profile = Wait-SmokeUserProfile $targetUser.SID.Value
    if ($null -eq $profile) {
        throw 'local RDP user profile did not initialize'
    }
    Write-Host ('RDP DEBUG: preinitialized_user_profile=' + $profile.LocalPath)
    Write-Host ('RDP DEBUG: preinitialized_user_ntuser_dat=' + (Join-Path $profile.LocalPath 'NTUSER.DAT'))
}

function Write-RdpDiagnostic([string] $Line) {
    if ([string]::IsNullOrWhiteSpace($script:rdpDiagnosticsPath)) {
        return
    }
    Add-Content -LiteralPath $script:rdpDiagnosticsPath -Value $Line -Encoding UTF8
}

function Capture-RdpDiagnostics([string] $Label, [string] $Name) {
    if ([string]::IsNullOrWhiteSpace($script:rdpDiagnosticsPath)) {
        return
    }
    Write-RdpDiagnostic ("`n=== " + $Label + ' @ ' + (Get-Date -Format o) + ' ===')
    try {
        Write-RdpDiagnostic ('RUNNER=' + [System.Security.Principal.WindowsIdentity]::GetCurrent().Name)
        Write-RdpDiagnostic ('RUNNER_SESSION=' + (Get-Process -Id $PID -ErrorAction Stop).SessionId)
    } catch {
        Write-RdpDiagnostic ('RUNNER_QUERY_ERROR=' + $_.Exception.Message)
    }
    try {
        Write-RdpDiagnostic 'MSTSC_PROCESSES:'
        Write-RdpDiagnostic ((Get-CimInstance Win32_Process -Filter "Name='mstsc.exe'" -ErrorAction SilentlyContinue |
            Select-Object ProcessId, SessionId, CommandLine | Format-List | Out-String).TrimEnd())
    } catch {
        Write-RdpDiagnostic ('MSTSC_QUERY_ERROR=' + $_.Exception.Message)
    }
    try {
        Write-RdpDiagnostic 'WTS_SESSIONS:'
        Write-RdpDiagnostic ((& qwinsta 2>&1 | Out-String).TrimEnd())
        $managedSessions = @(Get-MarkedUserSessions $Name)
        Write-RdpDiagnostic ('TARGET_WTS=' + (($managedSessions | ForEach-Object { $_.SessionId.ToString() + ':' + $_.UserName + ':' + $_.State }) -join ','))
        if (-not [string]::IsNullOrWhiteSpace($script:runnerUserName)) {
            $runnerSessions = @(Get-MarkedUserSessions $script:runnerUserName)
            Write-RdpDiagnostic ('RUNNER_WTS=' + (($runnerSessions | ForEach-Object { $_.SessionId.ToString() + ':' + $_.UserName + ':' + $_.State }) -join ','))
        }
    } catch {
        Write-RdpDiagnostic ('WTS_QUERY_ERROR=' + $_.Exception.Message)
    }
    try {
        Write-RdpDiagnostic 'CREDENTIAL_TARGET:'
        Write-RdpDiagnostic ((& cmdkey.exe /list:$script:rdpCredentialTarget 2>&1 | Out-String).TrimEnd())
    } catch {
        Write-RdpDiagnostic ('CREDENTIAL_QUERY_ERROR=' + $_.Exception.Message)
    }
    $since = if ($null -ne $script:rdpStartTime) { $script:rdpStartTime } else { (Get-Date).AddMinutes(-2) }
    $channels = @(
        'Microsoft-Windows-TerminalServices-RDPClient/Operational',
        'Microsoft-Windows-TerminalServices-LocalSessionManager/Operational',
        'Microsoft-Windows-TerminalServices-RemoteConnectionManager/Operational',
        'Microsoft-Windows-RemoteDesktopServices-RdpCoreTS/Operational'
    )
    foreach ($channel in $channels) {
        try {
            $events = @(Get-WinEvent -FilterHashtable @{ LogName = $channel; StartTime = $since } -ErrorAction SilentlyContinue |
                Select-Object -First 20 TimeCreated, Id, Message)
            if ($events.Count -gt 0) {
                Write-RdpDiagnostic ('EVENTS_' + $channel + ':')
                Write-RdpDiagnostic (($events | Format-List | Out-String).TrimEnd())
            }
        } catch {
            Write-RdpDiagnostic ('EVENT_QUERY_ERROR=' + $channel + ':' + $_.Exception.Message)
        }
    }
}

function Get-LocalRdpTarget {
    return '127.0.0.2'
}

function Start-LocalRdpSession([string] $Name) {
    if (-not [Environment]::UserInteractive) {
        throw 'interactive smoke console required'
    }
    Initialize-SmokeNativeHelpers
    $targetHost = Get-LocalRdpTarget
    $script:rdpCredentialTarget = 'TERMSRV/' + $targetHost
    if ([ChuziSmokeCredentialStore]::Exists($script:rdpCredentialTarget)) {
        throw 'local RDP credential target already exists'
    }

    # Credential Manager stores the canonical computer-qualified identity. Keep
    # the same form in the RDP profile so mstsc does not fall back to the
    # interactive runner when the saved credential username differs.
    $targetUsername = $env:COMPUTERNAME + '\' + $Name
    $credentialUsername = $targetUsername
    $password = New-SmokePassword
    $debugPassword = Convert-SmokeSecureStringToPlainText $password
    try {
        if (Get-LocalUser -Name $Name -ErrorAction SilentlyContinue) {
            throw 'temporary smoke user already exists'
        }
        New-LocalUser -Name $Name -Password $password -Description $marker -PasswordNeverExpires -ErrorAction Stop | Out-Null
        $rdpGroup = Get-LocalGroup -SID ([System.Security.Principal.SecurityIdentifier]::new('S-1-5-32-555')) -ErrorAction Stop
        Add-LocalGroupMember -Group $rdpGroup.Name -Member $Name -ErrorAction Stop
        [ChuziSmokeCredentialStore]::Write($script:rdpCredentialTarget, $credentialUsername, $password)
        $script:rdpCredentialOwned = $true
        Initialize-SmokeUserProfile $Name $password
    } finally {
        $password.Dispose()
    }

    $rdpProfile = Join-Path $runRoot 'local-rdp.rdp'
    $script:rdpDiagnosticsPath = Join-Path $runRoot 'rdp-diagnostics.log'
    New-Item -ItemType File -Path $script:rdpDiagnosticsPath -Force | Out-Null
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
        ('smoke_runner_user=' + $runnerIdentity),
        ('smoke_runner_session_id=' + $runnerSessionID),
        ('smoke_runner_sid=' + $script:runnerSID),
        ('rdp_target_host=' + $targetHost),
        ('rdp_target_user=' + $targetUsername),
        ('rdp_target_sid=' + $targetUser.SID.Value),
        ('rdp_credential_target=' + $script:rdpCredentialTarget),
        ('rdp_profile=' + $rdpProfile),
        'rdp_password=printed_to_console'
    )
    foreach ($line in $script:rdpDebugSummary) {
        Write-Host ('RDP DEBUG: ' + $line)
    }
    Write-Host ('RDP DEBUG: rdp_password=' + $debugPassword)
    Write-Host ('RDP DEBUG: rdp_diagnostics=' + $script:rdpDiagnosticsPath)
    @("full address:s:${targetHost}:3389",
      "username:s:$targetUsername",
      'prompt for credentials:i:0',
      'administrative session:i:0',
      'screen mode id:i:2',
      'session bpp:i:32',
      'compression:i:1',
      'redirectclipboard:i:1',
      'autoreconnection enabled:i:1',
      'authentication level:i:2',
      'negotiate security layer:i:1') |
        Set-Content -LiteralPath $rdpProfile -Encoding ASCII
    $script:rdpProfilePath = $rdpProfile

    Write-Host ('Opening a local RDP session for the disposable smoke user at ' + $targetHost + '.')
    Write-Host 'If Windows shows a first-connection certificate prompt, verify the local target and accept it.'
    Capture-RdpDiagnostics 'before_mstsc' $Name
    $mstsc = Join-Path $env:SystemRoot 'System32\mstsc.exe'
    $script:rdpStartTime = Get-Date
    $mstscArguments = @('"' + $rdpProfile + '"')
    Write-RdpDiagnostic ('MSTSC_LAUNCH=' + $mstsc + ' ' + ($mstscArguments -join ' '))
    $client = Start-Process -FilePath $mstsc -ArgumentList $mstscArguments -PassThru -ErrorAction Stop
    $script:rdpClientProcess = $client
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ((Get-Date) - $script:lastRdpDiagnosticAt -gt [TimeSpan]::FromSeconds(5)) {
            Capture-RdpDiagnostics 'rdp_poll' $Name
            $script:lastRdpDiagnosticAt = Get-Date
        }
        if (Test-UnexpectedRunnerSession) {
            throw 'local RDP authenticated as the interactive runner identity'
        }
        if (Test-ActiveManagedSession $Name) {
            $userProfile = Wait-SmokeUserProfile $targetUser.SID.Value
            if ($null -eq $userProfile) {
                throw 'local RDP user profile did not initialize'
            }
            Write-Host ('RDP DEBUG: rdp_user_profile=' + $userProfile.LocalPath)
            Write-Host ('RDP DEBUG: rdp_user_ntuser_dat=' + (Join-Path $userProfile.LocalPath 'NTUSER.DAT'))
            [ChuziSmokeCredentialStore]::Delete($script:rdpCredentialTarget)
            $script:rdpCredentialOwned = $false
            return
        }
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
    & icacls.exe $path /inheritance:r /grant:r ($identity + ':(OI)(CI)(F)') $system $administrators $users /T /C 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'runtime ACL preparation failed'
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
    $failureStage = 'root_setup'
    New-Item -ItemType Directory -Path $runRoot -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $runRoot '.chuzi-smoke-ownership') -Value $ownershipMarker -NoNewline -Encoding ASCII
    Set-Content -LiteralPath (Join-Path $runRoot '.chuzi-smoke-user-ownership') -Value $userOwnershipMarker -NoNewline -Encoding ASCII
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

    $failureStage = 'build_agent'
    & $go build -trimpath -o $agentPath (Join-Path $repoRoot 'cmd/user-agent') 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'user-agent build failed'
    }
    Copy-Item -LiteralPath $node -Destination $nodePath -Force
    Copy-Item -LiteralPath (Join-Path $repoRoot 'browser-worker/src/worker.mjs') -Destination $workerPath -Force
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
        Start-LocalRdpSession ($userPrefix + '0001')
        $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_LOCAL_RDP = '1'
    }

    $failureStage = 'native_smoke'
    New-Item -ItemType File -Path $testLog -Force | Out-Null
    & $go test -count=1 -run '^TestWindowsJobPoolNativeSmoke$' ./internal/slotwindows 1>$testLog 2>&1
    if ($LASTEXITCODE -ne 0) {
        if (Select-String -LiteralPath $testLog -Pattern 'session_unavailable' -Quiet) {
            $failureStage = 'session_unavailable'
        }
        throw 'Windows job-pool native smoke failed'
    }
    $smokePassed = $true
    } catch {
        $script:failureDetail = ([string]$_.Exception.Message).Replace([Environment]::NewLine, ' ').Trim()
        if (Test-Path -LiteralPath $testLog -PathType Leaf) {
        try {
            Copy-Item -LiteralPath $testLog -Destination $preservedLog -Force
        } catch {
            $cleanupErrors.Add('test_log_preservation_failed')
        }
    } else {
        try {
            $failureRecord = @('failure_stage=' + $failureStage) + @($script:rdpDebugSummary)
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
} finally {
    Remove-Item Env:CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_RUN_ID -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_LOCAL_RDP -ErrorAction SilentlyContinue
    if ($script:rdpCredentialOwned) {
        try {
            [ChuziSmokeCredentialStore]::Delete($script:rdpCredentialTarget)
            $script:rdpCredentialOwned = $false
        } catch {
            $cleanupErrors.Add('rdp_credential_cleanup_failed')
        }
    }
    if ($null -ne $script:rdpClientProcess -and -not $script:rdpClientProcess.HasExited) {
        Stop-Process -InputObject $script:rdpClientProcess -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $script:rdpProfilePath) {
        Remove-Item -LiteralPath $script:rdpProfilePath -Force -ErrorAction SilentlyContinue
        $script:rdpProfilePath = $null
    }
    if (-not $smokePassed -and $failureStage -eq 'local_rdp_session') {
        $script:preserveRootOnFailure = $true
    }
    $cleanupResult = Stop-SmokeResources
    Write-Host ('RemainingSmokeUsers = ' + $script:remainingSmokeUsers)
    Write-Host ('UnownedSmokeUsers = ' + $script:unownedSmokeUsers)
    if (Test-RunOwnership) {
        Write-Host ('SmokeRoot = ' + $runRoot)
    }
    if ($smokePassed -and $script:remainingSmokeUsers -eq 0 -and $cleanupErrors.Count -eq 0 -and $cleanupResult.Users -and $cleanupResult.Root) {
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
