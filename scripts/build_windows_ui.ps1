param(
    [ValidateSet("InstallerExe", "UnpackagedZip")]
    [string]$Mode = "UnpackagedZip",
    [ValidateSet("Debug", "Release")]
    [string]$Configuration = "Release",
    [string]$OutputDir = "",
    [string]$CorePayloadDir = ""
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($OutputDir)) {
    $OutputDir = Join-Path $repoRoot "dist/windows-ui"
}
$project = Join-Path $repoRoot "ui/windows/Chuzi.Native.Windows.csproj"
if (-not (Test-Path $project)) { throw "Windows client project is missing" }
$projectRoot = Split-Path $project -Parent
$projectPayload = Join-Path $projectRoot "CorePayload"

if (Test-Path $projectPayload) { Remove-Item -Recurse -Force $projectPayload }
if (-not [string]::IsNullOrWhiteSpace($CorePayloadDir)) {
    if (-not (Test-Path $CorePayloadDir)) { throw "Core payload directory is missing: $CorePayloadDir" }
    New-Item -ItemType Directory -Force -Path $projectPayload | Out-Null
    Copy-Item (Join-Path $CorePayloadDir "*") $projectPayload -Recurse -Force
}

if (Test-Path $OutputDir) { Remove-Item -Recurse -Force $OutputDir }
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$version = $env:CHUZI_BUILD_VERSION
if ([string]::IsNullOrWhiteSpace($version)) { $version = "dev" }

if ($Mode -eq "UnpackagedZip") {
    $publishDir = Join-Path $OutputDir "publish"
    & dotnet publish $project `
        --configuration $Configuration `
        --framework net8.0-windows10.0.19041.0 `
        --runtime win-x64 `
        --self-contained false `
        --output $publishDir `
        -p:Platform=x64 `
        -p:WindowsPackageType=None `
        -p:EnableMsixTooling=false `
        -p:WindowsAppSDKSelfContained=true `
        -p:GenerateAppxPackageOnBuild=false
    if ($LASTEXITCODE -ne 0) { throw "Windows client unpackaged publish failed" }

    $artifact = Join-Path $OutputDir "chuzi-native-windows-$version.zip"
    Compress-Archive -Path (Join-Path $publishDir "*") -DestinationPath $artifact
    $hash = (Get-FileHash -Algorithm SHA256 $artifact).Hash.ToLowerInvariant()
    "$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
    Write-Output "built unpackaged Windows native client at $artifact"
    if (Test-Path $projectPayload) { Remove-Item -Recurse -Force $projectPayload }
    return
}

$publishDir = Join-Path $OutputDir "payload"
& dotnet publish $project `
    --configuration $Configuration `
    --framework net8.0-windows10.0.19041.0 `
    --runtime win-x64 `
    --self-contained true `
    --output $publishDir `
    -p:Platform=x64 `
    -p:WindowsPackageType=None `
    -p:EnableMsixTooling=false `
    -p:WindowsAppSDKSelfContained=true `
    -p:GenerateAppxPackageOnBuild=false `
    -p:PublishSingleFile=false
if ($LASTEXITCODE -ne 0) { throw "Windows client self-contained publish failed" }

$installerScript = Join-Path $repoRoot "packaging/windows/Chuzi.iss"
if (-not (Test-Path $installerScript)) { throw "Windows installer script is missing" }
$iscc = $null
$isccCommand = Get-Command ISCC.exe -ErrorAction SilentlyContinue
if ($null -ne $isccCommand) {
    $iscc = $isccCommand.Source
}
if ([string]::IsNullOrWhiteSpace($iscc)) {
    foreach ($candidate in @(
        "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
        "$env:ProgramFiles\Inno Setup 6\ISCC.exe"
    )) {
        if (Test-Path $candidate) {
            $iscc = $candidate
            break
        }
    }
}
if ([string]::IsNullOrWhiteSpace($iscc)) {
    throw "ISCC.exe was not found; install Inno Setup 6 before building the Windows installer"
}

& $iscc "/DAppVersion=$version" "/DPayloadDir=$publishDir" "/DOutputDir=$OutputDir" $installerScript
if ($LASTEXITCODE -ne 0) { throw "Windows installer EXE build failed" }

$artifact = Join-Path $OutputDir "ChuziSetup.exe"
if (-not (Test-Path $artifact)) {
    Get-ChildItem -Path $OutputDir -Recurse | Select-Object FullName, Length | Format-Table -AutoSize
    throw "No Windows installer EXE was generated"
}
$hash = (Get-FileHash -Algorithm SHA256 $artifact).Hash.ToLowerInvariant()
"$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
Remove-Item -Recurse -Force $publishDir
if (Test-Path $projectPayload) { Remove-Item -Recurse -Force $projectPayload }
Write-Output "built Windows installer at $artifact"
