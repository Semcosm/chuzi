param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("Install", "Stop", "UpgradePrepare", "Backup", "Rollback", "CleanupBackup", "ConfirmResources", "ConfirmDataOwnership", "UninstallPrepare", "Repair", "Uninstall", "Status")]
    [string]$Operation,
    [string]$RuntimeRoot = "",
    [string]$DataRoot = "",
    [string]$InstallRoot = ""
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$serviceName = "ChuziCore"
$brokerServiceName = "ChuziSessionBroker"
$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path

function Resolve-FixedRoot([string]$value, [string]$fallback) {
    $candidate = if ([string]::IsNullOrWhiteSpace($value)) { $fallback } else { $value }
    $resolved = [IO.Path]::GetFullPath($candidate)
    if (-not [IO.Path]::IsPathRooted($resolved) -or $resolved.Contains([char]13) -or $resolved.Contains([char]10) -or $resolved.Contains('"') -or $resolved.Contains('..')) {
        throw "service_root_invalid"
    }
    return $resolved.TrimEnd('\')
}

$runtime = Resolve-FixedRoot $RuntimeRoot $scriptRoot
$data = Resolve-FixedRoot $DataRoot (Join-Path $env:ProgramData "chuzi\data")
$install = Resolve-FixedRoot $InstallRoot (Split-Path -Parent $runtime)
$fixedData = (Resolve-FixedRoot "" (Join-Path $env:ProgramData "chuzi\data"))
if ($data -ne $fixedData) { throw "service_data_root_invalid" }
if ((Split-Path -Parent $runtime) -ne $install -or (Split-Path -Leaf $runtime) -ne "CorePayload") { throw "install_root_invalid" }
$core = Join-Path $runtime "chuzi.exe"
$broker = Join-Path $runtime "chuzi-session-broker.exe"
$config = Join-Path $data "core-config.json"
$management = Join-Path $runtime "windows-service-management.ps1"
$metadata = Join-Path $data ".chuzi"
$ownershipMarker = Join-Path $data "service-ownership.json"
$backupRoot = Join-Path $metadata "installer-backups"
$backupMarker = Join-Path $backupRoot "upgrade-state.json"
$rollbackManagement = Join-Path $metadata "rollback-management.ps1"
$managedComponentNames = @(
    "chuzi.exe", "chuzi-launcher.exe", "chuzi-browser-launcher.exe",
    "chuzi-user-agent.exe", "chuzi-session-broker.exe", "node.exe",
    "session-shell.ps1", "windows-service-management.ps1",
    "validate_windows_payload.ps1", "authenticode-policy.json",
    "authenticode-signing.json", "release-manifest.json", "build-manifest.json",
    "service-ownership.json"
)

function Invoke-Sc([string[]]$Arguments) {
    & "$env:SystemRoot\System32\sc.exe" @Arguments | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "service_control_failed" }
}

function Wait-Stopped([string]$name) {
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        $query = & "$env:SystemRoot\System32\sc.exe" query $name 2>$null | Out-String
        if ($query -match 'STATE\s+:\s+\d+\s+STOPPED') { return }
        Start-Sleep -Milliseconds 250
    }
    throw "service_stop_timeout"
}

function Set-ChuziAcl([string]$path, [bool]$Writable, [bool]$Recursive = $true) {
    if (-not (Test-Path -LiteralPath $path)) { New-Item -ItemType Directory -Force -Path $path | Out-Null }
    # Use well-known SIDs so ACL setup is independent of the host UI locale.
    $aclArguments = @($path, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)(F)", "*S-1-5-32-544:(OI)(CI)(F)", "*S-1-5-32-545:(OI)(CI)(RX)", "/C")
    if ($Recursive) { $aclArguments += "/T" }
    & "$env:SystemRoot\System32\icacls.exe" @aclArguments | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "runtime_acl_failed" }
    if ($Writable) {
        $writeArguments = @($path, "/grant:r", "*S-1-5-18:(OI)(CI)(M)", "/C")
        if ($Recursive) { $writeArguments += "/T" }
        & "$env:SystemRoot\System32\icacls.exe" @writeArguments | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "data_acl_failed" }
    }
}

function Write-Utf8NoBom([string]$path, [string]$content) {
    # Inno invokes Windows PowerShell 5.1, where -Encoding utf8 writes a BOM.
    # Go's JSON decoder rejects that BOM, so write deployment JSON explicitly.
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($path, $content, $encoding)
}

function Test-ExclusiveFile([string]$path) {
    $stream = $null
    try {
        $stream = [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
        return $true
    } catch [IO.IOException] {
        return $false
    } catch [UnauthorizedAccessException] {
        throw "resource_access_denied"
    } finally {
        if ($null -ne $stream) { $stream.Dispose() }
    }
}

function Test-ResourceDirectory([string]$path) {
    if (-not (Test-Path -LiteralPath $path -PathType Container)) { return $true }
    $rootItem = Get-Item -LiteralPath $path -Force -ErrorAction Stop
    if (($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "resource_ownership_unknown" }
    $files = @(Get-ChildItem -LiteralPath $path -File -Recurse -Force -ErrorAction Stop)
    foreach ($file in $files) {
        if (($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "resource_ownership_unknown" }
        if (-not (Test-ExclusiveFile $file.FullName)) { return $false }
    }
    $directories = @(Get-ChildItem -LiteralPath $path -Directory -Recurse -Force -ErrorAction Stop)
    foreach ($directory in $directories) {
        if (($directory.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "resource_ownership_unknown" }
    }
    $probe = Join-Path $path (".chuzi-resource-probe-{0}.tmp" -f ([guid]::NewGuid().ToString("N")))
    try {
        $handle = [IO.File]::OpenNew($probe)
        $handle.Dispose()
        Remove-Item -LiteralPath $probe -Force -ErrorAction Stop
    } catch [IO.IOException] {
        if (Test-Path -LiteralPath $probe) { Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue }
        return $false
    } catch [UnauthorizedAccessException] {
        if (Test-Path -LiteralPath $probe) { Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue }
        throw "resource_access_denied"
    }
    return $true
}

function Confirm-DataOwnership {
    if (-not (Test-Path -LiteralPath $data -PathType Container)) { return $true }
    $rootItem = Get-Item -LiteralPath $data -Force -ErrorAction Stop
    if (($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "resource_ownership_unknown" }
    $items = @(Get-ChildItem -LiteralPath $data -Recurse -Force -ErrorAction Stop)
    foreach ($item in $items) {
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "resource_ownership_unknown" }
        $fullName = [IO.Path]::GetFullPath($item.FullName)
        $prefix = $data + [IO.Path]::DirectorySeparatorChar
        if (-not $fullName.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw "resource_ownership_unknown" }
    }
    if (-not (Test-Path -LiteralPath $ownershipMarker -PathType Leaf)) { throw "resource_ownership_unknown" }
    try {
        $ownership = Get-Content -Raw -LiteralPath $ownershipMarker | ConvertFrom-Json
    } catch {
        throw "resource_ownership_unknown"
    }
    if ([string]$ownership.format -ne "chuzi-service-ownership/v1" -or
        [string]$ownership.data_dir -ne $data -or [string]$ownership.install_root -ne $install) {
        throw "resource_ownership_unknown"
    }
    return $true
}

function Confirm-ResourcesReleased {
    $database = Join-Path $data "chuzi.db"
    if (Test-Path -LiteralPath $database -PathType Leaf) {
        if (-not (Test-ExclusiveFile $database)) { throw "resource_busy_database" }
    }
    foreach ($directoryName in @('profiles', 'journal')) {
        $directory = Join-Path $data $directoryName
        if (-not (Test-ResourceDirectory $directory)) { throw ("resource_busy_{0}" -f $directoryName) }
    }
    return $true
}

function Read-UpgradeState {
    if (-not (Test-Path -LiteralPath $backupMarker -PathType Leaf)) { return $null }
    try {
        $state = Get-Content -Raw -LiteralPath $backupMarker | ConvertFrom-Json
    } catch {
        throw "upgrade_state_invalid"
    }
    if ([string]$state.format -ne "chuzi-upgrade-state/v1" -or [string]::IsNullOrWhiteSpace([string]$state.backup_root)) {
        throw "upgrade_state_invalid"
    }
    $candidate = Resolve-FixedRoot ([string]$state.backup_root) $backupRoot
    if ((Split-Path -Parent $candidate) -ne (Resolve-FixedRoot $backupRoot $backupRoot)) { throw "upgrade_state_invalid" }
    if (([string]$state.install_root) -ne $install) { throw "upgrade_state_invalid" }
    return $state
}

function Stop-AndConfirm {
    Stop-ChuziServices
    Confirm-ResourcesReleased
}

function Backup-Install {
    if (Test-Path -LiteralPath $backupMarker -PathType Leaf) { throw "upgrade_backup_pending" }
    Stop-AndConfirm
    if (-not (Test-Path -LiteralPath $ownershipMarker -PathType Leaf)) {
        if (-not (Test-Path -LiteralPath $config -PathType Leaf)) { throw "resource_ownership_unknown" }
        try {
            $legacyConfig = Get-Content -Raw -LiteralPath $config | ConvertFrom-Json
        } catch {
            throw "resource_ownership_unknown"
        }
        if ([string]$legacyConfig.data_dir -ne $data) { throw "resource_ownership_unknown" }
        Write-Utf8NoBom $ownershipMarker (([ordered]@{
            format = "chuzi-service-ownership/v1"
            data_dir = $data
            install_root = $install
        }) | ConvertTo-Json -Depth 4)
        Set-ChuziAcl $ownershipMarker $false $false
    }
    New-Item -ItemType Directory -Force -Path $metadata | Out-Null
    New-Item -ItemType Directory -Force -Path $backupRoot | Out-Null
    Set-ChuziAcl $backupRoot $false $false
    $id = [guid]::NewGuid().ToString("N")
    $destination = Join-Path $backupRoot $id
    New-Item -ItemType Directory -Force -Path $destination | Out-Null
    try {
        if (Test-Path -LiteralPath $install -PathType Container) {
            Copy-Item -LiteralPath $install -Destination (Join-Path $destination "install") -Recurse -Force -ErrorAction Stop
        } else {
            New-Item -ItemType Directory -Force -Path (Join-Path $destination "install") | Out-Null
        }
        $componentDestination = Join-Path $destination "data-components"
        New-Item -ItemType Directory -Force -Path $componentDestination | Out-Null
        foreach ($name in $managedComponentNames) {
            $source = Join-Path $data $name
            if (Test-Path -LiteralPath $source -PathType Leaf) {
                Copy-Item -LiteralPath $source -Destination (Join-Path $componentDestination $name) -Force -ErrorAction Stop
            }
        }
        $launcherState = Join-Path $metadata "launcher-state.json"
        if (Test-Path -LiteralPath $launcherState -PathType Leaf) {
            Copy-Item -LiteralPath $launcherState -Destination (Join-Path $destination "launcher-state.json") -Force -ErrorAction Stop
        }
        if (Test-Path -LiteralPath $management -PathType Leaf) {
            Copy-Item -LiteralPath $management -Destination $rollbackManagement -Force -ErrorAction Stop
            Set-ChuziAcl $rollbackManagement $false $false
        }
        & "$env:SystemRoot\System32\sc.exe" query $serviceName 2>$null | Out-Null
        $serviceExists = $LASTEXITCODE -eq 0
        & "$env:SystemRoot\System32\sc.exe" query $brokerServiceName 2>$null | Out-Null
        $brokerExists = $LASTEXITCODE -eq 0
        $state = [ordered]@{ format = "chuzi-upgrade-state/v1"; backup_root = $destination; install_root = $install; service_exists = $serviceExists; broker_exists = $brokerExists }
        Write-Utf8NoBom $backupMarker ($state | ConvertTo-Json -Depth 4)
        Set-ChuziAcl $backupMarker $false $false
    } catch {
        Remove-Item -LiteralPath $destination -Recurse -Force -ErrorAction SilentlyContinue
        throw "upgrade_backup_failed"
    }
}

function Rollback-Install {
    $state = Read-UpgradeState
    if ($null -eq $state) { throw "upgrade_backup_missing" }
    Stop-AndConfirm
    $savedInstall = Join-Path ([string]$state.backup_root) "install"
    if (-not (Test-Path -LiteralPath $savedInstall -PathType Container)) { throw "upgrade_backup_missing" }
    New-Item -ItemType Directory -Force -Path $install | Out-Null
    & "$env:SystemRoot\System32\robocopy.exe" $savedInstall $install /MIR /COPY:DATS /DCOPY:DAT /R:0 /W:0 /XJ /NFL /NDL /NJH /NJS | Out-Null
    if ($LASTEXITCODE -gt 7) { throw "upgrade_rollback_failed" }
    $componentBackup = Join-Path ([string]$state.backup_root) "data-components"
    foreach ($name in $managedComponentNames) {
        $target = Join-Path $data $name
        $saved = Join-Path $componentBackup $name
        if (Test-Path -LiteralPath $saved -PathType Leaf) {
            Copy-Item -LiteralPath $saved -Destination $target -Force -ErrorAction Stop
        } elseif (Test-Path -LiteralPath $target -PathType Leaf) {
            Remove-Item -LiteralPath $target -Force -ErrorAction Stop
        }
    }
    $savedLauncherState = Join-Path ([string]$state.backup_root) "launcher-state.json"
    $launcherState = Join-Path $metadata "launcher-state.json"
    if (Test-Path -LiteralPath $savedLauncherState -PathType Leaf) {
        Copy-Item -LiteralPath $savedLauncherState -Destination $launcherState -Force -ErrorAction Stop
    } elseif (Test-Path -LiteralPath $launcherState -PathType Leaf) {
        Remove-Item -LiteralPath $launcherState -Force -ErrorAction Stop
    }
    if ([bool]$state.service_exists -or [bool]$state.broker_exists) {
        Install-ChuziServices
    } else {
        foreach ($name in @($brokerServiceName, $serviceName)) {
            & "$env:SystemRoot\System32\sc.exe" delete $name | Out-Null
            if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 1060 -and $LASTEXITCODE -ne 1072) { throw "service_delete_failed" }
        }
    }
}

function Cleanup-InstallBackup {
    $state = Read-UpgradeState
    if ($null -eq $state) { return }
    Confirm-ResourcesReleased
    $candidate = Resolve-FixedRoot ([string]$state.backup_root) $backupRoot
    if ((Split-Path -Parent $candidate) -ne (Resolve-FixedRoot $backupRoot $backupRoot)) { throw "upgrade_state_invalid" }
    Remove-Item -LiteralPath $candidate -Recurse -Force -ErrorAction Stop
    Remove-Item -LiteralPath $backupMarker -Force -ErrorAction Stop
    if (Test-Path -LiteralPath $rollbackManagement -PathType Leaf) { Remove-Item -LiteralPath $rollbackManagement -Force -ErrorAction Stop }
}

function Install-ChuziServices {
    if (-not (Test-Path -LiteralPath $core -PathType Leaf)) { throw "core_missing" }
    if (-not (Test-Path -LiteralPath $broker -PathType Leaf)) { throw "service_helper_missing" }
    if (-not (Test-Path -LiteralPath $management -PathType Leaf)) { throw "service_management_missing" }
    New-Item -ItemType Directory -Force -Path $data | Out-Null
    if (-not (Test-Path -LiteralPath $ownershipMarker -PathType Leaf)) {
        $topLevelAllowed = @(".chuzi", "db", "profiles", "runtime", "journal", "secrets")
        $unexpected = @(Get-ChildItem -LiteralPath $data -Force -ErrorAction Stop |
            Where-Object { $topLevelAllowed -notcontains $_.Name })
        if ($unexpected.Count -gt 0) { throw "resource_ownership_unknown" }
        foreach ($child in $topLevelAllowed) {
            $childPath = Join-Path $data $child
            if (Test-Path -LiteralPath $childPath -PathType Container -and
                @(Get-ChildItem -LiteralPath $childPath -Force -Recurse -ErrorAction Stop).Count -gt 0) {
                throw "resource_ownership_unknown"
            }
        }
    }
    if (Test-Path -LiteralPath $config -PathType Leaf) {
        $existingConfig = Get-Content -Raw -LiteralPath $config | ConvertFrom-Json
        if ([string]$existingConfig.data_dir -ne $data) { throw "service_config_invalid" }
    } else {
        $configDocument = [ordered]@{
            data_dir = $data
            credentials = [ordered]@{
                key_env = "CHUZI_CREDENTIAL_KEY"
                key_id_env = "CHUZI_CREDENTIAL_KEY_ID"
                history_env = "CHUZI_CREDENTIAL_KEYS"
            }
            health = [ordered]@{ listen = "" }
            observability = [ordered]@{
                metrics_listen = ""
                log_path = (Join-Path $data "core-service.log")
                log_max_bytes = 10485760
                log_max_files = 5
            }
        }
        Write-Utf8NoBom $config ($configDocument | ConvertTo-Json -Depth 8)
    }
    if (Test-Path -LiteralPath $ownershipMarker -PathType Leaf) {
        Confirm-DataOwnership
    } else {
        Write-Utf8NoBom $ownershipMarker (([ordered]@{
            format = "chuzi-service-ownership/v1"
            data_dir = $data
            install_root = $install
        }) | ConvertTo-Json -Depth 4)
    }
    Set-ChuziAcl $data $true $false
    Set-ChuziAcl $config $true $false
    Set-ChuziAcl $ownershipMarker $false $false
    foreach ($child in @("db", "profiles", "runtime", "journal", "secrets")) {
        Set-ChuziAcl (Join-Path $data $child) $true
    }
    Set-ChuziAcl $runtime $false
    $binPath = '"' + $core + '" -service -config "' + $config + '"'
    & "$env:SystemRoot\System32\sc.exe" query $serviceName 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Invoke-Sc @("create", $serviceName, "binPath=", $binPath, "start=", "auto", "obj=", "LocalSystem", "DisplayName=", "Chuzi Core Service")
    } else {
        Invoke-Sc @("config", $serviceName, "binPath=", $binPath, "start=", "auto", "obj=", "LocalSystem")
    }
    Invoke-Sc @("sidtype", $serviceName, "unrestricted")
    Invoke-Sc @("failure", $serviceName, "reset=", "86400", "actions=", "restart/5000/restart/30000/none/0")
    Invoke-Sc @("sidtype", $serviceName, "unrestricted")
    $brokerPath = '"' + $broker + '"'
    & "$env:SystemRoot\System32\sc.exe" query $brokerServiceName 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Invoke-Sc @("create", $brokerServiceName, "binPath=", $brokerPath, "start=", "demand", "obj=", "LocalSystem", "DisplayName=", "Chuzi Session Broker")
    } else {
        Invoke-Sc @("config", $brokerServiceName, "binPath=", $brokerPath, "start=", "demand", "obj=", "LocalSystem")
    }
    Invoke-Sc @("sidtype", $brokerServiceName, "unrestricted")
}

function Stop-ChuziServices {
    foreach ($name in @($brokerServiceName, $serviceName)) {
        $query = & "$env:SystemRoot\System32\sc.exe" query $name 2>$null | Out-String
        if ($LASTEXITCODE -eq 0 -and $query -notmatch 'STATE\s+:\s+\d+\s+STOPPED') {
            & "$env:SystemRoot\System32\sc.exe" stop $name | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "service_stop_failed" }
            Wait-Stopped $name
        }
    }
}

switch ($Operation) {
    "Status" {
        & "$env:SystemRoot\System32\sc.exe" query $serviceName
        exit $LASTEXITCODE
    }
    "Stop" { Stop-ChuziServices }
    "ConfirmResources" { Confirm-ResourcesReleased }
    "ConfirmDataOwnership" { Confirm-DataOwnership }
    "UpgradePrepare" {
        if (Test-Path -LiteralPath $backupMarker -PathType Leaf) {
            Rollback-Install
            Cleanup-InstallBackup
        }
        Stop-AndConfirm
    }
    "UninstallPrepare" {
        if (Test-Path -LiteralPath $backupMarker -PathType Leaf) {
            Rollback-Install
            Cleanup-InstallBackup
        }
        Stop-AndConfirm
    }
    "Backup" { Backup-Install }
    "Rollback" { Rollback-Install }
    "CleanupBackup" { Cleanup-InstallBackup }
    "Uninstall" {
        Stop-AndConfirm
        foreach ($name in @($brokerServiceName, $serviceName)) {
            & "$env:SystemRoot\System32\sc.exe" delete $name | Out-Null
            if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 1060 -and $LASTEXITCODE -ne 1072) { throw "service_delete_failed" }
        }
    }
    "Install" { Install-ChuziServices }
    "Repair" { Stop-AndConfirm; Install-ChuziServices }
}
Write-Output "service management $Operation completed"
