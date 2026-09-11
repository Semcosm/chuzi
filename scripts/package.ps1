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
foreach ($component in @("launcher", "service", "browser-worker", "desktop-runtime")) {
    $componentRoot = Join-Path $repoRoot "dist/component-$component"
    if (Test-Path $componentRoot) { Remove-Item -Recurse -Force $componentRoot }
    New-Item -ItemType Directory -Force -Path $componentRoot | Out-Null
    switch ($component) {
        "launcher" { Copy-Item -Force (Join-Path $stageDir "chuzi-launcher.exe") $componentRoot; Copy-Item -Force (Join-Path $stageDir "release-manifest.json") $componentRoot }
        "service" { Copy-Item -Force (Join-Path $stageDir "chuzi.exe") $componentRoot }
        "browser-worker" { Copy-Item -Recurse -Force (Join-Path $stageDir "browser-worker") $componentRoot }
        "desktop-runtime" { Copy-Item -Force (Join-Path $stageDir "chuzi-browser-runtime.exe") $componentRoot }
    }
    $componentArtifact = Join-Path $repoRoot "dist/chuzi-$Version-$Target-$component.zip"
    Compress-Archive -Path (Join-Path $componentRoot "*") -DestinationPath $componentArtifact
    $componentHash = (Get-FileHash -Algorithm SHA256 $componentArtifact).Hash.ToLowerInvariant()
    "$componentHash  $(Split-Path -Leaf $componentArtifact)" | Set-Content -Encoding ascii "$componentArtifact.sha256"
    Remove-Item -Recurse -Force $componentRoot
}
$manifestCopy = Join-Path $repoRoot "dist/chuzi-$Version-$Target.manifest.json"
Copy-Item -Force (Join-Path $stageDir "release-manifest.json") $manifestCopy
$hash = (Get-FileHash -Algorithm SHA256 $artifact).Hash.ToLowerInvariant()
"$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
Write-Output "packaged $artifact"
