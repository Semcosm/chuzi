using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed class SettingsPage : Page
{
    public event EventHandler<BehaviorSettings>? SaveRequested;
    public event EventHandler<string>? CoreChannelChanged;
    public event EventHandler<CoreActionRequest>? CoreActionRequested;
    public event EventHandler? UninstallCoreRequested;

    private readonly ToggleSwitch AutoCheckUpdates = new() { Header = "Check for updates automatically" };
    private readonly ToggleSwitch AutoRepair = new() { Header = "Repair missing files automatically" };
    private readonly ComboBox UpdateChannel = CreateCombo(("Nightly", "nightly"), ("Stable", "stable"));
    private readonly NumberBox CheckInterval = new() { Header = "Update interval (minutes)", Minimum = 0, Maximum = 10080, SmallChange = 5, Value = 60, Width = 260 };
    private readonly ToggleSwitch LaunchOnLogin = new() { Header = "Launch Chuzi when I sign in" };
    private readonly ToggleSwitch CloseToTray = new() { Header = "Keep Chuzi running when the window closes" };
    private readonly TextBlock CoreStatusText = new() { Text = "Core status is being checked.", Opacity = 0.72 };
    private readonly TextBlock CoreInstalledVersionText = new() { Text = "Installed version: none", Opacity = 0.72 };
    private readonly ComboBox CoreChannel = CreateCombo(("Test", "test"), ("Stable", "stable"));
    private readonly ComboBox CoreVersion = new() { Width = 420, DisplayMemberPath = nameof(CoreRelease.DisplayName) };
    private readonly Button CoreActionButton = new() { Content = "Install Core" };
    private readonly Button UninstallCoreButton = new() { Content = "Uninstall Core" };
    private readonly Button SaveButton = new() { Content = "Save settings" };
    private readonly ProgressRing BusyRing = new() { Width = 22, Height = 22, IsActive = false };
    private readonly InfoBar MessageBar = new() { IsOpen = false, IsClosable = true };
    private bool _suppressCoreEvents;
    private CoreSnapshot? _coreSnapshot;

    public SettingsPage()
    {
        CoreChannel.Header = "Core update channel";
        CoreChannel.Width = 300;
        CoreChannel.SelectedIndex = 0;
        CoreVersion.Header = "Core version";

        CoreChannel.SelectionChanged += CoreChannel_SelectionChanged;
        CoreVersion.SelectionChanged += CoreVersion_SelectionChanged;
        CoreActionButton.Click += CoreActionButton_Click;
        UninstallCoreButton.Click += UninstallCoreButton_Click;
        SaveButton.Click += SaveButton_Click;

        var root = new StackPanel { Spacing = 24 };
        root.Children.Add(new TextBlock { Text = "Settings", FontSize = 32, FontWeight = FontWeights.SemiBold });
        var core = Section("Core");
        core.Children.Add(CoreStatusText);
        core.Children.Add(CoreInstalledVersionText);
        core.Children.Add(CoreChannel);
        core.Children.Add(CoreVersion);
        var coreActions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        coreActions.Children.Add(CoreActionButton);
        coreActions.Children.Add(UninstallCoreButton);
        core.Children.Add(coreActions);
        root.Children.Add(core);

        var updates = Section("Updates");
        updates.Children.Add(AutoCheckUpdates);
        updates.Children.Add(AutoRepair);
        UpdateChannel.Header = "App update channel";
        UpdateChannel.Width = 260;
        UpdateChannel.SelectedIndex = 0;
        updates.Children.Add(UpdateChannel);
        updates.Children.Add(CheckInterval);
        root.Children.Add(updates);

        var startup = Section("Startup");
        startup.Children.Add(LaunchOnLogin);
        startup.Children.Add(CloseToTray);
        root.Children.Add(startup);

        var save = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        save.Children.Add(SaveButton);
        save.Children.Add(BusyRing);
        root.Children.Add(save);
        root.Children.Add(MessageBar);
        Content = new ScrollViewer
        {
            Content = new Border { Padding = new Thickness(32, 28, 32, 32), MaxWidth = 900, Child = root },
        };
    }

    private static StackPanel Section(string title)
    {
        var panel = new StackPanel { Spacing = 8 };
        panel.Children.Add(new TextBlock { Text = title, FontSize = 20, FontWeight = FontWeights.SemiBold });
        return panel;
    }

    private static ComboBox CreateCombo(params (string Label, string Tag)[] items)
    {
        var combo = new ComboBox();
        foreach (var item in items) combo.Items.Add(new ComboBoxItem { Content = item.Label, Tag = item.Tag });
        return combo;
    }

    public void SetSettings(BehaviorSettings settings)
    {
        AutoCheckUpdates.IsOn = settings.AutoCheckUpdates;
        AutoRepair.IsOn = settings.AutoRepair;
        LaunchOnLogin.IsOn = settings.LaunchOnLogin;
        CloseToTray.IsOn = settings.CloseToTray;
        CheckInterval.Value = settings.CheckIntervalMinutes;
        UpdateChannel.SelectedIndex = string.Equals(settings.UpdateChannel, "stable", StringComparison.OrdinalIgnoreCase) ? 1 : 0;
    }

    public BehaviorSettings GetSettings()
    {
        var item = UpdateChannel.SelectedItem as ComboBoxItem;
        return new BehaviorSettings
        {
            AutoCheckUpdates = AutoCheckUpdates.IsOn,
            AutoRepair = AutoRepair.IsOn,
            LaunchOnLogin = LaunchOnLogin.IsOn,
            CloseToTray = CloseToTray.IsOn,
            UpdateChannel = item?.Tag?.ToString() ?? "nightly",
            CheckIntervalNanoseconds = (long)Math.Round(Math.Max(0, CheckInterval.Value) * 60_000_000_000d),
        };
    }

    public void SetBusy(bool busy)
    {
        BusyRing.IsActive = busy;
        SaveButton.IsEnabled = !busy;
        CoreChannel.IsEnabled = !busy;
        CoreVersion.IsEnabled = !busy;
        if (busy)
        {
            CoreActionButton.IsEnabled = false;
            UninstallCoreButton.IsEnabled = false;
        }
        else
        {
            UpdateCoreActionButton();
        }
    }

    public void SetCoreSnapshot(CoreSnapshot snapshot)
    {
        _coreSnapshot = snapshot;
        if (string.Equals(snapshot.InstalledChannel, "stable", StringComparison.OrdinalIgnoreCase) ||
            string.Equals(snapshot.InstalledChannel, "test", StringComparison.OrdinalIgnoreCase))
        {
            _suppressCoreEvents = true;
            try { CoreChannel.SelectedIndex = string.Equals(snapshot.InstalledChannel, "stable", StringComparison.OrdinalIgnoreCase) ? 1 : 0; }
            finally { _suppressCoreEvents = false; }
        }
        CoreStatusText.Text = snapshot.Message;
        CoreInstalledVersionText.Text = string.IsNullOrWhiteSpace(snapshot.InstalledVersion)
            ? "Installed version: none"
            : $"Installed version: {snapshot.InstalledVersion} ({snapshot.InstalledChannel})";
        UpdateCoreActionButton();
    }

    public void SetCoreReleases(string channel, IReadOnlyList<CoreRelease> releases)
    {
        _suppressCoreEvents = true;
        try
        {
            CoreVersion.ItemsSource = releases.ToList();
            var selected = releases.FirstOrDefault(release =>
                string.Equals(release.Version, _coreSnapshot?.InstalledVersion, StringComparison.OrdinalIgnoreCase));
            CoreVersion.SelectedItem = selected ?? releases.FirstOrDefault();
        }
        finally { _suppressCoreEvents = false; }
        UpdateCoreActionButton();
    }

    public string SelectedCoreChannel => (CoreChannel.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "test";
    public CoreRelease? SelectedCoreRelease => CoreVersion.SelectedItem as CoreRelease;

    private void UpdateCoreActionButton()
    {
        var snapshot = _coreSnapshot;
        if (snapshot is null)
        {
            CoreActionButton.Content = "Install Core";
            CoreActionButton.IsEnabled = false;
            UninstallCoreButton.IsEnabled = false;
            return;
        }
        var selected = SelectedCoreRelease;
        var selectedDiffers = selected is not null &&
            (!string.Equals(selected.Version, snapshot.InstalledVersion, StringComparison.OrdinalIgnoreCase) ||
             !string.Equals(selected.Channel, snapshot.InstalledChannel, StringComparison.OrdinalIgnoreCase) ||
             !string.Equals(selected.Commit, snapshot.InstalledCommit, StringComparison.OrdinalIgnoreCase));
        if (selectedDiffers && snapshot.Status != CoreStatus.Missing)
        {
            CoreActionButton.Content = "Replace Core";
            CoreActionButton.IsEnabled = snapshot.Status != CoreStatus.Starting;
        }
        else if (snapshot.Status == CoreStatus.Running)
        {
            CoreActionButton.Content = "Stop Core";
            CoreActionButton.IsEnabled = true;
        }
        else if (snapshot.Status == CoreStatus.Missing)
        {
            CoreActionButton.Content = "Install Core";
            CoreActionButton.IsEnabled = selected is not null;
        }
        else
        {
            CoreActionButton.Content = "Start Core";
            CoreActionButton.IsEnabled = snapshot.Status != CoreStatus.Starting;
        }
        UninstallCoreButton.IsEnabled = snapshot.Status != CoreStatus.Missing;
    }

    public void ShowError(string message)
    {
        MessageBar.Severity = InfoBarSeverity.Error;
        MessageBar.Message = message;
        MessageBar.IsOpen = true;
    }

    public void ShowSuccess(string message)
    {
        MessageBar.Severity = InfoBarSeverity.Success;
        MessageBar.Message = message;
        MessageBar.IsOpen = true;
    }

    private void SaveButton_Click(object sender, RoutedEventArgs e) => SaveRequested?.Invoke(this, GetSettings());
    private void CoreChannel_SelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        if (!_suppressCoreEvents) CoreChannelChanged?.Invoke(this, SelectedCoreChannel);
    }
    private void CoreVersion_SelectionChanged(object sender, SelectionChangedEventArgs e) => UpdateCoreActionButton();
    private void CoreActionButton_Click(object sender, RoutedEventArgs e)
    {
        var snapshot = _coreSnapshot;
        if (snapshot is null) return;
        var selected = SelectedCoreRelease;
        var selectedDiffers = selected is not null &&
            (!string.Equals(selected.Version, snapshot.InstalledVersion, StringComparison.OrdinalIgnoreCase) ||
             !string.Equals(selected.Channel, snapshot.InstalledChannel, StringComparison.OrdinalIgnoreCase) ||
             !string.Equals(selected.Commit, snapshot.InstalledCommit, StringComparison.OrdinalIgnoreCase));
        var action = snapshot.Status == CoreStatus.Missing ? "install" : selectedDiffers ? "replace" : snapshot.Status == CoreStatus.Running ? "stop" : "start";
        CoreActionRequested?.Invoke(this, new CoreActionRequest(action, selected));
    }
    private void UninstallCoreButton_Click(object sender, RoutedEventArgs e) => UninstallCoreRequested?.Invoke(this, EventArgs.Empty);
}

public sealed record CoreActionRequest(string Action, CoreRelease? Release);
