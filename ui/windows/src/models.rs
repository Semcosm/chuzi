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
    #[serde(default)]
    pub(crate) status: String,
    #[serde(default)]
    pub(crate) protocol: String,
    #[serde(default)]
    pub(crate) capability_status: String,
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
    pub(crate) desired_slots: i32,
    #[serde(default)]
    pub(crate) max_concurrency: i32,
    #[serde(default)]
    pub(crate) environment_id: String,
    #[serde(default)]
    pub(crate) environment_version: String,
    #[serde(default)]
    pub(crate) manifest_digest: String,
    #[serde(default)]
    pub(crate) signer: String,
    #[serde(default)]
    pub(crate) require_trusted: bool,
    #[serde(default)]
    pub(crate) desired_state: String,
    #[serde(default)]
    pub(crate) enabled: bool,
    #[serde(default)]
    pub(crate) config_revision: u64,
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
    #[serde(default)]
    pub(crate) operation_id: String,
    #[serde(default)]
    pub(crate) config_revision: u64,
    #[serde(default)]
    pub(crate) environment_ready: bool,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreJobPoolOperation {
    pub(crate) operation_id: String,
    #[serde(default)]
    pub(crate) pool_id: String,
    #[serde(default)]
    pub(crate) operation: String,
    #[serde(default)]
    pub(crate) state: String,
    #[serde(default)]
    pub(crate) config_revision: u64,
    #[serde(default)]
    pub(crate) result: String,
    #[serde(default)]
    pub(crate) failure_code: String,
    #[serde(default)]
    pub(crate) idempotent: bool,
}

#[derive(Debug, Default, Deserialize, Serialize)]
pub(crate) struct CoreSlotSessionStatus {
    #[serde(default)]
    pub(crate) pool_id: String,
    #[serde(default)]
    pub(crate) slot_id: String,
    #[serde(default)]
    pub(crate) ordinal: i32,
    #[serde(default)]
    pub(crate) status: String,
    #[serde(default)]
    pub(crate) environment_generation: u64,
    #[serde(default)]
    pub(crate) session_state: String,
    #[serde(default)]
    pub(crate) agent_ready: bool,
}

#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct CoreSlotSessionOperation {
    #[serde(default)]
    pub(crate) operation_id: String,
    #[serde(default)]
    pub(crate) pool_id: String,
    #[serde(default)]
    pub(crate) slot_id: String,
    #[serde(default)]
    pub(crate) ordinal: i32,
    #[serde(default)]
    pub(crate) state: String,
    #[serde(default)]
    pub(crate) failure_code: String,
    #[serde(default)]
    pub(crate) environment_generation: u64,
    #[serde(default)]
    pub(crate) status: CoreSlotSessionStatus,
    #[serde(default)]
    pub(crate) idempotent: bool,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreEnvironmentList {
    pub(crate) environments: Vec<CoreEnvironment>,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreEnvironment {
    pub(crate) environment_id: String,
    pub(crate) version: String,
    #[serde(default)]
    pub(crate) installed: bool,
    #[serde(default)]
    pub(crate) verified: bool,
    #[serde(default)]
    pub(crate) trusted: bool,
    #[serde(default)]
    pub(crate) enabled: bool,
    #[serde(default)]
    pub(crate) healthy: bool,
    #[serde(default)]
    pub(crate) ready: bool,
    #[serde(default)]
    pub(crate) generation: u64,
}

#[derive(Debug, Deserialize)]
pub(crate) struct CoreEnvironmentOperation {
    pub(crate) operation_id: String,
    #[serde(default)]
    pub(crate) environment_id: String,
    #[serde(default)]
    pub(crate) version: String,
    #[serde(default)]
    pub(crate) operation: String,
    #[serde(default)]
    pub(crate) state: String,
    #[serde(default)]
    pub(crate) failure_code: String,
    #[serde(default)]
    pub(crate) idempotent: bool,
}

#[cfg(test)]
mod tests {
    use super::{CoreJobPoolStatus, CoreSlotSessionOperation};

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

    #[test]
    fn slot_session_operation_decodes_nested_status_without_sensitive_fields() {
        let operation: CoreSlotSessionOperation = serde_json::from_str(
            r#"{
                "operation_id":"slotop-1",
                "pool_id":"pool-a",
                "slot_id":"pool-a-001",
                "ordinal":1,
                "state":"ready",
                "environment_generation":7,
                "status":{
                    "pool_id":"pool-a",
                    "slot_id":"pool-a-001",
                    "ordinal":1,
                    "status":"ready",
                    "environment_generation":7,
                    "session_state":"ready",
                    "agent_ready":true
                },
                "idempotent":true,
                "password":"secret",
                "profile_path":"private"
            }"#,
        )
        .expect("slot-session operation should decode");
        assert_eq!(operation.operation_id, "slotop-1");
        assert_eq!(operation.status.session_state, "ready");
        assert!(operation.status.agent_ready);
        assert!(operation.idempotent);
        let encoded = serde_json::to_string(&operation).expect("projection should encode");
        assert!(!encoded.contains("password"));
        assert!(!encoded.contains("secret"));
        assert!(!encoded.contains("profile_path"));
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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub(crate) struct DiagnosticEvent {
    #[serde(default)]
    pub(crate) at: String,
    #[serde(default)]
    pub(crate) component: String,
    #[serde(default)]
    pub(crate) operation: String,
    #[serde(default)]
    pub(crate) outcome: String,
    #[serde(default)]
    pub(crate) request_id: String,
    #[serde(default)]
    pub(crate) resource: String,
    #[serde(default)]
    pub(crate) error_class: String,
    #[serde(default)]
    pub(crate) duration_ms: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub(crate) struct DiagnosticCoreStatus {
    #[serde(default)]
    pub(crate) installed: bool,
    #[serde(default)]
    pub(crate) running: bool,
    #[serde(default)]
    pub(crate) ready: bool,
    #[serde(default)]
    pub(crate) status: String,
    #[serde(default)]
    pub(crate) protocol: String,
    #[serde(default)]
    pub(crate) capability_status: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub(crate) struct DiagnosticSnapshot {
    #[serde(default)]
    pub(crate) schema: String,
    pub(crate) id: String,
    pub(crate) created_at: String,
    #[serde(default)]
    pub(crate) source: String,
    pub(crate) version: String,
    pub(crate) platform: String,
    pub(crate) arch: String,
    pub(crate) severity: String,
    pub(crate) category: String,
    pub(crate) summary: String,
    #[serde(default)]
    pub(crate) error_class: String,
    #[serde(default)]
    pub(crate) operation: String,
    #[serde(default)]
    pub(crate) event_count: usize,
    #[serde(default)]
    pub(crate) events_truncated: bool,
    #[serde(default)]
    pub(crate) capture_error_class: String,
    #[serde(default)]
    pub(crate) capture_error_code: String,
    #[serde(default)]
    pub(crate) capture_error_size: usize,
    #[serde(default)]
    pub(crate) capture_error_fingerprint: String,
    #[serde(default)]
    pub(crate) capture_attempts: usize,
    #[serde(default)]
    pub(crate) legacy_retry_attempted: bool,
    #[serde(default)]
    pub(crate) capture_request_shape: String,
    #[serde(default)]
    pub(crate) capture_initial_error_class: String,
    #[serde(default)]
    pub(crate) capture_initial_error_code: String,
    #[serde(default)]
    pub(crate) capture_initial_error_size: usize,
    #[serde(default)]
    pub(crate) capture_initial_error_fingerprint: String,
    #[serde(default)]
    pub(crate) capture_stage: String,
    #[serde(default)]
    pub(crate) core_schema: String,
    #[serde(default)]
    pub(crate) core_version: String,
    #[serde(default)]
    pub(crate) core_status_error_class: String,
    #[serde(default)]
    pub(crate) core_capability_status: String,
    #[serde(default)]
    pub(crate) core_capability_error_code: String,
    #[serde(default)]
    pub(crate) core_capability_error_fingerprint: String,
    #[serde(default)]
    pub(crate) core_protocol_version: String,
    #[serde(default)]
    pub(crate) core_method_supported: Option<bool>,
    #[serde(default)]
    pub(crate) core_supported_methods: Vec<String>,
    #[serde(default)]
    pub(crate) response_kind: String,
    #[serde(default)]
    pub(crate) response_size: usize,
    #[serde(default)]
    pub(crate) response_key_count: usize,
    #[serde(default)]
    pub(crate) response_fields: Vec<String>,
    #[serde(default)]
    pub(crate) response_fingerprint: String,
    #[serde(default)]
    pub(crate) capture_duration_ms: u64,
    #[serde(default)]
    pub(crate) client_version: String,
    #[serde(default)]
    pub(crate) core_status: DiagnosticCoreStatus,
    #[serde(default)]
    pub(crate) events: Vec<DiagnosticEvent>,
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
