param(
    [Parameter(Mandatory = $true)]
    [string]$PayloadDir,
    [string]$PolicyPath = "",
    [string]$EvidencePath = "",
    [string]$Version = "dev",
    [string[]]$Files = @(),
    [ValidateSet("payload", "ui", "installer")]
    [string]$ArtifactKind = "payload",
    [switch]$RequireProductionSigner,
    [switch]$Offline
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Write-Utf8NoBom([string]$path, [string]$content) {
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($path, $content, $encoding)
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$payload = (Resolve-Path $PayloadDir).Path
if ([string]::IsNullOrWhiteSpace($PolicyPath)) {
    $PolicyPath = Join-Path $repoRoot "packaging/windows/authenticode-policy.json"
}
if (-not (Test-Path -LiteralPath $PolicyPath -PathType Leaf)) {
    throw "authenticode_policy_missing"
}

$policy = Get-Content -Raw -LiteralPath $PolicyPath | ConvertFrom-Json
if ($policy.format -ne "chuzi-authenticode-policy/v1") { throw "authenticode_policy_invalid" }
if ([string]::IsNullOrWhiteSpace([string]$policy.publisher_subject_prefix)) { throw "authenticode_publisher_missing" }

$production = $RequireProductionSigner.IsPresent -or $Version -match '^v[0-9]+\.[0-9]+\.[0-9]+$' -or $env:CHUZI_REQUIRE_AUTHENTICODE -eq "1"
$configuredThumbprints = @()
if ($production -and -not [string]::IsNullOrWhiteSpace($env:CHUZI_AUTHENTICODE_THUMBPRINTS)) {
    $configuredThumbprints = @($env:CHUZI_AUTHENTICODE_THUMBPRINTS -split '[,; ]+' | Where-Object { $_ })
}
if ($production -and $configuredThumbprints.Count -eq 0 -and $null -ne $policy.allowed_thumbprints) {
    $configuredThumbprints = @($policy.allowed_thumbprints | Where-Object { $_ })
}
$configuredThumbprints = @($configuredThumbprints | ForEach-Object { ([string]$_).Replace(" ", "").ToUpperInvariant() } | Select-Object -Unique)

$timestampUrl = [string]$env:CHUZI_AUTHENTICODE_TIMESTAMP_URL
if ($production -and $configuredThumbprints.Count -eq 0) {
    throw "production_signer_missing"
}
if ($production -and [string]::IsNullOrWhiteSpace($timestampUrl)) {
    throw "production_timestamp_missing"
}
if ($configuredThumbprints.Count -gt 0 -and ($configuredThumbprints | Where-Object { $_ -notmatch '^[0-9A-F]{40}$' })) {
    throw "authenticode_thumbprint_invalid"
}

$signtoolCommand = Get-Command signtool.exe -ErrorAction SilentlyContinue
$signtool = if ($null -ne $signtoolCommand) { $signtoolCommand.Source } else { "" }
if ($configuredThumbprints.Count -gt 0 -and [string]::IsNullOrWhiteSpace($signtool)) {
    throw "signtool_missing"
}

function Resolve-Certificate([string]$thumbprint) {
    $normalized = $thumbprint.Replace(" ", "").ToUpperInvariant()
    $certificate = $null
    foreach ($store in @('Cert:\CurrentUser\My', 'Cert:\LocalMachine\My')) {
        $certificate = Get-ChildItem -Path $store |
            Where-Object { $_.Thumbprint.Replace(" ", "").ToUpperInvariant() -eq $normalized } |
            Select-Object -First 1
        if ($null -ne $certificate) { break }
    }
    if ($null -eq $certificate) { throw "authenticode_certificate_missing" }
    if (-not $certificate.HasPrivateKey) { throw "authenticode_private_key_unavailable" }
    $now = [DateTime]::UtcNow
    if ($certificate.NotBefore.ToUniversalTime() -gt $now) { throw "authenticode_certificate_not_yet_valid" }
    if ($certificate.NotAfter.ToUniversalTime() -le $now) { throw "authenticode_certificate_expired" }
    if (-not $certificate.Subject.StartsWith([string]$policy.publisher_subject_prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "authenticode_publisher_mismatch"
    }
    return $certificate
}

$relativeFiles = @()
$filePolicy = if ($production) { @($policy.production_required_files) } else { @($policy.required_files) }
if ($filePolicy.Count -eq 0) { throw "authenticode_required_file_invalid" }
if ($Files.Count -gt 0) {
    foreach ($file in $Files) {
        $resolved = (Resolve-Path -LiteralPath $file).Path
        $payloadPrefix = $payload.TrimEnd('\') + [IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($payloadPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw "authenticode_file_outside_payload" }
        $relative = $resolved.Substring($payloadPrefix.Length).Replace('\', '/')
        if ([string]::IsNullOrWhiteSpace($relative) -or $relative -match '(^|/)(\.|\.\.)($|/)') { throw "authenticode_file_path_invalid" }
        if (@($relativeFiles | Where-Object { $_.Relative -eq $relative }).Count -gt 0) { throw "authenticode_file_duplicate" }
        $relativeFiles += [PSCustomObject]@{ Path = $resolved; Relative = $relative }
    }
} else {
    foreach ($required in $filePolicy) {
        if ([string]::IsNullOrWhiteSpace([string]$required)) { throw "authenticode_required_file_invalid" }
        $candidate = Join-Path $payload ([string]$required)
        if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) { throw "authenticode_required_file_missing" }
        $relativeFiles += [PSCustomObject]@{ Path = (Resolve-Path -LiteralPath $candidate).Path; Relative = [string]$required }
    }
}

$signed = @()
$certificate = $null
if ($configuredThumbprints.Count -gt 0) {
    foreach ($candidateThumbprint in $configuredThumbprints) {
        try {
            $certificate = Resolve-Certificate $candidateThumbprint
            if ($null -ne $certificate) { break }
        } catch {
            $certificate = $null
        }
    }
    if ($null -eq $certificate) { throw "authenticode_certificate_missing" }
    $actualThumbprint = $certificate.Thumbprint.Replace(" ", "").ToUpperInvariant()
    if ($configuredThumbprints -notcontains $actualThumbprint) { throw "authenticode_thumbprint_mismatch" }
    foreach ($entry in $relativeFiles) {
        $path = $entry.Path
        $extension = [IO.Path]::GetExtension($path).ToLowerInvariant()
        if ($extension -eq ".ps1") {
            $arguments = @{ FilePath = $path; Certificate = $certificate; HashAlgorithm = "SHA256"; ErrorAction = "Stop" }
            if (-not [string]::IsNullOrWhiteSpace($timestampUrl)) { $arguments.TimestampServer = $timestampUrl }
            $result = Set-AuthenticodeSignature @arguments
            if ($result.Status -ne [Management.Automation.SignatureStatus]::Valid) { throw "authenticode_sign_failed" }
        } else {
            $signArgs = @('sign', '/sha1', $actualThumbprint, '/fd', 'SHA256')
            if (-not [string]::IsNullOrWhiteSpace($timestampUrl)) { $signArgs += @('/tr', $timestampUrl, '/td', 'SHA256') }
            $signArgs += $path
            & $signtool @signArgs | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "authenticode_sign_failed" }
        }
        $signed += $entry.Relative
    }
}

function Test-SignedFile([string]$path, [string]$relative) {
    $signature = Get-AuthenticodeSignature -LiteralPath $path
    if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid) { throw "authenticode_verify_failed" }
    if ($null -eq $signature.SignerCertificate) { throw "authenticode_signer_missing" }
    $thumbprint = $signature.SignerCertificate.Thumbprint.Replace(" ", "").ToUpperInvariant()
    if ($configuredThumbprints -notcontains $thumbprint) { throw "authenticode_publisher_mismatch" }
    if (-not $signature.SignerCertificate.Subject.StartsWith([string]$policy.publisher_subject_prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "authenticode_publisher_mismatch"
    }
    $timestampPresent = $null -ne $signature.TimeStamperCertificate
    if ($production -and $policy.required_timestamp -and -not $timestampPresent) { throw "authenticode_timestamp_missing" }
    if (-not $Offline -and [IO.Path]::GetExtension($path).ToLowerInvariant() -ne ".ps1") {
        $verifyArgs = @('verify', '/pa', '/all')
        if ($timestampPresent) { $verifyArgs += '/tw' }
        $verifyArgs += $path
        & $signtool @verifyArgs | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "authenticode_trust_failed" }
    }
    return [PSCustomObject]@{
        path = $relative
        thumbprint = $thumbprint
        subject = $signature.SignerCertificate.Subject
        timestamp_present = $timestampPresent
        sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
    }
}

$verified = @()
if ($signed.Count -gt 0) {
    foreach ($entry in $relativeFiles) { $verified += Test-SignedFile $entry.Path $entry.Relative }
}

if ([string]::IsNullOrWhiteSpace($EvidencePath)) { $EvidencePath = Join-Path $payload "authenticode-signing.json" }
$completeProductionSet = $ArtifactKind -eq "payload" -and $Files.Count -eq 0
$productionScopeComplete = ($ArtifactKind -eq "payload" -and $completeProductionSet) -or
    ($ArtifactKind -in @('ui', 'installer') -and $Files.Count -gt 0)
$productionReleaseAllowed = $production -and $productionScopeComplete -and $signed.Count -eq $relativeFiles.Count
$resolvedPolicy = [ordered]@{}
foreach ($property in $policy.PSObject.Properties) { $resolvedPolicy[$property.Name] = $property.Value }
$resolvedPolicy.allowed_thumbprints = @($configuredThumbprints)
$resolvedPolicy.artifact_kind = $ArtifactKind
$resolvedPolicy.status = if ($signed.Count -gt 0) { if ($production) { "production-signed" } else { "test-signed" } } else { "unsigned-development" }
$resolvedPolicy.production_release_allowed = $productionReleaseAllowed
$resolvedPolicy.signing_order = @('authenticode', 'release-manifest', 'archive', 'release-index', 'attestation')
$policyOutput = if ($Files.Count -eq 0) { Join-Path $payload "authenticode-policy.json" } else { Join-Path (Split-Path -Parent $EvidencePath) "authenticode-policy.json" }
$policyJson = $resolvedPolicy | ConvertTo-Json -Depth 8
Write-Utf8NoBom $policyOutput $policyJson

$evidence = [ordered]@{
    format = "chuzi-authenticode-evidence/v1"
    status = if ($signed.Count -gt 0) { if ($production) { "production-signed" } else { "test-signed" } } else { "unsigned-development" }
    production_release_allowed = $productionReleaseAllowed
    artifact_kind = $ArtifactKind
    publisher_subject_prefix = [string]$policy.publisher_subject_prefix
    allowed_thumbprints = @($configuredThumbprints)
    files = @($verified)
    signing_order = @('authenticode', 'release-manifest', 'archive', 'release-index', 'attestation')
    hashes_computed_after_signing = $signed.Count -gt 0
    timestamp = [ordered]@{ required = [bool]$policy.required_timestamp; present = (@($verified | Where-Object { $_.timestamp_present }).Count -eq $verified.Count -and $verified.Count -gt 0) }
    offline_validation = if ($Offline) { "cached-chain-and-timestamp" } else { "online-chain-at-sign-time" }
}
$evidenceJson = $evidence | ConvertTo-Json -Depth 8
Write-Utf8NoBom $EvidencePath $evidenceJson
Write-Output $evidence.status
