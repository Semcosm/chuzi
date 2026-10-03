#Requires -Version 5.1

[CmdletBinding()]
param(
    [ValidateSet('Headed', 'Headless')]
    [string]$Mode = 'Headed',

    [string]$DataDir = (Join-Path $env:ProgramData 'Chuzi'),

    [string]$UserName,

    [string]$BrowserCommand = 'chromium',

    [string]$AgentPath
)

$ErrorActionPreference = 'Stop'
$failed = $false

function Write-Check {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][bool]$Passed,
        [Parameter(Mandatory)][string]$Detail
    )

    if ($Passed) {
        Write-Host ("[PASS] {0}: {1}" -f $Name, $Detail) -ForegroundColor Green
    }
    else {
        $script:failed = $true
        Write-Host ("[FAIL] {0}: {1}" -f $Name, $Detail) -ForegroundColor Red
    }
}

function Write-Observation {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Detail
    )

    Write-Host ("[INFO] {0}: {1}" -f $Name, $Detail) -ForegroundColor DarkCyan
}

function Test-CommandPath {
    param([Parameter(Mandatory)][string]$Command)

    if ([string]::IsNullOrWhiteSpace($Command)) {
        return $false
    }

    if ([IO.Path]::IsPathRooted($Command)) {
        return Test-Path -LiteralPath $Command -PathType Leaf
    }

    return $null -ne (Get-Command $Command -ErrorAction SilentlyContinue)
}

function Get-ProfilePath {
    param([Parameter(Mandatory)][string]$Sid)

    $key = "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\$Sid"
    if (-not (Test-Path -LiteralPath $key)) {
        return $null
    }

    $raw = (Get-ItemProperty -LiteralPath $key -Name ProfileImagePath -ErrorAction Stop).ProfileImagePath
    if ([string]::IsNullOrWhiteSpace($raw)) {
        return $null
    }

    return [Environment]::ExpandEnvironmentVariables($raw)
}

function Get-RdpCredentialListing {
    $cmdkey = Get-Command cmdkey.exe -CommandType Application -ErrorAction Stop
    return (& $cmdkey.Source '/list' 2>&1 | Out-String)
}

function Get-AvailableRdpLoopbackHost {
    $listing = Get-RdpCredentialListing
    for ($octet = 2; $octet -le 254; $octet++) {
        $candidate = "127.0.0.$octet"
        $target = "TERMSRV/$candidate"
        if ($listing.IndexOf($target, [StringComparison]::OrdinalIgnoreCase) -lt 0) {
            return $candidate
        }
    }

    return $null
}

function Test-RdpFirewallRule {
    try {
        $rules = @(Get-NetFirewallRule -Enabled True -Direction Inbound -Action Allow -ErrorAction Stop)
        foreach ($rule in $rules) {
            $filters = @(Get-NetFirewallPortFilter -AssociatedNetFirewallRule $rule -ErrorAction SilentlyContinue)
            foreach ($filter in $filters) {
                $localPort = [string]$filter.LocalPort
                $protocol = [string]$filter.Protocol
                if (($protocol -eq 'TCP' -or $protocol -eq 'Any') -and ($localPort -eq '3389' -or $localPort -match '(^|[,\s])3389([,\s]|$)')) {
                    return $true
                }
            }
        }
    }
    catch {
        return $false
    }

    return $false
}

Write-Host "Chuzi Windows minimal base check ($Mode)" -ForegroundColor Cyan
Write-Host "DataDir: $DataDir"

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
Write-Check -Name 'Administrator' -Passed $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) -Detail 'elevated PowerShell is recommended for complete checks'

$os = Get-CimInstance Win32_OperatingSystem
Write-Check -Name 'Windows' -Passed ($os.Caption -match 'Windows') -Detail ("{0} build {1}" -f $os.Caption, $os.BuildNumber)

foreach ($serviceName in @('RpcSs', 'ProfSvc')) {
    $service = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction SilentlyContinue
    $passed = $null -ne $service -and $service.StartMode -ne 'Disabled'
    $detail = if ($null -eq $service) { 'service not found' } else { "status=$($service.State); start=$($service.StartMode)" }
    Write-Check -Name $serviceName -Passed $passed -Detail $detail
}

if ($Mode -eq 'Headed') {
    $term = Get-CimInstance Win32_Service -Filter "Name='TermService'" -ErrorAction SilentlyContinue
    $termPassed = $null -ne $term -and $term.State -eq 'Running' -and $term.StartMode -ne 'Disabled'
    $termDetail = if ($null -eq $term) { 'service not found' } else { "status=$($term.State); start=$($term.StartMode)" }
    Write-Check -Name 'TermService' -Passed $termPassed -Detail $termDetail

    $deny = (Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections -ErrorAction SilentlyContinue).fDenyTSConnections
    Write-Check -Name 'RDP enabled' -Passed ($deny -eq 0) -Detail ("fDenyTSConnections=$deny")

    $firewallPassed = Test-RdpFirewallRule
    Write-Check -Name 'RDP firewall' -Passed $firewallPassed -Detail 'enabled inbound allow rule for TCP 3389'
}

$dataAbsolute = [IO.Path]::GetFullPath($DataDir)
$dataExists = Test-Path -LiteralPath $dataAbsolute -PathType Container
$dataWritable = $false
if ($dataExists) {
    $probe = Join-Path $dataAbsolute (".chuzi-base-probe-{0}.tmp" -f [guid]::NewGuid().ToString('N'))
    try {
        [IO.File]::WriteAllText($probe, 'probe')
        $dataWritable = $true
    }
    finally {
        Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue
    }
}
Write-Check -Name 'DataDir' -Passed ($dataExists -and $dataWritable) -Detail ("exists=$dataExists; writable=$dataWritable; path=$dataAbsolute")

Write-Check -Name 'Node.js' -Passed (Test-CommandPath 'node') -Detail 'required for browser worker and adapters'
Write-Check -Name 'Browser' -Passed (Test-CommandPath $BrowserCommand) -Detail $BrowserCommand

if (-not [string]::IsNullOrWhiteSpace($AgentPath)) {
    $agentPassed = Test-Path -LiteralPath $AgentPath -PathType Leaf
    Write-Check -Name 'User agent' -Passed $agentPassed -Detail $AgentPath
}

if (-not [string]::IsNullOrWhiteSpace($UserName)) {
    $user = Get-LocalUser -Name $UserName -ErrorAction SilentlyContinue
    $userPassed = $null -ne $user -and $user.Enabled
    $userDetail = if ($null -eq $user) { 'user not found' } else { "enabled=$($user.Enabled); sid=$($user.SID.Value)" }
    Write-Check -Name 'Managed user' -Passed $userPassed -Detail $userDetail

    if ($null -ne $user) {
        $sid = $user.SID.Value
        $rdpMember = $false
        $adminMember = $false
        try {
            $rdpMember = @(Get-LocalGroupMember -SID 'S-1-5-32-555' -ErrorAction Stop | Where-Object { $_.SID.Value -eq $sid }).Count -gt 0
            $adminMember = @(Get-LocalGroupMember -SID 'S-1-5-32-544' -ErrorAction Stop | Where-Object { $_.SID.Value -eq $sid }).Count -gt 0
        }
        catch {
            $rdpMember = $false
            $adminMember = $true
        }
        Write-Check -Name 'Remote Desktop Users' -Passed $rdpMember -Detail "sid=$sid"
        Write-Check -Name 'Not Administrators' -Passed (-not $adminMember) -Detail "sid=$sid"

        $profile = Get-ProfilePath -Sid $sid
        $ntUser = if ($profile) { Join-Path $profile 'NTUSER.DAT' } else { $null }
        $usrClass = if ($profile) { Join-Path $profile 'AppData\Local\Microsoft\Windows\UsrClass.dat' } else { $null }
        Write-Check -Name 'User profile' -Passed ($null -ne $profile -and (Test-Path -LiteralPath $ntUser -PathType Leaf)) -Detail ("profile=$profile; NTUSER.DAT=$([bool]($ntUser -and (Test-Path -LiteralPath $ntUser)))")
        $usrClassPresent = $null -ne $usrClass -and (Test-Path -LiteralPath $usrClass -PathType Leaf)
        Write-Observation -Name 'UsrClass.dat' -Detail ("present=$usrClassPresent; path=$usrClass")
    }
}

if ($Mode -eq 'Headed') {
    $rdpHost = Get-AvailableRdpLoopbackHost
    if ([string]::IsNullOrWhiteSpace($rdpHost)) {
        Write-Check -Name 'RDP loopback target' -Passed $false -Detail 'no unused TERMSRV/127.0.0.2..254 credential target is available'
    }
    else {
        Write-Observation -Name 'RDP loopback target' -Detail "selected=$rdpHost; credential target=TERMSRV/$rdpHost"
        $loopback = Test-NetConnection -ComputerName $rdpHost -Port 3389 -WarningAction SilentlyContinue
        Write-Check -Name 'RDP loopback' -Passed $loopback.TcpTestSucceeded -Detail "${rdpHost}:3389=$($loopback.TcpTestSucceeded)"
    }
}

if ($failed) {
    Write-Host 'Windows minimal base check failed.' -ForegroundColor Red
    exit 1
}

Write-Host 'Windows minimal base check passed.' -ForegroundColor Green
