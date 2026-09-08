param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("windows-amd64", "linux-amd64", "linux-arm64", "darwin-arm64")]
    [string]$Target,
    [Parameter(Mandatory = $true)]
    [string]$Version
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$stageDir = Join-Path $repoRoot "dist/$Target/stage"
$artifact = Join-Path $repoRoot "dist/chuzi-$Version-$Target.zip"

if (-not (Test-Path $stageDir)) {
    throw "build stage does not exist: $stageDir"
}
if (Test-Path $artifact) {
    Remove-Item -Force $artifact
}
Compress-Archive -Path (Join-Path $stageDir "*") -DestinationPath $artifact
$hash = (Get-FileHash -Algorithm SHA256 $artifact).Hash.ToLowerInvariant()
"$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
Write-Output "packaged $artifact"
