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

function Write-Utf8NoBom([string]$path, [string]$content) {
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($path, $content, $encoding)
}
$targetDir = Join-Path $DistRoot $Target
$stageDir = Join-Path $targetDir "stage"
if (Test-Path $targetDir) { Remove-Item -Recurse -Force $targetDir }
New-Item -ItemType Directory -Force -Path (Join-Path $stageDir "browser-worker") | Out-Null
Copy-Item -Force (Join-Path $GoDir "chuzi.exe") (Join-Path $stageDir "chuzi.exe")
Copy-Item -Force (Join-Path $GoDir "chuzi-launcher.exe") (Join-Path $stageDir "chuzi-launcher.exe")
Copy-Item -Force (Join-Path $GoDir "chuzi-browser-launcher.exe") (Join-Path $stageDir "chuzi-browser-launcher.exe")
Copy-Item -Force (Join-Path $GoDir "chuzi-user-agent.exe") (Join-Path $stageDir "chuzi-user-agent.exe")
Copy-Item -Force (Join-Path $GoDir "chuzi-session-broker.exe") (Join-Path $stageDir "chuzi-session-broker.exe")
$nodeCommand = Get-Command node.exe -CommandType Application -ErrorAction Stop
$nodePath = $nodeCommand.Source
if ([string]::IsNullOrWhiteSpace($nodePath) -or -not (Test-Path -Path $nodePath -PathType Leaf)) {
    throw "Node.js executable could not be resolved for the Windows runtime package"
}
Copy-Item -Force $nodePath (Join-Path $stageDir "node.exe")
Copy-Item -Force (Join-Path $repoRoot "scripts/session-shell.ps1") (Join-Path $stageDir "session-shell.ps1")
Copy-Item -Force (Join-Path $repoRoot "scripts/windows-service-management.ps1") (Join-Path $stageDir "windows-service-management.ps1")
Copy-Item -Force (Join-Path $repoRoot "scripts/validate_windows_payload.ps1") (Join-Path $stageDir "validate_windows_payload.ps1")
$signParameters = @{
    PayloadDir = $stageDir
    PolicyPath = (Join-Path $repoRoot "packaging/windows/authenticode-policy.json")
    Version = $Version
}
if ($Version -match '^v[0-9]+\.[0-9]+\.[0-9]+$' -or $env:CHUZI_REQUIRE_AUTHENTICODE -eq "1") {
    $signParameters.RequireProductionSigner = $true
}
& (Join-Path $repoRoot "scripts/sign_windows_payload.ps1") @signParameters
if ($LASTEXITCODE -ne 0) { throw "Windows Authenticode signing failed" }
tar -xzf $WorkerArchive -C (Join-Path $stageDir "browser-worker")
if ($LASTEXITCODE -ne 0) { throw "browser worker archive extraction failed" }
$adapterArchive = Join-Path $targetDir "chuzi-$Version-$Target-genshin-cloudgame.zip"
$adapterManifest = Join-Path $targetDir "genshin-cloudgame-adapter-manifest.json"
& python (Join-Path $repoRoot "scripts/build_adapter_package.py") `
    --source (Join-Path $repoRoot "browser-worker/adapters/genshin-cloudgame") `
    --output $adapterArchive --manifest-output $adapterManifest --version $Version
if ($LASTEXITCODE -ne 0) { throw "Genshin adapter package generation failed" }
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
} | ConvertTo-Json | ForEach-Object { Write-Utf8NoBom (Join-Path $stageDir "build-manifest.json") $_ }

$channel = if ($Version -match '^v[0-9]+\.[0-9]+\.[0-9]+$') { "stable" } elseif ($Version -like 'test-*') { "test" } else { "nightly" }
& python (Join-Path $repoRoot "scripts/generate_release_manifest.py") --stage $stageDir --target $Target --version $Version --commit $commit --channel $channel --adapter-archive $adapterArchive --adapter-manifest $adapterManifest
if ($LASTEXITCODE -ne 0) { throw "release manifest generation failed" }
Write-Output "assembled $Target at $stageDir"
