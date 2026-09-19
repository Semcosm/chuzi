using System.Text.Json.Serialization;

namespace Chuzi.Native.Windows;

public sealed class BehaviorSettings
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

public sealed class ComponentState
{
    [JsonPropertyName("id")] public string ID { get; set; } = "";
    [JsonPropertyName("installed")] public bool Installed { get; set; }
    [JsonPropertyName("version")] public string Version { get; set; } = "";
    [JsonPropertyName("enabled")] public bool Enabled { get; set; }
    [JsonPropertyName("required")] public bool Required { get; set; }
    [JsonPropertyName("health")] public string Health { get; set; } = "";
}

public sealed class CoreReleaseCatalog
{
    [JsonPropertyName("format")] public string Format { get; set; } = "chuzi-release-catalog/v1";
    [JsonPropertyName("channel")] public string Channel { get; set; } = "test";
    [JsonPropertyName("target")] public string Target { get; set; } = "windows-amd64";
    [JsonPropertyName("releases")] public List<CoreRelease> Releases { get; set; } = [];
}

public sealed class CoreRelease
{
    [JsonPropertyName("channel")] public string Channel { get; set; } = "test";
    [JsonPropertyName("version")] public string Version { get; set; } = "";
    [JsonPropertyName("commit")] public string Commit { get; set; } = "";
    [JsonPropertyName("target")] public string Target { get; set; } = "windows-amd64";
    [JsonPropertyName("index_url")] public string IndexUrl { get; set; } = "";
    [JsonPropertyName("published_at")] public DateTimeOffset? PublishedAt { get; set; }
    [JsonPropertyName("prerelease")] public bool Prerelease { get; set; }

    [JsonIgnore]
    public string DisplayName
    {
        get
        {
            var shortCommit = string.IsNullOrWhiteSpace(Commit)
                ? ""
                : $" · {Commit[..Math.Min(12, Commit.Length)]}";
            return $"{Version}{shortCommit}";
        }
    }
}

public sealed class PluginDescriptor
{
    [JsonPropertyName("id")] public string ID { get; set; } = "";
    [JsonPropertyName("version")] public string Version { get; set; } = "";
    [JsonPropertyName("api")] public string API { get; set; } = "";
    [JsonPropertyName("signed_by")] public string SignedBy { get; set; } = "";
    [JsonPropertyName("capabilities")] public List<string> Capabilities { get; set; } = [];
    [JsonPropertyName("permissions")] public List<string> Permissions { get; set; } = [];
    [JsonPropertyName("installable")] public bool Installable { get; set; }

    [JsonIgnore]
    public string SignedByOrUnsigned => string.IsNullOrWhiteSpace(SignedBy) ? "Unsigned" : $"Signed by: {SignedBy}";

    [JsonIgnore]
    public string PermissionsSummary => Permissions.Count == 0
        ? "Permissions: none declared"
        : $"Permissions: {string.Join(", ", Permissions)}";

    [JsonIgnore]
    public string CapabilitiesSummary => Capabilities.Count == 0
        ? "Capabilities: none declared"
        : $"Capabilities: {string.Join(", ", Capabilities)}";
}

public sealed class PluginState
{
    [JsonPropertyName("descriptor")] public PluginDescriptor Descriptor { get; set; } = new();
    [JsonPropertyName("installed")] public bool Installed { get; set; }
    [JsonPropertyName("enabled")] public bool Enabled { get; set; }
    [JsonPropertyName("trusted")] public bool Trusted { get; set; }
    [JsonPropertyName("health")] public string Health { get; set; } = "";

    [JsonIgnore]
    public string TrustSummary => Trusted ? "Trusted" : "Not trusted — trust the signer before enabling";
}
