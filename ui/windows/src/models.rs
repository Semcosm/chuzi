use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
pub(crate) struct CoreRequest {
    pub(crate) request_id: String,
    pub(crate) account: String,
    pub(crate) state: String,
    pub(crate) attempt: i32,
    #[serde(default)]
    pub(crate) last_failure: String,
    #[serde(default)]
    pub(crate) created_at: String,
    #[serde(default)]
    pub(crate) updated_at: String,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreRequestList {
    pub(crate) requests: Vec<CoreRequest>,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreStatus {
    pub(crate) installed: bool,
    pub(crate) ready: bool,
    pub(crate) running: bool,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreJobPoolList {
    pub(crate) job_pools: Vec<CoreJobPool>,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreJobPool {
    pub(crate) config: CoreJobPoolConfig,
    pub(crate) status: CoreJobPoolStatus,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreJobPoolConfig {
    pub(crate) pool_id: String,
    #[serde(default)]
    pub(crate) environment_version: String,
}

#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct CoreJobPoolStatus {
    pub(crate) desired: i32,
    pub(crate) ready: i32,
    pub(crate) leased: i32,
    pub(crate) quarantined: i32,
    pub(crate) draining: i32,
    pub(crate) provisioning: i32,
    pub(crate) retiring: i32,
    pub(crate) effective_capacity: i32,
    #[serde(default)]
    pub(crate) environment_readiness: String,
    #[serde(default)]
    pub(crate) reconcile_state: String,
    #[serde(default)]
    pub(crate) last_failure_code: String,
}

#[cfg(test)]
mod tests {
    use super::CoreJobPoolStatus;

    #[test]
    fn job_pool_status_keeps_failure_classification_redacted() {
        let status: CoreJobPoolStatus = serde_json::from_str(
            r#"{"desired":2,"ready":1,"leased":1,"quarantined":0,"draining":0,"provisioning":1,"retiring":0,"effective_capacity":1,"reconcile_state":"failed","last_failure_code":"package_unavailable","password":"secret"}"#,
        )
        .expect("job pool projection should decode");
        assert_eq!(status.last_failure_code, "package_unavailable");
        let encoded = serde_json::to_string(&status).expect("status should encode");
        assert!(!encoded.contains("password"));
        assert!(!encoded.contains("secret"));
    }
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

#[derive(Debug, Default, Serialize, Deserialize)]
pub(crate) struct BehaviorSettings {
    pub(crate) auto_check_updates: bool,
    pub(crate) auto_repair: bool,
    pub(crate) update_channel: String,
    pub(crate) launch_on_login: bool,
    pub(crate) close_to_tray: bool,
    #[serde(default)]
    pub(crate) start_core_on_launch: bool,
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
