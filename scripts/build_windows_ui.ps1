param(
    [ValidateSet("Debug", "Release")]
    [string]$Configuration = "Release",
    [string]$OutputDir = ""
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($OutputDir)) {
    $OutputDir = Join-Path $repoRoot "dist/windows-ui"
}
$project = Join-Path $repoRoot "ui/windows/Chuzi.Native.Windows.csproj"
if (-not (Test-Path $project)) { throw "Windows client project is missing" }

if (Test-Path $OutputDir) { Remove-Item -Recurse -Force $OutputDir }
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$publishDir = Join-Path $OutputDir "publish"
& dotnet publish $project --configuration $Configuration --framework net8.0-windows10.0.19041.0 --runtime win-x64 --self-contained false --output $publishDir
if ($LASTEXITCODE -ne 0) { throw "Windows client publish failed" }

$version = $env:CHUZI_BUILD_VERSION
if ([string]::IsNullOrWhiteSpace($version)) { $version = "dev" }
$artifact = Join-Path $OutputDir "chuzi-native-windows-$version.zip"
if (Test-Path $artifact) { Remove-Item -Force $artifact }
Compress-Archive -Path (Join-Path $publishDir "*") -DestinationPath $artifact
$hash = (Get-FileHash -Algorithm SHA256 $artifact).Hash.ToLowerInvariant()
"$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
Write-Output "built Windows native client at $artifact"
