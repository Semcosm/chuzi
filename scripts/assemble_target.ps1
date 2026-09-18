param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("windows-amd64")]
    [string]$Target,
    [Parameter(Mandatory = $true)]
    [string]$Version,
    [Parameter(Mandatory = $true)]
    [string]$GoDir,
    [Parameter(Mandatory = $true)]
    [string]$WorkerArchive,
    [Parameter(Mandatory = $true)]
    [string]$DistRoot
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$targetDir = Join-Path $DistRoot $Target
$stageDir = Join-Path $targetDir "stage"
if (Test-Path $targetDir) { Remove-Item -Recurse -Force $targetDir }
New-Item -ItemType Directory -Force -Path (Join-Path $stageDir "browser-worker") | Out-Null
Copy-Item -Force (Join-Path $GoDir "chuzi.exe") (Join-Path $stageDir "chuzi.exe")
Copy-Item -Force (Join-Path $GoDir "chuzi-launcher.exe") (Join-Path $stageDir "chuzi-launcher.exe")
tar -xzf $WorkerArchive -C (Join-Path $stageDir "browser-worker")
if ($LASTEXITCODE -ne 0) { throw "browser worker archive extraction failed" }

$commit = $env:GITHUB_SHA
if ([string]::IsNullOrWhiteSpace($commit)) { $commit = (& git -C $repoRoot rev-parse HEAD).Trim() }
@{
    target = $Target
    version = $Version
    commit = $commit
    goos = "windows"
    goarch = "amd64"
    cgo = $false
} | ConvertTo-Json | Set-Content -Encoding utf8 (Join-Path $stageDir "build-manifest.json")

$channel = if ($Version -match '^v[0-9]+\.[0-9]+\.[0-9]+$') { "stable" } else { "nightly" }
& python (Join-Path $repoRoot "scripts/generate_release_manifest.py") --stage $stageDir --target $Target --version $Version --commit $commit --channel $channel
if ($LASTEXITCODE -ne 0) { throw "release manifest generation failed" }
Write-Output "assembled $Target at $stageDir"
