[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$tempRoot = if (-not [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) {
    $env:RUNNER_TEMP
} else {
    [System.IO.Path]::GetTempPath()
}
$runRoot = Join-Path $tempRoot ('chuzi-job-pool-smoke-' + [Guid]::NewGuid().ToString('N'))
$userPrefix = 'Cz' + ([Guid]::NewGuid().ToString('N').Substring(0, 9))
$marker = 'CHUZI-MANAGED:smoke-001:1'
$cleanupErrors = [System.Collections.Generic.List[string]]::new()

function Stop-SmokeResources {
    try {
        $markedUsers = @(Get-LocalUser -ErrorAction Stop | Where-Object {
            $_.Name.StartsWith($userPrefix, [System.StringComparison]::OrdinalIgnoreCase) -and
            $_.Description -eq $marker
        })
        foreach ($user in $markedUsers) {
            Remove-LocalUser -Name $user.Name -Confirm:$false -ErrorAction Stop
        }
    } catch {
        $cleanupErrors.Add('managed user cleanup failed')
    }
    try {
        if (Test-Path -LiteralPath $runRoot) {
            Remove-Item -LiteralPath $runRoot -Recurse -Force -ErrorAction Stop
        }
    } catch {
        $cleanupErrors.Add('temporary resource cleanup failed')
    }
}

function Invoke-Icacls([string] $path) {
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $system = 'NT AUTHORITY\SYSTEM:(OI)(CI)(F)'
    $administrators = 'BUILTIN\Administrators:(OI)(CI)(F)'
    $users = 'BUILTIN\Users:(OI)(CI)(RX)'
    & icacls.exe $path /inheritance:r /grant:r "${identity}:(OI)(CI)(F)" $system $administrators $users /T /C 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'runtime ACL preparation failed'
    }
}

try {
    if ($env:OS -ne 'Windows_NT') {
        throw 'Windows native smoke requires Windows'
    }
    New-Item -ItemType Directory -Path $runRoot -Force | Out-Null
    $runtimeRoot = Join-Path $runRoot 'runtime'
    $workerRoot = Join-Path $runtimeRoot 'browser-worker/src'
    $dataRoot = Join-Path $runRoot 'data'
    New-Item -ItemType Directory -Path $workerRoot -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $dataRoot 'profiles') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $dataRoot 'backups') -Force | Out-Null

    $go = (Get-Command go.exe -CommandType Application -ErrorAction Stop).Source
    $node = (Get-Command node.exe -CommandType Application -ErrorAction Stop).Source
    $agentPath = Join-Path $runtimeRoot 'chuzi-user-agent.exe'
    $nodePath = Join-Path $runtimeRoot 'node.exe'
    $workerPath = Join-Path $workerRoot 'worker.mjs'

    & $go build -trimpath -o $agentPath (Join-Path $repoRoot 'cmd/user-agent') 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'user-agent build failed'
    }
    Copy-Item -LiteralPath $node -Destination $nodePath -Force
    Copy-Item -LiteralPath (Join-Path $repoRoot 'browser-worker/src/worker.mjs') -Destination $workerPath -Force
    Invoke-Icacls $runtimeRoot

    $env:CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE = '1'
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT = $runRoot
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT = $agentPath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE = $nodePath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER = $workerPath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX = $userPrefix

    $testLog = Join-Path $runRoot 'test-output.log'
    & $go test -count=1 -run '^TestWindowsJobPoolNativeSmoke$' ./internal/slotwindows 1>$testLog 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw 'Windows job-pool native smoke failed'
    }
    Write-Host 'Windows job-pool native smoke passed'
} catch {
    Write-Error $_.Exception.Message
    exit 1
} finally {
    Remove-Item Env:CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX -ErrorAction SilentlyContinue
    Stop-SmokeResources
    if ($cleanupErrors.Count -gt 0) {
        foreach ($cleanupError in $cleanupErrors) {
            Write-Error $cleanupError
        }
        exit 1
    }
}
