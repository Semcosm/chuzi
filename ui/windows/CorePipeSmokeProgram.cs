using Chuzi.Native.Windows;

if (args.Length < 1 || string.IsNullOrWhiteSpace(args[^1]))
{
    Console.Error.WriteLine($"usage: CorePipeSmoke <data-directory> (received {args.Length} argument(s))");
    return 2;
}

var dataDirectory = args[^1];
using var cancellation = new CancellationTokenSource(TimeSpan.FromSeconds(30));
for (var attempt = 0; attempt < 8; attempt++)
{
    using var client = CoreApiClient.FromDataDirectory(dataDirectory);
    await client.ConnectAsync(cancellation.Token);
    try
    {
        await client.GetAccountAsync("core-pipe-smoke", cancellation.Token).ConfigureAwait(false);
        throw new InvalidOperationException("Core API smoke unexpectedly found an account.");
    }
    catch (CoreApiException exception) when (exception.Code == "not_found")
    {
        // The request/response path is healthy. A fresh lifecycle data root
        // deliberately has no accounts yet.
    }
}

Console.WriteLine("Core named-pipe handshake and API round-trip passed.");
return 0;
