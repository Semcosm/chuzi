using Microsoft.UI.Xaml;
using System;
using System.Threading;
using System.Threading.Tasks;

namespace Chuzi.Native.Windows;

public sealed partial class MainWindow : Window
{
    private readonly CoreApiClient _client = CoreApiClient.FromDeploymentEnvironment();
    private string? _requestId;
    private CancellationTokenSource? _requestCancellation;

    public MainWindow()
    {
        InitializeComponent();
        Closed += (_, _) => _client.Dispose();
        StatusText.Text = "Connecting to the local Core service...";
        _ = ConnectAsync();
    }

    private async Task ConnectAsync()
    {
        try
        {
            await _client.ConnectAsync(CancellationToken.None);
            StatusText.Text = "Core service connected. Submit a request to begin.";
        }
        catch (Exception)
        {
            StatusText.Text = "Core service unavailable.";
        }
    }

    private async void SubmitButton_Click(object sender, RoutedEventArgs args)
    {
        var accountID = AccountIdBox.Text.Trim();
        if (accountID.Length == 0)
        {
            StatusText.Text = "Account ID is required.";
            return;
        }
        SetBusy(true);
        _requestCancellation?.Cancel();
        _requestCancellation = new CancellationTokenSource();
        try
        {
            var request = await _client.SubmitRequestAsync(accountID, _requestCancellation.Token);
            _requestId = request.RequestId;
            StatusText.Text = $"Submitted {request.RequestId}. State: {request.State}";
        }
        catch (CoreApiException exception)
        {
            StatusText.Text = $"Core error: {exception.Code}";
        }
        catch (OperationCanceledException)
        {
            StatusText.Text = "Request cancelled.";
        }
        finally
        {
            SetBusy(false);
        }
    }

    private async void RefreshButton_Click(object sender, RoutedEventArgs args)
    {
        if (_requestId is null)
        {
            StatusText.Text = "No request has been submitted.";
            return;
        }
        try
        {
            var request = await _client.GetRequestAsync(_requestId, CancellationToken.None);
            StatusText.Text = $"{request.RequestId}\nState: {request.State}\nAttempt: {request.Attempt}";
        }
        catch (CoreApiException exception)
        {
            StatusText.Text = $"Core error: {exception.Code}";
        }
    }

    private async void CancelButton_Click(object sender, RoutedEventArgs args)
    {
        if (_requestId is null)
        {
            return;
        }
        try
        {
            var request = await _client.CancelRequestAsync(_requestId, CancellationToken.None);
            StatusText.Text = $"{request.RequestId}\nState: {request.State}";
        }
        catch (CoreApiException exception)
        {
            StatusText.Text = $"Core error: {exception.Code}";
        }
    }

    private void SetBusy(bool busy)
    {
        SubmitButton.IsEnabled = !busy;
        RefreshButton.IsEnabled = !busy;
        CancelButton.IsEnabled = !busy && _requestId is not null;
    }
}
