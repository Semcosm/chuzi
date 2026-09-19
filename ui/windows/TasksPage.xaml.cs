using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed class TasksPage : Page
{
    private bool _coreReady;
    private readonly InfoBar CoreRequiredBar = new() { IsOpen = false, IsClosable = false, Severity = InfoBarSeverity.Informational };
    private readonly TextBox RequestIDBox = new() { PlaceholderText = "Enter a request ID" };
    private readonly Button RefreshButton = new() { Content = "Refresh status" };
    private readonly Button CancelButton = new() { Content = "Cancel task" };
    private readonly ProgressRing BusyRing = new() { Width = 22, Height = 22, IsActive = false };
    private readonly TextBlock RequestText = new() { Text = "No task loaded." };
    private readonly TextBlock TaskAccountText = new() { Opacity = 0.72 };
    private readonly TextBlock TaskStateText = new() { Opacity = 0.72 };
    private readonly TextBlock AttemptText = new() { Opacity = 0.72 };
    private readonly TextBlock FailureText = new() { TextWrapping = TextWrapping.Wrap, Opacity = 0.72 };
    private readonly InfoBar MessageBar = new() { IsOpen = false, IsClosable = true };

    public event EventHandler<string>? RefreshRequested;
    public event EventHandler<string>? CancelRequested;

    public TasksPage()
    {
        RefreshButton.Click += RefreshButton_Click;
        CancelButton.Click += CancelButton_Click;
        var root = new StackPanel { Spacing = 22 };
        var heading = new StackPanel { Spacing = 6 };
        heading.Children.Add(new TextBlock { Text = "Tasks", FontSize = 32, FontWeight = FontWeights.SemiBold });
        heading.Children.Add(new TextBlock { Text = "Track a Core request and cancel it when it is still active.", Opacity = 0.72, TextWrapping = TextWrapping.Wrap });
        root.Children.Add(heading);
        root.Children.Add(CoreRequiredBar);

        var form = new StackPanel { Spacing = 12 };
        form.Children.Add(new TextBlock { Text = "Request ID", FontSize = 20, FontWeight = FontWeights.SemiBold });
        form.Children.Add(RequestIDBox);
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        actions.Children.Add(RefreshButton);
        actions.Children.Add(CancelButton);
        actions.Children.Add(BusyRing);
        form.Children.Add(actions);
        root.Children.Add(Card(form));

        var status = new StackPanel { Spacing = 7 };
        status.Children.Add(new TextBlock { Text = "Task status", FontSize = 20, FontWeight = FontWeights.SemiBold });
        status.Children.Add(RequestText);
        status.Children.Add(TaskAccountText);
        status.Children.Add(TaskStateText);
        status.Children.Add(AttemptText);
        status.Children.Add(FailureText);
        root.Children.Add(Card(status));
        root.Children.Add(MessageBar);
        Content = new ScrollViewer { Content = new Border { Padding = new Thickness(32, 28, 32, 32), MaxWidth = 900, Child = root } };
    }

    private static Border Card(UIElement child)
        => new() { BorderThickness = new Thickness(1), CornerRadius = new CornerRadius(6), Padding = new Thickness(20), Child = child };

    public bool IsCoreReady => _coreReady;

    public void SetCoreSnapshot(CoreSnapshot snapshot)
    {
        _coreReady = snapshot.Status == CoreStatus.Running;
        CoreRequiredBar.IsOpen = !_coreReady;
        CoreRequiredBar.Message = _coreReady ? string.Empty : "Start Core before checking or cancelling tasks.";
        RefreshButton.IsEnabled = _coreReady;
        CancelButton.IsEnabled = _coreReady;
    }

    public void SetBusy(bool busy)
    {
        BusyRing.IsActive = busy;
        RefreshButton.IsEnabled = _coreReady && !busy;
        CancelButton.IsEnabled = _coreReady && !busy;
        RequestIDBox.IsEnabled = !busy;
    }

    public void SetRequest(CoreRequest request)
    {
        RequestIDBox.Text = request.RequestId;
        RequestText.Text = $"Request: {request.RequestId}";
        TaskAccountText.Text = $"Account: {request.Account}";
        TaskStateText.Text = $"State: {request.State}";
        AttemptText.Text = $"Attempt: {request.Attempt}";
        FailureText.Text = string.IsNullOrWhiteSpace(request.LastFailure) ? string.Empty : $"Last failure: {request.LastFailure}";
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

    private string RequestID => RequestIDBox.Text.Trim();
    private void RefreshButton_Click(object sender, RoutedEventArgs e)
    {
        if (string.IsNullOrWhiteSpace(RequestID)) { ShowError("Enter a request ID first."); return; }
        RefreshRequested?.Invoke(this, RequestID);
    }
    private void CancelButton_Click(object sender, RoutedEventArgs e)
    {
        if (string.IsNullOrWhiteSpace(RequestID)) { ShowError("Enter a request ID first."); return; }
        CancelRequested?.Invoke(this, RequestID);
    }
}
