use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
pub(crate) struct CoreAccount {
    pub(crate) account: String,
    pub(crate) state: String,
    #[serde(default)]
    pub(crate) request_id: String,
    pub(crate) revision: u64,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreRequest {
    pub(crate) request_id: String,
    pub(crate) account: String,
    pub(crate) state: String,
    pub(crate) attempt: i32,
    #[serde(default)]
    pub(crate) last_failure: String,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreStatus {
    pub(crate) installed: bool,
    pub(crate) ready: bool,
    pub(crate) running: bool,
}

#[derive(Debug, Deserialize)]
pub(crate) struct SubmitResult {
    pub(crate) request: CoreRequest,
    pub(crate) idempotent: bool,
}

#[derive(Debug, Deserialize)]
pub(crate) struct BrowserView {
    pub(crate) request_id: String,
    pub(crate) content_type: String,
    pub(crate) width: u32,
    pub(crate) height: u32,
    pub(crate) data: String,
    pub(crate) captured_at: String,
}

#[derive(Debug, Deserialize)]
pub(crate) struct DiagnosticStatus {
    pub(crate) state: String,
}

#[derive(Debug, Deserialize)]
pub(crate) struct PluginDescriptor {
    pub(crate) id: String,
    pub(crate) version: String,
    #[serde(default)]
    pub(crate) api: String,
    #[serde(default)]
    pub(crate) target: String,
    #[serde(default)]
    pub(crate) capabilities: Vec<String>,
    #[serde(default)]
    pub(crate) permissions: Vec<String>,
    #[serde(default)]
    pub(crate) signed_by: String,
    #[serde(default)]
    pub(crate) installable: bool,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CorePlugin {
    pub(crate) descriptor: PluginDescriptor,
    pub(crate) installed: bool,
    pub(crate) enabled: bool,
    pub(crate) trusted: bool,
    pub(crate) health: String,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreComponent {
    pub(crate) id: String,
    pub(crate) installed: bool,
    #[serde(default)]
    pub(crate) version: String,
    pub(crate) enabled: bool,
    pub(crate) required: bool,
    pub(crate) health: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub(crate) struct BehaviorSettings {
    pub(crate) auto_check_updates: bool,
    pub(crate) auto_repair: bool,
    pub(crate) update_channel: String,
    pub(crate) launch_on_login: bool,
    pub(crate) close_to_tray: bool,
    pub(crate) check_interval: i64,
}

#[derive(Debug, Serialize, Deserialize)]
pub(crate) struct UiPreferences {
    #[serde(default = "default_theme")]
    pub(crate) theme: String,
}

pub(crate) fn default_theme() -> String {
    "system".to_owned()
}
