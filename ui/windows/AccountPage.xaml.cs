using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed class AccountPage : Page
{
    private bool _coreReady;
    private readonly InfoBar CoreRequiredBar = new() { IsOpen = false, IsClosable = false, Severity = InfoBarSeverity.Informational };
    private readonly TextBox AccountIDBox = new() { PlaceholderText = "Enter an authorized account ID" };
    private readonly Button LookupButton = new() { Content = "Check status" };
    private readonly Button SubmitButton = new() { Content = "Submit task" };
    private readonly ProgressRing BusyRing = new() { Width = 22, Height = 22, IsActive = false };
    private readonly TextBlock AccountText = new() { Text = "No account loaded." };
    private readonly TextBlock StateText = new() { Opacity = 0.72 };
    private readonly TextBlock RequestText = new() { Opacity = 0.72 };
    private readonly TextBlock RevisionText = new() { Opacity = 0.72 };
    private readonly InfoBar MessageBar = new() { IsOpen = false, IsClosable = true };

    public event EventHandler<string>? LookupRequested;
    public event EventHandler<string>? SubmitRequested;

    public AccountPage()
    {
        LookupButton.Click += LookupButton_Click;
        SubmitButton.Click += SubmitButton_Click;
        var root = new StackPanel { Spacing = 22 };
        var heading = new StackPanel { Spacing = 6 };
        heading.Children.Add(new TextBlock { Text = "Accounts", FontSize = 32, FontWeight = FontWeights.SemiBold });
        heading.Children.Add(new TextBlock { Text = "Look up a redacted account state or submit a new task.", Opacity = 0.72, TextWrapping = TextWrapping.Wrap });
        root.Children.Add(heading);
        root.Children.Add(CoreRequiredBar);

        var form = new StackPanel { Spacing = 12 };
        form.Children.Add(new TextBlock { Text = "Account ID", FontSize = 20, FontWeight = FontWeights.SemiBold });
        form.Children.Add(AccountIDBox);
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        actions.Children.Add(LookupButton);
        actions.Children.Add(SubmitButton);
        actions.Children.Add(BusyRing);
        form.Children.Add(actions);
        root.Children.Add(Card(form));

        var status = new StackPanel { Spacing = 7 };
        status.Children.Add(new TextBlock { Text = "Account status", FontSize = 20, FontWeight = FontWeights.SemiBold });
        status.Children.Add(AccountText);
        status.Children.Add(StateText);
        status.Children.Add(RequestText);
        status.Children.Add(RevisionText);
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
        CoreRequiredBar.Message = _coreReady ? string.Empty : "Start Core before looking up accounts or submitting tasks.";
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
        RequestText.Text = string.IsNullOrWhiteSpace(account.RequestID) ? "Active request: none" : $"Active request: {account.RequestID}";
        RevisionText.Text = $"Revision: {account.Revision}";
    }

    public void SetSubmittedRequest(CoreRequest request) => RequestText.Text = $"Latest submitted request: {request.RequestId}";

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
        if (string.IsNullOrWhiteSpace(AccountID)) { ShowError("Enter an account ID first."); return; }
        LookupRequested?.Invoke(this, AccountID);
    }
    private void SubmitButton_Click(object sender, RoutedEventArgs e)
    {
        if (string.IsNullOrWhiteSpace(AccountID)) { ShowError("Enter an account ID first."); return; }
        SubmitRequested?.Invoke(this, AccountID);
    }
}
