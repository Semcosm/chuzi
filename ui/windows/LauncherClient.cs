using System.Diagnostics;
using System.ComponentModel;
using System.Text.Json;

namespace Chuzi.Native.Windows;

internal sealed class LauncherException : Exception
{
    public LauncherException(string message) : base(message) { }
}

internal sealed class LauncherClient
{
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);

    public LauncherClient()
    {
        PackageRoot = Path.GetFullPath(AppContext.BaseDirectory);
        CorePayloadRoot = Path.Combine(PackageRoot, "CorePayload");
        DataRoot = ResolveDataRoot();
        LauncherPath = FindFile("chuzi-launcher.exe", CorePayloadRoot, PackageRoot, DataRoot);
        ManifestPath = FindFile("release-manifest.json", CorePayloadRoot, PackageRoot, DataRoot);
    }

    public string PackageRoot { get; }
    public string CorePayloadRoot { get; }
    public string DataRoot { get; }
    public string? LauncherPath { get; }
    public string? ManifestPath { get; }
    public bool IsAvailable => LauncherPath is not null && ManifestPath is not null;

    public async Task<BehaviorSettings> LoadSettingsAsync(CancellationToken cancellationToken)
        => await RunJsonAsync<BehaviorSettings>("settings", cancellationToken);

    public async Task SaveSettingsAsync(BehaviorSettings settings, CancellationToken cancellationToken)
    {
        var path = Path.Combine(Path.GetTempPath(), "chuzi-settings-" + Guid.NewGuid().ToString("N") + ".json");
        try
        {
            await File.WriteAllTextAsync(path, JsonSerializer.Serialize(settings, JsonOptions), cancellationToken);
            await RunJsonAsync<BehaviorSettings>("settings-save", cancellationToken, "-settings-input", path);
        }
        finally
        {
            try { File.Delete(path); } catch { }
        }
    }

    public Task<ComponentState> InstallCoreAsync(CancellationToken cancellationToken)
        => RunJsonAsync<ComponentState>("component-install", cancellationToken, "-item", "service");

    public Task<ComponentState[]> ListComponentsAsync(CancellationToken cancellationToken)
        => RunJsonAsync<ComponentState[]>("component-list", cancellationToken);

    public Task<PluginState[]> ListPluginsAsync(CancellationToken cancellationToken)
        => RunJsonAsync<PluginState[]>("plugin-list", cancellationToken);

    public Task<PluginState> InstallPluginAsync(string id, CancellationToken cancellationToken)
        => RunJsonAsync<PluginState>("plugin-install", cancellationToken, "-item", id);

    public Task<PluginState> SetPluginTrustedAsync(string id, bool trusted, CancellationToken cancellationToken)
        => RunJsonAsync<PluginState>(trusted ? "plugin-trust" : "plugin-untrust", cancellationToken, "-item", id);

    public Task<PluginState> SetPluginEnabledAsync(string id, bool enabled, CancellationToken cancellationToken)
        => RunJsonAsync<PluginState>(enabled ? "plugin-enable" : "plugin-disable", cancellationToken, "-item", id);

    public async Task RemovePluginAsync(string id, CancellationToken cancellationToken)
        => await RunAsync("plugin-remove", cancellationToken, "-item", id);

    private async Task<T> RunJsonAsync<T>(string command, CancellationToken cancellationToken, params string[] extraArguments)
    {
        var output = await RunAsync(command, cancellationToken, extraArguments);
        try
        {
            return JsonSerializer.Deserialize<T>(output, JsonOptions)
                ?? throw new LauncherException("Launcher returned an empty response.");
        }
        catch (JsonException)
        {
            throw new LauncherException("Launcher returned an invalid response.");
        }
    }

    private async Task<string> RunAsync(string command, CancellationToken cancellationToken, params string[] extraArguments)
    {
        if (!IsAvailable)
        {
            throw new LauncherException("Core installation payload is not available in this UI package.");
        }

        var startInfo = new ProcessStartInfo
        {
            FileName = LauncherPath!,
            WorkingDirectory = DataRoot,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        startInfo.ArgumentList.Add("-root");
        startInfo.ArgumentList.Add(DataRoot);
        startInfo.ArgumentList.Add("-source-root");
        startInfo.ArgumentList.Add(CorePayloadRoot);
        startInfo.ArgumentList.Add("-manifest");
        startInfo.ArgumentList.Add(ManifestPath!);
        startInfo.ArgumentList.Add("-command");
        startInfo.ArgumentList.Add(command);
        var trustedSigners = Environment.GetEnvironmentVariable("CHUZI_TRUSTED_PLUGIN_SIGNERS");
        if (!string.IsNullOrWhiteSpace(trustedSigners))
        {
            startInfo.ArgumentList.Add("-trusted-signers");
            startInfo.ArgumentList.Add(trustedSigners);
        }
        foreach (var argument in extraArguments)
        {
            startInfo.ArgumentList.Add(argument);
        }

        Directory.CreateDirectory(DataRoot);
        using var process = new Process { StartInfo = startInfo, EnableRaisingEvents = true };
        try
        {
            if (!process.Start())
            {
                throw new LauncherException("Launcher could not be started.");
            }
            var stdoutTask = process.StandardOutput.ReadToEndAsync(cancellationToken);
            var stderrTask = process.StandardError.ReadToEndAsync(cancellationToken);
            await process.WaitForExitAsync(cancellationToken);
            var stdout = await stdoutTask;
            var stderr = await stderrTask;
            if (process.ExitCode != 0)
            {
                throw new LauncherException(ClassifyError(stderr));
            }
            return stdout;
        }
        catch (OperationCanceledException)
        {
            try { if (!process.HasExited) process.Kill(entireProcessTree: true); } catch { }
            throw;
        }
        catch (Win32Exception exception)
        {
            throw new LauncherException("Launcher could not be started.");
        }
    }

    private static string ClassifyError(string error)
    {
        var value = error.Trim();
        if (value.Contains("not trusted", StringComparison.OrdinalIgnoreCase)) return "The plugin signer is not trusted.";
        if (value.Contains("not installed", StringComparison.OrdinalIgnoreCase)) return "The requested component is not installed.";
        if (value.Contains("already running", StringComparison.OrdinalIgnoreCase)) return "The Core service is already running.";
        if (value.Contains("not found", StringComparison.OrdinalIgnoreCase)) return "The requested item was not found.";
        if (value.Contains("invalid", StringComparison.OrdinalIgnoreCase)) return "The installed Core metadata is invalid.";
        return "The launcher operation failed.";
    }

    private static string ResolveDataRoot()
    {
        var configured = Environment.GetEnvironmentVariable("CHUZI_DATA_DIR");
        return Path.GetFullPath(string.IsNullOrWhiteSpace(configured)
            ? Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "chuzi")
            : configured);
    }

    private static string? FindFile(string name, params string[] roots)
    {
        foreach (var root in roots.Distinct(StringComparer.OrdinalIgnoreCase))
        {
            var path = Path.Combine(root, name);
            if (File.Exists(path)) return path;
        }
        return null;
    }
}
