using System.Diagnostics;
using System.Text.Json;

namespace Chuzi.Native.Windows;

public enum CoreStatus
{
    Missing,
    Stopped,
    Starting,
    Running,
    Unavailable,
}

public sealed record CoreSnapshot(CoreStatus Status, int? ProcessId, bool OwnedByThisWindow, string Message)
{
    public string DataDirectory { get; init; } = "";
    public string LogPath { get; init; } = "";
    public string InstalledVersion { get; init; } = "";
    public string InstalledChannel { get; init; } = "";
    public string InstalledCommit { get; init; } = "";
}

internal sealed class CoreServiceController : IDisposable
{
    private readonly LauncherClient _launcher;
    private Process? _process;
    private bool _ownsProcess;

    public CoreServiceController(LauncherClient launcher) => _launcher = launcher;

    public string DataDirectory => _launcher.DataRoot;
    public string LogPath => Path.Combine(_launcher.DataRoot, "core.log");
    private string ManagedPidPath => Path.Combine(_launcher.DataRoot, ".core.pid");

    public async Task<CoreSnapshot> GetStatusAsync(CancellationToken cancellationToken)
    {
        if (_process is { HasExited: true })
        {
            CleanupProcess(_process);
        }
        var executable = FindServiceExecutable();
        var metadata = await ReadInstalledMetadataAsync(cancellationToken);
        // The pipe is the authoritative readiness signal. Process inspection
        // can fail for an elevated Core process (or briefly race process
        // startup), so probe it before concluding that Core is missing or
        // stopped. This also prevents StartAsync from launching a duplicate
        // process against the same database and named pipe.
        var pipeSnapshot = await ProbeExistingPipeAsync(metadata, cancellationToken);
        if (pipeSnapshot is not null)
        {
            return pipeSnapshot;
        }
        if (executable is null)
        {
            return Snapshot(CoreStatus.Missing, null, false, "Core is not installed.", metadata);
        }
        // Prefer the process handle created by this window. Windows may deny
        // MainModule inspection for an elevated or service-hosted process even
        // when the caller owns the process, which previously made Stop/Start
        // report a false Stopped state.
        var runningProcess = _process is { HasExited: false }
            ? _process
            : FindRunningService(executable) ?? FindManagedProcess(executable);
        if (runningProcess is not null)
        {
            var owned = _process is not null && !_process.HasExited && _process.Id == runningProcess.Id && _ownsProcess;
            // A matching process is not enough to unlock the rest of the UI;
            // the named-pipe handshake must succeed as well. Report an
            // unreachable process as Unavailable so plugin/account actions do
            // not race a broken Core instance.
            try
            {
                return await ProbeAsync(CoreStatus.Unavailable, runningProcess.Id, owned, cancellationToken, metadata);
            }
            finally
            {
                // Processes discovered through enumeration are temporary
                // handles. Keep only the handle owned by this window.
                if (!ReferenceEquals(runningProcess, _process)) runningProcess.Dispose();
            }
        }
        // A named pipe is owned by a live Core process. If no matching process
        // exists and the bounded pipe probe above failed, report the stopped
        // state without waiting for another long connection timeout.
        return Snapshot(CoreStatus.Stopped, null, false, "Core is stopped.", metadata);
    }

    public async Task<CoreSnapshot> InstallAndStartAsync(CancellationToken cancellationToken)
    {
        await _launcher.InstallCoreAsync(cancellationToken);
        return await StartAsync(cancellationToken);
    }

    public async Task<CoreSnapshot> InstallAndStartAsync(CoreRelease? release, CancellationToken cancellationToken)
    {
        await _launcher.InstallCoreAsync(release, cancellationToken);
        return await StartAsync(cancellationToken);
    }

    public async Task<CoreSnapshot> ReplaceAndStartAsync(CoreRelease release, CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(release);
        var stopped = await StopAsync(cancellationToken);
        if (stopped.ProcessId is not null || stopped.Status is CoreStatus.Running or CoreStatus.Unavailable)
        {
            throw new LauncherException("The current Core process could not be stopped before replacement.");
        }
        await _launcher.InstallCoreAsync(release, cancellationToken);
        return await StartAsync(cancellationToken);
    }

    public async Task<CoreSnapshot> StartAsync(CancellationToken cancellationToken)
    {
        var existing = await GetStatusAsync(cancellationToken);
        if (existing.Status == CoreStatus.Running) return existing;
        if (existing.ProcessId is not null && existing.Status == CoreStatus.Unavailable)
        {
            // A stale or half-started process owns this installation. Reap it
            // before retrying so the next start cannot collide on the pipe,
            // database, or optional health endpoint.
            await StopAsync(cancellationToken);
        }
        var executable = FindServiceExecutable();
        if (executable is null)
        {
            return Snapshot(CoreStatus.Missing, null, false, "Install Core before starting the service.", await ReadInstalledMetadataAsync(cancellationToken));
        }

        Directory.CreateDirectory(_launcher.DataRoot);
        var configPath = await EnsureConfigAsync(cancellationToken);
        var startInfo = new ProcessStartInfo
        {
            FileName = executable,
            WorkingDirectory = _launcher.DataRoot,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        startInfo.ArgumentList.Add("-config");
        startInfo.ArgumentList.Add(configPath);
        startInfo.Environment["CHUZI_DATA_DIR"] = _launcher.DataRoot;
        var logPath = Path.Combine(_launcher.DataRoot, "core.log");
        StreamWriter log;
        try
        {
            log = new StreamWriter(logPath, append: true) { AutoFlush = true };
        }
        catch (Exception exception)
        {
            throw new LauncherException($"Core log could not be opened at '{logPath}': {exception.Message}");
        }
        var process = new Process { StartInfo = startInfo, EnableRaisingEvents = true };
        process.Exited += (_, _) => log.Dispose();
        if (!process.Start())
        {
            log.Dispose();
            throw new LauncherException("Core service could not be started.");
        }
        _process = process;
        _ownsProcess = true;
        WriteManagedPid(process);
        _ = DrainAsync(process.StandardOutput, log);
        _ = DrainAsync(process.StandardError, log);

        var deadline = DateTime.UtcNow + TimeSpan.FromSeconds(15);
        while (DateTime.UtcNow < deadline)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (process.HasExited)
            {
                var exitCode = process.ExitCode;
                CleanupProcess(process);
                var metadata = await ReadInstalledMetadataAsync(cancellationToken);
                return Snapshot(CoreStatus.Unavailable, null, false, StartupFailureMessage(exitCode), metadata);
            }
            var probe = await ProbeAsync(CoreStatus.Starting, process.Id, true, cancellationToken, await ReadInstalledMetadataAsync(cancellationToken));
            if (probe.Status == CoreStatus.Running) return probe;
            await Task.Delay(250, cancellationToken);
        }
        int? timedOutID = process.HasExited ? null : process.Id;
        if (process.HasExited)
        {
            var exitCode = process.ExitCode;
            CleanupProcess(process);
            return Snapshot(CoreStatus.Unavailable, null, false, StartupFailureMessage(exitCode), await ReadInstalledMetadataAsync(cancellationToken));
        }
        // Do not leave an unresponsive process behind after a failed start.
        // Keeping it alive makes every later action collide with its pipe and
        // database locks, which was the main source of a permanently broken UI
        // state after one failed launch.
        var terminated = await TryTerminateAsync(process);
        CleanupProcess(process);
        return Snapshot(
            CoreStatus.Unavailable,
            terminated ? null : timedOutID,
            false,
            "Core did not become ready. Check core.log for details.",
            await ReadInstalledMetadataAsync(cancellationToken));
    }

    public async Task<CoreSnapshot> StopAsync(CancellationToken cancellationToken)
    {
        var executable = FindServiceExecutable();
        var running = executable is null
            ? null
            : (_process is { HasExited: false } ? _process : FindRunningService(executable) ?? FindManagedProcess(executable));
        if (running is null)
        {
            return await GetStatusAsync(cancellationToken);
        }
        try
        {
            using var process = Process.GetProcessById(running.Id);
            process.Kill(entireProcessTree: true);
            await process.WaitForExitAsync(cancellationToken);
        }
        catch (ArgumentException) { }
        catch (InvalidOperationException) { }
        catch (System.ComponentModel.Win32Exception exception)
        {
            throw new LauncherException($"Core could not be stopped: {exception.Message}");
        }
        finally
        {
            if (!ReferenceEquals(running, _process))
            {
                running?.Dispose();
            }
            CleanupProcess(_process);
        }
        return await GetStatusAsync(cancellationToken);
    }

    public async Task<CoreSnapshot> UninstallAsync(CancellationToken cancellationToken)
    {
        await StopAsync(cancellationToken);
        await _launcher.RemoveCoreAsync(cancellationToken);
        try { File.Delete(ManagedPidPath); } catch (IOException) { }
        catch (UnauthorizedAccessException exception)
        {
            throw new LauncherException($"Core state could not be cleaned up: {exception.Message}");
        }
        if (HasInstalledCoreFiles())
        {
            return Snapshot(CoreStatus.Unavailable, null, false, "Core files could not be removed.", await ReadInstalledMetadataAsync(cancellationToken));
        }
        return Snapshot(CoreStatus.Missing, null, false, "Core is uninstalled.", default);
    }

    private async Task<CoreSnapshot> ProbeAsync(CoreStatus fallback, int? processId, bool owned, CancellationToken cancellationToken, InstalledMetadata metadata)
    {
        try
        {
            using var client = CoreApiClient.FromDataDirectory(DataDirectory);
            await client.ConnectAsync(cancellationToken);
            return Snapshot(CoreStatus.Running, processId, owned, "Core is ready.", metadata);
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested) { throw; }
        catch (Exception exception) when (exception is OperationCanceledException or IOException or InvalidOperationException or TimeoutException or UnauthorizedAccessException or ObjectDisposedException or CoreApiException)
        {
            return Snapshot(fallback, processId, owned, fallback == CoreStatus.Starting ? "Waiting for Core..." : "Core is not reachable.", metadata);
        }
    }

    private async Task<CoreSnapshot?> ProbeExistingPipeAsync(InstalledMetadata metadata, CancellationToken cancellationToken)
    {
        using var probeTimeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        probeTimeout.CancelAfter(TimeSpan.FromMilliseconds(600));
        try
        {
            using var client = CoreApiClient.FromDataDirectory(DataDirectory);
            await client.ConnectAsync(probeTimeout.Token).ConfigureAwait(false);
            return Snapshot(CoreStatus.Running, null, false, "Core is ready.", metadata);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            return null;
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            throw;
        }
        catch (Exception exception) when (exception is IOException or InvalidOperationException or TimeoutException or UnauthorizedAccessException or ObjectDisposedException or CoreApiException)
        {
            return null;
        }
    }

    private CoreSnapshot Snapshot(CoreStatus status, int? processId, bool owned, string message, InstalledMetadata metadata)
        => new(status, processId, owned, message)
        {
            DataDirectory = DataDirectory,
            LogPath = LogPath,
            InstalledVersion = metadata.Version,
            InstalledChannel = metadata.Channel,
            InstalledCommit = metadata.Commit,
        };

    private async Task<string> EnsureConfigAsync(CancellationToken cancellationToken)
    {
        var path = Path.Combine(_launcher.DataRoot, "core-config.json");
        var rewrite = true;
        if (File.Exists(path))
        {
            try
            {
                using var document = JsonDocument.Parse(await File.ReadAllTextAsync(path, cancellationToken));
                var root = document.RootElement;
                var configuredDataDir = root.TryGetProperty("data_dir", out var dataDir) ? dataDir.GetString() : null;
                var configuredHealth = root.TryGetProperty("health", out var health) && health.TryGetProperty("listen", out var listen)
                    ? listen.GetString()
                    : null;
                rewrite = !string.Equals(Path.GetFullPath(configuredDataDir ?? ""), Path.GetFullPath(_launcher.DataRoot), StringComparison.OrdinalIgnoreCase)
                    || !string.IsNullOrWhiteSpace(configuredHealth);
            }
            catch (JsonException) { rewrite = true; }
        }
        if (!rewrite) return path;
        var config = new
        {
            data_dir = _launcher.DataRoot,
            credentials = new { key_env = "CHUZI_CREDENTIAL_KEY", key_id_env = "CHUZI_CREDENTIAL_KEY_ID", history_env = "CHUZI_CREDENTIAL_KEYS" },
            // The UI uses the owner-only Core named pipe for readiness and API
            // calls. Leave the optional HTTP health endpoint disabled so an
            // older manually-started Core cannot make the managed instance
            // fail by occupying port 8080.
            health = new { listen = "" },
            observability = new { metrics_listen = "", log_path = Path.Combine(_launcher.DataRoot, "core-service.log"), log_max_bytes = 10485760, log_max_files = 5 },
        };
        var temporary = path + ".tmp-" + Guid.NewGuid().ToString("N");
        await File.WriteAllTextAsync(temporary, JsonSerializer.Serialize(config), cancellationToken);
        File.Move(temporary, path, overwrite: true);
        return path;
    }

    private string? FindServiceExecutable()
    {
        var installed = Path.Combine(_launcher.DataRoot, "chuzi.exe");
        return File.Exists(installed) ? installed : null;
    }

    private bool HasInstalledCoreFiles()
    {
        if (FindServiceExecutable() is not null) return true;
        var worker = Path.Combine(_launcher.DataRoot, "browser-worker");
        try { return Directory.Exists(worker) && Directory.EnumerateFileSystemEntries(worker).Any(); }
        catch (UnauthorizedAccessException) { return true; }
    }

    private static Process? FindRunningService(string executable)
    {
        var expected = Path.GetFullPath(executable);
        foreach (var process in Process.GetProcessesByName(Path.GetFileNameWithoutExtension(executable)))
        {
            try
            {
                var path = process.MainModule?.FileName;
                if (path is not null && string.Equals(Path.GetFullPath(path), expected, StringComparison.OrdinalIgnoreCase)) return process;
            }
            catch (Exception exception) when (exception is ArgumentException or InvalidOperationException or ObjectDisposedException or System.ComponentModel.Win32Exception or NotSupportedException) { }
            process.Dispose();
        }
        return null;
    }

    private Process? FindManagedProcess(string executable)
    {
        if (!File.Exists(ManagedPidPath)) return null;
        var expected = Path.GetFullPath(executable);
        try
        {
            var text = File.ReadAllText(ManagedPidPath).Trim();
            if (!int.TryParse(text, out var pid) || pid <= 0) return null;
            var process = Process.GetProcessById(pid);
            if (process.HasExited || !string.Equals(process.ProcessName, Path.GetFileNameWithoutExtension(expected), StringComparison.OrdinalIgnoreCase))
            {
                process.Dispose();
                return null;
            }
            try
            {
                var path = process.MainModule?.FileName;
                if (path is null || !string.Equals(Path.GetFullPath(path), expected, StringComparison.OrdinalIgnoreCase))
                {
                    process.Dispose();
                    return null;
                }
            }
            catch (Exception exception) when (exception is ArgumentException or InvalidOperationException or ObjectDisposedException or System.ComponentModel.Win32Exception or NotSupportedException)
            {
                // A PID marker is only useful when the executable identity can
                // be checked. Refuse to kill an unrelated process on access
                // denied or an inspection race.
                process.Dispose();
                return null;
            }
            return process;
        }
        catch (Exception exception) when (exception is ArgumentException or InvalidOperationException or ObjectDisposedException or System.ComponentModel.Win32Exception or NotSupportedException)
        {
            return null;
        }
    }

    private static async Task<bool> TryTerminateAsync(Process process)
    {
        try
        {
            if (!process.HasExited) process.Kill(entireProcessTree: true);
            await process.WaitForExitAsync(CancellationToken.None);
            return true;
        }
        catch (ArgumentException) { return true; }
        catch (InvalidOperationException) { return true; }
        catch (System.ComponentModel.Win32Exception) { return false; }
    }

    private void WriteManagedPid(Process process)
    {
        try
        {
            var temporary = ManagedPidPath + ".tmp-" + Guid.NewGuid().ToString("N");
            File.WriteAllText(temporary, process.Id.ToString(System.Globalization.CultureInfo.InvariantCulture));
            File.Move(temporary, ManagedPidPath, overwrite: true);
        }
        catch (Exception exception)
        {
            // The process is still controllable in this window; persistence is
            // best effort and must not turn a successful start into a failure.
            try { File.Delete(ManagedPidPath + ".tmp"); } catch { }
            _ = exception;
        }
    }

    private string StartupFailureMessage(int exitCode)
    {
        var detail = ReadLogTail();
        return string.IsNullOrWhiteSpace(detail)
            ? $"Core stopped during startup (exit code {exitCode}). Check {LogPath} for details."
            : $"Core stopped during startup (exit code {exitCode}): {detail}";
    }

    private string ReadLogTail()
    {
        try
        {
            if (!File.Exists(LogPath)) return "";
            var lines = File.ReadAllLines(LogPath);
            return string.Join(" | ", lines.TakeLast(8).Select(line => line.Trim()).Where(line => line.Length > 0));
        }
        catch { return ""; }
    }

    private void CleanupProcess(Process? process)
    {
        if (ReferenceEquals(_process, process))
        {
            _process = null;
            _ownsProcess = false;
        }
        var exited = false;
        try { exited = process is null || process.HasExited; }
        catch (Exception exception) when (exception is InvalidOperationException or ObjectDisposedException) { exited = true; }
        if (exited)
        {
            try { File.Delete(ManagedPidPath); } catch { }
        }
        try { process?.Dispose(); } catch (ObjectDisposedException) { }
    }

    private async Task<InstalledMetadata> ReadInstalledMetadataAsync(CancellationToken cancellationToken)
    {
        var path = Path.Combine(_launcher.DataRoot, "release-manifest.json");
        if (!File.Exists(path)) return default;
        try
        {
            using var document = JsonDocument.Parse(await File.ReadAllTextAsync(path, cancellationToken));
            var root = document.RootElement;
            return new InstalledMetadata(
                root.TryGetProperty("version", out var version) ? version.GetString() ?? "" : "",
                root.TryGetProperty("channel", out var channel) ? channel.GetString() ?? "" : "",
                root.TryGetProperty("commit", out var commit) ? commit.GetString() ?? "" : "");
        }
        catch (JsonException) { return default; }
    }

    private readonly record struct InstalledMetadata(string Version, string Channel, string Commit);

    private static async Task DrainAsync(StreamReader reader, StreamWriter log)
    {
        try
        {
            while (await reader.ReadLineAsync() is { } line) await log.WriteLineAsync(line);
        }
        catch (ObjectDisposedException) { }
        finally { reader.Dispose(); }
    }

    public void Dispose()
    {
        // Core is a user-installed background component. Closing the UI must
        // not terminate it; StopAsync is the explicit lifecycle action.
        CleanupProcess(_process);
    }
}
