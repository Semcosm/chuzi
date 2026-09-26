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
    [string]$PresentMonPath = "",
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
Copy-Item -Force (Join-Path $GoDir "chuzi-browser-launcher.exe") (Join-Path $stageDir "chuzi-browser-launcher.exe")
tar -xzf $WorkerArchive -C (Join-Path $stageDir "browser-worker")
if ($LASTEXITCODE -ne 0) { throw "browser worker archive extraction failed" }
if ([string]::IsNullOrWhiteSpace($PresentMonPath)) {
    $PresentMonPath = Join-Path ([System.IO.Path]::GetTempPath()) "chuzi-presentmon/PresentMon.exe"
    & (Join-Path $repoRoot "scripts/prepare_presentmon.ps1") -OutputPath $PresentMonPath
    if ($LASTEXITCODE -ne 0) { throw "PresentMon preparation failed" }
}
$presentMonHash = (Get-FileHash -Path $PresentMonPath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($presentMonHash -ne "b2a706bc6ad475749e3b7e3409263aa1e6906d45bdcf993f6dbc0f660188f1af") {
    throw "PresentMon SHA256 mismatch"
}
Copy-Item -Force $PresentMonPath (Join-Path $stageDir "PresentMon.exe")
Copy-Item -Force (Join-Path $repoRoot "third_party/licenses/PresentMon-2.6.0-LICENSE.txt") (Join-Path $stageDir "PresentMon-LICENSE.txt")

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

$channel = if ($Version -match '^v[0-9]+\.[0-9]+\.[0-9]+$') { "stable" } elseif ($Version -like 'test-*') { "test" } else { "nightly" }
& python (Join-Path $repoRoot "scripts/generate_release_manifest.py") --stage $stageDir --target $Target --version $Version --commit $commit --channel $channel
if ($LASTEXITCODE -ne 0) { throw "release manifest generation failed" }
Write-Output "assembled $Target at $stageDir"
