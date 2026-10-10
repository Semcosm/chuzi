#Requires -Version 5.1
#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [switch]$ValidateOnly,
    [switch]$AllowTestTrust,
    [string]$ProbeBinary = $env:CHUZI_SESSION_PROBE_BINARY
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Test-Command([string]$Name) {
    return $null -ne (Get-Command $Name -ErrorAction SilentlyContinue)
}

function Assert-ValidateOnlyContract {
    $required = @('mstsc.exe', 'cmdkey.exe', 'schtasks.exe')
    foreach ($name in $required) {
        if (-not (Test-Command $name)) { throw 'required native command is unavailable' }
    }
    $shell = Join-Path $PSScriptRoot '..\session-shell.ps1'
    if (-not (Test-Path -LiteralPath $shell -PathType Leaf)) { throw 'fixed session shell is unavailable' }
    if ([string]::IsNullOrWhiteSpace($ProbeBinary) -or -not (Test-Path -LiteralPath $ProbeBinary -PathType Leaf)) {
        throw 'compiled native probe is unavailable'
    }
}

if ($ValidateOnly) {
    try {
        Assert-ValidateOnlyContract
        Write-Output 'Windows session login probe contract is valid.'
        exit 0
    }
    catch {
        Write-Error 'Windows session login probe contract is invalid.'
        exit 1
    }
}

if ($env:CHUZI_ENABLE_WINDOWS_SESSION_LOGIN_PROBE -ne '1' -or -not $AllowTestTrust) {
    Write-Error 'native login probe requires CHUZI_ENABLE_WINDOWS_SESSION_LOGIN_PROBE=1 and -AllowTestTrust'
    exit 2
}

Assert-ValidateOnlyContract
if ([string]::IsNullOrWhiteSpace($env:SESSIONNAME) -or [int]$env:SESSIONID -lt 0) {
    Write-Error 'probe must run from an interactive operator session'
    exit 2
}

$runID = [Guid]::NewGuid().ToString('N')
$tempRoot = if ([string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) { Join-Path $env:windir 'Temp' } else { $env:RUNNER_TEMP }
$runRoot = Join-Path $tempRoot ('chuzi-session-login-' + $runID)
$userName = 'Cz' + $runID.Substring(0, 9) + '0001'
$marker = 'CHUZI-SESSION-PROBE:' + $runID
$credentialTarget = $null
$credentialWritten = $false
$userSID = $null
$profilePath = $null
$firstSessionID = 0
$secondSessionID = 0
$mstsc = @()
$signingThumbprint = $null
$taskName = 'ChuziSessionLoginProbe-' + $runID
$result = $false
$cleanupFailed = $false
$password = $null

Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Security;

public static class ChuziSessionProbeNativeV1
{
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct NativeCredential
    {
        public uint Flags; public uint Type; public IntPtr TargetName; public IntPtr Comment;
        public long LastWritten; public uint CredentialBlobSize; public IntPtr CredentialBlob;
        public uint Persist; public uint AttributeCount; public IntPtr Attributes;
        public IntPtr TargetAlias; public IntPtr UserName;
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct StartupInfo
    {
        public int cb; public string lpReserved; public string lpDesktop; public string lpTitle;
        public int dwX; public int dwY; public int dwXSize; public int dwYSize;
        public int dwXCountChars; public int dwYCountChars; public int dwFillAttribute;
        public int dwFlags; public short wShowWindow; public short cbReserved2;
        public IntPtr lpReserved2; public IntPtr hStdInput; public IntPtr hStdOutput; public IntPtr hStdError;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct ProcessInformation { public IntPtr hProcess; public IntPtr hThread; public int processId; public int threadId; }

    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredWrite(ref NativeCredential credential, uint flags);
    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredRead(string targetName, uint type, uint flags, out IntPtr credential);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern void CredFree(IntPtr credential);
    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CredDelete(string targetName, uint type, uint flags);
    [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CreateProcessWithLogonW(string userName, string domain, IntPtr password, uint logonFlags, string applicationName, IntPtr commandLine, uint creationFlags, IntPtr environment, string currentDirectory, ref StartupInfo startupInfo, out ProcessInformation processInformation);
    [DllImport("kernel32.dll", SetLastError = true)] private static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);
    [DllImport("kernel32.dll", SetLastError = true)] private static extern bool CloseHandle(IntPtr handle);

    public static void WriteCredential(string target, string user, SecureString password)
    {
        if (CredentialExists(target)) throw new InvalidOperationException("credential target is occupied");
        IntPtr targetPtr = IntPtr.Zero, userPtr = IntPtr.Zero, passwordPtr = IntPtr.Zero;
        try
        {
            targetPtr = Marshal.StringToHGlobalUni(target); userPtr = Marshal.StringToHGlobalUni(user);
            passwordPtr = Marshal.SecureStringToGlobalAllocUnicode(password);
            NativeCredential value = new NativeCredential { Type = 2, TargetName = targetPtr, UserName = userPtr, CredentialBlob = passwordPtr, CredentialBlobSize = checked((uint)(password.Length * 2)), Persist = 1 };
            if (!CredWrite(ref value, 0)) throw new Win32Exception(Marshal.GetLastWin32Error());
        }
        finally
        {
            if (passwordPtr != IntPtr.Zero) Marshal.ZeroFreeGlobalAllocUnicode(passwordPtr);
            if (userPtr != IntPtr.Zero) Marshal.FreeHGlobal(userPtr);
            if (targetPtr != IntPtr.Zero) Marshal.FreeHGlobal(targetPtr);
        }
    }

    private static bool CredentialExists(string target)
    {
        IntPtr credential;
        if (CredRead(target, 2, 0, out credential))
        {
            CredFree(credential);
            return true;
        }
        int error = Marshal.GetLastWin32Error();
        if (error == 1168) return false;
        throw new Win32Exception(error);
    }

    public static void DeleteCredential(string target, string expectedUser)
    {
        IntPtr credential;
        if (CredRead(target, 2, 0, out credential))
        {
            try
            {
                NativeCredential value = (NativeCredential)Marshal.PtrToStructure(credential, typeof(NativeCredential));
                string actualUser = value.UserName == IntPtr.Zero ? String.Empty : (Marshal.PtrToStringUni(value.UserName) ?? String.Empty);
                if (!String.Equals(actualUser, expectedUser, StringComparison.OrdinalIgnoreCase)) throw new InvalidOperationException("credential ownership changed");
            }
            finally { CredFree(credential); }
        }
        else
        {
            int readError = Marshal.GetLastWin32Error();
            if (readError == 1168) return;
            throw new Win32Exception(readError);
        }
        if (!CredDelete(target, 2, 0))
        {
            int error = Marshal.GetLastWin32Error();
            if (error != 1168) throw new Win32Exception(error);
        }
    }

    public static void BootstrapProfile(string user, SecureString password)
    {
        IntPtr passwordPtr = IntPtr.Zero;
        ProcessInformation info = new ProcessInformation();
        try
        {
            passwordPtr = Marshal.SecureStringToGlobalAllocUnicode(password);
            string system = Environment.GetFolderPath(Environment.SpecialFolder.System);
            StartupInfo startup = new StartupInfo { cb = Marshal.SizeOf(typeof(StartupInfo)) };
            IntPtr command = Marshal.StringToHGlobalUni("cmd.exe /d /s /c exit");
            try
            {
                if (!CreateProcessWithLogonW(user, ".", passwordPtr, 1, system + "\\cmd.exe", command, 0x400 | 0x08000000, IntPtr.Zero, system, ref startup, out info)) throw new Win32Exception(Marshal.GetLastWin32Error());
            }
            finally { Marshal.FreeHGlobal(command); }
            WaitForSingleObject(info.hProcess, 30000);
        }
        finally
        {
            if (info.hThread != IntPtr.Zero) CloseHandle(info.hThread);
            if (info.hProcess != IntPtr.Zero) CloseHandle(info.hProcess);
            if (passwordPtr != IntPtr.Zero) Marshal.ZeroFreeGlobalAllocUnicode(passwordPtr);
        }
    }
}

public static class ChuziSessionProbeWtsV1
{
    [StructLayout(LayoutKind.Sequential)] private struct Info { public uint SessionId; public IntPtr Name; public uint State; }
    [DllImport("wtsapi32.dll", CharSet = CharSet.Unicode)] private static extern IntPtr WTSOpenServer(string name);
    [DllImport("wtsapi32.dll")] private static extern void WTSCloseServer(IntPtr server);
    [DllImport("wtsapi32.dll", CharSet = CharSet.Unicode)] private static extern bool WTSEnumerateSessions(IntPtr server, int reserved, int version, out IntPtr info, out int count);
    [DllImport("wtsapi32.dll")] private static extern void WTSFreeMemory(IntPtr memory);
    [DllImport("wtsapi32.dll", CharSet = CharSet.Unicode)] private static extern bool WTSQuerySessionInformation(IntPtr server, uint sessionId, int classType, out IntPtr buffer, out int bytes);

    public static int FindActiveSession(string user)
    {
        IntPtr server = WTSOpenServer(null);
        if (server == IntPtr.Zero) return -1;
        try
        {
            IntPtr basePtr; int count;
            if (!WTSEnumerateSessions(server, 0, 1, out basePtr, out count)) return -1;
            try
            {
                int size = Marshal.SizeOf(typeof(Info));
                for (int i = 0; i < count; i++)
                {
                    Info item = (Info)Marshal.PtrToStructure(IntPtr.Add(basePtr, i * size), typeof(Info));
                    if (item.State != 0) continue;
                    IntPtr value; int bytes;
                    if (!WTSQuerySessionInformation(server, item.SessionId, 5, out value, out bytes)) continue;
                    try
                    {
                        string actual = Marshal.PtrToStringUni(value) ?? String.Empty;
                        if (String.Equals(actual, user, StringComparison.OrdinalIgnoreCase)) return checked((int)item.SessionId);
                    }
                    finally { WTSFreeMemory(value); }
                }
            }
            finally { WTSFreeMemory(basePtr); }
        }
        finally { WTSCloseServer(server); }
        return -1;
    }
}
'@

function Wait-ActiveSession([string]$Name, [int]$TimeoutSeconds = 45) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $id = [ChuziSessionProbeWtsV1]::FindActiveSession($Name)
        if ($id -ge 0) { return $id }
        Start-Sleep -Milliseconds 500
    }
    return -1
}

function Wait-NoSession([string]$Name, [int]$TimeoutSeconds = 30) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ([ChuziSessionProbeWtsV1]::FindActiveSession($Name) -lt 0) { return $true }
        Start-Sleep -Milliseconds 500
    }
    return $false
}

function Remove-OwnedProfile([string]$SID, [string]$ExpectedPath) {
    if ([string]::IsNullOrWhiteSpace($SID) -or [string]::IsNullOrWhiteSpace($ExpectedPath)) {
        return
    }
    $profiles = @(Get-CimInstance Win32_UserProfile -Filter ("SID='" + $SID + "'") -ErrorAction SilentlyContinue)
    foreach ($profile in $profiles) {
        if ([string]$profile.LocalPath -ine $ExpectedPath) {
            throw 'probe profile ownership mismatch'
        }
        if ($profile.Loaded) {
            throw 'probe profile remained loaded'
        }
        Remove-CimInstance -InputObject $profile -ErrorAction Stop
    }
}

function Wait-ShellReady([string]$SID, [int]$SessionID, [int]$TimeoutSeconds = 45) {
    $name = 'Global\ChuziSessionShell-' + $SID + '-' + $SessionID + '-ready'
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $event = $null
        try {
            $event = [Threading.EventWaitHandle]::OpenExisting($name)
            if ($event.WaitOne(0)) { return $true }
        } catch [Threading.WaitHandleCannotBeOpenedException] {
        } finally {
            if ($null -ne $event) { $event.Dispose() }
        }
        Start-Sleep -Milliseconds 250
    }
    return $false
}

function New-ProbePassword {
    $value = New-Object Security.SecureString
    $alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%+'
    $rng = New-Object Security.Cryptography.RNGCryptoServiceProvider
    try {
        for ($i = 0; $i -lt 40; $i++) {
            [byte[]]$bytes = @(0)
            $rng.GetBytes($bytes)
            $value.AppendChar($alphabet[$bytes[0] % $alphabet.Length])
        }
        $value.MakeReadOnly()
        return $value
    }
    finally { $rng.Dispose() }
}

function New-RdpTarget {
    $occupied = (& cmdkey.exe /list 2>$null | Out-String)
    foreach ($octet in 2..254) {
        $candidate = '127.0.0.' + $octet
        if ($occupied.IndexOf(('TERMSRV/' + $candidate), [StringComparison]::OrdinalIgnoreCase) -lt 0) { return $candidate }
    }
    throw 'no disposable loopback RDP target'
}

function Test-HostPreflight {
    $term = Get-Service -Name TermService -ErrorAction Stop
    $rpc = Get-Service -Name RpcSs -ErrorAction Stop
    $profile = Get-Service -Name ProfSvc -ErrorAction Stop
    $deny = (Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections -ErrorAction Stop).fDenyTSConnections
    $nla = (Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp' -Name UserAuthentication -ErrorAction Stop).UserAuthentication
    if ($term.Status -ne 'Running' -or $rpc.StartType -eq 'Disabled' -or $profile.StartType -eq 'Disabled' -or $deny -ne 0 -or $nla -ne 1) { throw 'host preflight rejected' }
}

function Get-SessionShellCommand([string]$Root) {
    return 'powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy AllSigned -File "' + (Join-Path $Root 'session-shell.ps1') + '"'
}

function Set-SessionShell([string]$SID, [string]$ProfilePath, [string]$Command) {
    $hive = 'HKEY_USERS\' + $SID
    $loaded = $false
    try {
        if (-not (Test-Path -LiteralPath ('Registry::' + $hive))) {
            & reg.exe load $hive (Join-Path $ProfilePath 'NTUSER.DAT') 1>$null 2>$null
            if ($LASTEXITCODE -ne 0) { throw 'profile hive load failed' }
            $loaded = $true
        }
        $key = 'Registry::' + $hive + '\Software\Microsoft\Windows NT\CurrentVersion\Winlogon'
        New-Item -Path $key -Force | Out-Null
        Set-ItemProperty -Path $key -Name Shell -Value $Command -Type String
        if ((Get-ItemProperty -Path $key -Name Shell).Shell -cne $Command) { throw 'session shell verification failed' }
    }
    finally {
        if ($loaded) { & reg.exe unload $hive 1>$null 2>$null }
    }
}

function Invoke-SystemProbe([string]$Manifest, [string]$Report, [bool]$ExpectOldSessionGone) {
    Import-Module ScheduledTasks -ErrorAction Stop
    $arguments = '-manifest "{0}" -output "{1}"' -f $Manifest, $Report
    $action = New-ScheduledTaskAction -Execute $ProbeBinary -Argument $arguments
    $principal = New-ScheduledTaskPrincipal -UserId 'NT AUTHORITY\SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::FromMinutes(2)) -StartWhenAvailable -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName $taskName -Action $action -Principal $principal -Settings $settings -Force | Out-Null
    try {
        Start-ScheduledTask -TaskName $taskName
        $deadline = [DateTime]::UtcNow.AddSeconds(90)
        while ([DateTime]::UtcNow -lt $deadline) {
            $state = (Get-ScheduledTask -TaskName $taskName).State
            if ($state -eq 'Ready') {
                $info = Get-ScheduledTaskInfo -TaskName $taskName
                if ($info.LastRunTime -gt [DateTime]::MinValue) { break }
            }
            Start-Sleep -Milliseconds 500
        }
        if (-not (Test-Path -LiteralPath $Report -PathType Leaf)) { throw 'system probe did not produce a report' }
        $report = Get-Content -LiteralPath $Report -Raw | ConvertFrom-Json
        if ($report.privilege_class -ne 'system_tcb_verified' -or $report.shell_ready -ne $true -or $report.old_session_gone -ne $ExpectOldSessionGone) { throw 'system probe report failed' }
        return $report
    }
    finally {
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    }
}

try {
    New-Item -ItemType Directory -Path $runRoot -Force | Out-Null
    $shellSource = Join-Path $PSScriptRoot '..\session-shell.ps1'
    Copy-Item -LiteralPath $shellSource -Destination (Join-Path $runRoot 'session-shell.ps1') -Force
    $cert = New-SelfSignedCertificate -Subject ('CN=Chuzi session probe ' + $runID) -CertStoreLocation Cert:\LocalMachine\My -KeyExportPolicy Exportable -NotAfter (Get-Date).AddHours(2)
    $signingThumbprint = $cert.Thumbprint
    $public = Join-Path $runRoot 'probe.cer'
    Export-Certificate -Cert $cert -FilePath $public -Force | Out-Null
    Import-Certificate -FilePath $public -CertStoreLocation Cert:\LocalMachine\Root | Out-Null
    Import-Certificate -FilePath $public -CertStoreLocation Cert:\LocalMachine\TrustedPublisher | Out-Null
    Remove-Item -LiteralPath $public -Force
    $signature = Set-AuthenticodeSignature -FilePath (Join-Path $runRoot 'session-shell.ps1') -Certificate $cert -HashAlgorithm SHA256
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid) { throw 'test signer trust failed' }
    Test-HostPreflight

    $operatorSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $helperRegistration = [ordered]@{ schema = 'chuzi.session-helper-registration/v1'; run_id = $runID; helper_sid_hash = [BitConverter]::ToString((New-Object Security.Cryptography.SHA256Managed).ComputeHash([Text.Encoding]::UTF8.GetBytes($operatorSID))).Replace('-', '').Substring(0, 16); helper_session_id = [Diagnostics.Process]::GetCurrentProcess().SessionId; state = 'registered' }
    $helperRegistration | ConvertTo-Json -Compress | Set-Content -LiteralPath (Join-Path $runRoot 'helper-registration.json') -Encoding UTF8

    & icacls.exe $runRoot /inheritance:r /grant:r 'SYSTEM:(OI)(CI)(F)' 'Administrators:(OI)(CI)(F)' ($operatorSID + ':(OI)(CI)(F)') 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'probe root ownership ACL failed' }
    $password = New-ProbePassword
    $beforeUser = Get-LocalUser -Name $userName -ErrorAction SilentlyContinue
    if ($null -ne $beforeUser) { throw 'random probe user already exists' }
    if ([ChuziSessionProbeWtsV1]::FindActiveSession($userName) -ge 0) { throw 'random probe session already exists' }
    New-LocalUser -Name $userName -Password $password -Description $marker -AccountNeverExpires -PasswordNeverExpires | Out-Null
    Add-LocalGroupMember -SID 'S-1-5-32-555' -Member $userName
    $user = Get-LocalUser -Name $userName
    $userSID = $user.SID.Value
    if ((& icacls.exe $runRoot /grant:r ($userSID + ':(X)') 1>$null 2>$null; $LASTEXITCODE) -ne 0) { throw 'probe runtime traversal ACL failed' }
    [ChuziSessionProbeNativeV1]::BootstrapProfile($userName, $password)
    $profile = @(Get-CimInstance Win32_UserProfile -Filter ("SID='" + $userSID + "'")) | Select-Object -First 1
    if ($null -eq $profile -or [string]::IsNullOrWhiteSpace($profile.LocalPath) -or -not (Test-Path -LiteralPath (Join-Path $profile.LocalPath 'NTUSER.DAT') -PathType Leaf)) { throw 'profile bootstrap failed' }
    $profilePath = [string]$profile.LocalPath
    $shellCommand = Get-SessionShellCommand $runRoot
    if ((& icacls.exe (Join-Path $runRoot 'session-shell.ps1') /grant:r ($userSID + ':(RX)') 1>$null 2>$null; $LASTEXITCODE) -ne 0) { throw 'probe session shell ACL failed' }
    Set-SessionShell $userSID $profile.LocalPath $shellCommand
    $credentialTarget = 'TERMSRV/' + (New-RdpTarget)
    [ChuziSessionProbeNativeV1]::WriteCredential($credentialTarget, $userName, $password)
    $credentialWritten = $true
    $rdpPath = Join-Path $runRoot 'login.rdp'
    @('full address:s:' + $credentialTarget.Substring(8) + ':3389', 'username:s:' + $userName, 'prompt for credentials:i:0', 'administrative session:i:0', 'screen mode id:i:2', 'session bpp:i:32', 'compression:i:1', 'redirectclipboard:i:1', 'autoreconnection enabled:i:1', 'authentication level:i:2', 'negotiate security layer:i:1') | Set-Content -LiteralPath $rdpPath -Encoding ASCII

    $mstsc += Start-Process -FilePath (Join-Path $env:SystemRoot 'System32\mstsc.exe') -ArgumentList ('"' + $rdpPath + '"') -PassThru
    $firstSessionID = Wait-ActiveSession $userName
    if ($firstSessionID -lt 0 -or -not (Wait-ShellReady $userSID $firstSessionID)) { throw 'automatic first login did not become ready' }
    $manifestPath = Join-Path $runRoot 'probe-input.json'
    $reportPath = Join-Path $runRoot 'probe-report.json'
    [ordered]@{ schema = 'chuzi.session-probe-input/v1'; run_id = $runID; login_method = 'registered-interactive-helper-loopback-rdp'; sid = $userSID; session_id = $firstSessionID; old_session_gone = $false; shell_ready = $true; agent_ready = $false; worker_ready = $false; identity_check = 'helper_sid_and_session_verified'; login_check = 'automatic_credential_manager_login'; hive_check = 'sid_derived_profile_hive_ready'; native_pending = $true } | ConvertTo-Json -Compress | Set-Content -LiteralPath $manifestPath -Encoding UTF8
    $firstReport = Invoke-SystemProbe $manifestPath $reportPath $false

    & logoff.exe $firstSessionID 1>$null 2>$null
    if (-not (Wait-NoSession $userName)) { throw 'first session did not disappear' }
    $mstsc | ForEach-Object { if ($_.HasExited -eq $false) { $_.CloseMainWindow() | Out-Null } }
    $mstsc = @()
    $password.Dispose()
    $password = New-ProbePassword
    Set-LocalUser -Name $userName -Password $password
    [ChuziSessionProbeNativeV1]::DeleteCredential($credentialTarget, $userName)
    [ChuziSessionProbeNativeV1]::WriteCredential($credentialTarget, $userName, $password)
    $second = Start-Process -FilePath (Join-Path $env:SystemRoot 'System32\mstsc.exe') -ArgumentList ('"' + $rdpPath + '"') -PassThru
    $mstsc += $second
    $secondSessionID = Wait-ActiveSession $userName
    if ($secondSessionID -lt 0 -or -not (Wait-ShellReady $userSID $secondSessionID)) { throw 'automatic re-login did not become ready' }
    [ordered]@{ schema = 'chuzi.session-probe-input/v1'; run_id = $runID; login_method = 'registered-interactive-helper-loopback-rdp'; sid = $userSID; session_id = $secondSessionID; old_session_gone = $true; shell_ready = $true; agent_ready = $false; worker_ready = $false; identity_check = 'same_owned_sid_after_relogin'; login_check = 'automatic_credential_manager_relogin'; hive_check = 'sid_derived_profile_hive_reused'; native_pending = $true } | ConvertTo-Json -Compress | Set-Content -LiteralPath $manifestPath -Encoding UTF8
    $secondReport = Invoke-SystemProbe $manifestPath $reportPath $true
    & logoff.exe $secondSessionID 1>$null 2>$null
    $result = $true
    Write-Output 'Windows automatic session login probe passed; agent and broker native acceptance remain pending.'
}
catch {
    Write-Error 'Windows automatic session login probe failed (details retained only in the run-scoped private directory).'
    $result = $false
}
finally {
    foreach ($process in $mstsc) { try { if (-not $process.HasExited) { $process.CloseMainWindow() | Out-Null; $process.WaitForExit(5000) } } catch {} }
    if ($secondSessionID -gt 0) { & logoff.exe $secondSessionID 1>$null 2>$null }
    if ($firstSessionID -gt 0) { & logoff.exe $firstSessionID 1>$null 2>$null }
    if ($credentialWritten -and $credentialTarget) { try { [ChuziSessionProbeNativeV1]::DeleteCredential($credentialTarget, $userName) } catch { $cleanupFailed = $true } }
    if ($userName) {
        try {
            Remove-OwnedProfile $userSID $profilePath
            $owned = Get-LocalUser -Name $userName -ErrorAction SilentlyContinue
            if ($null -ne $owned -and $owned.Description -eq $marker) { Remove-LocalUser -Name $userName -Confirm:$false }
        } catch { $cleanupFailed = $true }
    }
    if ($signingThumbprint) {
        foreach ($store in @('Cert:\LocalMachine\My\', 'Cert:\LocalMachine\Root\', 'Cert:\LocalMachine\TrustedPublisher\')) { Remove-Item -LiteralPath ($store + $signingThumbprint) -Force -ErrorAction SilentlyContinue }
    }
    if ($password) { $password.Dispose() }
    if (Test-Path -LiteralPath $runRoot) { Remove-Item -LiteralPath $runRoot -Recurse -Force -ErrorAction SilentlyContinue }
}

if (-not $result -or $cleanupFailed) { exit 1 }
