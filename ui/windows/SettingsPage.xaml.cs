using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed partial class SettingsPage : Page
{
    public event EventHandler<BehaviorSettings>? SaveRequested;
    public event EventHandler? InstallCoreRequested;

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
    }

    public void SetCoreSnapshot(CoreSnapshot snapshot)
    {
        CoreStatus.Text = snapshot.Message;
        InstallCoreButton.Content = snapshot.Status == CoreStatus.Running ? "Start Core" : "Install Core";
        InstallCoreButton.IsEnabled = snapshot.Status != CoreStatus.Starting;
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
    private void InstallCoreButton_Click(object sender, RoutedEventArgs e) => InstallCoreRequested?.Invoke(this, EventArgs.Empty);
}
