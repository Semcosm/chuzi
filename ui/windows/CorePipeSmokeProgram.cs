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
}

Console.WriteLine("Core named-pipe client handshakes passed.");
return 0;
