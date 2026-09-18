using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;

namespace Chuzi.Native.Windows;

public static class Program
{
    [STAThread]
    public static void Main(string[] args)
    {
        UiDiagnostics.Log("Program.Main: begin");
        WinRT.ComWrappersSupport.InitializeComWrappers();
        UiDiagnostics.Log("Program.Main: COM wrappers initialized");
        Application.Start((_) =>
        {
            UiDiagnostics.Log("Application.Start callback: begin");
            var context = new DispatcherQueueSynchronizationContext(DispatcherQueue.GetForCurrentThread());
            SynchronizationContext.SetSynchronizationContext(context);
            UiDiagnostics.Log("Application.Start callback: synchronization context set");
            new App();
            UiDiagnostics.Log("Application.Start callback: App constructed");
        });
        UiDiagnostics.Log("Program.Main: Application.Start returned");
    }
}
