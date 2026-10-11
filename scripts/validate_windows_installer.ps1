param(
    [Parameter(Mandatory = $true)]
    [string]$InstallerPath,
    [Parameter(Mandatory = $true)]
    [string]$EvidencePath,
    [string]$PolicyPath = "",
    [switch]$Production,
    [switch]$Offline
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
if (-not (Test-Path -LiteralPath $InstallerPath -PathType Leaf)) { throw "installer_missing" }
if (-not (Test-Path -LiteralPath $EvidencePath -PathType Leaf)) { throw "installer_signing_evidence_missing" }
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($PolicyPath)) {
    $sidecarPolicy = Join-Path (Split-Path -Parent $EvidencePath) "authenticode-policy.json"
    $PolicyPath = if (Test-Path -LiteralPath $sidecarPolicy -PathType Leaf) { $sidecarPolicy } else { Join-Path $repoRoot "packaging/windows/authenticode-policy.json" }
}
$policy = Get-Content -Raw -LiteralPath $PolicyPath | ConvertFrom-Json
$evidence = Get-Content -Raw -LiteralPath $EvidencePath | ConvertFrom-Json
if ($policy.format -ne "chuzi-authenticode-policy/v1" -or $evidence.format -ne "chuzi-authenticode-evidence/v1") { throw "installer_signing_metadata_invalid" }
if ([string]$evidence.publisher_subject_prefix -ne [string]$policy.publisher_subject_prefix) { throw "installer_signing_metadata_mismatch" }
$expectedSigningOrder = @('authenticode', 'release-manifest', 'archive', 'release-index', 'attestation')
if ((@($policy.signing_order) -join ',') -ne ($expectedSigningOrder -join ',') -or
    (@($evidence.signing_order) -join ',') -ne ($expectedSigningOrder -join ',')) {
    throw "installer_signing_order_invalid"
}
$evidenceTimestampRequired = if ($null -eq $evidence.timestamp) { $false } else { [bool]$evidence.timestamp.required }
if ($evidenceTimestampRequired -ne [bool]$policy.required_timestamp) { throw "installer_timestamp_policy_mismatch" }
$allowlist = @($evidence.allowed_thumbprints | ForEach-Object { ([string]$_).Replace(" ", "").ToUpperInvariant() })
$policyAllowlist = @($policy.allowed_thumbprints | ForEach-Object { ([string]$_).Replace(" ", "").ToUpperInvariant() })
if ((@($allowlist | Sort-Object -Unique) -join ',') -ne (@($policyAllowlist | Sort-Object -Unique) -join ',')) { throw "installer_signing_metadata_mismatch" }
if ($Production -and $allowlist.Count -eq 0) { throw "production_signer_missing" }
if ($Production -and ($policyAllowlist.Count -eq 0 -or (@($policyAllowlist | Where-Object { $allowlist -notcontains $_ }).Count -gt 0 -or @($allowlist | Where-Object { $policyAllowlist -notcontains $_ }).Count -gt 0))) { throw "installer_signing_metadata_mismatch" }
if (-not $Production -and $evidence.status -eq "unsigned-development") {
    Write-Output "windows development installer is explicitly unsigned"
    exit 0
}
if ($Production -and ($evidence.status -ne "production-signed" -or [string]$evidence.artifact_kind -ne "installer" -or $evidence.production_release_allowed -ne $true)) { throw "production_installer_signing_invalid" }
if ($Production -and ($policy.status -ne "production-signed" -or [string]$policy.artifact_kind -ne "installer" -or $policy.production_release_allowed -ne $true)) { throw "production_installer_signing_invalid" }
if ($Production -and $evidence.hashes_computed_after_signing -ne $true) { throw "installer_signing_hash_order_invalid" }
$signature = Get-AuthenticodeSignature -LiteralPath $InstallerPath
if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid -or $null -eq $signature.SignerCertificate) { throw "installer_authenticode_invalid" }
if ($allowlist.Count -gt 0 -and $allowlist -notcontains $signature.SignerCertificate.Thumbprint.Replace(" ", "").ToUpperInvariant()) { throw "installer_publisher_mismatch" }
if (-not $signature.SignerCertificate.Subject.StartsWith([string]$evidence.publisher_subject_prefix, [StringComparison]::OrdinalIgnoreCase)) { throw "installer_publisher_mismatch" }
if ([bool]$policy.required_timestamp -and $null -eq $signature.TimeStamperCertificate) { throw "installer_timestamp_missing" }
if ($Production -and ([bool]$policy.required_timestamp) -and $evidence.timestamp.present -ne $true) { throw "installer_timestamp_evidence_missing" }
$record = @($evidence.files | Where-Object { [string]$_.path -eq [IO.Path]::GetFileName($InstallerPath) -or [string]$_.path -eq $InstallerPath })
if ($Production -and @($evidence.files).Count -ne 1) { throw "installer_signing_evidence_mismatch" }
if ($record.Count -ne 1) { throw "installer_signing_evidence_mismatch" }
if ($Production -and $record[0].timestamp_present -ne $true) { throw "installer_timestamp_evidence_missing" }
if ([string]$record[0].thumbprint -ne $signature.SignerCertificate.Thumbprint.Replace(" ", "").ToUpperInvariant()) { throw "installer_publisher_mismatch" }
if ([string]$record[0].sha256 -ne (Get-FileHash -Algorithm SHA256 -LiteralPath $InstallerPath).Hash.ToLowerInvariant()) { throw "installer_signing_hash_mismatch" }
if (-not $Offline) {
    $signtoolCommand = Get-Command signtool.exe -ErrorAction SilentlyContinue
    $signtool = if ($null -ne $signtoolCommand) { $signtoolCommand.Source } else { "" }
    if ([string]::IsNullOrWhiteSpace($signtool)) { throw "signtool_missing" }
    & $signtool verify /pa /all /tw $InstallerPath | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "installer_authenticode_trust_failed" }
}
Write-Output "windows installer signature validation passed"
