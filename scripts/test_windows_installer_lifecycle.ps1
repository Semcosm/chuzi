param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][string]$LogRoot
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# This runs only on the disposable hosted Windows installer runner, after its
# first install. It never removes data directly or runs native session tests.
$installRoot = Join-Path $env:ProgramFiles 'Chuzi'
$dataRoot = Join-Path $env:ProgramData 'chuzi\data'
$ownershipPath = Join-Path $dataRoot 'service-ownership.json'
$configPath = Join-Path $dataRoot 'core-config.json'

function Assert-Registration {
    $service = Get-CimInstance Win32_Service -Filter "Name='ChuziCore'"
    if ($null -eq $service -or $service.StartName -ne 'LocalSystem' -or $service.StartMode -ne 'Auto') {
        throw 'Installed Core service registration is missing or invalid'
    }
    $core = Join-Path $installRoot 'CorePayload\chuzi.exe'
    $expectedCommand = '"' + $core + '" -service -config "' + $configPath + '"'
    if ($service.PathName -ne $expectedCommand) {
        throw "Installed Core service command is invalid: $($service.PathName)"
    }
    $task = Get-ScheduledTask -TaskName 'ChuziSessionBroker'
    if ($task.Principal.LogonType -ne 'Interactive' -or $task.Principal.RunLevel -ne 'Limited') {
        throw 'Installed Broker task must use a limited interactive token'
    }
    $actions = @($task.Actions)
    $broker = Join-Path $installRoot 'CorePayload\chuzi-session-broker.exe'
    if ($actions.Count -ne 1 -or $actions[0].Execute -ne $broker -or
        -not [string]::IsNullOrEmpty([string]$actions[0].Arguments)) {
        throw 'Installed Broker task command is invalid'
    }
}

function Invoke-Setup([string]$Executable, [string]$LogName) {
    $logPath = Join-Path $LogRoot $LogName
    $process = Start-Process -FilePath $Executable -ArgumentList @(
        '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/SP-', ('/LOG="' + $logPath + '"')
    ) -PassThru
    try {
        if (-not $process.WaitForExit(120000)) { throw 'Installer lifecycle operation timed out' }
        if ($process.ExitCode -ne 0) { throw "Installer lifecycle exit code: $($process.ExitCode)" }
    } finally {
        $process.Refresh()
        if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force }
    }
    if (-not (Test-Path -LiteralPath $logPath)) { throw 'Installer lifecycle log is missing' }
    if (Select-String -LiteralPath $logPath -Pattern 'raised an exception|卸载已停止|安装已停止') {
        Get-Content -LiteralPath $logPath -Tail 80 | Write-Host
        throw 'Installer lifecycle reported a script exception'
    }
}

Assert-Registration
$ownershipHash = (Get-FileHash -LiteralPath $ownershipPath -Algorithm SHA256).Hash
$configHash = (Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash
Invoke-Setup (Join-Path $installRoot 'unins000.exe') 'uninstaller-runtime.log'
if (Get-Service -Name 'ChuziCore' -ErrorAction SilentlyContinue) { throw 'Uninstall left the Core service registered' }
if (Get-ScheduledTask -TaskName 'ChuziSessionBroker' -ErrorAction SilentlyContinue) { throw 'Uninstall left the Broker task registered' }
if (Test-Path -LiteralPath $installRoot) { throw 'Uninstall left the application directory' }
if ((Get-FileHash -LiteralPath $ownershipPath -Algorithm SHA256).Hash -ne $ownershipHash -or
    (Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash -ne $configHash) {
    throw 'Uninstall did not preserve the managed data by default'
}
Invoke-Setup $Installer 'reinstaller-runtime.log'
Assert-Registration
if ((Get-FileHash -LiteralPath $ownershipPath -Algorithm SHA256).Hash -ne $ownershipHash -or
    (Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash -ne $configHash) {
    throw 'Reinstall changed the preserved ownership or configuration'
}
Write-Host 'Windows installer uninstall/reinstall lifecycle passed'
