param(
    [Parameter(Mandatory = $true)]
    [string]$PayloadDir,
    [string]$PolicyPath = "",
    [string]$EvidencePath = "",
    [string]$DataRoot = "",
    [switch]$Production,
    [switch]$Offline
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$payload = (Resolve-Path $PayloadDir).Path
if (-not [string]::IsNullOrWhiteSpace($DataRoot)) {
    $data = (Resolve-Path $DataRoot).Path
    $configPath = Join-Path $data "core-config.json"
    if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) { throw "service_config_missing" }
    $config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
    if ([string]$config.data_dir -ne $data) { throw "service_config_invalid" }
    $systemSid = ([System.Security.Principal.SecurityIdentifier]::new("S-1-5-18")).Value
    $usersSid = ([System.Security.Principal.SecurityIdentifier]::new("S-1-5-32-545")).Value
    $writeRights = @([System.Security.AccessControl.FileSystemRights]::WriteData, [System.Security.AccessControl.FileSystemRights]::AppendData, [System.Security.AccessControl.FileSystemRights]::WriteAttributes, [System.Security.AccessControl.FileSystemRights]::WriteExtendedAttributes, [System.Security.AccessControl.FileSystemRights]::Delete)
    $ownershipPath = Join-Path $data "service-ownership.json"
    if (-not (Test-Path -LiteralPath $ownershipPath -PathType Leaf)) { throw "service_ownership_missing" }
    try {
        $ownership = Get-Content -Raw -LiteralPath $ownershipPath | ConvertFrom-Json
    } catch {
        throw "service_ownership_invalid"
    }
    if ([string]$ownership.format -ne "chuzi-service-ownership/v1" -or [string]$ownership.data_dir -ne $data) { throw "service_ownership_invalid" }
    foreach ($managedPath in @($data, $configPath, $ownershipPath) + @(("db", "profiles", "runtime", "journal", "secrets") | ForEach-Object { Join-Path $data $_ })) {
        if (-not (Test-Path -LiteralPath $managedPath)) { throw "service_data_path_missing" }
        $acl = Get-Acl -LiteralPath $managedPath
        $systemWritable = $false
        foreach ($rule in $acl.Access) {
            try { $ruleSid = $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value } catch { continue }
            $hasWrite = $false
            foreach ($right in $writeRights) {
                if (($rule.FileSystemRights -band $right) -ne [System.Security.AccessControl.FileSystemRights]::None) { $hasWrite = $true; break }
            }
            if ($ruleSid -eq $usersSid -and $rule.AccessControlType -eq [System.Security.AccessControl.AccessControlType]::Allow -and $hasWrite) { throw "service_data_acl_too_wide" }
            if ($ruleSid -eq $systemSid -and $rule.AccessControlType -eq [System.Security.AccessControl.AccessControlType]::Allow -and $hasWrite) { $systemWritable = $true }
        }
        if (-not $systemWritable) { throw "service_data_acl_missing" }
    }
}
if ([string]::IsNullOrWhiteSpace($PolicyPath)) { $PolicyPath = Join-Path $payload "authenticode-policy.json" }
if ([string]::IsNullOrWhiteSpace($EvidencePath)) { $EvidencePath = Join-Path $payload "authenticode-signing.json" }
if (-not (Test-Path -LiteralPath $PolicyPath -PathType Leaf)) { throw "authenticode_policy_missing" }
if (-not (Test-Path -LiteralPath $EvidencePath -PathType Leaf)) { throw "authenticode_evidence_missing" }

$policy = Get-Content -Raw -LiteralPath $PolicyPath | ConvertFrom-Json
$evidence = Get-Content -Raw -LiteralPath $EvidencePath | ConvertFrom-Json
if ($policy.format -ne "chuzi-authenticode-policy/v1") { throw "authenticode_policy_invalid" }
if ($evidence.format -ne "chuzi-authenticode-evidence/v1") { throw "authenticode_evidence_invalid" }
if ([string]::IsNullOrWhiteSpace([string]$policy.publisher_subject_prefix)) { throw "authenticode_publisher_missing" }
$expectedSigningOrder = @('authenticode', 'release-manifest', 'archive', 'release-index', 'attestation')
if ((@($policy.signing_order) -join ',') -ne ($expectedSigningOrder -join ',') -or
    (@($evidence.signing_order) -join ',') -ne ($expectedSigningOrder -join ',')) {
    throw "authenticode_signing_order_invalid"
}
$evidenceTimestampRequired = if ($null -eq $evidence.timestamp) { $false } else { [bool]$evidence.timestamp.required }
if ($evidenceTimestampRequired -ne [bool]$policy.required_timestamp) { throw "authenticode_timestamp_policy_mismatch" }

$required = if ($Production) { @($policy.production_required_files) } else { @($policy.required_files) }
if ($required.Count -eq 0) { throw "authenticode_required_files_missing" }
$allowlist = @($policy.allowed_thumbprints | ForEach-Object { ([string]$_).Replace(" ", "").ToUpperInvariant() })
$evidenceAllowlist = @($evidence.allowed_thumbprints | ForEach-Object { ([string]$_).Replace(" ", "").ToUpperInvariant() })
if ([string]$evidence.publisher_subject_prefix -ne [string]$policy.publisher_subject_prefix) { throw "authenticode_evidence_mismatch" }
if ((@($evidenceAllowlist | Where-Object { $allowlist -notcontains $_ }).Count -gt 0) -or (@($allowlist | Where-Object { $evidenceAllowlist -notcontains $_ }).Count -gt 0)) { throw "authenticode_evidence_mismatch" }
if ($Production -and $allowlist.Count -eq 0) { throw "production_signer_missing" }
$signedByEvidence = @($evidence.files | ForEach-Object { [string]$_.path })
if ($Production -and $evidence.status -ne "production-signed") { throw "production_signing_evidence_invalid" }
if ($Production -and $policy.status -ne "production-signed") { throw "production_signing_policy_invalid" }
if ($Production -and -not [bool]$evidence.production_release_allowed) { throw "production_release_not_allowed" }
if ($Production -and [string]$evidence.artifact_kind -ne "payload") { throw "production_artifact_scope_invalid" }
if ($Production -and [string]$policy.artifact_kind -ne "payload") { throw "production_artifact_scope_invalid" }
if ($Production -and -not [bool]$evidence.hashes_computed_after_signing) { throw "hash_order_invalid" }
foreach ($relative in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $payload ([string]$relative)) -PathType Leaf)) { throw "authenticode_required_file_missing" }
}
if (-not $Production -and $evidence.status -eq "unsigned-development") {
    if (@($evidence.files).Count -ne 0 -or [bool]$evidence.production_release_allowed -or [bool]$policy.production_release_allowed) {
        throw "unsigned_development_evidence_invalid"
    }
    Write-Output "windows development payload is explicitly unsigned"
    exit 0
}

foreach ($relative in $required) {
    $path = Join-Path $payload ([string]$relative)
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "authenticode_required_file_missing" }
    $signature = Get-AuthenticodeSignature -LiteralPath $path
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid) {
        throw "authenticode_verify_failed"
    }
    if ($null -eq $signature.SignerCertificate) { throw "authenticode_signer_missing" }
    $thumbprint = $signature.SignerCertificate.Thumbprint.Replace(" ", "").ToUpperInvariant()
    if ($allowlist -notcontains $thumbprint) { throw "authenticode_publisher_mismatch" }
    if (-not $signature.SignerCertificate.Subject.StartsWith([string]$policy.publisher_subject_prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "authenticode_publisher_mismatch"
    }
    if ([bool]$policy.required_timestamp -and $null -eq $signature.TimeStamperCertificate) {
        throw "authenticode_timestamp_missing"
    }
    $record = @($evidence.files | Where-Object { [string]$_.path -eq [string]$relative })
    if ($record.Count -ne 1 -or [string]$record[0].thumbprint -ne $thumbprint) { throw "authenticode_evidence_mismatch" }
    if ([bool]$record[0].timestamp_present -ne ($null -ne $signature.TimeStamperCertificate)) { throw "authenticode_evidence_mismatch" }
    if ([string]$record[0].sha256 -ne (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()) {
        throw "authenticode_evidence_hash_mismatch"
    }
    if (-not $Offline -and [IO.Path]::GetExtension($path).ToLowerInvariant() -ne ".ps1") {
        $signtoolCommand = Get-Command signtool.exe -ErrorAction SilentlyContinue
        $signtool = if ($null -ne $signtoolCommand) { $signtoolCommand.Source } else { "" }
        if ([string]::IsNullOrWhiteSpace($signtool)) { throw "signtool_missing" }
        & $signtool verify /pa /all /tw $path | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "authenticode_trust_failed" }
    }
}

if ($Production -and ($signedByEvidence.Count -ne $required.Count -or ((@($signedByEvidence | Sort-Object -Unique) -join ',') -ne (@($required | Sort-Object -Unique) -join ',')))) { throw "authenticode_file_set_incomplete" }
if ($Production -and [bool]$policy.production_release_allowed -ne $true) { throw "production_release_not_allowed" }
Write-Output "windows payload signature validation passed"
