using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed partial class TasksPage : Page
{
    private bool _coreReady;

    public event EventHandler<string>? RefreshRequested;
    public event EventHandler<string>? CancelRequested;

    public TasksPage() => InitializeComponent();

    public bool IsCoreReady => _coreReady;

    public void SetCoreSnapshot(CoreSnapshot snapshot)
    {
        _coreReady = snapshot.Status == CoreStatus.Running;
        CoreRequiredBar.IsOpen = !_coreReady;
        CoreRequiredBar.Message = _coreReady
            ? string.Empty
            : "Start Core before checking or cancelling tasks.";
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
        FailureText.Text = string.IsNullOrWhiteSpace(request.LastFailure)
            ? string.Empty
            : $"Last failure: {request.LastFailure}";
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
        if (string.IsNullOrWhiteSpace(RequestID))
        {
            ShowError("Enter a request ID first.");
            return;
        }
        RefreshRequested?.Invoke(this, RequestID);
    }

    private void CancelButton_Click(object sender, RoutedEventArgs e)
    {
        if (string.IsNullOrWhiteSpace(RequestID))
        {
            ShowError("Enter a request ID first.");
            return;
        }
        CancelRequested?.Invoke(this, RequestID);
    }
}
