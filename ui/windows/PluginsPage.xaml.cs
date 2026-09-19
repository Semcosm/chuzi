using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed class PluginsPage : Page
{
    private IReadOnlyList<PluginState> _plugins = [];
    private bool _coreReady;
    private readonly ListView PluginList = new() { SelectionMode = ListViewSelectionMode.None, IsItemClickEnabled = false };
    private readonly TextBlock EmptyText = new() { Text = "No plugins are included in this Core release.", HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center, Opacity = 0.72 };
    private readonly InfoBar CoreRequiredBar = new() { IsOpen = false, IsClosable = false, Severity = InfoBarSeverity.Informational };
    private readonly ProgressRing BusyRing = new() { Width = 22, Height = 22, IsActive = false };
    private readonly InfoBar MessageBar = new() { IsOpen = false, IsClosable = true };

    public event EventHandler? RefreshRequested;
    public event EventHandler<string>? InstallRequested;
    public event EventHandler<string>? TrustRequested;
    public event EventHandler<string>? UntrustRequested;
    public event EventHandler<string>? EnableRequested;
    public event EventHandler<string>? DisableRequested;
    public event EventHandler<string>? RemoveRequested;

    public PluginsPage()
    {
        var root = new Grid { RowSpacing = 18, Padding = new Thickness(32, 28, 32, 32) };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });

        var heading = new StackPanel { Spacing = 4 };
        heading.Children.Add(new TextBlock { Text = "Plugins", FontSize = 32, FontWeight = FontWeights.SemiBold });
        heading.Children.Add(new TextBlock { Text = "Review permissions and trust before enabling a plugin.", Opacity = 0.72 });
        root.Children.Add(heading);
        root.Children.Add(CoreRequiredBar);
        Grid.SetRow(CoreRequiredBar, 1);

        var listHost = new Grid();
        listHost.Children.Add(PluginList);
        listHost.Children.Add(EmptyText);
        root.Children.Add(listHost);
        Grid.SetRow(listHost, 2);

        var footer = new Grid();
        var refresh = new Button { Content = "Refresh" };
        refresh.Click += RefreshButton_Click;
        var progress = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        progress.Children.Add(refresh);
        progress.Children.Add(BusyRing);
        footer.Children.Add(progress);
        footer.Children.Add(MessageBar);
        MessageBar.HorizontalAlignment = HorizontalAlignment.Right;
        root.Children.Add(footer);
        Grid.SetRow(footer, 3);
        Content = root;
    }

    public void SetPlugins(IEnumerable<PluginState> plugins)
    {
        var values = plugins.ToList();
        _plugins = values;
        PluginList.Items.Clear();
        foreach (var plugin in values) PluginList.Items.Add(CreatePluginItem(plugin));
        EmptyText.Visibility = values.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private UIElement CreatePluginItem(PluginState plugin)
    {
        var details = new StackPanel { Spacing = 4 };
        details.Children.Add(new TextBlock { Text = plugin.Descriptor.ID, FontSize = 17, FontWeight = FontWeights.SemiBold });
        details.Children.Add(new TextBlock { Text = plugin.Descriptor.Version, Opacity = 0.68 });
        details.Children.Add(new TextBlock { Text = plugin.Descriptor.API, Opacity = 0.68 });
        details.Children.Add(new TextBlock { Text = plugin.TrustSummary });
        details.Children.Add(new TextBlock { Text = plugin.Descriptor.PermissionsSummary, TextWrapping = TextWrapping.Wrap, Opacity = 0.68 });
        details.Children.Add(new TextBlock { Text = plugin.Descriptor.CapabilitiesSummary, TextWrapping = TextWrapping.Wrap, Opacity = 0.68 });
        details.Children.Add(new TextBlock { Text = plugin.Health });

        var actions = new StackPanel { Spacing = 8, MinWidth = 150 };
        actions.Children.Add(ActionButton("Install", plugin.Descriptor.Installable, plugin.Descriptor.ID, (_, _) => InstallRequested?.Invoke(this, plugin.Descriptor.ID)));
        actions.Children.Add(ActionButton("Trust", plugin.Installed, plugin.Descriptor.ID, (_, _) => TrustRequested?.Invoke(this, plugin.Descriptor.ID)));
        actions.Children.Add(ActionButton("Untrust", plugin.Trusted, plugin.Descriptor.ID, (_, _) => UntrustRequested?.Invoke(this, plugin.Descriptor.ID)));
        actions.Children.Add(ActionButton("Enable", plugin.Trusted, plugin.Descriptor.ID, (_, _) => EnableRequested?.Invoke(this, plugin.Descriptor.ID)));
        actions.Children.Add(ActionButton("Disable", plugin.Enabled, plugin.Descriptor.ID, (_, _) => DisableRequested?.Invoke(this, plugin.Descriptor.ID)));
        actions.Children.Add(ActionButton("Remove", plugin.Installed, plugin.Descriptor.ID, (_, _) => RemoveRequested?.Invoke(this, plugin.Descriptor.ID)));

        var grid = new Grid { ColumnSpacing = 16 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.Children.Add(details);
        grid.Children.Add(actions);
        Grid.SetColumn(actions, 1);
        return new Border { BorderThickness = new Thickness(1), CornerRadius = new CornerRadius(6), Padding = new Thickness(16), Margin = new Thickness(0, 0, 0, 10), Child = grid };
    }

    private static Button ActionButton(string label, bool enabled, string id, RoutedEventHandler handler)
    {
        var button = new Button { Content = label, IsEnabled = enabled, Tag = id };
        button.Click += handler;
        return button;
    }

    public void SetCoreSnapshot(CoreSnapshot snapshot)
    {
        _coreReady = snapshot.Status == CoreStatus.Running;
        CoreRequiredBar.IsOpen = !_coreReady;
        CoreRequiredBar.Message = _coreReady ? string.Empty : "Start Core before installing, trusting, enabling, or removing plugins.";
        PluginList.IsEnabled = _coreReady;
    }

    public void SetBusy(bool busy)
    {
        BusyRing.IsActive = busy;
        PluginList.IsEnabled = _coreReady && !busy;
    }

    public bool IsCoreReady => _coreReady;
    public PluginState? FindPlugin(string id) => _plugins.FirstOrDefault(plugin => string.Equals(plugin.Descriptor.ID, id, StringComparison.OrdinalIgnoreCase));

    public async Task<bool> ConfirmTrustAsync(string id, bool trust)
    {
        var plugin = FindPlugin(id);
        if (plugin is null || XamlRoot is null) return false;
        var content = new StackPanel { Spacing = 8 };
        content.Children.Add(new TextBlock { Text = trust ? $"Trust {plugin.Descriptor.ID} before enabling it?" : $"Remove trust from {plugin.Descriptor.ID}?", TextWrapping = TextWrapping.Wrap });
        content.Children.Add(new TextBlock { Text = $"Signer: {plugin.Descriptor.SignedByOrUnsigned}", Opacity = 0.72 });
        content.Children.Add(new TextBlock { Text = plugin.Descriptor.PermissionsSummary, TextWrapping = TextWrapping.Wrap, Opacity = 0.72 });
        content.Children.Add(new TextBlock { Text = plugin.Descriptor.CapabilitiesSummary, TextWrapping = TextWrapping.Wrap, Opacity = 0.72 });
        var dialog = new ContentDialog { Title = trust ? "Trust plugin" : "Untrust plugin", Content = content, PrimaryButtonText = trust ? "Trust" : "Untrust", CloseButtonText = "Cancel", XamlRoot = XamlRoot };
        return await dialog.ShowAsync() == ContentDialogResult.Primary;
    }

    public async Task<bool> ConfirmRemoveAsync(string id)
    {
        var plugin = FindPlugin(id);
        if (plugin is null || XamlRoot is null) return false;
        var dialog = new ContentDialog { Title = "Remove plugin", Content = $"Remove {plugin.Descriptor.ID}? You can install it again from the Core payload.", PrimaryButtonText = "Remove", CloseButtonText = "Cancel", XamlRoot = XamlRoot };
        return await dialog.ShowAsync() == ContentDialogResult.Primary;
    }

    public void ShowError(string message)
    {
        MessageBar.Severity = InfoBarSeverity.Error;
        MessageBar.Message = message;
        MessageBar.IsOpen = true;
    }

    private void RefreshButton_Click(object sender, RoutedEventArgs e) => RefreshRequested?.Invoke(this, EventArgs.Empty);
}
