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
    private static readonly TimeSpan PipeWriteTimeout = TimeSpan.FromSeconds(2);
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
                // Prefer a synchronous handle. Some Windows/.NET builds
                // report an overlapped client as connected before accepting
                // its first write. Keep the asynchronous handle as a final
                // compatibility fallback, never as the first attempt.
                var synchronous = attempt < PipeConnectAttempts - 1;
                var pipeOptions = synchronous ? PipeOptions.None : PipeOptions.Asynchronous;
                var pipe = new NamedPipeClientStream(".", _pipeName, PipeDirection.InOut, pipeOptions);
                var reader = new StreamReader(pipe, Encoding.UTF8, false, 1024, leaveOpen: true);
                try
                {
                    await ConnectPipeAsync(pipe, cancellationToken).ConfigureAwait(false);
                    await WaitUntilConnectedAsync(pipe, cancellationToken).ConfigureAwait(false);

                    // Publish the connection before sending hello so the
                    // normal request path and its reader use the same handle.
                    // The previous implementation disposed this stream in a
                    // finally block after hello, then left _connected=true;
                    // the next operation consequently had no connected pipe.
                    _pipe = pipe;
                    _reader = reader;
                    _readerTask = Task.Run(() => ReadLoopAsync(reader));
                    var hello = await CallAsync<HelloResult>(
                        "hello",
                        new { version = Protocol },
                        cancellationToken,
                        reconnectOnWriteFailure: false).ConfigureAwait(false);
                    if (!string.Equals(hello.Version, Protocol, StringComparison.Ordinal))
                    {
                        throw new CoreApiException("unavailable", "Core protocol version is not supported.");
                    }
                    _connected = true;
                    return;
                }
                catch (OperationCanceledException)
                {
                    ResetConnection(pipe, reader);
                    throw;
                }
                catch (CoreApiException exception)
                {
                    ResetConnection(pipe, reader);
                    if (exception.Code != "unavailable") throw;
                    last = exception;
                    await RetryConnectionAsync(attempt, cancellationToken).ConfigureAwait(false);
                }
                catch (Exception exception) when (exception is InvalidOperationException or IOException or TimeoutException or UnauthorizedAccessException or ObjectDisposedException)
                {
                    ResetConnection(pipe, reader);
                    last = exception;
                    await RetryConnectionAsync(attempt, cancellationToken).ConfigureAwait(false);
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
        // Windows named-pipe handles created for synchronous I/O can hang on
        // their second write on some go-winio/Windows combinations. Keep the
        // long-lived connection for readiness probes, but issue each API call
        // over a fresh connection and batch the handshake with the request in
        // one write. This also avoids the first overlapped-write race seen on
        // asynchronous handles.
        if (method != "hello")
        {
            return await CallOnFreshConnectionAsync<T>(method, parameters, cancellationToken).ConfigureAwait(false);
        }

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

    private async Task<T> CallOnFreshConnectionAsync<T>(string method, object parameters, CancellationToken cancellationToken)
    {
        var requestID = "ui-call-" + Interlocked.Increment(ref _nextID);
        var helloID = "ui-hello-" + Interlocked.Increment(ref _nextID);
        Exception? last = null;

        for (var attempt = 0; attempt < 4; attempt++)
        {
            cancellationToken.ThrowIfCancellationRequested();
            // A subset of Windows/.NET builds reports a newly connected
            // overlapped handle as connected but rejects its first
            // WriteAsync with "pipe hasn't been connected yet". Retry that
            // exact transport path with a synchronous handle; the blocking
            // write is kept off the UI thread and remains bounded below.
            // Keep synchronous handles as the primary path for the same
            // first-write reason as ConnectAsync. The asynchronous handle is
            // retained only as a final compatibility fallback.
            var synchronous = attempt < 3;
            var pipeOptions = synchronous ? PipeOptions.None : PipeOptions.Asynchronous;
            var pipe = new NamedPipeClientStream(".", _pipeName, PipeDirection.InOut, pipeOptions);
            var reader = new StreamReader(pipe, Encoding.UTF8, false, 1024, leaveOpen: true);
            try
            {
                await ConnectPipeAsync(pipe, cancellationToken).ConfigureAwait(false);
                await WaitUntilConnectedAsync(pipe, cancellationToken).ConfigureAwait(false);

                var hello = new WireEnvelope
                {
                    Protocol = Protocol,
                    Id = helloID,
                    Method = "hello",
                    Params = JsonSerializer.SerializeToElement(new { version = Protocol }, JsonOptions),
                };
                var request = new WireEnvelope
                {
                    Protocol = Protocol,
                    Id = requestID,
                    Method = method,
                    Params = JsonSerializer.SerializeToElement(parameters, JsonOptions),
                };
                var frame = Utf8.GetBytes(JsonSerializer.Serialize(hello, JsonOptions) + "\n" + JsonSerializer.Serialize(request, JsonOptions) + "\n");
                if (frame.Length > 1 << 20)
                {
                    throw new CoreApiException("invalid_argument", "Core request is too large.");
                }
                await WriteFrameWithTimeoutAsync(pipe, frame, cancellationToken, synchronous).ConfigureAwait(false);

                var helloResponse = await ReadEnvelopeWithTimeoutAsync(reader, cancellationToken).ConfigureAwait(false);
                ValidateHelloResponse(helloResponse, helloID);
                var response = await ReadEnvelopeWithTimeoutAsync(reader, cancellationToken).ConfigureAwait(false);
                return DeserializeResponse<T>(response, requestID);
            }
            catch (CoreApiException exception) when (exception.Code == "unavailable")
            {
                last = exception;
            }
            catch (Exception exception) when (exception is IOException or InvalidOperationException or TimeoutException or UnauthorizedAccessException or ObjectDisposedException)
            {
                last = exception;
            }
            finally
            {
                reader.Dispose();
                pipe.Dispose();
            }

            if (attempt + 1 < 4)
            {
                await Task.Delay(PipeWriteRetryDelay, cancellationToken).ConfigureAwait(false);
            }
        }

        throw new CoreApiException("unavailable", $"Core service pipe is not connected: {last?.Message ?? "unknown error"}");
    }

    private static void ValidateHelloResponse(WireEnvelope response, string requestID)
    {
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
        var hello = response.Result.Value.Deserialize<HelloResult>(JsonOptions)
            ?? throw new CoreApiException("internal", "Invalid Core response.");
        if (hello.Version != Protocol)
        {
            throw new CoreApiException("unavailable", "Core protocol version is not supported.");
        }
    }

    private static T DeserializeResponse<T>(WireEnvelope response, string requestID)
    {
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

    private static async Task<WireEnvelope> ReadEnvelopeWithTimeoutAsync(StreamReader reader, CancellationToken cancellationToken)
    {
        var readTask = Task.Run(reader.ReadLine);
        string? line;
        try
        {
            line = await readTask.WaitAsync(PipeWriteTimeout, cancellationToken).ConfigureAwait(false);
        }
        catch (TimeoutException)
        {
            _ = readTask.ContinueWith(
                completed => _ = completed.Exception,
                CancellationToken.None,
                TaskContinuationOptions.OnlyOnFaulted,
                TaskScheduler.Default);
            throw new IOException("Core pipe read timed out.");
        }
        if (line is null)
        {
            throw new IOException("Core service closed the pipe.");
        }
        if (Encoding.UTF8.GetByteCount(line) > 1 << 20)
        {
            throw new CoreApiException("invalid_argument", "Core response is too large.");
        }
        return JsonSerializer.Deserialize<WireEnvelope>(line, JsonOptions)
            ?? throw new CoreApiException("internal", "Invalid Core response.");
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
                    // Windows named-pipe async write path can report "pipe
                    // hasn't been connected yet" immediately after a
                    // successful connect, even when IsConnected is true.
                    // Keep the blocking operation off the UI thread and bound
                    // it so a broken peer can still be replaced and retried.
                    await WriteFrameWithTimeoutAsync(pipe, frame, cancellationToken).ConfigureAwait(false);
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
                catch (TimeoutException exception)
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
            // A failed write can leave the underlying Windows pipe handle in
            // a permanently unusable state. Reconnect a few times with a
            // fresh handle; retrying writes on the same instance only repeats
            // the misleading "pipe hasn't been connected yet" exception.
            Exception? reconnectFailure = last;
            for (var reconnectAttempt = 0; reconnectAttempt < 3; reconnectAttempt++)
            {
                try
                {
                    await ReconnectAsync(cancellationToken);
                    await SendAsync(envelope, cancellationToken, reconnectOnWriteFailure: false);
                    return;
                }
                catch (CoreApiException exception) when (exception.Code == "unavailable")
                {
                    reconnectFailure = exception;
                }
                catch (IOException exception)
                {
                    reconnectFailure = exception;
                }
                if (reconnectAttempt + 1 < 3)
                {
                    await Task.Delay(PipeWriteRetryDelay, cancellationToken).ConfigureAwait(false);
                }
            }
            throw new CoreApiException("unavailable", $"Core service pipe is not connected: {reconnectFailure?.Message ?? "unknown error"}");
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

    private static async Task WriteFrameWithTimeoutAsync(
        NamedPipeClientStream pipe,
        byte[] frame,
        CancellationToken cancellationToken,
        bool synchronous = true)
    {
        // Keep the async implementation for normal overlapped handles. For
        // the Windows fallback path, a synchronous Write avoids the native
        // race while Task.Run prevents it from blocking the WinUI dispatcher.
        var writeTask = synchronous
            ? Task.Run(() =>
            {
                pipe.Write(frame, 0, frame.Length);
                pipe.Flush();
            })
            : pipe.WriteAsync(frame.AsMemory(), CancellationToken.None).AsTask();
        try
        {
            await writeTask.WaitAsync(PipeWriteTimeout, cancellationToken).ConfigureAwait(false);
        }
        catch (TimeoutException)
        {
            // ReconnectAsync owns disposal of the timed-out handle. Observe
            // the detached task so a late native write failure is not raised
            // as an unobserved task exception.
            _ = writeTask.ContinueWith(
                completed => _ = completed.Exception,
                CancellationToken.None,
                TaskContinuationOptions.OnlyOnFaulted,
                TaskScheduler.Default);
            throw new IOException("Core pipe write timed out.");
        }
    }

    private static async Task ConnectPipeAsync(NamedPipeClientStream pipe, CancellationToken cancellationToken)
    {
        var connectTask = Task.Run(() => pipe.Connect(5000));
        try
        {
            await connectTask.WaitAsync(cancellationToken).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // Dispose the handle so a worker still inside Connect can return;
            // the caller will discard this connection attempt.
            pipe.Dispose();
            _ = connectTask.ContinueWith(
                completed => _ = completed.Exception,
                CancellationToken.None,
                TaskContinuationOptions.OnlyOnFaulted,
                TaskScheduler.Default);
            throw;
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
