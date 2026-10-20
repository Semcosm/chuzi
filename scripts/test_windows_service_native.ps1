$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Windows PowerShell 5.1 must preserve the SCM image command and the Task
# Scheduler action as single arguments. Exercise the real process boundary
# without creating services, tasks, or touching an installed Chuzi instance.
$tokens = $null
$parseErrors = $null
$source = Join-Path $PSScriptRoot 'windows-service-management.ps1'
$ast = [Management.Automation.Language.Parser]::ParseFile($source, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Service management script has parser errors' }
foreach ($definition in $ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst]}, $true)) {
    . ([scriptblock]::Create($definition.Extent.Text))
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('chuzi-service-native-' + [guid]::NewGuid().ToString('N'))
$probeDir = Join-Path $testRoot 'path with spaces'
New-Item -ItemType Directory -Path $probeDir | Out-Null
try {
    $probe = Join-Path $probeDir 'ArgvProbe.exe'
    Add-Type -OutputAssembly $probe -OutputType ConsoleApplication -TypeDefinition @'
using System;
using System.Text;
class ChuziInstallerArgvFixture {
    static void Main(string[] args) {
        if (args.Length == 1 && args[0] == "--fail") {
            Console.Error.Write("fixture stderr");
            Environment.Exit(7);
        }
        foreach (string arg in args)
            Console.WriteLine("ARG:" + Convert.ToBase64String(Encoding.UTF8.GetBytes(arg)));
    }
}
'@
    function Assert-NativeArguments([string[]]$Expected) {
        $result = Invoke-NativeCapture $probe $Expected
        if ($result.ExitCode -ne 0) { throw 'Argument fixture failed to run' }
        $output = if ($null -eq $result.StdOut) { '' } else { $result.StdOut.TrimEnd([char[]]([Environment]::NewLine)) }
        $lines = if ($output.Length -eq 0) { @() } else { @($output -split [Environment]::NewLine) }
        if ($lines.Count -ne $Expected.Count) { throw 'Native argument count changed' }
        for ($index = 0; $index -lt $Expected.Count; $index++) {
            if (-not $lines[$index].StartsWith('ARG:')) { throw 'Invalid argument fixture output' }
            $actual = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($lines[$index].Substring(4)))
            if ($actual -cne $Expected[$index]) { throw ("Native argument changed at index {0}" -f $index) }
        }
    }

    $quote = [char]34
    $image = $quote + 'C:\Program Files\Chuzi\CorePayload\chuzi.exe' + $quote + ' -service -config ' + $quote + 'C:\ProgramData\chuzi\data\core-config.json' + $quote
    Assert-NativeArguments @('create', 'ChuziCore', 'binPath=', $image, 'start=', 'auto', 'obj=', 'LocalSystem', 'DisplayName=', 'Chuzi Core Service')
    Assert-NativeArguments @('config', 'ChuziCore', 'binPath=', $image, 'start=', 'auto', 'obj=', 'LocalSystem')
    Assert-NativeArguments @('/Create', '/TN', 'ChuziSessionBroker', '/TR', ($quote + 'C:\Program Files\Chuzi\CorePayload\chuzi-session-broker.exe' + $quote), '/SC', 'ONLOGON', '/IT', '/RL', 'LIMITED', '/F')
    Assert-NativeArguments @('', 'plain', 'path with spaces\', 'embedded' + $quote + 'quote', '\' + $quote, '\\' + $quote + 'quotes', 'a' + $quote + $quote + 'b', ('tab' + [char]9 + 'value'), 'tail path\')

    $failure = Invoke-NativeCapture $probe @('--fail')
    if ($failure.ExitCode -ne 7 -or $failure.StdErr -ne 'fixture stderr') { throw 'Native stderr or exit code was lost' }
    $missingService = 'ChuziMissing-' + [guid]::NewGuid().ToString('N')
    $missing = Invoke-NativeCapture (Join-Path $env:SystemRoot 'System32/sc.exe') @('query', $missingService)
    if ($missing.ExitCode -ne 1060 -or [string]::IsNullOrWhiteSpace($missing.StdOut)) { throw 'SCM absence was not captured' }
    $rejected = $false
    try { Invoke-Sc @('query', $missingService) } catch {
        if ($_.Exception.Message -ne 'service_control_failed: action=query; exit_code=1060') { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw 'SCM failure did not preserve its action and exit code' }
    Write-Host 'Windows service/task native argument and failure-capture tests passed'
} finally {
    $resolved = [IO.Path]::GetFullPath($testRoot)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $resolved.StartsWith($temp, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $resolved) -notlike 'chuzi-service-native-*') { throw 'Invalid test cleanup root' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
