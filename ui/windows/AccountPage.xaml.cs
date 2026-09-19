using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed partial class AccountPage : Page
{
    private bool _coreReady;

    public event EventHandler<string>? LookupRequested;
    public event EventHandler<string>? SubmitRequested;

    public AccountPage() => InitializeComponent();

    public bool IsCoreReady => _coreReady;

    public void SetCoreSnapshot(CoreSnapshot snapshot)
    {
        _coreReady = snapshot.Status == CoreStatus.Running;
        CoreRequiredBar.IsOpen = !_coreReady;
        CoreRequiredBar.Message = _coreReady
            ? string.Empty
            : "Start Core before looking up accounts or submitting tasks.";
        LookupButton.IsEnabled = _coreReady;
        SubmitButton.IsEnabled = _coreReady;
    }

    public void SetBusy(bool busy)
    {
        BusyRing.IsActive = busy;
        LookupButton.IsEnabled = _coreReady && !busy;
        SubmitButton.IsEnabled = _coreReady && !busy;
        AccountIDBox.IsEnabled = !busy;
    }

    public void SetAccount(CoreAccount account)
    {
        AccountText.Text = $"Account: {account.Account}";
        StateText.Text = $"State: {account.State}";
        RequestText.Text = string.IsNullOrWhiteSpace(account.RequestID)
            ? "Active request: none"
            : $"Active request: {account.RequestID}";
        RevisionText.Text = $"Revision: {account.Revision}";
    }

    public void SetSubmittedRequest(CoreRequest request)
    {
        RequestText.Text = $"Latest submitted request: {request.RequestId}";
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

    private string AccountID => AccountIDBox.Text.Trim();

    private void LookupButton_Click(object sender, RoutedEventArgs e)
    {
        if (string.IsNullOrWhiteSpace(AccountID))
        {
            ShowError("Enter an account ID first.");
            return;
        }
        LookupRequested?.Invoke(this, AccountID);
    }

    private void SubmitButton_Click(object sender, RoutedEventArgs e)
    {
        if (string.IsNullOrWhiteSpace(AccountID))
        {
            ShowError("Enter an account ID first.");
            return;
        }
        SubmitRequested?.Invoke(this, AccountID);
    }
}
