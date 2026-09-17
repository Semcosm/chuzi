param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("windows-amd64")]
    [string]$Target,
    [Parameter(Mandatory = $true)]
    [string]$OutputDir
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$outputPath = (Resolve-Path (New-Item -ItemType Directory -Force -Path $OutputDir)).Path

& cargo build --locked --manifest-path (Join-Path $repoRoot "browser-runtime/Cargo.toml") --release --features desktop-webview
if ($LASTEXITCODE -ne 0) { throw "browser runtime build failed" }
Copy-Item -Force (Join-Path $repoRoot "browser-runtime/target/release/chuzi-browser-runtime.exe") (Join-Path $outputPath "chuzi-browser-runtime.exe")
Write-Output "built native browser runtime for $Target at $outputPath"
