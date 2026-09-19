using Microsoft.UI.Xaml;

namespace Chuzi.Native.Windows;

public partial class App : Application
{
    private Window? _window;

    public App()
    {
        UnhandledException += App_UnhandledException;
        InitializeComponent();
    }

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        try
        {
            _window = new MainWindow();
            _window.Activate();
        }
        catch (Exception exception)
        {
            StartupDiagnostics.Write(exception);
            throw;
        }
    }

    private void App_UnhandledException(object sender, Microsoft.UI.Xaml.UnhandledExceptionEventArgs args)
    {
        StartupDiagnostics.Write(args.Exception);
    }

    private static class StartupDiagnostics
    {
        public static void Write(Exception exception)
        {
            try
            {
                var directory = Path.Combine(
                    Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                    "Chuzi");
                Directory.CreateDirectory(directory);
                File.AppendAllText(
                    Path.Combine(directory, "startup.log"),
                    $"[{DateTimeOffset.Now:O}] {exception}\n\n");
            }
            catch
            {
                // Diagnostics must never mask the original startup failure.
            }
        }
    }
}
