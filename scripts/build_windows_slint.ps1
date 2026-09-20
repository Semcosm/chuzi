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
$projectRoot = Join-Path $repoRoot "ui/windows"
$manifest = Join-Path $projectRoot "Cargo.toml"
if (-not (Test-Path $manifest)) { throw "Slint Windows client manifest is missing: $manifest" }
$version = $env:CHUZI_BUILD_VERSION
if ([string]::IsNullOrWhiteSpace($version)) { $version = "dev" }

if ([string]::IsNullOrWhiteSpace($OutputDir)) {
    $OutputDir = Join-Path $repoRoot "dist/windows-ui"
}
if (Test-Path $OutputDir) { Remove-Item -Recurse -Force $OutputDir }
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

$payloadDir = Join-Path $OutputDir "payload"
New-Item -ItemType Directory -Force -Path $payloadDir | Out-Null
$coreDir = Join-Path $payloadDir "CorePayload"
if (-not [string]::IsNullOrWhiteSpace($CorePayloadDir)) {
    if (-not (Test-Path $CorePayloadDir)) { throw "Core payload directory is missing: $CorePayloadDir" }
    New-Item -ItemType Directory -Force -Path $coreDir | Out-Null
    Copy-Item (Join-Path $CorePayloadDir "*") $coreDir -Recurse -Force
}

$target = "x86_64-pc-windows-msvc"
$profile = if ($Configuration -eq "Release") { "release" } else { "debug" }
$cargoArgs = @("build", "--manifest-path", $manifest, "--target", $target, "--locked")
if ($Configuration -eq "Release") { $cargoArgs += "--release" }
Push-Location $projectRoot
try {
    & cargo @cargoArgs
    if ($LASTEXITCODE -ne 0) { throw "Slint Windows client build failed" }
}
finally {
    Pop-Location
}

$builtBinary = Join-Path $projectRoot "target/$target/$profile/chuzi-native-windows.exe"
if (-not (Test-Path $builtBinary)) { throw "Slint Windows executable is missing: $builtBinary" }
Copy-Item $builtBinary (Join-Path $payloadDir "Chuzi.Native.Windows.exe") -Force

if ($Mode -eq "UnpackagedZip") {
	$artifact = Join-Path $OutputDir "chuzi-native-windows-$version.zip"
    Compress-Archive -Path (Join-Path $payloadDir "*") -DestinationPath $artifact
    $hash = (Get-FileHash -Algorithm SHA256 $artifact).Hash.ToLowerInvariant()
    "$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
    Write-Output "built Slint Windows client at $artifact"
    return
}

$installerScript = Join-Path $repoRoot "packaging/windows/Chuzi.iss"
if (-not (Test-Path $installerScript)) { throw "Windows installer script is missing" }
$iscc = (Get-Command ISCC.exe -ErrorAction SilentlyContinue).Source
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

& $iscc "/DAppVersion=$version" "/DPayloadDir=$payloadDir" "/DOutputDir=$OutputDir" $installerScript
if ($LASTEXITCODE -ne 0) { throw "Windows Slint installer build failed" }

$artifact = Join-Path $OutputDir "ChuziSetup.exe"
if (-not (Test-Path $artifact)) { throw "No Windows Slint installer EXE was generated" }
$hash = (Get-FileHash -Algorithm SHA256 $artifact).Hash.ToLowerInvariant()
"$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
Write-Output "built Slint Windows installer at $artifact"
