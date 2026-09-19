using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Chuzi.Native.Windows;

public sealed partial class MainWindow : Window
{
    private readonly LauncherClient _launcher;
    private readonly CoreServiceController _core;
    private readonly OverviewPage _overview;
    private readonly SettingsPage _settings;
    private readonly PluginsPage _plugins;
    private readonly AccountPage _accounts;
    private readonly TasksPage _tasks;
    private readonly CancellationTokenSource _shutdown = new();
    private bool _loaded;

    public MainWindow()
    {
        InitializeComponent();

        _launcher = new LauncherClient();
        _core = new CoreServiceController(_launcher);
        _overview = new OverviewPage();
        _settings = new SettingsPage();
        _plugins = new PluginsPage();
        _accounts = new AccountPage();
        _tasks = new TasksPage();

        _overview.InstallRequested += (_, _) => RunAsync(InstallCoreAsync);
        _overview.StartRequested += (_, _) => RunAsync(StartCoreAsync);
        _overview.StopRequested += (_, _) => RunAsync(StopCoreAsync);
        _overview.RefreshRequested += (_, _) => RunAsync(RefreshCoreAsync);
        _settings.SaveRequested += (_, settings) => RunAsync(() => SaveSettingsAsync(settings));
        _settings.CoreChannelChanged += (_, channel) => RunAsync(() => RefreshCoreReleasesAsync(channel));
        _settings.CoreActionRequested += (_, request) => RunAsync(() => HandleCoreActionAsync(request));
        _settings.UninstallCoreRequested += (_, _) => RunAsync(UninstallCoreAsync);
        _plugins.RefreshRequested += (_, _) => RunAsync(RefreshPluginsAsync);
        _plugins.InstallRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "install"));
        _plugins.TrustRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "trust"));
        _plugins.UntrustRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "untrust"));
        _plugins.EnableRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "enable"));
        _plugins.DisableRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "disable"));
        _plugins.RemoveRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "remove"));
        _accounts.LookupRequested += (_, id) => RunAsync(() => LookupAccountAsync(id));
        _accounts.SubmitRequested += (_, id) => RunAsync(() => SubmitTaskAsync(id));
        _tasks.RefreshRequested += (_, id) => RunAsync(() => RefreshTaskAsync(id));
        _tasks.CancelRequested += (_, id) => RunAsync(() => CancelTaskAsync(id));
        Navigation.SelectedItem = Navigation.MenuItems[0];
        Activated += MainWindow_Activated;
        Closed += MainWindow_Closed;
    }

    private void MainWindow_Activated(object sender, WindowActivatedEventArgs args)
    {
        if (_loaded || args.WindowActivationState == WindowActivationState.Deactivated) return;
        _loaded = true;
        RunAsync(RefreshAllAsync);
    }

    private void Navigation_SelectionChanged(NavigationView sender, NavigationViewSelectionChangedEventArgs args)
    {
        if (args.SelectedItem is not NavigationViewItem item) return;
        ContentFrame.Content = item.Tag?.ToString() switch
        {
            "settings" => _settings,
            "plugins" => _plugins,
            "accounts" => _accounts,
            "tasks" => _tasks,
            _ => _overview,
        };
    }

    private async Task RefreshAllAsync()
    {
        await RefreshCoreAsync();
        await LoadSettingsAsync();
        await RefreshCoreReleasesAsync(_settings.SelectedCoreChannel);
        await RefreshPluginsAsync();
    }

    private async Task RefreshCoreAsync()
    {
        try
        {
            var snapshot = await _core.GetStatusAsync(_shutdown.Token);
            _overview.SetSnapshot(snapshot);
            _settings.SetCoreSnapshot(snapshot);
            _plugins.SetCoreSnapshot(snapshot);
            _accounts.SetCoreSnapshot(snapshot);
            _tasks.SetCoreSnapshot(snapshot);
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (Exception) { _overview.ShowError("Core status could not be read."); }
    }

    private async Task InstallCoreAsync()
    {
        await RunCoreActionAsync(() => _core.InstallAndStartAsync(_settings.SelectedCoreRelease, _shutdown.Token), "Core installed and started.");
    }

    private async Task StartCoreAsync()
    {
        await RunCoreActionAsync(() => _core.StartAsync(_shutdown.Token), "Core started.");
    }

    private async Task StopCoreAsync()
    {
        try
        {
            _overview.SetBusy(true);
            _settings.SetBusy(true);
            var snapshot = await _core.StopAsync(_shutdown.Token);
            ApplyCoreSnapshot(snapshot);
            _overview.ShowSuccess("Core stopped.");
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _overview.ShowError(exception.Message); }
        catch (Exception exception) { _overview.ShowError($"Core could not be stopped: {exception.Message}"); }
        finally { _overview.SetBusy(false); _settings.SetBusy(false); }
    }

    private async Task HandleCoreActionAsync(CoreActionRequest request)
    {
        switch (request.Action)
        {
            case "install":
                await RunCoreActionAsync(() => _core.InstallAndStartAsync(request.Release, _shutdown.Token), "Core installed and started.");
                break;
            case "replace":
                if (request.Release is null)
                {
                    _settings.ShowError("Select a Core version before replacing it.");
                    return;
                }
                await RunCoreActionAsync(() => _core.ReplaceAndStartAsync(request.Release, _shutdown.Token), "Core replaced and started.");
                break;
            case "start":
                await StartCoreAsync();
                break;
            case "stop":
                await StopCoreAsync();
                break;
        }
    }

    private async Task UninstallCoreAsync()
    {
        try
        {
            _overview.SetBusy(true);
            _settings.SetBusy(true);
            var snapshot = await _core.UninstallAsync(_shutdown.Token);
            ApplyCoreSnapshot(snapshot);
            if (snapshot.Status == CoreStatus.Missing) _overview.ShowSuccess("Core uninstalled.");
            else _overview.ShowError(snapshot.Message);
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _overview.ShowError(exception.Message); }
        catch (Exception exception) { _overview.ShowError($"Core could not be uninstalled: {exception.Message}"); }
        finally { _overview.SetBusy(false); _settings.SetBusy(false); }
    }

    private async Task RunCoreActionAsync(Func<Task<CoreSnapshot>> action, string success)
    {
        try
        {
            _overview.SetBusy(true);
            _settings.SetBusy(true);
            var snapshot = await action();
            ApplyCoreSnapshot(snapshot);
            if (snapshot.Status == CoreStatus.Running) _overview.ShowSuccess(success);
            else _overview.ShowError(snapshot.Message);
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _overview.ShowError(exception.Message); }
        catch (Exception exception) { _overview.ShowError($"Core operation failed: {exception.Message}"); }
        finally { _overview.SetBusy(false); _settings.SetBusy(false); }
    }

    private void ApplyCoreSnapshot(CoreSnapshot snapshot)
    {
        _overview.SetSnapshot(snapshot);
        _settings.SetCoreSnapshot(snapshot);
        _plugins.SetCoreSnapshot(snapshot);
        _accounts.SetCoreSnapshot(snapshot);
        _tasks.SetCoreSnapshot(snapshot);
    }

    private async Task RefreshCoreReleasesAsync(string channel)
    {
        try
        {
            var catalog = await _launcher.LoadCoreCatalogAsync(channel, _shutdown.Token);
            _settings.SetCoreReleases(channel, catalog.Releases);
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _settings.ShowError(exception.Message); }
        catch (Exception exception) { _settings.ShowError($"Core releases could not be loaded: {exception.Message}"); }
    }

    private async Task LoadSettingsAsync()
    {
        try { _settings.SetSettings(await _launcher.LoadSettingsAsync(_shutdown.Token)); }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _settings.ShowError(exception.Message); }
        catch (Exception) { _settings.ShowError("Settings could not be loaded."); }
    }

    private async Task SaveSettingsAsync(BehaviorSettings settings)
    {
        try
        {
            _settings.SetBusy(true);
            await _launcher.SaveSettingsAsync(settings, _shutdown.Token);
            _settings.ShowSuccess("Settings saved.");
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _settings.ShowError(exception.Message); }
        catch (Exception) { _settings.ShowError("Settings could not be saved."); }
        finally { _settings.SetBusy(false); }
    }

    private async Task RefreshPluginsAsync()
    {
        try { _plugins.SetPlugins(await _launcher.ListPluginsAsync(_shutdown.Token)); }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _plugins.ShowError(exception.Message); }
        catch (Exception) { _plugins.ShowError("Plugins could not be loaded."); }
    }

    private async Task PluginOperationAsync(string id, string operation)
    {
        if (string.IsNullOrWhiteSpace(id)) return;
        if (!_plugins.IsCoreReady)
        {
            _plugins.ShowError("Start Core before managing plugins.");
            return;
        }
        var plugin = _plugins.FindPlugin(id);
        if (plugin is null)
        {
            _plugins.ShowError("The selected plugin is no longer available.");
            return;
        }
        if (operation == "enable" && !plugin.Trusted)
        {
            _plugins.ShowError("Trust the plugin signer before enabling this plugin.");
            return;
        }
        if (operation == "trust" && !await _plugins.ConfirmTrustAsync(id, true)) return;
        if (operation == "untrust" && !await _plugins.ConfirmTrustAsync(id, false)) return;
        if (operation == "remove" && !await _plugins.ConfirmRemoveAsync(id)) return;
        try
        {
            _plugins.SetBusy(true);
            switch (operation)
            {
                case "install": await _launcher.InstallPluginAsync(id, _shutdown.Token); break;
                case "trust": await _launcher.SetPluginTrustedAsync(id, true, _shutdown.Token); break;
                case "untrust": await _launcher.SetPluginTrustedAsync(id, false, _shutdown.Token); break;
                case "enable": await _launcher.SetPluginEnabledAsync(id, true, _shutdown.Token); break;
                case "disable": await _launcher.SetPluginEnabledAsync(id, false, _shutdown.Token); break;
                case "remove": await _launcher.RemovePluginAsync(id, _shutdown.Token); break;
            }
            await RefreshPluginsAsync();
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _plugins.ShowError(exception.Message); }
        catch (Exception) { _plugins.ShowError("Plugin operation failed."); }
        finally { _plugins.SetBusy(false); }
    }

    private async Task LookupAccountAsync(string accountID)
    {
        if (!_accounts.IsCoreReady)
        {
            _accounts.ShowError("Start Core before looking up an account.");
            return;
        }
        try
        {
            _accounts.SetBusy(true);
            using var client = await ConnectCoreAsync();
            var account = await client.GetAccountAsync(accountID, _shutdown.Token);
            _accounts.SetAccount(account);
            _accounts.ShowSuccess("Account status loaded.");
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (CoreApiException exception) { _accounts.ShowError(exception.Message); }
        catch (Exception) { _accounts.ShowError("Account status could not be loaded."); }
        finally { _accounts.SetBusy(false); }
    }

    private async Task SubmitTaskAsync(string accountID)
    {
        if (!_accounts.IsCoreReady)
        {
            _accounts.ShowError("Start Core before submitting a task.");
            return;
        }
        try
        {
            _accounts.SetBusy(true);
            using var client = await ConnectCoreAsync();
            var request = await client.SubmitRequestAsync(accountID, _shutdown.Token);
            _accounts.SetSubmittedRequest(request);
            _tasks.SetRequest(request);
            _accounts.ShowSuccess($"Task submitted: {request.RequestId}");
            SelectNavigation("tasks");
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (CoreApiException exception) { _accounts.ShowError(exception.Message); }
        catch (Exception) { _accounts.ShowError("Task could not be submitted."); }
        finally { _accounts.SetBusy(false); }
    }

    private async Task RefreshTaskAsync(string requestID)
    {
        if (!_tasks.IsCoreReady)
        {
            _tasks.ShowError("Start Core before checking task status.");
            return;
        }
        try
        {
            _tasks.SetBusy(true);
            using var client = await ConnectCoreAsync();
            var request = await client.GetRequestAsync(requestID, _shutdown.Token);
            _tasks.SetRequest(request);
            _tasks.ShowSuccess("Task status refreshed.");
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (CoreApiException exception) { _tasks.ShowError(exception.Message); }
        catch (Exception) { _tasks.ShowError("Task status could not be loaded."); }
        finally { _tasks.SetBusy(false); }
    }

    private async Task CancelTaskAsync(string requestID)
    {
        if (!_tasks.IsCoreReady)
        {
            _tasks.ShowError("Start Core before cancelling a task.");
            return;
        }
        try
        {
            _tasks.SetBusy(true);
            using var client = await ConnectCoreAsync();
            var request = await client.CancelRequestAsync(requestID, _shutdown.Token);
            _tasks.SetRequest(request);
            _tasks.ShowSuccess("Task cancellation requested.");
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (CoreApiException exception) { _tasks.ShowError(exception.Message); }
        catch (Exception) { _tasks.ShowError("Task could not be cancelled."); }
        finally { _tasks.SetBusy(false); }
    }

    private async Task<CoreApiClient> ConnectCoreAsync()
    {
        Exception? last = null;
        for (var attempt = 0; attempt < 12; attempt++)
        {
            var client = CoreApiClient.FromDataDirectory(_core.DataDirectory);
            try
            {
                await client.ConnectAsync(_shutdown.Token);
                return client;
            }
            catch (Exception exception) when (exception is CoreApiException or IOException or InvalidOperationException or TimeoutException)
            {
                client.Dispose();
                last = exception;
                if (attempt == 11) break;
                await Task.Delay(300, _shutdown.Token);
            }
        }
        throw last ?? new CoreApiException("unavailable", "Core service pipe could not be connected.");
    }

    private void SelectNavigation(string tag)
    {
        var item = Navigation.MenuItems
            .OfType<NavigationViewItem>()
            .Concat(Navigation.FooterMenuItems.OfType<NavigationViewItem>())
            .FirstOrDefault(candidate => string.Equals(candidate.Tag?.ToString(), tag, StringComparison.Ordinal));
        if (item is not null) Navigation.SelectedItem = item;
    }

    private void RunAsync(Func<Task> operation) => _ = operation();

    private void MainWindow_Closed(object sender, WindowEventArgs args)
    {
        _shutdown.Cancel();
        _core.Dispose();
        _shutdown.Dispose();
    }
}
