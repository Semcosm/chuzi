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

        _overview.InstallRequested += (_, _) => RunAsync(InstallCoreAsync);
        _overview.StartRequested += (_, _) => RunAsync(StartCoreAsync);
        _overview.StopRequested += (_, _) => RunAsync(StopCoreAsync);
        _overview.RefreshRequested += (_, _) => RunAsync(RefreshCoreAsync);
        _settings.SaveRequested += (_, settings) => RunAsync(() => SaveSettingsAsync(settings));
        _settings.InstallCoreRequested += (_, _) => RunAsync(InstallCoreAsync);
        _plugins.RefreshRequested += (_, _) => RunAsync(RefreshPluginsAsync);
        _plugins.InstallRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "install"));
        _plugins.TrustRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "trust"));
        _plugins.UntrustRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "untrust"));
        _plugins.EnableRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "enable"));
        _plugins.DisableRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "disable"));
        _plugins.RemoveRequested += (_, id) => RunAsync(() => PluginOperationAsync(id, "remove"));
        ContentFrame.Content = _overview;
        Activated += MainWindow_Activated;
        Closed += MainWindow_Closed;
    }

    private void MainWindow_Activated(object sender, WindowActivatedEventArgs args)
    {
        if (_loaded || args.WindowActivationState == WindowActivationState.Deactivated) return;
        _loaded = true;
        RunAsync(RefreshAllAsync);
    }

    private void OverviewButton_Click(object sender, RoutedEventArgs e) => ContentFrame.Content = _overview;
    private void SettingsButton_Click(object sender, RoutedEventArgs e) => ContentFrame.Content = _settings;
    private void PluginsButton_Click(object sender, RoutedEventArgs e) => ContentFrame.Content = _plugins;

    private async Task RefreshAllAsync()
    {
        await RefreshCoreAsync();
        await LoadSettingsAsync();
        await RefreshPluginsAsync();
    }

    private async Task RefreshCoreAsync()
    {
        try
        {
            var snapshot = await _core.GetStatusAsync(_shutdown.Token);
            _overview.SetSnapshot(snapshot);
            _settings.SetCoreSnapshot(snapshot);
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (Exception) { _overview.ShowError("Core status could not be read."); }
    }

    private async Task InstallCoreAsync()
    {
        await RunCoreActionAsync(() => _core.InstallAndStartAsync(_shutdown.Token), "Core installed and started.");
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
            var snapshot = await _core.StopAsync(_shutdown.Token);
            _overview.SetSnapshot(snapshot);
            _settings.SetCoreSnapshot(snapshot);
            _overview.ShowSuccess("Core stopped.");
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (Exception) { _overview.ShowError("Core could not be stopped."); }
        finally { _overview.SetBusy(false); }
    }

    private async Task RunCoreActionAsync(Func<Task<CoreSnapshot>> action, string success)
    {
        try
        {
            _overview.SetBusy(true);
            var snapshot = await action();
            _overview.SetSnapshot(snapshot);
            _settings.SetCoreSnapshot(snapshot);
            if (snapshot.Status == CoreStatus.Running) _overview.ShowSuccess(success);
            else _overview.ShowError(snapshot.Message);
        }
        catch (OperationCanceledException) when (_shutdown.IsCancellationRequested) { }
        catch (LauncherException exception) { _overview.ShowError(exception.Message); }
        catch (Exception) { _overview.ShowError("Core operation failed."); }
        finally { _overview.SetBusy(false); }
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

    private void RunAsync(Func<Task> operation) => _ = operation();

    private void MainWindow_Closed(object sender, WindowEventArgs args)
    {
        _shutdown.Cancel();
        _core.Dispose();
        _shutdown.Dispose();
    }
}
