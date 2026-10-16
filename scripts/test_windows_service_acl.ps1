$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Run the actual installer functions on owned temporary files under Windows
# PowerShell 5.1. Do not execute the script's service operations.
$tokens = $null
$parseErrors = $null
$source = Join-Path $PSScriptRoot 'windows-service-management.ps1'
$ast = [Management.Automation.Language.Parser]::ParseFile($source, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Service management script has parser errors' }
foreach ($definition in $ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst]}, $true)) {
    . ([scriptblock]::Create($definition.Extent.Text))
}

function Assert-Acl([string]$path, [bool]$Writable) {
    $item = Get-Item -LiteralPath $path -Force
    $acl = Get-Acl -LiteralPath $path
    $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    if (-not $acl.AreAccessRulesProtected -or $rules.Count -ne 3) { throw "Invalid protected allow list: $path" }
    $systemRights = if ($Writable) { 'Modify' } else { 'FullControl' }
    $expected = @{'S-1-5-18' = $systemRights; 'S-1-5-32-544' = 'FullControl'; 'S-1-5-32-545' = 'ReadAndExecute'}
    foreach ($rule in $rules) {
        $sid = $rule.IdentityReference.Value
        if (-not $expected.ContainsKey($sid) -or $rule.AccessControlType -ne 'Allow' -or $rule.IsInherited) { throw "Unexpected ACL principal: $path" }
        $rights = [Security.AccessControl.FileSystemRights]$expected[$sid] -bor [Security.AccessControl.FileSystemRights]::Synchronize
        if ($rule.FileSystemRights -ne $rights) { throw "Unexpected rights for $sid on $path" }
        $inheritance = if ($item.PSIsContainer) { [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit' } else { [Security.AccessControl.InheritanceFlags]::None }
        if ($rule.InheritanceFlags -ne $inheritance -or $rule.PropagationFlags -ne 'None') { throw "Invalid inheritance: $path" }
    }
}

function Set-FixtureAcl([string]$path, [bool]$Empty = $false) {
    $item = Get-Item -LiteralPath $path -Force
    $acl = if ($item.PSIsContainer) { New-Object Security.AccessControl.DirectorySecurity } else { New-Object Security.AccessControl.FileSecurity }
    $acl.SetAccessRuleProtection($true, $false)
    if (-not $Empty) {
        $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'Allow')))
    }
    if ($item.PSIsContainer) { [IO.Directory]::SetAccessControl($item.FullName, $acl) } else { [IO.File]::SetAccessControl($item.FullName, $acl) }
}

function Assert-Rejected([scriptblock]$action) {
    try { & $action } catch {
        if ($_.Exception.Message -eq 'resource_ownership_unknown') { return }
        throw
    }
    throw 'Unsafe ownership or reparse point was accepted'
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('chuzi-service-acl-' + [guid]::NewGuid().ToString('N'))
$install = Join-Path $testRoot 'install'
$runtime = Join-Path $install 'CorePayload'
$data = Join-Path $testRoot 'data'
$ownershipMarker = Join-Path $data 'service-ownership.json'
$config = Join-Path $data 'core-config.json'
$junction = Join-Path $runtime 'junction'
New-Item -ItemType Directory -Path (Join-Path $runtime 'nested'), $data | Out-Null
try {
    $manifest = Join-Path $runtime 'build-manifest.json'
    $nested = Join-Path $runtime 'nested/worker.json'
    [IO.File]::WriteAllText($manifest, '{"version":"test-acl"}')
    [IO.File]::WriteAllText($nested, '{}')
    [IO.File]::WriteAllText($config, '{"data_dir":"test"}')
    $untouched = Join-Path $data 'component.json'
    [IO.File]::WriteAllText($untouched, '{}')
    $ownership = [ordered]@{format='chuzi-service-ownership/v1'; data_dir=$data; install_root=$install}
    [IO.File]::WriteAllText($ownershipMarker, ($ownership | ConvertTo-Json))
    $owner = (Get-Acl -LiteralPath $manifest).Owner
    Set-ChuziAcl $runtime $false
    foreach ($item in @((Get-Item -LiteralPath $runtime)) + @(Get-ChildItem -LiteralPath $runtime -Recurse -Force)) { Assert-Acl $item.FullName $false }
    if ((Get-Content -Raw -LiteralPath $manifest) -ne '{"version":"test-acl"}' -or (Get-Acl -LiteralPath $manifest).Owner -ne $owner) { throw 'Runtime content or owner changed' }
    $before = (Get-Acl -LiteralPath $nested).Sddl
    Set-ChuziAcl $runtime $false
    if ((Get-Acl -LiteralPath $nested).Sddl -ne $before) { throw 'Runtime ACL setup is not idempotent' }
    Set-ChuziAcl $data $true $false
    Assert-Acl $data $true
    Set-ChuziAcl $config $true $false
    Assert-Acl $config $true
    Get-Content -Raw -LiteralPath $config | Out-Null

    # Non-recursive setup must leave component files alone.
    Set-FixtureAcl $untouched
    $before = (Get-Acl -LiteralPath $untouched).Sddl
    Set-ChuziAcl $data $true $false
    if ((Get-Acl -LiteralPath $untouched).Sddl -ne $before) { throw 'Non-recursive setup rewrote a component ACL' }

    $hashes = @{}
    foreach ($path in @($ownershipMarker, $config, $manifest, $nested)) {
        $hashes[$path] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
        Set-FixtureAcl $path $true
    }
    $denied = $false
    try { Get-Content -Raw -LiteralPath $manifest | Out-Null } catch [UnauthorizedAccessException] { $denied = $true }
    if (-not $denied) { throw 'Empty DACL did not deny read' }
    Repair-ChuziFileAccess
    foreach ($path in @($ownershipMarker, $config, $manifest, $nested)) {
        Assert-Acl $path $false
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $hashes[$path]) { throw 'Recovery changed file content' }
    }
    $before = (Get-Acl -LiteralPath $config).Sddl
    Repair-ChuziFileAccess
    if ((Get-Acl -LiteralPath $config).Sddl -ne $before) { throw 'Recovery changed a healthy ACL' }

    # A mismatched ownership marker must not authorize config/payload repair.
    $ownership.install_root = Join-Path $testRoot 'foreign-install'
    Set-FixtureAcl $ownershipMarker
    [IO.File]::WriteAllText($ownershipMarker, ($ownership | ConvertTo-Json))
    foreach ($path in @($ownershipMarker, $config, $manifest)) { Set-FixtureAcl $path $true }
    $markerAcl = (Get-Acl -LiteralPath $ownershipMarker).Sddl
    Assert-Rejected { Repair-ChuziFileAccess }
    foreach ($path in @($ownershipMarker, $config, $manifest)) {
        if (@((Get-Acl -LiteralPath $path).Access).Count -ne 0) { throw 'Recovery adopted unverified data' }
    }
    if ((Get-Acl -LiteralPath $ownershipMarker).Sddl -ne $markerAcl) { throw 'Invalid marker ACL was not restored' }

    # Check both an embedded junction and a reparse point in a parent path.
    $outside = Join-Path $testRoot 'outside'
    New-Item -ItemType Directory -Path $outside | Out-Null
    $outsideFile = Join-Path $outside 'foreign.json'
    [IO.File]::WriteAllText($outsideFile, '{}')
    $before = (Get-Acl -LiteralPath $outsideFile).Sddl
    Set-FixtureAcl $runtime
    New-Item -ItemType Junction -Path $junction -Value $outside | Out-Null
    Assert-Rejected { Set-ChuziAcl $runtime $false }
    Assert-Rejected { Set-ChuziAcl (Join-Path $junction 'foreign.json') $false $false }
    if ((Get-Acl -LiteralPath $outsideFile).Sddl -ne $before) { throw 'ACL setup changed a junction target' }
    Write-Host 'Windows service file/directory ACL and interrupted-install recovery tests passed'
} finally {
    # Restore access only to owned fixtures, without requiring audit privileges.
    if (Test-Path -LiteralPath $junction) { [IO.Directory]::Delete($junction) }
    $cleanupItems = @((Get-Item -LiteralPath $testRoot -Force)) + @(Get-ChildItem -LiteralPath $testRoot -Recurse -Force)
    foreach ($item in $cleanupItems) { Set-FixtureAcl $item.FullName }
    $resolved = [IO.Path]::GetFullPath($testRoot)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $resolved.StartsWith($temp, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $resolved) -notlike 'chuzi-service-acl-*') { throw 'Invalid test cleanup root' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
