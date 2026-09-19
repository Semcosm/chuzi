using System.Collections.Concurrent;
using System.IO.Pipes;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Chuzi.Native.Windows;

public sealed class CoreApiException : Exception
{
    public CoreApiException(string code, string message) : base(message) => Code = code;
    public string Code { get; }
}

public sealed record CoreRequest(
    [property: JsonPropertyName("request_id")] string RequestId,
    [property: JsonPropertyName("account")] string Account,
    [property: JsonPropertyName("state")] string State,
    [property: JsonPropertyName("attempt")] int Attempt,
    [property: JsonPropertyName("last_failure")] string? LastFailure = null);

internal sealed record SubmitResult(CoreRequest Request, bool Idempotent);
internal sealed record ErrorPayload(string Code, string Message);
internal sealed record HelloResult(string Version, string[] Methods);
public sealed record CoreAccount(
    [property: JsonPropertyName("account")] string Account,
    [property: JsonPropertyName("state")] string State,
    [property: JsonPropertyName("request_id")] string RequestID,
    [property: JsonPropertyName("revision")] ulong Revision);

internal sealed class WireEnvelope
{
    [JsonPropertyName("protocol")] public string Protocol { get; init; } = "";
    [JsonPropertyName("id")] public string Id { get; init; } = "";
    [JsonPropertyName("method")] public string? Method { get; init; }
    [JsonPropertyName("params")] public JsonElement? Params { get; init; }
    [JsonPropertyName("type")] public string? Type { get; init; }
    [JsonPropertyName("result")] public JsonElement? Result { get; init; }
    [JsonPropertyName("error")] public ErrorPayload? Error { get; init; }
}

internal sealed class CoreApiClient : IDisposable
{
    private const string Protocol = "chuzi.core/v1";
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);
    private static readonly UTF8Encoding Utf8 = new(false);

    private readonly string _pipeName;
    private NamedPipeClientStream? _pipe;
    private StreamReader? _reader;
    private readonly ConcurrentDictionary<string, TaskCompletionSource<WireEnvelope>> _pending = new();
    private readonly SemaphoreSlim _writeLock = new(1, 1);
    private readonly CancellationTokenSource _shutdown = new();
    private readonly SemaphoreSlim _connectLock = new(1, 1);
    private Task? _readerTask;
    private int _nextID;
    private bool _connected;

    // Windows can complete the named-pipe connect task just before the first
    // stream write is accepted. Keep retries here instead of exposing the
    // transient platform exception to the UI.
    private static readonly TimeSpan PipeWriteRetryDelay = TimeSpan.FromMilliseconds(50);
    private const int PipeWriteAttempts = 12;
    private const int PipeConnectAttempts = 4;

    private CoreApiClient(string dataDirectory)
    {
        var absolute = Path.GetFullPath(dataDirectory);
        while (absolute.Length > 3 && Path.EndsInDirectorySeparator(absolute))
        {
            absolute = absolute[..^1];
        }
        var hash = SHA256.HashData(Encoding.UTF8.GetBytes(absolute));
        _pipeName = "chuzi-core-" + Convert.ToHexString(hash.AsSpan(0, 8)).ToLowerInvariant();
    }

    public static CoreApiClient FromDeploymentEnvironment()
    {
        var configured = Environment.GetEnvironmentVariable("CHUZI_DATA_DIR");
        var dataDirectory = string.IsNullOrWhiteSpace(configured)
            ? ResolveDefaultDataDirectory()
            : configured;
        return new CoreApiClient(dataDirectory);
    }

    public static CoreApiClient FromDataDirectory(string dataDirectory)
        => new(string.IsNullOrWhiteSpace(dataDirectory)
            ? throw new ArgumentException("Core data directory is required.", nameof(dataDirectory))
            : dataDirectory);

    private static string ResolveDefaultDataDirectory()
    {
        var root = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "chuzi");
        var legacy = Path.Combine(root, "data");
        return Directory.Exists(legacy) &&
               (File.Exists(Path.Combine(legacy, "chuzi.exe")) ||
                File.Exists(Path.Combine(legacy, "release-manifest.json")) ||
                File.Exists(Path.Combine(legacy, "core-config.json")))
            ? legacy
            : root;
    }

    public async Task ConnectAsync(CancellationToken cancellationToken)
    {
        if (_connected)
        {
            return;
        }
        await _connectLock.WaitAsync(cancellationToken);
        try
        {
            if (_connected)
            {
                return;
            }

            Exception? last = null;
            for (var attempt = 0; attempt < PipeConnectAttempts; attempt++)
            {
                cancellationToken.ThrowIfCancellationRequested();
                var pipe = new NamedPipeClientStream(".", _pipeName, PipeDirection.InOut, PipeOptions.Asynchronous);
                var reader = new StreamReader(pipe, Encoding.UTF8, false, 1024, leaveOpen: true);
                _pipe = pipe;
                _reader = reader;
                try
                {
                    await pipe.ConnectAsync(5000, cancellationToken).ConfigureAwait(false);

                    // ConnectAsync may complete one scheduler turn before the
                    // Windows stream transitions to Connected. Waiting here
                    // prevents the first hello write from surfacing the
                    // platform's misleading "pipe hasn't been connected yet"
                    // exception.
                    await WaitUntilConnectedAsync(pipe, cancellationToken).ConfigureAwait(false);

                    // On some Windows builds the async pipe state becomes
                    // observable one scheduler turn before the underlying
                    // handle accepts an overlapped write. Give the handle a
                    // short settle period before publishing the connection.
                    await Task.Delay(PipeWriteRetryDelay, cancellationToken).ConfigureAwait(false);

                    // Start the reader before writing hello. If the first
                    // write races the Windows pipe state transition, the
                    // write path replaces this stream and retries the frame.
                    _readerTask = ReadLoopAsync(reader);
                    var hello = await CallAsync<HelloResult>("hello", new { version = Protocol }, cancellationToken, reconnectOnWriteFailure: false).ConfigureAwait(false);
                    if (hello.Version != Protocol)
                    {
                        throw new CoreApiException("unavailable", "Core protocol version is not supported.");
                    }
                    // A response can be delivered while the peer is already
                    // closing the stream. Do not publish a client that would
                    // fail its first Core operation on a stale connection.
                    if (!pipe.IsConnected || _readerTask is null || _readerTask.IsCompleted)
                    {
                        throw new InvalidOperationException("The Core pipe disconnected during handshake.");
                    }
                    _connected = true;
                    return;
                }
                catch (Exception exception) when (exception is InvalidOperationException or IOException or TimeoutException or UnauthorizedAccessException or ObjectDisposedException)
                {
                    last = exception;
                    ResetConnection(pipe, reader);
                    await RetryConnectionAsync(attempt, cancellationToken);
                }
                catch (CoreApiException exception) when (exception.Code == "unavailable")
                {
                    last = exception;
                    ResetConnection(pipe, reader);
                    await RetryConnectionAsync(attempt, cancellationToken);
                }
                catch (CoreApiException)
                {
                    ResetConnection(pipe, reader);
                    throw;
                }
                catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
                {
                    ResetConnection(pipe, reader);
                    throw;
                }
            }
            throw new CoreApiException("unavailable", $"Core service pipe connection failed: {last?.Message ?? "unknown error"}");
        }
        finally
        {
            _connectLock.Release();
        }
    }

    public async Task<CoreRequest> SubmitRequestAsync(string accountID, CancellationToken cancellationToken)
    {
        await EnsureConnectedAsync(cancellationToken);
        var requestID = "ui-" + Guid.NewGuid().ToString("N");
        var result = await CallAsync<SubmitResult>("submit_request", new
        {
            request_id = requestID,
            account_id = accountID,
            idempotency_key = requestID,
        }, cancellationToken);
        return result.Request;
    }

    public async Task<CoreRequest> GetRequestAsync(string requestID, CancellationToken cancellationToken)
    {
        await EnsureConnectedAsync(cancellationToken);
        return await CallAsync<CoreRequest>("get_request", new { request_id = requestID }, cancellationToken);
    }

    public async Task<CoreAccount> GetAccountAsync(string accountID, CancellationToken cancellationToken)
    {
        await EnsureConnectedAsync(cancellationToken);
        return await CallAsync<CoreAccount>("get_account", new { account_id = accountID }, cancellationToken);
    }

    public async Task<CoreRequest> CancelRequestAsync(string requestID, CancellationToken cancellationToken)
    {
        await EnsureConnectedAsync(cancellationToken);
        return await CallAsync<CoreRequest>("cancel_request", new { request_id = requestID, reason = "cancelled by Windows client" }, cancellationToken);
    }

    private async Task<T> CallAsync<T>(string method, object parameters, CancellationToken cancellationToken, bool reconnectOnWriteFailure = true)
    {
        var requestID = "ui-call-" + Interlocked.Increment(ref _nextID);
        var pending = new TaskCompletionSource<WireEnvelope>(TaskCreationOptions.RunContinuationsAsynchronously);
        _pending[requestID] = pending;
        try
        {
            await SendAsync(new WireEnvelope
            {
                Protocol = Protocol,
                Id = requestID,
                Method = method,
                Params = JsonSerializer.SerializeToElement(parameters, JsonOptions),
            }, cancellationToken, reconnectOnWriteFailure);
            WireEnvelope response;
            try
            {
                response = await pending.Task.WaitAsync(cancellationToken);
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                await SendCancelAsync(requestID);
                throw;
            }
            if (response.Protocol != Protocol || response.Id != requestID)
            {
                throw new CoreApiException("internal", "Invalid Core response.");
            }
            if (response.Type == "error")
            {
                var error = response.Error ?? new ErrorPayload("internal", "Core operation failed.");
                throw new CoreApiException(error.Code, error.Message);
            }
            if (response.Type != "result" || response.Result is null)
            {
                throw new CoreApiException("internal", "Invalid Core response.");
            }
            return response.Result.Value.Deserialize<T>(JsonOptions)
                ?? throw new CoreApiException("internal", "Invalid Core response.");
        }
        finally
        {
            _pending.TryRemove(requestID, out _);
        }
    }

    private async Task SendCancelAsync(string requestID)
    {
        try
        {
            await SendAsync(new WireEnvelope
            {
                Protocol = Protocol,
                Id = "ui-cancel-" + Interlocked.Increment(ref _nextID),
                Method = "cancel",
                Params = JsonSerializer.SerializeToElement(new { id = requestID }, JsonOptions),
            }, CancellationToken.None, reconnectOnWriteFailure: false);
        }
        catch (Exception exception) when (exception is IOException or ObjectDisposedException or CoreApiException or OperationCanceledException)
        {
            // The original cancellation is already reflected in the caller's context.
        }
    }

    private async Task SendAsync(WireEnvelope envelope, CancellationToken cancellationToken, bool reconnectOnWriteFailure = true)
    {
        var json = JsonSerializer.Serialize(envelope, JsonOptions);
        var frame = Utf8.GetBytes(json + "\n");
        if (frame.Length > 1 << 20)
        {
            throw new CoreApiException("invalid_argument", "Core request is too large.");
        }
        await _writeLock.WaitAsync(cancellationToken);
        Exception? last = null;
        var reconnect = false;
        try
        {
            for (var attempt = 0; attempt < PipeWriteAttempts; attempt++)
            {
                cancellationToken.ThrowIfCancellationRequested();
                try
                {
                    var pipe = _pipe ?? throw new CoreApiException("unavailable", "Core service pipe is not connected.");
                    await WaitUntilConnectedAsync(pipe, cancellationToken).ConfigureAwait(false);
                    // Use one synchronous write for the complete frame. The
                    // Windows named-pipe async write path can still report
                    // "Pipe hasn't been connected yet" immediately after
                    // ConnectAsync, even when IsConnected is true. The Core
                    // server reads frames continuously, so this local write
                    // is bounded by the pipe buffer and avoids that race.
                    pipe.Write(frame, 0, frame.Length);
                    pipe.Flush();
                    return;
                }
                catch (InvalidOperationException exception)
                {
                    last = exception;
                    if (reconnectOnWriteFailure && attempt == 0)
                    {
                        reconnect = true;
                        break;
                    }
                }
                catch (IOException exception)
                {
                    last = exception;
                    if (reconnectOnWriteFailure && attempt == 0)
                    {
                        reconnect = true;
                        break;
                    }
                }
                catch (CoreApiException exception) when (exception.Code == "unavailable")
                {
                    last = exception;
                    if (reconnectOnWriteFailure && attempt == 0)
                    {
                        reconnect = true;
                        break;
                    }
                }
                if (attempt + 1 < PipeWriteAttempts)
                {
                    await Task.Delay(PipeWriteRetryDelay, cancellationToken);
                }
            }
        }
        finally
        {
            _writeLock.Release();
        }
        if (reconnect)
        {
            await ReconnectAsync(cancellationToken);
            await SendAsync(envelope, cancellationToken, reconnectOnWriteFailure: false);
            return;
        }
        throw new CoreApiException("unavailable", $"Core service pipe is not connected: {last?.Message ?? "unknown error"}");
    }

    private async Task ReconnectAsync(CancellationToken cancellationToken)
    {
        // Detach the old reader before disposing it. Its EOF/error callback
        // must not fail the request that will be sent on the replacement
        // connection.
        var oldReader = _reader;
        var oldPipe = _pipe;
        _reader = null;
        _pipe = null;
        _connected = false;
        oldReader?.Dispose();
        oldPipe?.Dispose();
        await ConnectAsync(cancellationToken);
    }

    private async Task ReadLoopAsync(StreamReader reader)
    {
        try
        {
            while (!_shutdown.IsCancellationRequested)
            {
                var line = await reader.ReadLineAsync(_shutdown.Token);
                if (line is null)
                {
                    break;
                }
                if (Encoding.UTF8.GetByteCount(line) > 1 << 20)
                {
                    throw new CoreApiException("invalid_argument", "Core response is too large.");
                }
                var response = JsonSerializer.Deserialize<WireEnvelope>(line, JsonOptions)
                    ?? throw new CoreApiException("internal", "Invalid Core response.");
                if (_pending.TryGetValue(response.Id, out var pending))
                {
                    pending.TrySetResult(response);
                }
            }
        }
        catch (Exception exception) when (exception is CoreApiException or IOException or InvalidOperationException or JsonException or OperationCanceledException or ObjectDisposedException)
        {
            if (!ReferenceEquals(_reader, reader)) return;
            _connected = false;
            var error = exception is JsonException
                ? new CoreApiException("internal", "Invalid Core response.")
                : new CoreApiException("unavailable", "Core service disconnected.");
            FailPending(error);
        }
        finally
        {
            if (ReferenceEquals(_reader, reader))
            {
                _connected = false;
                FailPending(new CoreApiException("unavailable", "Core service disconnected."));
            }
        }
    }

    private void FailPending(Exception exception)
    {
        foreach (var item in _pending.Values)
        {
            item.TrySetException(exception);
        }
    }

    private async Task EnsureConnectedAsync(CancellationToken cancellationToken)
    {
        // Do not gate calls on IsConnected. Windows can report a transient
        // false value after ConnectAsync even though the stream is usable;
        // SendAsync handles the actual write and maps a real disconnect.
        if (!_connected)
        {
            await ConnectAsync(cancellationToken);
        }
        if (!_connected)
        {
            throw new CoreApiException("unavailable", "Core service is not connected.");
        }
    }

    private static async Task WaitUntilConnectedAsync(NamedPipeClientStream pipe, CancellationToken cancellationToken)
    {
        var deadline = DateTime.UtcNow + TimeSpan.FromSeconds(2);
        while (!pipe.IsConnected)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (DateTime.UtcNow >= deadline)
            {
                throw new InvalidOperationException("The pipe did not reach the connected state.");
            }
            await Task.Delay(PipeWriteRetryDelay, cancellationToken);
        }
    }

    public void Dispose()
    {
        _shutdown.Cancel();
        FailPending(new CoreApiException("cancelled", "Core client closed."));
        _connected = false;
        _reader?.Dispose();
        _pipe?.Dispose();
        _connectLock.Dispose();
        _writeLock.Dispose();
        _shutdown.Dispose();
    }

    private static void DisposeConnection(NamedPipeClientStream pipe, StreamReader reader)
    {
        reader.Dispose();
        pipe.Dispose();
    }

    private void ResetConnection(NamedPipeClientStream pipe, StreamReader reader)
    {
        _connected = false;
        FailPending(new CoreApiException("unavailable", "Core service pipe connection failed."));
        DisposeConnection(pipe, reader);
        if (ReferenceEquals(_pipe, pipe)) _pipe = null;
        if (ReferenceEquals(_reader, reader)) _reader = null;
    }

    private static async Task RetryConnectionAsync(int attempt, CancellationToken cancellationToken)
    {
        if (attempt + 1 < PipeConnectAttempts)
        {
            await Task.Delay(PipeWriteRetryDelay, cancellationToken);
        }
    }
}
