param(
    [ValidateSet("PackagedMsix", "UnpackagedZip")]
    [string]$Mode = "UnpackagedZip",
    [ValidateSet("Debug", "Release")]
    [string]$Configuration = "Release",
    [string]$OutputDir = "",
    [string]$CertificateThumbprint = ""
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
        -p:GenerateAppxPackageOnBuild=false
    if ($LASTEXITCODE -ne 0) { throw "Windows client unpackaged publish failed" }

    $artifact = Join-Path $OutputDir "chuzi-native-windows-$version.zip"
    Compress-Archive -Path (Join-Path $publishDir "*") -DestinationPath $artifact
    $hash = (Get-FileHash -Algorithm SHA256 $artifact).Hash.ToLowerInvariant()
    "$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
    Write-Output "built unpackaged Windows native client at $artifact"
    return
}

if ([string]::IsNullOrWhiteSpace($CertificateThumbprint)) {
    $CertificateThumbprint = $env:CHUZI_PACKAGE_CERT_THUMBPRINT
}
if ([string]::IsNullOrWhiteSpace($CertificateThumbprint)) {
    throw "PackagedMsix mode requires -CertificateThumbprint or CHUZI_PACKAGE_CERT_THUMBPRINT"
}

$packageRoot = Join-Path $OutputDir "package"
New-Item -ItemType Directory -Force -Path $packageRoot | Out-Null
& dotnet publish $project `
    --configuration $Configuration `
    --framework net8.0-windows10.0.19041.0 `
    --runtime win-x64 `
    --self-contained false `
    --output $packageRoot `
    -p:Platform=x64 `
    -p:PublishProfile=win-x64.pubxml `
    -p:WindowsPackageType=MSIX `
    -p:EnableMsixTooling=true `
    -p:WindowsAppSDKSelfContained=true `
    -p:GenerateAppxPackageOnBuild=true `
    -p:AppxBundle=Never `
    -p:AppxPackageDir="$packageRoot\" `
    -p:AppxPackageSigningEnabled=true `
    -p:PackageCertificateThumbprint=$CertificateThumbprint
if ($LASTEXITCODE -ne 0) { throw "Windows client MSIX publish failed" }

$packages = @(Get-ChildItem -Path $packageRoot -Recurse -Include *.msix,*.msixbundle,*.appx)
if ($packages.Count -eq 0) {
    Get-ChildItem -Path $packageRoot -Recurse | Select-Object FullName, Length | Format-Table -AutoSize
    throw "No Windows MSIX package was generated"
}
$packages | Copy-Item -Destination $OutputDir -Force
$packages | ForEach-Object { Write-Output "built Windows package at $($_.FullName)" }
