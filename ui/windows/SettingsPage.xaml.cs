using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed partial class SettingsPage : Page
{
    public event EventHandler<BehaviorSettings>? SaveRequested;
    public event EventHandler<string>? CoreChannelChanged;
    public event EventHandler<CoreActionRequest>? CoreActionRequested;
    public event EventHandler? UninstallCoreRequested;
    private bool _suppressCoreEvents;
    private CoreSnapshot? _coreSnapshot;

    public SettingsPage()
    {
        InitializeComponent();
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
            try
            {
                CoreChannel.SelectedIndex = string.Equals(snapshot.InstalledChannel, "stable", StringComparison.OrdinalIgnoreCase) ? 1 : 0;
            }
            finally
            {
                _suppressCoreEvents = false;
            }
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
        finally
        {
            _suppressCoreEvents = false;
        }
        UpdateCoreActionButton();
    }

    public string SelectedCoreChannel
    {
        get
        {
            var item = CoreChannel.SelectedItem as ComboBoxItem;
            return item?.Tag?.ToString() ?? "test";
        }
    }

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
        if (_suppressCoreEvents) return;
        CoreChannelChanged?.Invoke(this, SelectedCoreChannel);
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
        var action = snapshot.Status == CoreStatus.Missing
            ? "install"
            : selectedDiffers
                ? "replace"
                : snapshot.Status == CoreStatus.Running
                    ? "stop"
                    : "start";
        CoreActionRequested?.Invoke(this, new CoreActionRequest(action, selected));
    }

    private void UninstallCoreButton_Click(object sender, RoutedEventArgs e) => UninstallCoreRequested?.Invoke(this, EventArgs.Empty);
}

public sealed record CoreActionRequest(string Action, CoreRelease? Release);
