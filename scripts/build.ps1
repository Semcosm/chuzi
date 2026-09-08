param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("windows-amd64", "linux-amd64", "linux-arm64", "darwin-arm64")]
    [string]$Target,
    [string]$Version = "dev"
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$targetDir = Join-Path $repoRoot "dist/$Target"
$stageDir = Join-Path $targetDir "stage"

if ($Version -notmatch '^(dev|dev-|v[0-9]+\.[0-9]+\.[0-9]+)') {
    throw "invalid build version: $Version"
}

if (Test-Path $targetDir) {
    Remove-Item -Recurse -Force $targetDir
}
New-Item -ItemType Directory -Force -Path (Join-Path $stageDir "browser-worker") | Out-Null

$goos = ""
$goarch = ""
$binary = "chuzi"
switch ($Target) {
    "windows-amd64" { $goos = "windows"; $goarch = "amd64"; $binary = "chuzi.exe" }
    "linux-amd64" { $goos = "linux"; $goarch = "amd64" }
    "linux-arm64" { $goos = "linux"; $goarch = "arm64" }
    "darwin-arm64" { $goos = "darwin"; $goarch = "arm64" }
}

$env:CGO_ENABLED = "0"
$env:GOOS = $goos
$env:GOARCH = $goarch
$ldflags = "-s -w -X github.com/Semcosm/chuzi/cmd/service.version=$Version"
& go build -trimpath "-ldflags=$ldflags" -o (Join-Path $stageDir $binary) ./cmd/service
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

& npm --prefix (Join-Path $repoRoot "browser-worker") ci --ignore-scripts
if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
& npm --prefix (Join-Path $repoRoot "browser-worker") run build
if ($LASTEXITCODE -ne 0) { throw "browser worker build failed" }
Copy-Item -Recurse -Force (Join-Path $repoRoot "browser-worker/dist/*") (Join-Path $stageDir "browser-worker")

$commit = $env:GITHUB_SHA
if ([string]::IsNullOrWhiteSpace($commit)) {
    $commit = (& git -C $repoRoot rev-parse HEAD).Trim()
}
@{
    target = $Target
    version = $Version
    commit = $commit
    goos = $goos
    goarch = $goarch
    cgo = $false
} | ConvertTo-Json | Set-Content -Encoding utf8 (Join-Path $stageDir "build-manifest.json")

Write-Output "built $Target at $stageDir"
