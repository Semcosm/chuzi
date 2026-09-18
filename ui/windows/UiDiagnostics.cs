using System;
using System.IO;

namespace Chuzi.Native.Windows;

internal static class UiDiagnostics
{
    private static readonly object Gate = new();

    public static void Log(string message)
    {
        var directory = Environment.GetEnvironmentVariable("CHUZI_UI_DIAGNOSTIC_DIR");
        if (string.IsNullOrWhiteSpace(directory))
        {
            return;
        }

        try
        {
            Directory.CreateDirectory(directory);
            lock (Gate)
            {
                File.AppendAllText(
                    Path.Combine(directory, "ui-startup.log"),
                    $"{DateTimeOffset.UtcNow:O} {message}{Environment.NewLine}");
            }
        }
        catch
        {
            // Diagnostics must never affect application startup.
        }
    }
}
