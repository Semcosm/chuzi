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

    private readonly string _pipeName;
    private readonly NamedPipeClientStream _pipe;
    private readonly StreamReader _reader;
    private readonly StreamWriter _writer;
    private readonly ConcurrentDictionary<string, TaskCompletionSource<WireEnvelope>> _pending = new();
    private readonly SemaphoreSlim _writeLock = new(1, 1);
    private readonly CancellationTokenSource _shutdown = new();
    private Task? _readerTask;
    private int _nextID;
    private bool _connected;

    private CoreApiClient(string dataDirectory)
    {
        var absolute = Path.GetFullPath(dataDirectory);
        while (absolute.Length > 3 && Path.EndsInDirectorySeparator(absolute))
        {
            absolute = absolute[..^1];
        }
        var hash = SHA256.HashData(Encoding.UTF8.GetBytes(absolute));
        _pipeName = "chuzi-core-" + Convert.ToHexString(hash.AsSpan(0, 8)).ToLowerInvariant();
        _pipe = new NamedPipeClientStream(".", _pipeName, PipeDirection.InOut, PipeOptions.Asynchronous);
        _reader = new StreamReader(_pipe, Encoding.UTF8, false, 1024, leaveOpen: true);
        _writer = new StreamWriter(_pipe, new UTF8Encoding(false), 1024, leaveOpen: true) { AutoFlush = true };
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
        try
        {
            await _pipe.ConnectAsync(5000, cancellationToken);
            if (!_pipe.IsConnected)
            {
                throw new CoreApiException("unavailable", "Core service pipe did not connect.");
            }

            // Start the reader before writing hello. Task.Run introduced a
            // scheduling window where the first response could race the
            // reader, and a just-created Windows pipe can briefly report an
            // unconnected state while the server finishes accepting it.
            _readerTask = ReadLoopAsync();
            var hello = await CallAsync<HelloResult>("hello", new { version = Protocol }, cancellationToken);
            if (hello.Version != Protocol)
            {
                throw new CoreApiException("unavailable", "Core protocol version is not supported.");
            }
            _connected = true;
        }
        catch
        {
            _connected = false;
            FailPending(new CoreApiException("unavailable", "Core service pipe connection failed."));
            throw;
        }
    }

    public async Task<CoreRequest> SubmitRequestAsync(string accountID, CancellationToken cancellationToken)
    {
        EnsureConnected();
        var requestID = "ui-" + Guid.NewGuid().ToString("N");
        var result = await CallAsync<SubmitResult>("submit_request", new
        {
            request_id = requestID,
            account_id = accountID,
            idempotency_key = requestID,
        }, cancellationToken);
        return result.Request;
    }

    public Task<CoreRequest> GetRequestAsync(string requestID, CancellationToken cancellationToken)
    {
        EnsureConnected();
        return CallAsync<CoreRequest>("get_request", new { request_id = requestID }, cancellationToken);
    }

    public Task<CoreAccount> GetAccountAsync(string accountID, CancellationToken cancellationToken)
    {
        EnsureConnected();
        return CallAsync<CoreAccount>("get_account", new { account_id = accountID }, cancellationToken);
    }

    public Task<CoreRequest> CancelRequestAsync(string requestID, CancellationToken cancellationToken)
    {
        EnsureConnected();
        return CallAsync<CoreRequest>("cancel_request", new { request_id = requestID, reason = "cancelled by Windows client" }, cancellationToken);
    }

    private async Task<T> CallAsync<T>(string method, object parameters, CancellationToken cancellationToken)
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
            }, cancellationToken);
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
            }, CancellationToken.None);
        }
        catch (Exception exception) when (exception is IOException or ObjectDisposedException or CoreApiException or OperationCanceledException)
        {
            // The original cancellation is already reflected in the caller's context.
        }
    }

    private async Task SendAsync(WireEnvelope envelope, CancellationToken cancellationToken)
    {
        var json = JsonSerializer.Serialize(envelope, JsonOptions);
        if (Encoding.UTF8.GetByteCount(json) > 1 << 20)
        {
            throw new CoreApiException("invalid_argument", "Core request is too large.");
        }
        await _writeLock.WaitAsync(cancellationToken);
        try
        {
            await _writer.WriteLineAsync(json.AsMemory(), cancellationToken);
        }
        finally
        {
            _writeLock.Release();
        }
    }

    private async Task ReadLoopAsync()
    {
        try
        {
            while (!_shutdown.IsCancellationRequested)
            {
                var line = await _reader.ReadLineAsync(_shutdown.Token);
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
            var error = exception is JsonException
                ? new CoreApiException("internal", "Invalid Core response.")
                : new CoreApiException("unavailable", "Core service disconnected.");
            FailPending(error);
        }
    }

    private void FailPending(Exception exception)
    {
        foreach (var item in _pending.Values)
        {
            item.TrySetException(exception);
        }
    }

    private void EnsureConnected()
    {
        if (!_connected || !_pipe.IsConnected)
        {
            throw new CoreApiException("unavailable", "Core service is not connected.");
        }
    }

    public void Dispose()
    {
        _shutdown.Cancel();
        FailPending(new CoreApiException("cancelled", "Core client closed."));
        _writer.Dispose();
        _reader.Dispose();
        _pipe.Dispose();
        _writeLock.Dispose();
        _shutdown.Dispose();
    }
}
