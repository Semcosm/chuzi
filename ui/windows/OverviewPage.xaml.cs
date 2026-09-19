using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Windows.UI;

namespace Chuzi.Native.Windows;

public sealed class OverviewPage : Page
{
    public event EventHandler? InstallRequested;
    public event EventHandler? StartRequested;
    public event EventHandler? StopRequested;
    public event EventHandler? RefreshRequested;

    private readonly TextBlock SetupHint;
    private readonly FontIcon StatusIcon;
    private readonly TextBlock StatusText;
    private readonly TextBlock StatusDetails;
    private readonly TextBlock DataDirectoryText;
    private readonly ProgressRing BusyRing;
    private readonly Button InstallButton;
    private readonly Button StartButton;
    private readonly Button StopButton;
    private readonly Button RefreshButton;
    private readonly TextBlock CoreStepText;
    private readonly TextBlock PluginStepText;
    private readonly TextBlock SettingsStepText;
    private readonly InfoBar MessageBar;

    public OverviewPage()
    {
        SetupHint = new TextBlock { Text = "Checking the Core installation...", Opacity = 0.72, TextWrapping = TextWrapping.Wrap };
        StatusIcon = new FontIcon { Glyph = "\uE930", FontSize = 28, VerticalAlignment = VerticalAlignment.Top };
        StatusText = new TextBlock { Text = "Checking Core...", FontSize = 20, FontWeight = FontWeights.SemiBold };
        StatusDetails = new TextBlock { TextWrapping = TextWrapping.Wrap, Opacity = 0.72 };
        DataDirectoryText = new TextBlock { TextWrapping = TextWrapping.Wrap, Opacity = 0.62 };
        BusyRing = new ProgressRing { Width = 24, Height = 24, IsActive = false };
        InstallButton = new Button { Content = "Install Core" };
        StartButton = new Button { Content = "Start Core" };
        StopButton = new Button { Content = "Stop Core" };
        RefreshButton = new Button { Content = "Refresh" };
        CoreStepText = new TextBlock { Text = "1. Install Core" };
        PluginStepText = new TextBlock { Text = "2. Configure plugins" };
        SettingsStepText = new TextBlock { Text = "3. Personalize settings" };
        MessageBar = new InfoBar { IsOpen = false, IsClosable = true };

        InstallButton.Click += InstallButton_Click;
        StartButton.Click += StartButton_Click;
        StopButton.Click += StopButton_Click;
        RefreshButton.Click += RefreshButton_Click;

        var statusGrid = new Grid { ColumnSpacing = 18 };
        statusGrid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        statusGrid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        statusGrid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        statusGrid.Children.Add(StatusIcon);
        Grid.SetColumn(StatusIcon, 0);
        var statusText = new StackPanel { Spacing = 5 };
        statusText.Children.Add(StatusText);
        statusText.Children.Add(StatusDetails);
        statusText.Children.Add(DataDirectoryText);
        statusGrid.Children.Add(statusText);
        Grid.SetColumn(statusText, 1);
        statusGrid.Children.Add(BusyRing);
        Grid.SetColumn(BusyRing, 2);

        var checklist = new StackPanel { Spacing = 8 };
        checklist.Children.Add(new TextBlock { Text = "First-run checklist", FontSize = 20, FontWeight = FontWeights.SemiBold });
        checklist.Children.Add(CoreStepText);
        checklist.Children.Add(PluginStepText);
        checklist.Children.Add(SettingsStepText);

        var content = new StackPanel { Spacing = 24 };
        var heading = new StackPanel { Spacing = 6 };
        heading.Children.Add(new TextBlock { Text = "Welcome to Chuzi", FontSize = 32, FontWeight = FontWeights.SemiBold });
        heading.Children.Add(SetupHint);
        content.Children.Add(heading);
        content.Children.Add(CreateCard(statusGrid, new Thickness(20)));
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        actions.Children.Add(InstallButton);
        actions.Children.Add(StartButton);
        actions.Children.Add(StopButton);
        actions.Children.Add(RefreshButton);
        content.Children.Add(actions);
        content.Children.Add(CreateCard(checklist, new Thickness(20)));
        content.Children.Add(MessageBar);

        var root = new Border { Padding = new Thickness(32, 28, 32, 32), MaxWidth = 900, Child = content };
        Content = new ScrollViewer { Content = root };
    }

    private static Border CreateCard(UIElement child, Thickness padding)
        => new()
        {
            BorderBrush = new SolidColorBrush(Color.FromArgb(48, 128, 128, 128)),
            BorderThickness = new Thickness(1),
            CornerRadius = new CornerRadius(6),
            Padding = padding,
            Child = child,
        };

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
        StopButton.Visibility = snapshot.Status == CoreStatus.Running || snapshot.ProcessId is not null
            ? Visibility.Visible
            : Visibility.Collapsed;
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
