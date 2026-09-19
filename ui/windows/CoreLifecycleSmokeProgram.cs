using System.Text.Json;
using Chuzi.Native.Windows;

if (args.Length != 1 || string.IsNullOrWhiteSpace(args[0]))
{
    Console.Error.WriteLine("usage: CoreLifecycleSmoke <data-directory>");
    return 2;
}

var dataDirectory = Path.GetFullPath(args[0]);
Directory.CreateDirectory(dataDirectory);
Environment.SetEnvironmentVariable("CHUZI_DATA_DIR", dataDirectory);
using var cancellation = new CancellationTokenSource(TimeSpan.FromMinutes(2));
var token = cancellation.Token;
var launcher = new LauncherClient();
using var controller = new CoreServiceController(launcher);

var initial = await controller.GetStatusAsync(token);
Require(initial.Status == CoreStatus.Missing, $"expected Missing before install, got {initial.Status}: {initial.Message}");

var installed = await controller.InstallAndStartAsync(token);
Require(installed.Status == CoreStatus.Running, $"install/start failed: {installed.Message}");
await AssertCoreRoundTripAsync(dataDirectory, token);

var stopped = await controller.StopAsync(token);
Require(stopped.Status == CoreStatus.Stopped, $"stop failed: {stopped.Status}: {stopped.Message}");

var restarted = await controller.StartAsync(token);
Require(restarted.Status == CoreStatus.Running, $"restart failed: {restarted.Message}");
await AssertCoreRoundTripAsync(dataDirectory, token);

var manifestPath = Path.Combine(dataDirectory, "release-manifest.json");
using var manifest = JsonDocument.Parse(await File.ReadAllTextAsync(manifestPath, token));
var root = manifest.RootElement;
var replacement = new CoreRelease
{
    Channel = root.GetProperty("channel").GetString() ?? "test",
    Version = root.GetProperty("version").GetString() ?? "",
    Commit = root.GetProperty("commit").GetString() ?? "",
    Target = root.GetProperty("target").GetString() ?? "windows-amd64",
};
var replaced = await controller.ReplaceAndStartAsync(replacement, token);
Require(replaced.Status == CoreStatus.Running, $"replace/start failed: {replaced.Message}");

var uninstalled = await controller.UninstallAsync(token);
Require(uninstalled.Status == CoreStatus.Missing, $"uninstall failed: {uninstalled.Status}: {uninstalled.Message}");
Require(!File.Exists(Path.Combine(dataDirectory, "chuzi.exe")), "Core executable remains after uninstall");
Console.WriteLine("Windows Core controller install/start/stop/restart/replace/uninstall passed.");
return 0;

static async Task AssertCoreRoundTripAsync(string dataDirectory, CancellationToken token)
{
    using var client = CoreApiClient.FromDataDirectory(dataDirectory);
    await client.ConnectAsync(token);
    try
    {
        await client.GetAccountAsync("core-lifecycle-smoke", token);
        throw new InvalidOperationException("Core API unexpectedly returned an account.");
    }
    catch (CoreApiException exception) when (exception.Code == "not_found")
    {
        // A not_found response proves the hello and request/response paths work.
    }
}

static void Require(bool condition, string message)
{
    if (!condition) throw new InvalidOperationException(message);
}
