using System.Diagnostics;
using System.ComponentModel;
using System.Net;
using System.Net.Http;
using System.Text.Json;

namespace Chuzi.Native.Windows;

internal sealed class LauncherException : Exception
{
    public LauncherException(string message) : base(message) { }
}

internal sealed class LauncherClient
{
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);
    private static readonly HttpClient Http = new() { Timeout = TimeSpan.FromSeconds(15) };
    private const string TestCatalogEnvironment = "CHUZI_CORE_TEST_CATALOG_URL";
    private const string StableCatalogEnvironment = "CHUZI_CORE_STABLE_CATALOG_URL";
    private const string DefaultTestCatalogUrl = "https://raw.githubusercontent.com/Semcosm/chuzi/release-catalog/test/windows-amd64.json";
    private const string DefaultStableCatalogUrl = "https://raw.githubusercontent.com/Semcosm/chuzi/release-catalog/stable/windows-amd64.json";

    public LauncherClient()
    {
        PackageRoot = Path.GetFullPath(AppContext.BaseDirectory);
        CorePayloadRoot = Path.Combine(PackageRoot, "CorePayload");
        DataRoot = ResolveDataRoot();
        // Keep the launcher, manifest, and local resources from one payload.
        // Mixing a stale launcher in the data directory with the package
        // manifest makes component verification fail during first install or
        // replacement.
        PayloadRoot = FindPayloadRoot(CorePayloadRoot, PackageRoot, DataRoot);
        LauncherPath = PayloadRoot is null ? null : Path.Combine(PayloadRoot, "chuzi-launcher.exe");
    }

    public string PackageRoot { get; }
    public string CorePayloadRoot { get; }
    public string DataRoot { get; }
    public string? PayloadRoot { get; }
    public string? LauncherPath { get; }
    public string? ManifestPath => FindFile("release-manifest.json", DataRoot, CorePayloadRoot, PackageRoot);
    private string? BundledManifestPath => PayloadRoot is null ? null : Path.Combine(PayloadRoot, "release-manifest.json");
    public bool IsAvailable => LauncherPath is not null && BundledManifestPath is not null;

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
        => InstallCoreAsync(null, cancellationToken);

    public async Task<ComponentState> InstallCoreAsync(CoreRelease? release, CancellationToken cancellationToken)
    {
        string? installedManifest = null;
        var arguments = new List<string>();
        if (release is not null)
        {
            if (string.IsNullOrWhiteSpace(release.IndexUrl))
            {
                // A catalog outage can fall back to the manifest bundled in the
                // installer. That entry is intentionally local-only and must
                // use the payload already shipped with the UI instead of being
                // treated as a broken network release.
                var bundled = await ReadBundledManifestAsync(cancellationToken);
                if (bundled is null || !BundledManifestMatches(bundled, release))
                {
                    throw new LauncherException("The selected Core release has no download index.");
                }
                release = null;
            }
        }
        if (release is not null)
        {
            if (string.IsNullOrWhiteSpace(release.IndexUrl))
            {
                throw new LauncherException("The selected Core release has no download index.");
            }
            installedManifest = await FetchManifestJsonAsync(release.IndexUrl, cancellationToken);
            arguments.Add("-release-index");
            arguments.Add(release.IndexUrl);
            if (IsLoopbackHttp(release.IndexUrl)) arguments.Add("-allow-http-loopback");
        }
        arguments.Add("-item");
        arguments.Add("service");
        // Installation must use the candidate manifest bundled with the UI (or
        // the selected release index), never the installed manifest left in the
        // data directory. The latter describes the old payload during a
        // replacement and would make source hashes fail closed.
        var candidateManifestPath = BundledManifestPath ?? ManifestPath;
        if (candidateManifestPath is null)
        {
            throw new LauncherException("Core installation payload is missing its release manifest.");
        }
        var state = await RunJsonAsync<ComponentState>("component-install", cancellationToken, candidateManifestPath, arguments.ToArray());
        await PersistInstalledManifestAsync(installedManifest ?? await ReadBundledManifestAsync(cancellationToken), cancellationToken);
        return state;
    }

    public async Task RemoveCoreAsync(CancellationToken cancellationToken)
    {
        // A Core installed by an older UI may not have a launcher manifest or
        // state file. Explicit uninstall must still remove only the known Core
        // component files; ordinary install/repair remains fail closed when its
        // payload is incomplete.
        if (ManifestPath is null)
        {
            RemoveLegacyCoreFiles();
            return;
        }
        await RemoveComponentWithRetryAsync("service", allowRequiredRemoval: true, cancellationToken);
        // Windows can keep the just-exited executable mapped for a short
        // interval. The launcher removes by rename, so retry the operation
        // inside the client rather than exposing a transient file-lock error
        // as a failed uninstall.
        try
        {
            await RemoveComponentWithRetryAsync("browser-worker", allowRequiredRemoval: false, cancellationToken);
        }
        catch (LauncherException exception) when (IsMissingItemError(exception.Message)) { }
        try { File.Delete(Path.Combine(DataRoot, "release-manifest.json")); } catch (FileNotFoundException) { }
    }

    private async Task RemoveComponentWithRetryAsync(string id, bool allowRequiredRemoval, CancellationToken cancellationToken)
    {
        LauncherException? last = null;
        for (var attempt = 0; attempt < 5; attempt++)
        {
            try
            {
                var arguments = new List<string> { "-item", id };
                if (allowRequiredRemoval) arguments.Add("-allow-required-removal");
                await RunAsync("component-remove", cancellationToken, arguments.ToArray());
                return;
            }
            catch (LauncherException exception) when (IsMissingItemError(exception.Message))
            {
                return;
            }
            catch (LauncherException exception)
            {
                last = exception;
                if (attempt + 1 >= 5) throw;
                await Task.Delay(TimeSpan.FromMilliseconds(150 * (attempt + 1)), cancellationToken);
            }
        }
        throw last ?? new LauncherException($"Core component '{id}' could not be removed.");
    }

    private void RemoveLegacyCoreFiles()
    {
        try
        {
            File.Delete(Path.Combine(DataRoot, "chuzi.exe"));
            File.Delete(Path.Combine(DataRoot, "release-manifest.json"));
            File.Delete(Path.Combine(DataRoot, "build-manifest.json"));
            var worker = Path.Combine(DataRoot, "browser-worker");
            if (Directory.Exists(worker)) Directory.Delete(worker, recursive: true);
        }
        catch (Exception exception) when (exception is IOException or UnauthorizedAccessException)
        {
            throw new LauncherException($"Legacy Core files could not be removed: {exception.Message}");
        }
    }

    public async Task<CoreReleaseCatalog> LoadCoreCatalogAsync(string channel, CancellationToken cancellationToken)
    {
        var normalized = string.Equals(channel, "stable", StringComparison.OrdinalIgnoreCase) ? "stable" : "test";
        var url = CatalogUrl(normalized);
        try
        {
            var catalog = await FetchJsonAsync<CoreReleaseCatalog>(url, cancellationToken);
            ValidateCatalog(catalog, normalized);
            return catalog;
        }
        catch (Exception exception) when (exception is HttpRequestException or TaskCanceledException or JsonException or LauncherException)
        {
            var fallback = await LocalReleaseFallbackAsync(normalized, cancellationToken);
            if (fallback.Releases.Count > 0) return fallback;
            throw new LauncherException($"Core {normalized} release catalog could not be loaded: {exception.Message}");
        }
    }

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
        var output = await RunAsync(command, cancellationToken, null, extraArguments);
        try
        {
            return JsonSerializer.Deserialize<T>(output, JsonOptions)
                ?? throw new LauncherException("Launcher returned an empty response.");
        }
        catch (JsonException exception)
        {
            throw new LauncherException($"Launcher returned an invalid response: {exception.Message}");
        }
    }

    private async Task<T> RunJsonAsync<T>(string command, CancellationToken cancellationToken, string manifestPath, string[] extraArguments)
    {
        var output = await RunAsync(command, cancellationToken, manifestPath, extraArguments);
        try
        {
            return JsonSerializer.Deserialize<T>(output, JsonOptions)
                ?? throw new LauncherException("Launcher returned an empty response.");
        }
        catch (JsonException exception)
        {
            throw new LauncherException($"Launcher returned an invalid response: {exception.Message}");
        }
    }

    private async Task<string> RunAsync(string command, CancellationToken cancellationToken, params string[] extraArguments)
        => await RunAsync(command, cancellationToken, null, extraArguments);

    private async Task<string> RunAsync(string command, CancellationToken cancellationToken, string? manifestPath, string[] extraArguments)
    {
        if (!IsAvailable)
        {
            throw new LauncherException("Core installation payload is not available in this UI package.");
        }

        manifestPath ??= ManifestPath;
        if (manifestPath is null)
        {
            throw new LauncherException("Core installation payload is missing its release manifest.");
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
        startInfo.ArgumentList.Add(PayloadRoot ?? CorePayloadRoot);
        startInfo.ArgumentList.Add("-manifest");
        startInfo.ArgumentList.Add(manifestPath);
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
                throw new LauncherException(ClassifyError(command, process.ExitCode, stderr));
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
            throw new LauncherException($"Launcher could not be started: {exception.Message}");
        }
    }

    private static string ClassifyError(string command, int exitCode, string error)
    {
        var value = error.Trim();
        if (value.Contains("not trusted", StringComparison.OrdinalIgnoreCase)) return "The plugin signer is not trusted.";
        if (value.Contains("not installed", StringComparison.OrdinalIgnoreCase)) return "The requested component is not installed.";
        if (value.Contains("already running", StringComparison.OrdinalIgnoreCase)) return "The Core service is already running.";
        if (value.Contains("not found", StringComparison.OrdinalIgnoreCase)) return "The requested item was not found.";
        if (value.Contains("invalid", StringComparison.OrdinalIgnoreCase)) return "The installed Core metadata is invalid.";
        if (string.IsNullOrWhiteSpace(value)) return $"Launcher command '{command}' failed with exit code {exitCode}.";
        if (value.Length > 1200) value = value[^1200..];
        return $"Launcher command '{command}' failed with exit code {exitCode}: {value}";
    }

    private static bool IsMissingItemError(string message)
        => message.Contains("not installed", StringComparison.OrdinalIgnoreCase)
            || message.Contains("not found", StringComparison.OrdinalIgnoreCase)
            || message.Contains("item not found", StringComparison.OrdinalIgnoreCase);

    private string CatalogUrl(string channel)
    {
        var configured = Environment.GetEnvironmentVariable(channel == "stable" ? StableCatalogEnvironment : TestCatalogEnvironment);
        if (!string.IsNullOrWhiteSpace(configured)) return configured.Trim();
        return channel == "stable" ? DefaultStableCatalogUrl : DefaultTestCatalogUrl;
    }

    private async Task<CoreReleaseCatalog> LocalReleaseFallbackAsync(string channel, CancellationToken cancellationToken)
    {
        var path = ManifestPath;
        if (path is null || !File.Exists(path)) return new CoreReleaseCatalog { Channel = channel };
        var data = await File.ReadAllTextAsync(path, cancellationToken);
        using var document = JsonDocument.Parse(data);
        var root = document.RootElement;
        var manifestChannel = root.TryGetProperty("channel", out var channelElement) ? channelElement.GetString() : null;
        var version = root.TryGetProperty("version", out var versionElement) ? versionElement.GetString() : null;
        var commit = root.TryGetProperty("commit", out var commitElement) ? commitElement.GetString() : null;
        if (string.IsNullOrWhiteSpace(version) || !string.Equals(channel, manifestChannel, StringComparison.OrdinalIgnoreCase))
        {
            return new CoreReleaseCatalog { Channel = channel };
        }
        return new CoreReleaseCatalog
        {
            Channel = channel,
            Releases = [new CoreRelease { Channel = channel, Version = version!, Commit = commit ?? "", Target = "windows-amd64", IndexUrl = "" }],
        };
    }

    private static void ValidateCatalog(CoreReleaseCatalog catalog, string channel)
    {
        if (catalog.Format != "chuzi-release-catalog/v1" || !string.Equals(catalog.Channel, channel, StringComparison.OrdinalIgnoreCase))
        {
            throw new LauncherException("The Core release catalog format or channel is invalid.");
        }
        foreach (var release in catalog.Releases)
        {
            if (!string.Equals(release.Channel, channel, StringComparison.OrdinalIgnoreCase) ||
                !string.Equals(release.Target, "windows-amd64", StringComparison.OrdinalIgnoreCase) ||
                string.IsNullOrWhiteSpace(release.Version) || string.IsNullOrWhiteSpace(release.Commit))
            {
                throw new LauncherException("The Core release catalog contains an invalid release entry.");
            }
        }
    }

    private async Task<string> FetchManifestJsonAsync(string indexUrl, CancellationToken cancellationToken)
    {
        using var response = await Http.GetAsync(CreateUri(indexUrl), HttpCompletionOption.ResponseHeadersRead, cancellationToken);
        response.EnsureSuccessStatusCode();
        var json = await response.Content.ReadAsStringAsync(cancellationToken);
        using var document = JsonDocument.Parse(json);
        if (!document.RootElement.TryGetProperty("manifest", out var manifest))
        {
            throw new LauncherException("The selected Core release index has no manifest.");
        }
        return manifest.GetRawText();
    }

    private async Task<T> FetchJsonAsync<T>(string url, CancellationToken cancellationToken)
    {
        using var response = await Http.GetAsync(CreateUri(url), HttpCompletionOption.ResponseHeadersRead, cancellationToken);
        response.EnsureSuccessStatusCode();
        var json = await response.Content.ReadAsStringAsync(cancellationToken);
        return JsonSerializer.Deserialize<T>(json, JsonOptions)
            ?? throw new LauncherException("The release catalog response was empty.");
    }

    private static Uri CreateUri(string value)
    {
        if (!Uri.TryCreate(value, UriKind.Absolute, out var uri) ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && IsLoopbackHttp(uri))))
        {
            throw new LauncherException("Release catalog URLs must use HTTPS (or loopback HTTP for local testing).");
        }
        return uri;
    }

    private static bool IsLoopbackHttp(string value)
        => Uri.TryCreate(value, UriKind.Absolute, out var uri) && IsLoopbackHttp(uri);

    private static bool IsLoopbackHttp(Uri uri)
        => uri.Scheme == Uri.UriSchemeHttp && (uri.HostNameType == UriHostNameType.IPv4 || uri.HostNameType == UriHostNameType.Dns) && IPAddress.TryParse(uri.Host, out var address) && IPAddress.IsLoopback(address);

    private async Task<string?> ReadBundledManifestAsync(CancellationToken cancellationToken)
    {
        var path = BundledManifestPath;
        return path is null ? null : await File.ReadAllTextAsync(path, cancellationToken);
    }

    private static bool BundledManifestMatches(string json, CoreRelease release)
    {
        try
        {
            using var document = JsonDocument.Parse(json);
            var root = document.RootElement;
            var channel = root.TryGetProperty("channel", out var channelElement) ? channelElement.GetString() : null;
            var version = root.TryGetProperty("version", out var versionElement) ? versionElement.GetString() : null;
            var commit = root.TryGetProperty("commit", out var commitElement) ? commitElement.GetString() : null;
            var target = root.TryGetProperty("target", out var targetElement) ? targetElement.GetString() : null;
            return string.Equals(channel, release.Channel, StringComparison.OrdinalIgnoreCase)
                && string.Equals(version, release.Version, StringComparison.OrdinalIgnoreCase)
                && string.Equals(commit, release.Commit, StringComparison.OrdinalIgnoreCase)
                && string.Equals(target, release.Target, StringComparison.OrdinalIgnoreCase);
        }
        catch (JsonException)
        {
            return false;
        }
    }

    private async Task PersistInstalledManifestAsync(string? manifest, CancellationToken cancellationToken)
    {
        if (string.IsNullOrWhiteSpace(manifest)) return;
        Directory.CreateDirectory(DataRoot);
        var target = Path.Combine(DataRoot, "release-manifest.json");
        var temporary = target + ".tmp-" + Guid.NewGuid().ToString("N");
        await File.WriteAllTextAsync(temporary, manifest, cancellationToken);
        File.Move(temporary, target, overwrite: true);
    }

    private static string ResolveDataRoot()
    {
        var configured = Environment.GetEnvironmentVariable("CHUZI_DATA_DIR");
        if (!string.IsNullOrWhiteSpace(configured))
        {
            return Path.GetFullPath(configured);
        }

        var commonRoot = Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData);
        var defaultRoot = Path.Combine(commonRoot, "chuzi");
        var legacyDataRoot = Path.Combine(defaultRoot, "data");
        // Older Core launch instructions used %ProgramData%\chuzi\data.
        // Reuse that directory when it already contains a Core installation so
        // the UI does not start a second service against a different pipe or
        // database. Fresh installs continue to use the documented root.
        if (Directory.Exists(legacyDataRoot) &&
            (File.Exists(Path.Combine(legacyDataRoot, "chuzi.exe") ) ||
             File.Exists(Path.Combine(legacyDataRoot, "release-manifest.json")) ||
             File.Exists(Path.Combine(legacyDataRoot, "core-config.json"))))
        {
            return Path.GetFullPath(legacyDataRoot);
        }
        return Path.GetFullPath(defaultRoot);
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

    private static string? FindPayloadRoot(params string[] roots)
    {
        foreach (var root in roots.Distinct(StringComparer.OrdinalIgnoreCase))
        {
            if (File.Exists(Path.Combine(root, "chuzi-launcher.exe")) &&
                File.Exists(Path.Combine(root, "release-manifest.json")))
            {
                return root;
            }
        }
        return null;
    }
}
