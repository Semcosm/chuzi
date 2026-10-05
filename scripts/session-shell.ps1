<#
    Chuzi's fixed user-level Winlogon supervisor. The installer signs this
    file and grants the runtime tree read/execute access to managed users.
    It intentionally has no parameters and never starts an agent or worker.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$sid = $identity.User.Value
$sessionId = (Get-Process -Id $PID -ErrorAction Stop).SessionId
$prefix = 'Global\ChuziSessionShell-' + $sid + '-' + $sessionId
$ready = $null
$stop = $null
$security = [System.Security.AccessControl.EventWaitHandleSecurity]::new()
$userRule = [System.Security.AccessControl.EventWaitHandleAccessRule]::new(
    $identity.User,
    [System.Security.AccessControl.EventWaitHandleRights]::FullControl,
    [System.Security.AccessControl.AccessControlType]::Allow)
$systemRule = [System.Security.AccessControl.EventWaitHandleAccessRule]::new(
    [System.Security.Principal.SecurityIdentifier]::new('S-1-5-18'),
    [System.Security.AccessControl.EventWaitHandleRights]::FullControl,
    [System.Security.AccessControl.AccessControlType]::Allow)
$administratorsRule = [System.Security.AccessControl.EventWaitHandleAccessRule]::new(
    [System.Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'),
    [System.Security.AccessControl.EventWaitHandleRights]::FullControl,
    [System.Security.AccessControl.AccessControlType]::Allow)
$security.SetAccessRule($userRule)
$security.AddAccessRule($systemRule)
$security.AddAccessRule($administratorsRule)
try {
    $createdNew = $false
    $ready = [System.Threading.EventWaitHandle]::new(
        $false,
        [System.Threading.EventResetMode]::ManualReset,
        ($prefix + '-ready'),
        [ref]$createdNew,
        $security)
    if (-not $createdNew) { throw 'session shell readiness event already exists' }
    $createdNew = $false
    $stop = [System.Threading.EventWaitHandle]::new(
        $false,
        [System.Threading.EventResetMode]::ManualReset,
        ($prefix + '-stop'),
        [ref]$createdNew,
        $security)
    if (-not $createdNew) { throw 'session shell stop event already exists' }
    $ready.Set() | Out-Null
    # The service owns chuzi-user-agent.exe on its derived ChuziSlot desktop.
    # Waiting here keeps the user's default Winlogon desktop alive without
    # accepting executable, slot, profile, or credential input.
    $stop.WaitOne() | Out-Null
} finally {
    if ($null -ne $stop) { $stop.Dispose() }
    if ($null -ne $ready) { $ready.Dispose() }
}
