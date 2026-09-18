using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public partial class App : Application
{
    private Window? _window;

    public App()
    {
        UiDiagnostics.Log("App.ctor: begin");
        InitializeComponent();
        UiDiagnostics.Log("App.ctor: InitializeComponent returned");
        if (!string.Equals(Environment.GetEnvironmentVariable("CHUZI_UI_DISABLE_XAML_RESOURCES"), "1", StringComparison.Ordinal))
        {
            UiDiagnostics.Log("App.ctor: adding XamlControlsResources");
            Resources.MergedDictionaries.Add(new XamlControlsResources());
            UiDiagnostics.Log("App.ctor: XamlControlsResources added");
        }
        else
        {
            UiDiagnostics.Log("App.ctor: XamlControlsResources skipped");
        }
    }

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        UiDiagnostics.Log("App.OnLaunched: begin");
        _window = new MainWindow();
        UiDiagnostics.Log("App.OnLaunched: MainWindow constructed");
        _window.Activate();
        UiDiagnostics.Log("App.OnLaunched: window activated");
    }
}
