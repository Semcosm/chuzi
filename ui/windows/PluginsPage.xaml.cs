using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed partial class PluginsPage : Page
{
    public event EventHandler? RefreshRequested;
    public event EventHandler<string>? InstallRequested;
    public event EventHandler<string>? TrustRequested;
    public event EventHandler<string>? UntrustRequested;
    public event EventHandler<string>? EnableRequested;
    public event EventHandler<string>? DisableRequested;
    public event EventHandler<string>? RemoveRequested;

    public PluginsPage() => InitializeComponent();

    public void SetPlugins(IEnumerable<PluginState> plugins)
    {
        var values = plugins.ToList();
        PluginList.ItemsSource = values;
        EmptyText.Visibility = values.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    public void SetBusy(bool busy)
    {
        BusyRing.IsActive = busy;
        PluginList.IsEnabled = !busy;
    }

    public void ShowError(string message)
    {
        MessageBar.Severity = InfoBarSeverity.Error;
        MessageBar.Message = message;
        MessageBar.IsOpen = true;
    }

    private static string ID(object sender) => ((FrameworkElement)sender).Tag?.ToString() ?? "";
    private void RefreshButton_Click(object sender, RoutedEventArgs e) => RefreshRequested?.Invoke(this, EventArgs.Empty);
    private void InstallButton_Click(object sender, RoutedEventArgs e) => InstallRequested?.Invoke(this, ID(sender));
    private void TrustButton_Click(object sender, RoutedEventArgs e) => TrustRequested?.Invoke(this, ID(sender));
    private void UntrustButton_Click(object sender, RoutedEventArgs e) => UntrustRequested?.Invoke(this, ID(sender));
    private void EnableButton_Click(object sender, RoutedEventArgs e) => EnableRequested?.Invoke(this, ID(sender));
    private void DisableButton_Click(object sender, RoutedEventArgs e) => DisableRequested?.Invoke(this, ID(sender));
    private void RemoveButton_Click(object sender, RoutedEventArgs e) => RemoveRequested?.Invoke(this, ID(sender));
}
