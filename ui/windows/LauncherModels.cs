using System.Text.Json.Serialization;

namespace Chuzi.Native.Windows;

internal sealed class BehaviorSettings
{
    [JsonPropertyName("auto_check_updates")] public bool AutoCheckUpdates { get; set; }
    [JsonPropertyName("auto_repair")] public bool AutoRepair { get; set; }
    [JsonPropertyName("update_channel")] public string UpdateChannel { get; set; } = "nightly";
    [JsonPropertyName("launch_on_login")] public bool LaunchOnLogin { get; set; }
    [JsonPropertyName("close_to_tray")] public bool CloseToTray { get; set; }
    [JsonPropertyName("check_interval")] public long CheckIntervalNanoseconds { get; set; }

    public double CheckIntervalMinutes
    {
        get => CheckIntervalNanoseconds / 60_000_000_000d;
        set => CheckIntervalNanoseconds = checked((long)Math.Round(Math.Max(0, value) * 60_000_000_000d));
    }
}

internal sealed class ComponentState
{
    [JsonPropertyName("id")] public string ID { get; set; } = "";
    [JsonPropertyName("installed")] public bool Installed { get; set; }
    [JsonPropertyName("version")] public string Version { get; set; } = "";
    [JsonPropertyName("enabled")] public bool Enabled { get; set; }
    [JsonPropertyName("required")] public bool Required { get; set; }
    [JsonPropertyName("health")] public string Health { get; set; } = "";
}

internal sealed class PluginDescriptor
{
    [JsonPropertyName("id")] public string ID { get; set; } = "";
    [JsonPropertyName("version")] public string Version { get; set; } = "";
    [JsonPropertyName("api")] public string API { get; set; } = "";
    [JsonPropertyName("signed_by")] public string SignedBy { get; set; } = "";
    [JsonPropertyName("capabilities")] public List<string> Capabilities { get; set; } = [];
    [JsonPropertyName("permissions")] public List<string> Permissions { get; set; } = [];
    [JsonPropertyName("installable")] public bool Installable { get; set; }
}

internal sealed class PluginState
{
    [JsonPropertyName("descriptor")] public PluginDescriptor Descriptor { get; set; } = new();
    [JsonPropertyName("installed")] public bool Installed { get; set; }
    [JsonPropertyName("enabled")] public bool Enabled { get; set; }
    [JsonPropertyName("trusted")] public bool Trusted { get; set; }
    [JsonPropertyName("health")] public string Health { get; set; } = "";
}
