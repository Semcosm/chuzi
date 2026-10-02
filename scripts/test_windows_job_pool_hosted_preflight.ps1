
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if ($env:OS -ne 'Windows_NT') {
    throw 'Windows hosted preflight requires Windows'
}

$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [System.Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Windows hosted preflight requires an administrator runner'
}

$go = (Get-Command go.exe -CommandType Application -ErrorAction Stop).Source
$node = (Get-Command node.exe -CommandType Application -ErrorAction Stop).Source
$goVersion = & $go version
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($goVersion)) {
    throw 'Go toolchain is unavailable'
}
$nodeVersion = & $node --version
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($nodeVersion)) {
    throw 'Node.js runtime is unavailable'
}

# Hosted Windows validates the native compilation and control-plane contracts.
# The managed-user WTS session smoke remains in the dedicated runner job.
& $go test -count=1 ./...
if ($LASTEXITCODE -ne 0) {
    throw 'Windows Go test suite failed'
}
& $go vet ./...
if ($LASTEXITCODE -ne 0) {
    throw 'Windows Go vet failed'
}

$outputRoot = Join-Path $env:RUNNER_TEMP 'chuzi-hosted-preflight'
if (Test-Path -LiteralPath $outputRoot) {
    Remove-Item -LiteralPath $outputRoot -Recurse -Force
}
New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null
$agent = Join-Path $outputRoot 'chuzi-user-agent.exe'
& $go build -trimpath -o $agent (Join-Path $env:GITHUB_WORKSPACE 'cmd/user-agent')
if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $agent)) {
    throw 'Windows user-agent build failed'
}

# Confirm that the hosted image exposes a queryable terminal-session API without
# printing session identities or tokens into workflow logs.
& query.exe session 1>$null 2>$null
if ($LASTEXITCODE -ne 0) {
    throw 'Windows terminal-session query is unavailable'
}

Write-Host 'Windows hosted job-pool preflight passed'
