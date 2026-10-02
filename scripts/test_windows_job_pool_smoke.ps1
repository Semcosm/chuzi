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
$runID = [Guid]::NewGuid().ToString('N')
$runRoot = Join-Path $tempRoot ('chuzi-job-pool-smoke-' + $runID)
$userPrefix = 'Cz' + $runID.Substring(0, 9)
$marker = 'CHUZI-MANAGED:smoke-001:1'
$ownershipMarker = 'CHUZI-SMOKE-OWNERSHIP:' + $runID
$cleanupErrors = [System.Collections.Generic.List[string]]::new()
$smokePassed = $false
$script:rootPreserved = $false
$failureStage = 'setup'
$testLog = Join-Path $runRoot 'test-output.log'
$preservedLog = Join-Path $tempRoot 'chuzi-job-pool-smoke-test-output.log'

function Invoke-SmokeRetry([scriptblock] $Action, [int] $Attempts = 6) {
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        try {
            & $Action
            return $true
        } catch {
            if ($attempt -eq $Attempts) {
                return $false
            }
            Start-Sleep -Milliseconds ([Math]::Min(2000, 250 * [Math]::Pow(2, $attempt - 1)))
        }
    }
    return $false
}

function Get-MarkedUsers {
    @(Get-LocalUser -ErrorAction Stop | Where-Object {
        $_.Name.StartsWith($userPrefix, [System.StringComparison]::OrdinalIgnoreCase) -and
        $_.Description -eq $marker
    })
}

function Stop-MarkedUserSessions([string] $name) {
    $lines = @(quser.exe $name 2>$null)
    foreach ($line in $lines) {
        if ($line -match '\s(?<sessionId>[0-9]+)\s+(Active|Disc)') {
            $sessionID = $Matches['sessionId']
            [void](Invoke-SmokeRetry {
                & logoff.exe $sessionID 2>$null 1>$null
                if ($LASTEXITCODE -ne 0) { throw 'session logoff failed' }
            })
        }
    }
}

function Test-RunOwnership {
    if (-not (Test-Path -LiteralPath $runRoot -PathType Container)) {
        return $false
    }
    $ownershipPath = Join-Path $runRoot '.chuzi-smoke-ownership'
    if (-not (Test-Path -LiteralPath $ownershipPath -PathType Leaf)) {
        return $false
    }
    try {
        return ((Get-Content -LiteralPath $ownershipPath -Raw -ErrorAction Stop).Trim() -eq $ownershipMarker)
    } catch {
        return $false
    }
}

function Stop-SmokeResources {
    $usersClean = $true
    $rootClean = $true
    try {
        $markedUsers = Get-MarkedUsers
        foreach ($user in $markedUsers) {
            Stop-MarkedUserSessions $user.Name
            if (-not (Invoke-SmokeRetry {
                Remove-LocalUser -Name $user.Name -Confirm:$false -ErrorAction Stop
            })) {
                $usersClean = $false
            }
        }
        if ((Get-MarkedUsers).Count -ne 0) {
            $usersClean = $false
        }
    } catch {
        $usersClean = $false
    }
    if (-not $usersClean) {
        $cleanupErrors.Add('user_cleanup_failed')
    }

    if ($env:CHUZI_PRESERVE_WINDOWS_JOB_POOL_SMOKE_ROOT -eq '1') {
        $script:rootPreserved = $true
    } elseif (Test-RunOwnership) {
        if (-not (Invoke-SmokeRetry {
            Remove-Item -LiteralPath $runRoot -Recurse -Force -ErrorAction Stop
        })) {
            $rootClean = $false
        }
    }
    if (-not $rootClean -and $env:CHUZI_PRESERVE_WINDOWS_JOB_POOL_SMOKE_ROOT -ne '1') {
        $cleanupErrors.Add('root_cleanup_failed')
    }
    return @{ Users = $usersClean; Root = $rootClean }
}

function Invoke-Icacls([string] $path) {
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $system = 'NT AUTHORITY\SYSTEM:(OI)(CI)(F)'
    $administrators = 'BUILTIN\Administrators:(OI)(CI)(F)'
    $users = 'BUILTIN\Users:(OI)(CI)(RX)'
    & icacls.exe $path /inheritance:r /grant:r ($identity + ':(OI)(CI)(F)') $system $administrators $users /T /C 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'runtime ACL preparation failed'
    }
}

try {
    if ($env:OS -ne 'Windows_NT') {
        $failureStage = 'windows_required'
        throw 'Windows native smoke requires Windows'
    }
    $failureStage = 'administrator_required'
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [System.Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Windows native smoke requires an administrator runner'
    }
    $failureStage = 'root_setup'
    New-Item -ItemType Directory -Path $runRoot -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $runRoot '.chuzi-smoke-ownership') -Value $ownershipMarker -NoNewline -Encoding ASCII
    $failureStage = 'runtime_setup'
    $runtimeRoot = Join-Path $runRoot 'runtime'
    $workerRoot = Join-Path $runtimeRoot 'browser-worker/src'
    $dataRoot = Join-Path $runRoot 'data'
    New-Item -ItemType Directory -Path $workerRoot -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $dataRoot 'profiles') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $dataRoot 'backups') -Force | Out-Null

    $failureStage = 'toolchain_required'
    $go = (Get-Command go.exe -CommandType Application -ErrorAction Stop).Source
    $node = (Get-Command node.exe -CommandType Application -ErrorAction Stop).Source
    $agentPath = Join-Path $runtimeRoot 'chuzi-user-agent.exe'
    $nodePath = Join-Path $runtimeRoot 'node.exe'
    $workerPath = Join-Path $workerRoot 'worker.mjs'

    $failureStage = 'build_agent'
    & $go build -trimpath -o $agentPath (Join-Path $repoRoot 'cmd/user-agent') 1>$null 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw 'user-agent build failed'
    }
    Copy-Item -LiteralPath $node -Destination $nodePath -Force
    Copy-Item -LiteralPath (Join-Path $repoRoot 'browser-worker/src/worker.mjs') -Destination $workerPath -Force
    $failureStage = 'runtime_acl'
    Invoke-Icacls $runtimeRoot

    $env:CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE = '1'
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT = $runRoot
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT = $agentPath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE = $nodePath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER = $workerPath
    $env:CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX = $userPrefix

    $failureStage = 'native_smoke'
    New-Item -ItemType File -Path $testLog -Force | Out-Null
    & $go test -count=1 -run '^TestWindowsJobPoolNativeSmoke$' ./internal/slotwindows 1>$testLog 2>&1
    if ($LASTEXITCODE -ne 0) {
        if (Select-String -LiteralPath $testLog -Pattern 'session_unavailable' -Quiet) {
            $failureStage = 'session_unavailable'
        }
        throw 'Windows job-pool native smoke failed'
    }
    $smokePassed = $true
} catch {
    if (Test-Path -LiteralPath $testLog -PathType Leaf) {
        try {
            Copy-Item -LiteralPath $testLog -Destination $preservedLog -Force
        } catch {
            $cleanupErrors.Add('test_log_preservation_failed')
        }
    } else {
        try {
            Set-Content -LiteralPath $preservedLog -Value ('failure_stage=' + $failureStage) -Encoding ASCII
        } catch {
            $cleanupErrors.Add('test_log_preservation_failed')
        }
    }
} finally {
    Remove-Item Env:CHUZI_RUN_WINDOWS_JOB_POOL_SMOKE -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_ROOT -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_AGENT -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_NODE -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_WORKER -ErrorAction SilentlyContinue
    Remove-Item Env:CHUZI_WINDOWS_JOB_POOL_SMOKE_USER_PREFIX -ErrorAction SilentlyContinue
    $cleanupResult = Stop-SmokeResources
    if ($smokePassed -and $cleanupErrors.Count -eq 0 -and $cleanupResult.Users -and $cleanupResult.Root) {
        Write-Host 'RemainingSmokeUsers = 0'
        if ($script:rootPreserved) {
            Write-Host 'RemainingSmokeRoots = preserved'
        } else {
            Write-Host 'RemainingSmokeRoots = 0'
        }
        Write-Host 'Windows job-pool native smoke passed'
        exit 0
    }
    if ($smokePassed) {
        Write-Error 'Windows job-pool native smoke failed: cleanup_failed'
    } else {
        Write-Error ('Windows job-pool native smoke failed: ' + $failureStage)
    }
    foreach ($cleanupError in $cleanupErrors) {
        Write-Error $cleanupError
    }
    exit 1
}
