using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed partial class PluginsPage : Page
{
    private IReadOnlyList<PluginState> _plugins = [];
    private bool _coreReady;

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
        _plugins = values;
        PluginList.ItemsSource = values;
        EmptyText.Visibility = values.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    public void SetCoreSnapshot(CoreSnapshot snapshot)
    {
        _coreReady = snapshot.Status == CoreStatus.Running;
        CoreRequiredBar.IsOpen = !_coreReady;
        CoreRequiredBar.Message = _coreReady
            ? string.Empty
            : "Start Core before installing, trusting, enabling, or removing plugins.";
        PluginList.IsEnabled = _coreReady;
    }

    public void SetBusy(bool busy)
    {
        BusyRing.IsActive = busy;
        PluginList.IsEnabled = _coreReady && !busy;
    }

    public bool IsCoreReady => _coreReady;

    public PluginState? FindPlugin(string id)
        => _plugins.FirstOrDefault(plugin => string.Equals(plugin.Descriptor.ID, id, StringComparison.OrdinalIgnoreCase));

    public async Task<bool> ConfirmTrustAsync(string id, bool trust)
    {
        var plugin = FindPlugin(id);
        if (plugin is null || XamlRoot is null) return false;

        var permissions = plugin.Descriptor.PermissionsSummary;
        var capabilities = plugin.Descriptor.CapabilitiesSummary;
        var content = new StackPanel { Spacing = 8 };
        content.Children.Add(new TextBlock
        {
            Text = trust
                ? $"Trust {plugin.Descriptor.ID} before enabling it?"
                : $"Remove trust from {plugin.Descriptor.ID}?",
            TextWrapping = TextWrapping.Wrap,
        });
        content.Children.Add(new TextBlock { Text = $"Signer: {plugin.Descriptor.SignedByOrUnsigned}", Opacity = 0.72 });
        content.Children.Add(new TextBlock { Text = permissions, TextWrapping = TextWrapping.Wrap, Opacity = 0.72 });
        content.Children.Add(new TextBlock { Text = capabilities, TextWrapping = TextWrapping.Wrap, Opacity = 0.72 });

        var dialog = new ContentDialog
        {
            Title = trust ? "Trust plugin" : "Untrust plugin",
            Content = content,
            PrimaryButtonText = trust ? "Trust" : "Untrust",
            CloseButtonText = "Cancel",
            XamlRoot = XamlRoot,
        };
        return await dialog.ShowAsync() == ContentDialogResult.Primary;
    }

    public async Task<bool> ConfirmRemoveAsync(string id)
    {
        var plugin = FindPlugin(id);
        if (plugin is null || XamlRoot is null) return false;
        var dialog = new ContentDialog
        {
            Title = "Remove plugin",
            Content = $"Remove {plugin.Descriptor.ID}? You can install it again from the Core payload.",
            PrimaryButtonText = "Remove",
            CloseButtonText = "Cancel",
            XamlRoot = XamlRoot,
        };
        return await dialog.ShowAsync() == ContentDialogResult.Primary;
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
