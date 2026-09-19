using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed partial class OverviewPage : Page
{
    public event EventHandler? InstallRequested;
    public event EventHandler? StartRequested;
    public event EventHandler? StopRequested;
    public event EventHandler? RefreshRequested;

    public OverviewPage()
    {
        InitializeComponent();
    }

    public void SetBusy(bool busy)
    {
        BusyRing.IsActive = busy;
        InstallButton.IsEnabled = !busy;
        StartButton.IsEnabled = !busy;
        StopButton.IsEnabled = !busy;
        RefreshButton.IsEnabled = !busy;
    }

    public void SetSnapshot(CoreSnapshot snapshot)
    {
        StatusText.Text = snapshot.Status switch
        {
            CoreStatus.Running => "Core is running",
            CoreStatus.Missing => "Core is not installed",
            CoreStatus.Starting => "Starting Core...",
            CoreStatus.Unavailable => "Core is unavailable",
            _ => "Core is stopped",
        };
        StatusDetails.Text = snapshot.ProcessId is int pid ? $"{snapshot.Message} Process ID {pid}." : snapshot.Message;
        DataDirectoryText.Text = string.IsNullOrWhiteSpace(snapshot.DataDirectory)
            ? string.Empty
            : $"Data directory: {snapshot.DataDirectory}";
        StatusIcon.Glyph = snapshot.Status == CoreStatus.Running ? "\uE73E" : "\uE783";
        SetupHint.Text = snapshot.Status switch
        {
            CoreStatus.Missing => "Install Core to unlock plugin management and background tasks.",
            CoreStatus.Starting => "Core is starting. Chuzi will keep checking until it is ready.",
            CoreStatus.Running => "Core is ready. Continue with plugins and personal settings.",
            CoreStatus.Unavailable => "Core was found but could not be reached. Check the Core log.",
            _ => "Core is installed but stopped. Start it to continue setup.",
        };
        CoreStepText.Text = snapshot.Status == CoreStatus.Missing
            ? "1. Install Core"
            : snapshot.Status == CoreStatus.Running
                ? "1. Core is ready"
                : "1. Start Core";
        PluginStepText.Text = snapshot.Status == CoreStatus.Running
            ? "2. Configure plugins in Plugins"
            : "2. Configure plugins after Core starts";
        SettingsStepText.Text = "3. Personalize settings when you are ready";
        InstallButton.Visibility = snapshot.Status == CoreStatus.Missing ? Visibility.Visible : Visibility.Collapsed;
        StartButton.Visibility = snapshot.Status is CoreStatus.Missing or CoreStatus.Running ? Visibility.Collapsed : Visibility.Visible;
        StopButton.Visibility = snapshot.Status == CoreStatus.Running && snapshot.OwnedByThisWindow ? Visibility.Visible : Visibility.Collapsed;
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

    private void InstallButton_Click(object sender, RoutedEventArgs e) => InstallRequested?.Invoke(this, EventArgs.Empty);
    private void StartButton_Click(object sender, RoutedEventArgs e) => StartRequested?.Invoke(this, EventArgs.Empty);
    private void StopButton_Click(object sender, RoutedEventArgs e) => StopRequested?.Invoke(this, EventArgs.Empty);
    private void RefreshButton_Click(object sender, RoutedEventArgs e) => RefreshRequested?.Invoke(this, EventArgs.Empty);
}
