mod desktop_rdp;
mod models;
mod session_ui;
mod view_model;

use base64::Engine;
use desktop_rdp::RdpHost;
use models::{
    default_theme, BehaviorSettings, BrowserView, CoreEnvironment, CoreEnvironmentList,
    CoreEnvironmentOperation, CoreJobPool, CoreJobPoolList, CoreJobPoolOperation, CoreRequest,
    CoreRequestList, CoreSlotSessionOperation, CoreStatus, DiagnosticCoreStatus, DiagnosticEvent,
    DiagnosticSnapshot, UiPreferences,
};
use serde_json::{json, Value};
use slint::language::ColorScheme;
use slint::{ComponentHandle, Image, Model, ModelRc, SharedString};
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use view_model::{
    sessions_from_requests, ProjectionError, SelectionMove, SessionFilter, SessionViewModel,
};

slint::include_modules!();

#[derive(Default)]
struct AppState {
    data_root: PathBuf,
    payload_root: PathBuf,
    release_index_url: Option<String>,
    busy: bool,
    slot_session_retry: Mutex<Option<(String, bool)>>,
    settings: BehaviorSettings,
    session_view_model: SessionViewModel,
    workspace_host: RdpHost,
    diagnostic_error_class: String,
    diagnostic_operation: String,
}

const CORE_PROTOCOL_VERSION: &str = "chuzi.core/v1";
const DIAGNOSTIC_METHOD: &str = "get_diagnostic_snapshot";

#[derive(Debug, Default)]
struct DiagnosticCapabilities {
    status: String,
    error_code: String,
    error_fingerprint: String,
    protocol_version: String,
    method_supported: Option<bool>,
    supported_methods: Vec<String>,
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let smoke_test = std::env::args().any(|argument| argument == "--smoke-test");
    if smoke_test {
        return Ok(());
    }
    let ui = MainWindow::new()?;
    let state = Arc::new(Mutex::new(AppState::new()?));
    let initial_theme = state.lock().unwrap().load_ui_theme();
    apply_theme(&ui, &initial_theme);
    ui.set_theme(initial_theme.into());

    connect_callbacks(&ui, Arc::clone(&state));
    load_launcher_settings(&ui, Arc::clone(&state));
    ui.run()?;
    Ok(())
}

impl AppState {
    fn new() -> Result<Self, Box<dyn std::error::Error>> {
        let package_root = std::env::current_exe()?
            .parent()
            .map(Path::to_path_buf)
            .ok_or("executable has no parent directory")?;
        let data_root = if let Some(value) = std::env::var_os("CHUZI_DATA_DIR") {
            PathBuf::from(value)
        } else if cfg!(windows) {
            PathBuf::from(
                std::env::var_os("ProgramData").unwrap_or_else(|| "C:\\ProgramData".into()),
            )
            .join("chuzi")
            .join("data")
        } else {
            package_root.join("data")
        };
        Ok(Self {
            data_root,
            payload_root: package_root.join("CorePayload"),
            release_index_url: std::env::var("CHUZI_RELEASE_INDEX_URL")
                .ok()
                .map(|value| value.trim().to_owned())
                .filter(|value| !value.is_empty()),
            busy: false,
            slot_session_retry: Mutex::new(None),
            settings: BehaviorSettings {
                auto_check_updates: false,
                auto_repair: false,
                update_channel: "nightly".to_owned(),
                launch_on_login: false,
                close_to_tray: false,
                start_core_on_launch: false,
                check_interval: 60 * 60 * 1_000_000_000,
            },
            session_view_model: SessionViewModel::default(),
            workspace_host: RdpHost::Docked,
            diagnostic_error_class: "".to_owned(),
            diagnostic_operation: "".to_owned(),
        })
    }

    fn launcher_path(&self) -> PathBuf {
        self.payload_root.join(if cfg!(windows) {
            "chuzi-launcher.exe"
        } else {
            "chuzi-launcher"
        })
    }

    fn manifest_path(&self) -> PathBuf {
        self.payload_root.join("release-manifest.json")
    }

    fn ui_preferences_path(&self) -> PathBuf {
        self.data_root.join(".chuzi-ui-settings.json")
    }

    fn local_diagnostics_dir(&self) -> PathBuf {
        if cfg!(windows) {
            if let Some(root) = std::env::var_os("LOCALAPPDATA") {
                return PathBuf::from(root).join("Chuzi").join("diagnostics");
            }
        } else if let Some(root) = std::env::var_os("XDG_STATE_HOME") {
            return PathBuf::from(root).join("chuzi").join("diagnostics");
        }
        self.data_root.join("diagnostics")
    }

    fn save_diagnostic_snapshot(&self, snapshot: &DiagnosticSnapshot) -> Result<PathBuf, String> {
        let mut id = snapshot
            .id
            .chars()
            .filter(|value| value.is_ascii_alphanumeric() || matches!(value, '-' | '_'))
            .take(80)
            .collect::<String>();
        if id.is_empty() {
            id = format!(
                "local-{}",
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .map_err(|error| error.to_string())?
                    .as_millis()
            );
        }
        let directory = self.local_diagnostics_dir();
        fs::create_dir_all(&directory).map_err(|error| error.to_string())?;
        let path = directory.join(format!("diagnostic-{id}.json"));
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_err(|error| error.to_string())?
            .as_nanos();
        let temporary = directory.join(format!(".diagnostic-{id}-{nonce}.tmp"));
        let bytes = serde_json::to_vec_pretty(snapshot).map_err(|error| error.to_string())?;
        if bytes.len() > 256 * 1024 {
            return Err("diagnostic_snapshot_too_large".to_owned());
        }
        let write_result = (|| {
            let mut file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&temporary)
                .map_err(|error| error.to_string())?;
            file.write_all(&bytes).map_err(|error| error.to_string())?;
            file.flush().map_err(|error| error.to_string())?;
            Ok::<(), String>(())
        })();
        if let Err(error) = write_result {
            let _ = fs::remove_file(&temporary);
            return Err(error);
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            if let Err(error) = fs::set_permissions(&temporary, fs::Permissions::from_mode(0o600)) {
                let _ = fs::remove_file(&temporary);
                return Err(error.to_string());
            }
        }
        if let Err(error) = fs::rename(&temporary, &path) {
            let _ = fs::remove_file(&temporary);
            return Err(error.to_string());
        }
        Ok(path)
    }

    fn load_ui_theme(&self) -> String {
        let path = self.ui_preferences_path();
        let Ok(data) = fs::read(path) else {
            return default_theme();
        };
        let Ok(preferences) = serde_json::from_slice::<UiPreferences>(&data) else {
            return default_theme();
        };
        normalize_theme(&preferences.theme).to_owned()
    }

    fn save_ui_theme(&self, theme: &str) -> Result<(), String> {
        fs::create_dir_all(&self.data_root).map_err(|error| error.to_string())?;
        let preferences = UiPreferences {
            theme: normalize_theme(theme).to_owned(),
        };
        fs::write(
            self.ui_preferences_path(),
            serde_json::to_vec_pretty(&preferences).map_err(|error| error.to_string())?,
        )
        .map_err(|error| error.to_string())
    }

    fn run_launcher(&self, command: &str, args: &[&str]) -> Result<String, String> {
        if !self.launcher_path().is_file() {
            return Err(format!(
                "Core launcher is missing: {}",
                self.launcher_path().display()
            ));
        }
        if launcher_command_needs_manifest(command) && !self.manifest_path().is_file() {
            return Err(format!(
                "Core release manifest is missing: {}",
                self.manifest_path().display()
            ));
        }
        fs::create_dir_all(&self.data_root).map_err(|error| error.to_string())?;
        let mut launcher = Command::new(self.launcher_path());
        launcher
            .arg("-root")
            .arg(&self.data_root)
            .arg("-source-root")
            .arg(&self.payload_root);
        if self.manifest_path().is_file() {
            launcher.arg("-manifest").arg(self.manifest_path());
        }
        if command.starts_with("component-") && self.release_index_url.is_some() {
            launcher.args(
                self.release_index_url
                    .as_ref()
                    .into_iter()
                    .flat_map(|url| ["-release-index", url.as_str()]),
            );
        }
        let output = launcher
            .arg("-command")
            .arg(command)
            .args(args)
            .output()
            .map_err(|error| error.to_string())?;
        if !output.status.success() {
            return Err(String::from_utf8_lossy(&output.stderr).trim().to_string());
        }
        Ok(String::from_utf8_lossy(&output.stdout).to_string())
    }

    fn diagnostic_context(&self) -> (DiagnosticCoreStatus, String) {
        match core_status(self) {
            Ok(status) => (
                DiagnosticCoreStatus {
                    installed: status.installed,
                    running: status.running,
                    ready: status.ready,
                    status: if status.status.trim().is_empty() {
                        if status.ready {
                            "ready"
                        } else if status.running {
                            "running"
                        } else {
                            "stopped"
                        }
                    } else {
                        status.status.as_str()
                    }
                    .to_owned(),
                    protocol: status.protocol,
                    capability_status: status.capability_status,
                },
                String::new(),
            ),
            Err(error) => (
                DiagnosticCoreStatus {
                    status: "unknown".to_owned(),
                    ..Default::default()
                },
                diagnostic_capture_error_class(&error),
            ),
        }
    }
}

fn launcher_command_needs_manifest(command: &str) -> bool {
    !matches!(
        command,
        "core-status"
            | "core-start"
            | "core-stop"
            | "core-call"
            | "core-pool-mode-save"
            | "job-pool-list"
            | "job-pool-get"
            | "job-pool-apply"
            | "job-pool-scale"
            | "job-pool-drain"
            | "job-pool-resume"
            | "job-pool-delete"
            | "job-pool-operation"
            | "environment-list"
            | "environment-install"
            | "environment-upgrade"
            | "environment-verify"
            | "environment-trust"
            | "environment-enable"
            | "environment-disable"
            | "environment-health"
            | "environment-rollback"
            | "environment-operation"
    )
}

#[derive(Clone, Copy)]
enum CoreMethod {
    SubmitDiagnosticReport,
    GetDiagnosticSnapshot,
    ListJobPools,
    ApplyJobPool,
    ScaleJobPool,
    DrainJobPool,
    ResumeJobPool,
    DeleteJobPool,
    GetJobPoolOperation,
    StartSlotSession,
    GetSlotSessionOperation,
    ListEnvironments,
    EnvironmentOperation,
    GetEnvironmentOperation,
    CancelRequest,
    GetRequest,
    GetBrowserView,
    ListRequests,
}

impl CoreMethod {
    fn wire_name(self) -> &'static str {
        match self {
            Self::SubmitDiagnosticReport => "submit_diagnostic_report",
            Self::GetDiagnosticSnapshot => "get_diagnostic_snapshot",
            Self::ListJobPools => "list_job_pools",
            Self::ApplyJobPool => "apply_job_pool",
            Self::ScaleJobPool => "scale_job_pool",
            Self::DrainJobPool => "drain_job_pool",
            Self::ResumeJobPool => "resume_job_pool",
            Self::DeleteJobPool => "delete_job_pool",
            Self::GetJobPoolOperation => "get_job_pool_operation",
            Self::StartSlotSession => "start_slot_session",
            Self::GetSlotSessionOperation => "get_slot_session_operation",
            Self::ListEnvironments => "list_environments",
            Self::EnvironmentOperation => "environment_operation",
            Self::GetEnvironmentOperation => "get_environment_operation",
            Self::CancelRequest => "cancel_request",
            Self::GetRequest => "get_request",
            Self::GetBrowserView => "get_browser_view",
            Self::ListRequests => "list_requests",
        }
    }
}

fn core_call(state: &AppState, method: CoreMethod, params: Value) -> Result<Value, String> {
    let params_json = serde_json::to_string(&params).map_err(|error| error.to_string())?;
    let output = state.run_launcher(
        "core-call",
        &[
            "-core-method",
            method.wire_name(),
            "-core-params-json",
            params_json.as_str(),
        ],
    )?;
    serde_json::from_str(output.trim()).map_err(|error| format!("Core projection: {error}"))
}

fn core_status(state: &AppState) -> Result<CoreStatus, String> {
    let output = state.run_launcher("core-status", &[])?;
    serde_json::from_str(output.trim()).map_err(|error| format!("Core status: {error}"))
}

fn connect_callbacks(ui: &MainWindow, state: Arc<Mutex<AppState>>) {
    let navigate_weak = ui.as_weak();
    ui.on_navigate(move |page| {
        let page = if page.as_str() == "settings" {
            "settings"
        } else {
            "sessions"
        };
        if let Some(window) = navigate_weak.upgrade() {
            // Navigation is a presentation concern; launcher settings are
            // loaded once and saved explicitly from the Settings page.
            window.set_page(page.into());
            window.set_inspector_open(false);
        }
    });

    let weak = ui.as_weak();
    let install_state = Arc::clone(&state);
    ui.on_install_core(move || {
        run_background_status(
            &weak,
            Arc::clone(&install_state),
            |state| {
                state.run_launcher("component-install", &["-item", "service"])?;
                state.run_launcher("core-start", &[])?;
                Ok("Core installed and started.".to_owned())
            },
            Some(true),
        )
    });

    let weak = ui.as_weak();
    let start_state = Arc::clone(&state);
    ui.on_start_core(move || {
        run_background_status(
            &weak,
            Arc::clone(&start_state),
            |state| {
                state.run_launcher("core-start", &[])?;
                Ok("Core started.".to_owned())
            },
            Some(true),
        )
    });

    let weak = ui.as_weak();
    let stop_state = Arc::clone(&state);
    ui.on_stop_core(move || {
        run_background_status(
            &weak,
            Arc::clone(&stop_state),
            |state| {
                state.run_launcher("core-stop", &[])?;
                Ok("Core stopped.".to_owned())
            },
            Some(false),
        )
    });

    let weak = ui.as_weak();
    let refresh_state = Arc::clone(&state);
    ui.on_refresh_core(move || refresh_core(&weak, Arc::clone(&refresh_state)));

    let weak = ui.as_weak();
    let pool_state = Arc::clone(&state);
    ui.on_refresh_job_pools(move || refresh_job_pools(&weak, Arc::clone(&pool_state)));

    let weak = ui.as_weak();
    let operation_state = Arc::clone(&state);
    ui.on_job_pool_apply(
        move |pool_id, environment_id, environment_version, desired, max| {
            submit_job_pool_apply(
                &weak,
                Arc::clone(&operation_state),
                pool_id.to_string(),
                environment_id.to_string(),
                environment_version.to_string(),
                desired,
                max,
            );
        },
    );
    let weak = ui.as_weak();
    let operation_state = Arc::clone(&state);
    ui.on_job_pool_action(move |pool_id, action| {
        submit_job_pool_action(
            &weak,
            Arc::clone(&operation_state),
            pool_id.to_string(),
            action.to_string(),
        );
    });
    let weak = ui.as_weak();
    let operation_state = Arc::clone(&state);
    ui.on_job_pool_operation(move |operation_id| {
        poll_job_pool_operation(
            &weak,
            Arc::clone(&operation_state),
            operation_id.to_string(),
        );
    });
    let weak = ui.as_weak();
    let operation_state = Arc::clone(&state);
    ui.on_refresh_operation(move |operation_id| {
        if weak
            .upgrade()
            .is_some_and(|window| window.get_operation_kind() == "environment")
        {
            poll_environment_operation(
                &weak,
                Arc::clone(&operation_state),
                operation_id.to_string(),
            );
            return;
        }
        let is_slot_session = weak
            .upgrade()
            .map(|window| window.get_operation_kind() == "slot-session")
            .unwrap_or(false);
        if is_slot_session {
            poll_slot_session_operation(
                &weak,
                Arc::clone(&operation_state),
                operation_id.to_string(),
            );
        } else {
            poll_job_pool_operation(
                &weak,
                Arc::clone(&operation_state),
                operation_id.to_string(),
            );
        }
    });
    let weak = ui.as_weak();
    let operation_state = Arc::clone(&state);
    ui.on_environment_operation(move |environment_id, version, operation, package_ref| {
        submit_environment_operation(
            &weak,
            Arc::clone(&operation_state),
            environment_id.to_string(),
            version.to_string(),
            operation.to_string(),
            package_ref.to_string(),
            0,
        );
    });
    let weak = ui.as_weak();
    let operation_state = Arc::clone(&state);
    ui.on_environment_operation_poll(move |operation_id| {
        poll_environment_operation(
            &weak,
            Arc::clone(&operation_state),
            operation_id.to_string(),
        );
    });
    let weak = ui.as_weak();
    let operation_state = Arc::clone(&state);
    ui.on_operation_confirmed(move || {
        let Some(window) = weak.upgrade() else {
            return;
        };
        let pending = window.get_pending_operation().to_string();
        let pool_id = window.get_pending_pool_id().to_string();
        let environment_id = window.get_pending_environment_id().to_string();
        let environment_version = window.get_pending_environment_version().to_string();
        let environment_generation = window
            .get_pending_environment_generation()
            .parse::<u64>()
            .unwrap_or(0);
        window.set_operation_confirmation_visible(false);
        match pending.as_str() {
            "pool-mode:windows" | "pool-mode:logical" => submit_pool_mode(
                &weak,
                Arc::clone(&operation_state),
                pending.trim_start_matches("pool-mode:").to_owned(),
                pool_id,
            ),
            "apply" => submit_job_pool_apply(
                &weak,
                Arc::clone(&operation_state),
                window.get_job_pool_edit_id().to_string(),
                window.get_job_pool_edit_environment_id().to_string(),
                window.get_job_pool_edit_environment_version().to_string(),
                window.get_job_pool_edit_desired(),
                window.get_job_pool_edit_max(),
            ),
            "scale" => submit_job_pool_scale(
                &weak,
                Arc::clone(&operation_state),
                pool_id,
                window.get_job_pool_edit_desired(),
            ),
            "drain" | "resume" | "delete" => {
                submit_job_pool_action(&weak, Arc::clone(&operation_state), pool_id, pending)
            }
            "start-slot-session" => {
                submit_slot_session(&weak, Arc::clone(&operation_state), pool_id)
            }
            value if value.starts_with("environment:") => submit_environment_operation(
                &weak,
                Arc::clone(&operation_state),
                environment_id,
                environment_version,
                value.trim_start_matches("environment:").to_owned(),
                window.get_package_reference().to_string(),
                environment_generation,
            ),
            _ => {}
        }
    });
    let weak = ui.as_weak();
    ui.on_operation_cancelled(move || {
        if let Some(window) = weak.upgrade() {
            window.set_operation_confirmation_visible(false);
            window.set_pending_operation(SharedString::default());
        }
    });

    let weak = ui.as_weak();
    let settings_state = Arc::clone(&state);
    ui.on_save_settings(move || {
        let Some(window) = weak.upgrade() else {
            return;
        };
        let settings = BehaviorSettings {
            auto_check_updates: window.get_auto_check_updates(),
            auto_repair: window.get_auto_repair(),
            update_channel: match window.get_update_channel().as_str() {
                "stable" => "stable".to_owned(),
                "test" => "test".to_owned(),
                _ => "nightly".to_owned(),
            },
            launch_on_login: window.get_launch_on_login(),
            close_to_tray: window.get_close_to_tray(),
            start_core_on_launch: window.get_start_core_on_launch(),
            check_interval: i64::from(window.get_update_interval().max(5)) * 60 * 1_000_000_000,
        };
        window.set_settings_phase("saving".into());
        run_background_with_failure(
            &weak,
            Arc::clone(&settings_state),
            move |state| {
                let input = state.data_root.join(".chuzi-settings-input.json");
                let input_arg = input.to_string_lossy().into_owned();
                let result = (|| {
                    fs::write(
                        &input,
                        serde_json::to_vec(&settings).map_err(|error| error.to_string())?,
                    )
                    .map_err(|error| error.to_string())?;
                    state.run_launcher("settings-save", &["-settings-input", input_arg.as_str()])
                })();
                let _ = fs::remove_file(&input);
                result.map(|_| {
                    state.settings = settings;
                    ("Settings saved.".to_owned(), ())
                })
            },
            |window, _| {
                window.set_settings_phase("ready".into());
            },
            |window, _| {
                window.set_settings_phase("error".into());
            },
        );
    });

    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_refresh_sessions(move || refresh_sessions(&weak, Arc::clone(&session_state)));
    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_load_more_sessions(move || load_more_sessions(&weak, Arc::clone(&session_state)));
    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_session_filter_changed(move |filter| {
        let Some(filter) = SessionFilter::from_str(filter.as_str()) else {
            return;
        };
        if let Some(window) = weak.upgrade() {
            let mut state = session_state.lock().unwrap();
            state.session_view_model.set_filter(filter);
            session_ui::render(&window, &state.session_view_model);
        }
    });
    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_session_search_changed(move |search| {
        if let Some(window) = weak.upgrade() {
            let mut state = session_state.lock().unwrap();
            state.session_view_model.set_search(search.as_str());
            session_ui::render(&window, &state.session_view_model);
        }
    });
    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_select_session(move |request_id| {
        if let Some(window) = weak.upgrade() {
            let mut state = session_state.lock().unwrap();
            if state.session_view_model.select_key(request_id.as_str()) {
                window.set_session_browser_view(Image::default());
                window.set_session_browser_view_loaded(false);
                window.set_session_browser_view_status(SharedString::default());
                session_ui::render(&window, &state.session_view_model);
            }
        }
    });
    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_move_session_selection(move |movement| {
        let movement = match movement.as_str() {
            "up" => SelectionMove::Up,
            "down" => SelectionMove::Down,
            "home" => SelectionMove::Home,
            "end" => SelectionMove::End,
            _ => return,
        };
        if let Some(window) = weak.upgrade() {
            let mut state = session_state.lock().unwrap();
            if state.session_view_model.move_selection(movement) {
                window.set_session_browser_view(Image::default());
                window.set_session_browser_view_loaded(false);
                window.set_session_browser_view_status(SharedString::default());
                session_ui::render(&window, &state.session_view_model);
            }
        }
    });
    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_session_primary_action(move |request_id, action| {
        run_session_action(
            &weak,
            Arc::clone(&session_state),
            request_id.to_string(),
            action.to_string(),
            false,
        );
    });
    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_session_cancel_confirmed(move |request_id| {
        run_session_action(
            &weak,
            Arc::clone(&session_state),
            request_id.to_string(),
            "cancel".to_owned(),
            false,
        );
    });
    let weak = ui.as_weak();
    let session_state = Arc::clone(&state);
    ui.on_session_more_action(move |request_id, action| {
        run_session_action(
            &weak,
            Arc::clone(&session_state),
            request_id.to_string(),
            action.to_string(),
            true,
        );
    });

    let weak = ui.as_weak();
    let workspace_state = Arc::clone(&state);
    ui.on_session_workspace_dock(move || {
        set_workspace_host(&weak, &workspace_state, RdpHost::Docked)
    });
    let weak = ui.as_weak();
    let workspace_state = Arc::clone(&state);
    ui.on_session_workspace_float(move || {
        set_workspace_host(&weak, &workspace_state, RdpHost::Floating)
    });
    let weak = ui.as_weak();
    let workspace_state = Arc::clone(&state);
    ui.on_session_workspace_hide(move || {
        set_workspace_host(&weak, &workspace_state, RdpHost::Hidden)
    });
    let weak = ui.as_weak();
    let workspace_state = Arc::clone(&state);
    ui.on_session_workspace_stop(move || {
        set_workspace_host(&weak, &workspace_state, RdpHost::Stopped)
    });

    let weak = ui.as_weak();
    let theme_state = Arc::clone(&state);
    ui.on_theme_changed(move |theme| {
        let theme = normalize_theme(theme.as_str()).to_owned();
        if let Some(window) = weak.upgrade() {
            window.set_theme(theme.clone().into());
            apply_theme(&window, &theme);
        }
        let weak = weak.clone();
        let theme_state = Arc::clone(&theme_state);
        thread::spawn(move || {
            let result = theme_state.lock().unwrap().save_ui_theme(&theme);
            if let Err(error) = result {
                set_feedback(
                    &weak,
                    friendly_error(&format!("theme preference: {error}")),
                    "error",
                );
            }
        });
    });

    let diagnostic_weak = ui.as_weak();
    let diagnostic_state = Arc::clone(&state);
    ui.on_diagnostic_save(move || {
        let Some(window) = diagnostic_weak.upgrade() else {
            return;
        };
        let summary = window.get_diagnostic_consent_summary().to_string();
        let category = window.get_diagnostic_consent_category().to_string();
        let severity = window.get_diagnostic_consent_severity().to_string();
        window.set_diagnostic_submitting(true);
        let weak = diagnostic_weak.clone();
        run_background_with(
            &weak,
            Arc::clone(&diagnostic_state),
            move |state| {
                let snapshot = diagnostic_snapshot(state, &severity, &category, &summary);
                let path = state.save_diagnostic_snapshot(&snapshot)?;
                Ok((format!("诊断文件已保存：{}", path.display()), path))
            },
            |window, _path: PathBuf| {
                window.set_diagnostic_submitting(false);
                window.set_diagnostic_consent_visible(false);
            },
        );
    });

    let diagnostic_open_weak = ui.as_weak();
    ui.on_diagnostic_open(move || {
        if let Some(window) = diagnostic_open_weak.upgrade() {
            show_diagnostic_consent(&window, "diagnostics", "warning");
        }
    });

    let diagnostic_dismiss_weak = ui.as_weak();
    ui.on_diagnostic_dismiss(move || {
        if let Some(window) = diagnostic_dismiss_weak.upgrade() {
            window.set_diagnostic_submitting(false);
            window.set_diagnostic_consent_visible(false);
        }
    });
}

fn set_workspace_host(ui: &slint::Weak<MainWindow>, state: &Arc<Mutex<AppState>>, host: RdpHost) {
    let Ok(mut guard) = state.lock() else {
        return;
    };
    if guard.workspace_host == RdpHost::Stopped && host != RdpHost::Stopped {
        return;
    }
    guard.workspace_host = host;
    if let Some(window) = ui.upgrade() {
        window.set_session_workspace_host(host.as_str().into());
        if host == RdpHost::Stopped {
            window.set_session_workspace_visible(false);
        }
    }
}

fn show_diagnostic_consent(window: &MainWindow, category: &str, severity: &str) {
    window.set_diagnostic_consent_category(category.into());
    window.set_diagnostic_consent_severity(severity.into());
    window.set_diagnostic_consent_summary(
        match category {
            "core" => "Core 操作出现问题。可以保存一份脱敏诊断文件交给支持人员。",
            "diagnostics" => "可以保存最近的脱敏运行诊断文件。",
            _ => "应用操作出现问题。可以保存一份脱敏诊断文件交给支持人员。",
        }
        .into(),
    );
    window.set_diagnostic_consent_visible(true);
}

fn diagnostic_snapshot(
    state: &AppState,
    severity: &str,
    category: &str,
    summary: &str,
) -> DiagnosticSnapshot {
    let capture_started = Instant::now();
    let (status, status_error_class) = state.diagnostic_context();
    let capabilities = diagnostic_core_capabilities(state);
    let error_class = if state.diagnostic_error_class.is_empty() {
        diagnostic_error_class(summary).to_owned()
    } else {
        state.diagnostic_error_class.clone()
    };
    let operation = if state.diagnostic_operation.is_empty() {
        diagnostic_operation(summary).to_owned()
    } else {
        state.diagnostic_operation.clone()
    };
    let params = json!({"severity": severity, "category": category, "summary": summary, "error_class": error_class, "operation": operation});
    let capture = match diagnostic_core_call(state, params, &capabilities) {
        Ok((value, _response_size, capture)) => {
            let value =
                normalize_legacy_diagnostic_response(value, &status, &error_class, &operation);
            let mut inspected = inspect_diagnostic_response(value);
            inspected.merge_call_metadata(capture);
            inspected
        }
        Err(capture) => capture,
    };
    let capture_duration_ms = capture_started.elapsed().as_millis() as u64;
    if let Some(mut snapshot) = capture.snapshot {
        if snapshot.error_class.is_empty() {
            snapshot.error_class = error_class.clone();
        }
        if snapshot.operation.is_empty() {
            snapshot.operation = operation.clone();
        }
        snapshot.capture_stage = capture.capture_stage;
        snapshot.core_schema = capture.core_schema;
        snapshot.core_version = capture.core_version;
        snapshot.capture_error_code = capture.capture_error_code;
        snapshot.capture_error_size = capture.capture_error_size;
        snapshot.capture_error_fingerprint = capture.capture_error_fingerprint;
        snapshot.capture_attempts = capture.capture_attempts;
        snapshot.legacy_retry_attempted = capture.legacy_retry_attempted;
        snapshot.capture_request_shape = capture.capture_request_shape;
        snapshot.capture_initial_error_class = capture.capture_initial_error_class;
        snapshot.capture_initial_error_code = capture.capture_initial_error_code;
        snapshot.capture_initial_error_size = capture.capture_initial_error_size;
        snapshot.capture_initial_error_fingerprint = capture.capture_initial_error_fingerprint;
        snapshot.response_kind = capture.response_kind;
        snapshot.response_size = capture.response_size;
        snapshot.response_key_count = capture.response_key_count;
        snapshot.response_fields = capture.response_fields;
        snapshot.response_fingerprint = capture.response_fingerprint;
        snapshot.capture_duration_ms = capture_duration_ms;
        snapshot.client_version = env!("CARGO_PKG_VERSION").to_owned();
        snapshot.core_status_error_class = status_error_class;
        snapshot.core_capability_status = capture.core_capability_status;
        snapshot.core_capability_error_code = capture.core_capability_error_code;
        snapshot.core_capability_error_fingerprint = capture.core_capability_error_fingerprint;
        snapshot.core_protocol_version = capture.core_protocol_version;
        snapshot.core_method_supported = capture.core_method_supported;
        snapshot.core_supported_methods = capture.core_supported_methods;
        return snapshot;
    }
    let capture_error_class = capture.capture_error_class;
    let error_class = if error_class.is_empty() {
        capture_error_class.clone()
    } else {
        error_class
    };
    let operation = if operation.is_empty() {
        "diagnostic_capture".to_owned()
    } else {
        operation
    };
    let capture_event = DiagnosticEvent {
        at: unix_timestamp_label(),
        component: "ui".to_owned(),
        operation: operation.clone(),
        outcome: "failed".to_owned(),
        request_id: String::new(),
        resource: String::new(),
        error_class: capture_error_class.clone(),
        duration_ms: 0,
    };
    DiagnosticSnapshot {
        schema: "chuzi.diagnostic/v2".to_owned(),
        id: format!(
            "ui-{}",
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map(|value| value.as_millis())
                .unwrap_or_default()
        ),
        created_at: unix_timestamp_label(),
        source: "ui_fallback".to_owned(),
        version: env!("CARGO_PKG_VERSION").to_owned(),
        platform: std::env::consts::OS.to_owned(),
        arch: std::env::consts::ARCH.to_owned(),
        severity: bounded_diagnostic_field(severity),
        category: bounded_diagnostic_field(category),
        summary: bounded_diagnostic_field(summary),
        error_class,
        operation,
        event_count: 1,
        events_truncated: false,
        capture_error_class,
        capture_error_code: capture.capture_error_code,
        capture_error_size: capture.capture_error_size,
        capture_error_fingerprint: capture.capture_error_fingerprint,
        capture_attempts: capture.capture_attempts,
        legacy_retry_attempted: capture.legacy_retry_attempted,
        capture_request_shape: capture.capture_request_shape,
        capture_initial_error_class: capture.capture_initial_error_class,
        capture_initial_error_code: capture.capture_initial_error_code,
        capture_initial_error_size: capture.capture_initial_error_size,
        capture_initial_error_fingerprint: capture.capture_initial_error_fingerprint,
        capture_stage: capture.capture_stage,
        core_schema: capture.core_schema,
        core_version: capture.core_version,
        core_status_error_class: status_error_class,
        core_capability_status: capture.core_capability_status,
        core_capability_error_code: capture.core_capability_error_code,
        core_capability_error_fingerprint: capture.core_capability_error_fingerprint,
        core_protocol_version: capture.core_protocol_version,
        core_method_supported: capture.core_method_supported,
        core_supported_methods: capture.core_supported_methods,
        response_kind: capture.response_kind,
        response_size: capture.response_size,
        response_key_count: capture.response_key_count,
        response_fields: capture.response_fields,
        response_fingerprint: capture.response_fingerprint,
        capture_duration_ms,
        client_version: env!("CARGO_PKG_VERSION").to_owned(),
        core_status: DiagnosticCoreStatus {
            installed: status.installed,
            running: status.running,
            ready: status.ready,
            status: status.status,
            protocol: status.protocol,
            capability_status: status.capability_status,
        },
        events: vec![capture_event],
    }
}

fn diagnostic_core_capabilities(state: &AppState) -> DiagnosticCapabilities {
    let params = json!({"version": CORE_PROTOCOL_VERSION});
    let params_json = match serde_json::to_string(&params) {
        Ok(value) => value,
        Err(_) => {
            return DiagnosticCapabilities {
                status: "probe_error".to_owned(),
                error_code: "params_serialization".to_owned(),
                ..Default::default()
            };
        }
    };
    let output = match state.run_launcher(
        "core-call",
        &[
            "-core-method",
            "hello",
            "-core-params-json",
            params_json.as_str(),
        ],
    ) {
        Ok(output) => output,
        Err(error) => {
            return DiagnosticCapabilities {
                status: "probe_error".to_owned(),
                error_code: diagnostic_capture_error_code(&error),
                error_fingerprint: diagnostic_response_fingerprint(error.as_bytes()),
                ..Default::default()
            };
        }
    };
    let raw = output.trim();
    let value = match serde_json::from_str::<Value>(raw) {
        Ok(value) => value,
        Err(_) => {
            return DiagnosticCapabilities {
                status: "malformed_response".to_owned(),
                error_code: "malformed_json".to_owned(),
                error_fingerprint: diagnostic_response_fingerprint(raw.as_bytes()),
                ..Default::default()
            };
        }
    };
    parse_diagnostic_capabilities(value, raw.as_bytes())
}

fn parse_diagnostic_capabilities(value: Value, raw: &[u8]) -> DiagnosticCapabilities {
    let Some(object) = value.as_object() else {
        return DiagnosticCapabilities {
            status: "malformed_response".to_owned(),
            error_code: "malformed_hello".to_owned(),
            error_fingerprint: diagnostic_response_fingerprint(raw),
            ..Default::default()
        };
    };
    let protocol_version = object
        .get("version")
        .and_then(Value::as_str)
        .map(bounded_diagnostic_token)
        .unwrap_or_default();
    let Some(methods_value) = object.get("methods") else {
        return DiagnosticCapabilities {
            status: "malformed_response".to_owned(),
            error_code: "malformed_hello".to_owned(),
            error_fingerprint: diagnostic_response_fingerprint(raw),
            protocol_version,
            ..Default::default()
        };
    };
    let Some(methods) = methods_value.as_array() else {
        return DiagnosticCapabilities {
            status: "malformed_response".to_owned(),
            error_code: "malformed_hello".to_owned(),
            error_fingerprint: diagnostic_response_fingerprint(raw),
            protocol_version,
            ..Default::default()
        };
    };
    let mut supported_methods = methods
        .iter()
        .filter_map(Value::as_str)
        .filter(|method| valid_diagnostic_method_name(method))
        .map(ToOwned::to_owned)
        .collect::<Vec<_>>();
    supported_methods.sort();
    supported_methods.dedup();
    supported_methods.truncate(64);
    let method_supported = supported_methods
        .iter()
        .any(|method| method == DIAGNOSTIC_METHOD);
    DiagnosticCapabilities {
        status: if method_supported {
            "supported".to_owned()
        } else {
            "unsupported".to_owned()
        },
        error_code: String::new(),
        error_fingerprint: String::new(),
        protocol_version,
        method_supported: Some(method_supported),
        supported_methods,
    }
}

fn valid_diagnostic_method_name(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 64
        && value
            .chars()
            .all(|character| character.is_ascii_lowercase() || character == '_')
}

fn diagnostic_core_call(
    state: &AppState,
    params: Value,
    capabilities: &DiagnosticCapabilities,
) -> Result<(Value, usize, DiagnosticCapture), DiagnosticCapture> {
    if capabilities.method_supported == Some(false) {
        let mut capture =
            diagnostic_call_failure("core_method_unsupported", "unsupported_method", 0, &[]);
        capture.capture_stage = "capability_probe".to_owned();
        capture.capture_request_shape = "none".to_owned();
        capture.apply_capabilities(capabilities);
        return Err(capture);
    }

    let first = match diagnostic_core_call_once(state, &params) {
        Ok((value, response_size)) => {
            let mut capture = inspect_diagnostic_response(value.clone());
            capture.response_size = response_size;
            capture.capture_attempts = 1;
            capture.capture_request_shape = "extended".to_owned();
            capture.apply_capabilities(capabilities);
            return Ok((value, response_size, capture));
        }
        Err(capture) => capture,
    };
    if first.capture_error_code != "invalid_argument" {
        let mut capture = first;
        capture.capture_attempts = 1;
        capture.capture_request_shape = "extended".to_owned();
        capture.apply_capabilities(capabilities);
        return Err(capture);
    }

    // Older Core builds strictly reject additive error_class/operation fields.
    // Retry the stable request shape so a component version skew cannot block
    // a support snapshot, while retaining the first failure classification.
    let legacy_params = diagnostic_legacy_params(&params);
    let retry = diagnostic_core_call_once(state, &legacy_params);
    match retry {
        Ok((value, response_size)) => {
            let mut capture = inspect_diagnostic_response(value.clone());
            capture.response_size = response_size;
            capture.capture_attempts = 2;
            capture.legacy_retry_attempted = true;
            capture.capture_request_shape = "legacy".to_owned();
            capture.capture_initial_error_class = first.capture_error_class;
            capture.capture_initial_error_code = first.capture_error_code;
            capture.capture_initial_error_size = first.capture_error_size;
            capture.capture_initial_error_fingerprint = first.capture_error_fingerprint;
            capture.apply_capabilities(capabilities);
            Ok((value, response_size, capture))
        }
        Err(mut capture) => {
            capture.capture_attempts = 2;
            capture.legacy_retry_attempted = true;
            capture.capture_request_shape = "legacy".to_owned();
            capture.capture_initial_error_class = first.capture_error_class;
            capture.capture_initial_error_code = first.capture_error_code;
            capture.capture_initial_error_size = first.capture_error_size;
            capture.capture_initial_error_fingerprint = first.capture_error_fingerprint;
            capture.apply_capabilities(capabilities);
            Err(capture)
        }
    }
}

fn diagnostic_core_call_once(
    state: &AppState,
    params: &Value,
) -> Result<(Value, usize), DiagnosticCapture> {
    let params_json = serde_json::to_string(&params).map_err(|_| {
        diagnostic_call_failure("core_method_error", "params_serialization", 0, &[])
    })?;
    let output = state
        .run_launcher(
            "core-call",
            &[
                "-core-method",
                DIAGNOSTIC_METHOD,
                "-core-params-json",
                params_json.as_str(),
            ],
        )
        .map_err(|error| {
            diagnostic_call_failure(&diagnostic_capture_error_class(&error), &error, 0, &[])
        })?;
    let raw = output.trim();
    match serde_json::from_str::<Value>(raw) {
        Ok(value) => Ok((value, raw.len())),
        Err(_) => Err(diagnostic_call_failure(
            "malformed_response",
            "malformed_json",
            raw.len(),
            raw.as_bytes(),
        )),
    }
}

fn diagnostic_legacy_params(params: &Value) -> Value {
    let Some(object) = params.as_object() else {
        return params.clone();
    };
    let mut legacy = serde_json::Map::new();
    for field in ["severity", "category", "summary"] {
        if let Some(value) = object.get(field) {
            legacy.insert(field.to_owned(), value.clone());
        }
    }
    Value::Object(legacy)
}

fn normalize_legacy_diagnostic_response(
    value: Value,
    core_status: &DiagnosticCoreStatus,
    error_class: &str,
    operation: &str,
) -> Value {
    let Some(object) = value.as_object() else {
        return value;
    };
    if object.contains_key("schema") {
        return value;
    }
    for field in [
        "id",
        "created_at",
        "version",
        "platform",
        "arch",
        "severity",
        "category",
        "summary",
    ] {
        if !object.contains_key(field) {
            return value;
        }
    }
    let event_count = object
        .get("events")
        .and_then(Value::as_array)
        .map_or(0, Vec::len);
    let mut normalized = object.clone();
    normalized.insert("schema".to_owned(), json!("chuzi.diagnostic/v2"));
    normalized.insert("source".to_owned(), json!("core"));
    normalized.insert("error_class".to_owned(), json!(error_class));
    normalized.insert("operation".to_owned(), json!(operation));
    normalized.insert("event_count".to_owned(), json!(event_count));
    normalized.insert("events_truncated".to_owned(), json!(false));
    normalized.insert(
        "core_status".to_owned(),
        serde_json::to_value(core_status).unwrap_or_else(|_| json!({"status": "unknown"})),
    );
    Value::Object(normalized)
}

fn diagnostic_call_failure(
    capture_error_class: &str,
    capture_error: &str,
    response_size: usize,
    response: &[u8],
) -> DiagnosticCapture {
    DiagnosticCapture {
        snapshot: None,
        capture_error_class: capture_error_class.to_owned(),
        capture_error_code: diagnostic_capture_error_code(capture_error),
        capture_error_size: capture_error.len(),
        capture_error_fingerprint: diagnostic_response_fingerprint(capture_error.as_bytes()),
        capture_attempts: 0,
        legacy_retry_attempted: false,
        capture_request_shape: String::new(),
        capture_initial_error_class: String::new(),
        capture_initial_error_code: String::new(),
        capture_initial_error_size: 0,
        capture_initial_error_fingerprint: String::new(),
        capture_stage: "core_call".to_owned(),
        core_schema: String::new(),
        core_version: String::new(),
        response_kind: if response.is_empty() {
            "unavailable".to_owned()
        } else {
            "invalid_json".to_owned()
        },
        response_size,
        response_key_count: 0,
        response_fields: Vec::new(),
        response_fingerprint: if response.is_empty() {
            String::new()
        } else {
            diagnostic_response_fingerprint(response)
        },
        core_capability_status: String::new(),
        core_capability_error_code: String::new(),
        core_capability_error_fingerprint: String::new(),
        core_protocol_version: String::new(),
        core_method_supported: None,
        core_supported_methods: Vec::new(),
    }
}

#[derive(Debug)]
struct DiagnosticCapture {
    snapshot: Option<DiagnosticSnapshot>,
    capture_error_class: String,
    capture_error_code: String,
    capture_error_size: usize,
    capture_error_fingerprint: String,
    capture_attempts: usize,
    legacy_retry_attempted: bool,
    capture_request_shape: String,
    capture_initial_error_class: String,
    capture_initial_error_code: String,
    capture_initial_error_size: usize,
    capture_initial_error_fingerprint: String,
    capture_stage: String,
    core_schema: String,
    core_version: String,
    response_kind: String,
    response_size: usize,
    response_key_count: usize,
    response_fields: Vec<String>,
    response_fingerprint: String,
    core_capability_status: String,
    core_capability_error_code: String,
    core_capability_error_fingerprint: String,
    core_protocol_version: String,
    core_method_supported: Option<bool>,
    core_supported_methods: Vec<String>,
}

impl DiagnosticCapture {
    fn apply_capabilities(&mut self, capabilities: &DiagnosticCapabilities) {
        self.core_capability_status = capabilities.status.clone();
        self.core_capability_error_code = capabilities.error_code.clone();
        self.core_capability_error_fingerprint = capabilities.error_fingerprint.clone();
        self.core_protocol_version = capabilities.protocol_version.clone();
        self.core_method_supported = capabilities.method_supported;
        self.core_supported_methods = capabilities.supported_methods.clone();
    }

    fn merge_call_metadata(&mut self, source: DiagnosticCapture) {
        self.capture_attempts = source.capture_attempts;
        self.legacy_retry_attempted = source.legacy_retry_attempted;
        self.capture_request_shape = source.capture_request_shape;
        self.capture_initial_error_class = source.capture_initial_error_class;
        self.capture_initial_error_code = source.capture_initial_error_code;
        self.capture_initial_error_size = source.capture_initial_error_size;
        self.capture_initial_error_fingerprint = source.capture_initial_error_fingerprint;
        self.core_capability_status = source.core_capability_status;
        self.core_capability_error_code = source.core_capability_error_code;
        self.core_capability_error_fingerprint = source.core_capability_error_fingerprint;
        self.core_protocol_version = source.core_protocol_version;
        self.core_method_supported = source.core_method_supported;
        self.core_supported_methods = source.core_supported_methods;
        self.response_size = source.response_size;
    }
}

fn inspect_diagnostic_response(value: Value) -> DiagnosticCapture {
    let encoded = serde_json::to_vec(&value).unwrap_or_default();
    let (response_kind, response_key_count, response_fields) = response_shape(&value);
    let response_fingerprint = diagnostic_response_fingerprint(&encoded);
    let response_size = encoded.len();
    let mut capture = DiagnosticCapture {
        snapshot: None,
        capture_error_class: String::new(),
        capture_error_code: String::new(),
        capture_error_size: 0,
        capture_error_fingerprint: String::new(),
        capture_attempts: 0,
        legacy_retry_attempted: false,
        capture_request_shape: String::new(),
        capture_initial_error_class: String::new(),
        capture_initial_error_code: String::new(),
        capture_initial_error_size: 0,
        capture_initial_error_fingerprint: String::new(),
        capture_stage: "schema_validation".to_owned(),
        core_schema: String::new(),
        core_version: String::new(),
        response_kind,
        response_size,
        response_key_count,
        response_fields,
        response_fingerprint,
        core_capability_status: String::new(),
        core_capability_error_code: String::new(),
        core_capability_error_fingerprint: String::new(),
        core_protocol_version: String::new(),
        core_method_supported: None,
        core_supported_methods: Vec::new(),
    };
    let Some(object) = value.as_object() else {
        capture.capture_error_class = "malformed_response".to_owned();
        return capture;
    };
    if let Some(version) = object.get("version") {
        capture.core_version = version
            .as_str()
            .map(bounded_diagnostic_token)
            .unwrap_or_else(|| "non_string".to_owned());
    }
    let Some(schema_value) = object.get("schema") else {
        capture.capture_error_class = "missing_schema".to_owned();
        return capture;
    };
    let Some(schema) = schema_value.as_str() else {
        capture.core_schema = "non_string".to_owned();
        capture.capture_error_class = "malformed_schema".to_owned();
        return capture;
    };
    capture.core_schema = bounded_diagnostic_token(schema);
    if schema != "chuzi.diagnostic/v2" {
        capture.capture_error_class = "unsupported_schema".to_owned();
        return capture;
    }
    capture.capture_stage = "snapshot_decode".to_owned();
    match serde_json::from_value::<DiagnosticSnapshot>(value) {
        Ok(snapshot) if diagnostic_snapshot_is_well_formed(&snapshot) => {
            capture.capture_stage = "complete".to_owned();
            capture.snapshot = Some(snapshot);
        }
        Ok(_) | Err(_) => {
            capture.capture_error_class = "malformed_snapshot".to_owned();
        }
    }
    capture
}

fn diagnostic_snapshot_is_well_formed(snapshot: &DiagnosticSnapshot) -> bool {
    [
        &snapshot.id,
        &snapshot.created_at,
        &snapshot.source,
        &snapshot.version,
        &snapshot.platform,
        &snapshot.arch,
        &snapshot.severity,
        &snapshot.category,
        &snapshot.summary,
        &snapshot.core_status.status,
    ]
    .iter()
    .all(|value| !value.trim().is_empty())
        && snapshot.event_count >= snapshot.events.len()
}

fn diagnostic_capture_error_class(error: &str) -> String {
    if error.starts_with("Core projection:") {
        return "malformed_response".to_owned();
    }
    if error.starts_with("Core status:") {
        return "core_status_error".to_owned();
    }
    let lower = error.to_ascii_lowercase();
    if lower.contains("core transport")
        || lower.contains("core operation")
        || lower.starts_with("chuzi core:")
    {
        return "core_method_error".to_owned();
    }
    diagnostic_error_class(error).to_owned()
}

fn diagnostic_capture_error_code(error: &str) -> String {
    let lower = error.to_ascii_lowercase();
    if matches!(
        lower.as_str(),
        "malformed_json" | "params_serialization" | "unsupported_method" | "malformed_hello"
    ) {
        return lower;
    }
    if let Some(code) = lower.strip_prefix("chuzi core:") {
        let code = code.trim().split_whitespace().next().unwrap_or_default();
        if matches!(
            code,
            "invalid_argument"
                | "not_found"
                | "conflict"
                | "forbidden"
                | "unavailable"
                | "cancelled"
                | "deadline_exceeded"
                | "rate_limited"
                | "internal"
        ) {
            return code.to_owned();
        }
    }
    if lower.contains("core transport") {
        return "transport_error".to_owned();
    }
    if lower.contains("core operation") {
        return "operation_error".to_owned();
    }
    diagnostic_capture_error_class(error)
}

fn response_shape(value: &Value) -> (String, usize, Vec<String>) {
    const KNOWN_FIELDS: [&str; 44] = [
        "schema",
        "id",
        "created_at",
        "source",
        "version",
        "platform",
        "arch",
        "severity",
        "category",
        "summary",
        "error_class",
        "operation",
        "event_count",
        "events_truncated",
        "capture_error_class",
        "capture_error_code",
        "capture_error_size",
        "capture_error_fingerprint",
        "capture_attempts",
        "legacy_retry_attempted",
        "capture_request_shape",
        "capture_initial_error_class",
        "capture_initial_error_code",
        "capture_initial_error_size",
        "capture_initial_error_fingerprint",
        "core_status",
        "events",
        "capture_stage",
        "core_schema",
        "core_version",
        "core_status_error_class",
        "core_capability_status",
        "core_capability_error_code",
        "core_capability_error_fingerprint",
        "core_protocol_version",
        "core_method_supported",
        "core_supported_methods",
        "response_kind",
        "response_size",
        "response_key_count",
        "response_fields",
        "response_fingerprint",
        "capture_duration_ms",
        "client_version",
    ];
    match value {
        Value::Object(object) => {
            let fields = KNOWN_FIELDS
                .iter()
                .filter(|field| object.contains_key(**field))
                .map(|field| (*field).to_owned())
                .collect();
            ("object".to_owned(), object.len(), fields)
        }
        Value::Array(_) => ("array".to_owned(), 0, Vec::new()),
        Value::String(_) => ("string".to_owned(), 0, Vec::new()),
        Value::Number(_) => ("number".to_owned(), 0, Vec::new()),
        Value::Bool(_) => ("boolean".to_owned(), 0, Vec::new()),
        Value::Null => ("null".to_owned(), 0, Vec::new()),
    }
}

fn diagnostic_response_fingerprint(encoded: &[u8]) -> String {
    let mut hash = 0xcbf29ce484222325_u64;
    for byte in encoded {
        hash ^= u64::from(*byte);
        hash = hash.wrapping_mul(0x100000001b3);
    }
    format!("fnv1a64:{hash:016x}")
}

fn bounded_diagnostic_token(value: &str) -> String {
    let token = value
        .chars()
        .filter(|character| {
            character.is_ascii_alphanumeric() || matches!(character, '-' | '_' | '.' | '/')
        })
        .take(96)
        .collect::<String>();
    if token.is_empty() {
        "malformed".to_owned()
    } else {
        token
    }
}

fn diagnostic_error_class(error: &str) -> &'static str {
    let value = error.to_ascii_lowercase();
    if value.contains("core_capability_mismatch") || value.contains("core update required") {
        "core_capability_mismatch"
    } else if value.contains("core_not_installed") || value.contains("launcher is missing") {
        "core_not_installed"
    } else if value.contains("core_start_timeout") || value.contains("start timeout") {
        "core_start_timeout"
    } else if value.contains("core_unavailable")
        || value.contains("connect core")
        || value.contains("core unavailable")
    {
        "core_unavailable"
    } else if value.contains("not_found") || value.contains("not found") {
        "not_found"
    } else if value.contains("rate_limit") || value.contains("rate limited") {
        "rate_limited"
    } else if value.contains("deadline") || value.contains("timeout") {
        "deadline_exceeded"
    } else if value.contains("cancel") {
        "cancelled"
    } else if value.contains("invalid_") || value.contains("projection") {
        "invalid_projection"
    } else if value.contains("forbidden") || value.contains("not allowed") {
        "forbidden"
    } else if value.contains("conflict") || value.contains("stale_revision") {
        "conflict"
    } else if value.contains("internal") || value.contains("panic") {
        "internal"
    } else {
        "ui_operation_failed"
    }
}

fn diagnostic_operation(error: &str) -> &'static str {
    let value = error.to_ascii_lowercase();
    if value.contains("core_") || value.contains("launcher") || value.contains("connect core") {
        "core_lifecycle"
    } else if value.contains("session") || value.contains("browser") || value.contains("request") {
        "session_projection"
    } else if value.contains("job_pool") || value.contains("environment") {
        "control_plane_operation"
    } else if value.contains("theme") || value.contains("settings") {
        "ui_settings"
    } else {
        "ui_operation"
    }
}

fn bounded_diagnostic_field(value: &str) -> String {
    let value = value.trim();
    let lower = value.to_ascii_lowercase();
    if [
        "password",
        "passwd",
        "token",
        "cookie",
        "authorization",
        "credential",
        "secret",
        "private key",
        "profile path",
    ]
    .iter()
    .any(|marker| lower.contains(marker))
    {
        return "user-visible diagnostic message redacted".to_owned();
    }
    value
        .chars()
        .filter(|character| !character.is_control())
        .take(240)
        .collect::<String>()
        .trim()
        .to_owned()
}

fn unix_timestamp_label() -> String {
    match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(value) => format!("unix:{}", value.as_secs()),
        Err(_) => "unix:0".to_owned(),
    }
}

#[derive(Debug)]
struct CoreSnapshot {
    ready: bool,
    installed: bool,
    status: String,
    details: String,
}

fn refresh_core(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    let sessions_state = Arc::clone(&state);
    let pools_state = Arc::clone(&state);
    run_background_with(
        ui,
        state,
        |state| {
            let status = core_status(state)?;
            let snapshot = if status.ready {
                CoreSnapshot {
                    ready: true,
                    installed: true,
                    status: "Core is running".to_owned(),
                    details: "Core is running and ready for requests.".to_owned(),
                }
            } else if status.installed
                && status.running
                && status.capability_status == "incompatible"
            {
                CoreSnapshot {
                    ready: false,
                    installed: true,
                    status: "Core update required".to_owned(),
                    details: "The installed Core is older than this UI. Reinstall or install Core again to repair the shared Core payload.".to_owned(),
                }
            } else if status.installed && status.running {
                CoreSnapshot {
                    ready: false,
                    installed: true,
                    status: "Core is unavailable".to_owned(),
                    details: "Core is installed and running, but its local API is not responding. Restart Core and refresh its status.".to_owned(),
                }
            } else if status.installed {
                CoreSnapshot {
                    ready: false,
                    installed: true,
                    status: "Core is stopped".to_owned(),
                    details: "Start Core to load Sessions.".to_owned(),
                }
            } else {
                CoreSnapshot {
                    ready: false,
                    installed: false,
                    status: "Core is not installed".to_owned(),
                    details: "Install Core to load Sessions.".to_owned(),
                }
            };
            Ok(("Core status refreshed.".to_owned(), (snapshot, status)))
        },
        move |window, (snapshot, status): (CoreSnapshot, CoreStatus)| {
            window.set_core_running(status.running);
            window.set_configured_pool_mode(execution_mode(&status.configured_pool_mode).into());
            window.set_core_ready(snapshot.ready);
            window.set_core_installed(snapshot.installed);
            window.set_core_status_known(true);
            window.set_core_status(snapshot.status.into());
            window.set_core_details(snapshot.details.into());
            if snapshot.ready {
                if window.get_page() == "settings" {
                    refresh_job_pools(&window.as_weak(), Arc::clone(&pools_state));
                } else {
                    refresh_sessions(&window.as_weak(), Arc::clone(&sessions_state));
                }
            }
        },
    );
}

fn refresh_job_pools(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    if state.lock().unwrap().busy {
        return;
    }
    if let Some(window) = ui.upgrade() {
        window.set_job_pool_phase("loading".into());
        window.set_environment_operation_phase("loading".into());
    }
    run_background_with_failure(
        ui,
        state,
        |state| {
            ensure_core_ready(state)?;
            let value = core_call(state, CoreMethod::ListJobPools, json!({}))?;
            let list: CoreJobPoolList = serde_json::from_value(value)
                .map_err(|error| format!("invalid_job_pool_projection: {error}"))?;
            let environments =
                core_call(state, CoreMethod::ListEnvironments, json!({})).and_then(|value| {
                    serde_json::from_value::<CoreEnvironmentList>(value)
                        .map_err(|error| format!("invalid_environment_projection: {error}"))
                })?;
            let summary = if list.job_pools.is_empty() {
                "No job pools are configured.".to_owned()
            } else {
                format_job_pool_summary(&list)
            };
            Ok((
                "Job pool and environment status refreshed.".to_owned(),
                (list, environments, summary),
            ))
        },
        |window, (list, environments, summary): (CoreJobPoolList, CoreEnvironmentList, String)| {
            window.set_job_pool_phase("ready".into());
            window.set_job_pool_summary(summary.into());
            let pools = list.job_pools.iter().map(job_pool_row).collect::<Vec<_>>();
            let environment_rows = environments
                .environments
                .iter()
                .map(environment_row)
                .collect::<Vec<_>>();
            window.set_job_pool_rows(ModelRc::from(pools.as_slice()));
            window.set_environment_rows(ModelRc::from(environment_rows.as_slice()));
            window.set_environment_operation_phase("ready".into());
        },
        |window, _| {
            window.set_job_pool_phase("error".into());
            window.set_environment_operation_phase("unavailable".into());
        },
    );
}

fn job_pool_row(pool: &CoreJobPool) -> JobPoolRowData {
    JobPoolRowData {
        execution_mode: execution_mode(&pool.status.execution_mode).into(),
        pool_id: pool.config.pool_id.clone().into(),
        environment_id: pool.config.environment_id.clone().into(),
        environment_version: pool.config.environment_version.clone().into(),
        desired_slots: pool.config.desired_slots,
        max_concurrency: pool.config.max_concurrency,
        desired_state: if pool.config.desired_state.is_empty() {
            "enabled"
        } else {
            pool.config.desired_state.as_str()
        }
        .into(),
        ready: pool.status.ready,
        leased: pool.status.leased,
        quarantined: pool.status.quarantined,
        draining: pool.status.draining,
        provisioning: pool.status.provisioning,
        retiring: pool.status.retiring,
        effective_capacity: pool.status.effective_capacity,
        environment_readiness: if pool.status.environment_readiness.is_empty() {
            "unknown"
        } else {
            pool.status.environment_readiness.as_str()
        }
        .into(),
        reconcile_state: if pool.status.reconcile_state.is_empty() {
            "unknown"
        } else {
            pool.status.reconcile_state.as_str()
        }
        .into(),
        last_failure_code: pool.status.last_failure_code.clone().into(),
        config_revision: pool.status.config_revision.to_string().into(),
        operation_id: pool.status.operation_id.clone().into(),
        selected: false,
    }
}

fn environment_row(environment: &CoreEnvironment) -> EnvironmentRowData {
    let lifecycle = if !environment.trusted {
        "untrusted"
    } else if !environment.healthy {
        "unhealthy"
    } else if environment.ready {
        "ready"
    } else if !environment.enabled {
        "disabled"
    } else if !environment.verified {
        "unverified"
    } else if !environment.installed {
        "not installed"
    } else {
        "provisioning"
    };
    EnvironmentRowData {
        environment_id: environment.environment_id.clone().into(),
        version: environment.version.clone().into(),
        lifecycle: lifecycle.into(),
        generation: environment.generation.to_string().into(),
        selected: false,
    }
}

fn idempotency_key(operation: &str, subject: &str, revision: u64, details: &str) -> String {
    let fingerprint_input = format!("{operation}\0{subject}\0{revision}\0{details}");
    let mut fingerprint = 14695981039346656037_u64;
    for byte in fingerprint_input.bytes() {
        fingerprint ^= u64::from(byte);
        fingerprint = fingerprint.wrapping_mul(1099511628211);
    }
    let subject = subject
        .chars()
        .map(|character| {
            if character.is_ascii_alphanumeric() || character == '-' || character == '_' {
                character
            } else {
                '-'
            }
        })
        .take(48)
        .collect::<String>();
    format!("windows-ui-{operation}-{subject}-{revision}-{fingerprint:016x}")
}

fn pool_revision(state: &AppState, pool_id: &str) -> Result<u64, String> {
    let value = core_call(state, CoreMethod::ListJobPools, json!({}))?;
    let list: CoreJobPoolList =
        serde_json::from_value(value).map_err(|_| "invalid_job_pool_projection".to_owned())?;
    Ok(list
        .job_pools
        .iter()
        .find(|pool| pool.config.pool_id == pool_id)
        .map(|pool| pool.status.config_revision.max(pool.config.config_revision))
        .unwrap_or(0))
}

fn execution_mode(value: &str) -> &str {
    match value {
        "logical" | "windows" => value,
        _ => "unknown",
    }
}

fn submit_pool_mode(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    mode: String,
    pool_id: String,
) {
    let Some(window) = ui.upgrade() else {
        return;
    };
    if state.lock().unwrap().busy {
        return;
    }
    if !window.get_core_status_known() || window.get_core_running() {
        window.set_pool_mode_phase("failed".into());
        window.set_pool_mode_detail(friendly_error("core_stop_required").into());
        return;
    }
    let revision = window
        .get_job_pool_rows()
        .iter()
        .find(|row| row.pool_id.as_str() == pool_id)
        .and_then(|row| row.config_revision.parse::<u64>().ok())
        .unwrap_or(0);
    window.set_pool_mode_phase("saving".into());
    window.set_pool_mode_detail("Validating cleanup and saving mode…".into());
    run_background_with_failure(
        ui,
        state,
        move |state| {
            if !matches!(mode.as_str(), "logical" | "windows") {
                return Err("invalid_argument".to_owned());
            }
            if core_status(state)?.running {
                return Err("core_stop_required".to_owned());
            }
            if mode == "windows" && revision == 0 {
                return Err("stale_revision".to_owned());
            }
            let output = state.run_launcher(
                "core-pool-mode-save",
                &[
                    "-pool-mode",
                    &mode,
                    "-pool-id",
                    &pool_id,
                    "-expected-revision",
                    &revision.to_string(),
                ],
            )?;
            let result: Value =
                serde_json::from_str(&output).map_err(|_| "pool_mode_save_failed".to_owned())?;
            if result["configured_pool_mode"] != mode || result["status"] != "restart_required" {
                return Err("pool_mode_save_failed".to_owned());
            }
            Ok((
                "Pool mode saved. Start Core to apply it, then scale the pool.".to_owned(),
                mode,
            ))
        },
        |window, mode: String| {
            let detail = if mode == "windows" {
                "Saved. Start Core to apply the mode, then scale the pool. Windows Agent readiness is verified during provisioning."
            } else {
                "Saved. Start Core to apply the mode, then scale the pool. Logical capacity creates no Windows user or Agent."
            };
            window.set_configured_pool_mode(mode.into());
            window.set_pool_mode_phase("restart required".into());
            window.set_pool_mode_detail(detail.into());
        },
        |window, error| {
            window.set_pool_mode_phase("failed".into());
            window.set_pool_mode_detail(friendly_error(error).into());
        },
    );
}

fn pool_environment_metadata(
    existing: Option<&CoreJobPool>,
    environments: &CoreEnvironmentList,
    id: &str,
    version: &str,
) -> Result<(String, String), String> {
    if let Some(env) = environments.environments.iter().find(|env| {
        env.environment_id == id
            && env.version == version
            && env.ready
            && env.installed
            && env.verified
            && env.trusted
            && env.enabled
            && env.healthy
            && env.manifest_digest.len() == 64
            && !env.signer.is_empty()
    }) {
        return Ok((env.manifest_digest.clone(), env.signer.clone()));
    }
    if let Some(pool) = existing {
        if pool.config.environment_id == id && pool.config.environment_version == version {
            return Ok((
                pool.config.manifest_digest.clone(),
                pool.config.signer.clone(),
            ));
        }
        if !pool.config.manifest_digest.is_empty()
            || !pool.config.signer.is_empty()
            || pool.status.execution_mode == "windows"
        {
            return Err("signed_environment_required".to_owned());
        }
    }
    Ok((String::new(), String::new()))
}

fn submit_job_pool_apply(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    pool_id: String,
    environment_id: String,
    environment_version: String,
    desired: i32,
    max_concurrency: i32,
) {
    let pool_id_for_key = pool_id.clone();
    start_job_pool_operation(ui, state, pool_id.clone(), move |state| {
        validate_text(&pool_id, "pool ID")?;
        validate_text(&environment_id, "environment ID")?;
        validate_text(&environment_version, "environment version")?;
        if desired < 0 || max_concurrency < 0 {
            return Err("invalid_argument".to_owned());
        }
        let pools: CoreJobPoolList =
            serde_json::from_value(core_call(state, CoreMethod::ListJobPools, json!({}))?)
                .map_err(|_| "invalid_job_pool_projection".to_owned())?;
        let existing = pools
            .job_pools
            .iter()
            .find(|pool| pool.config.pool_id == pool_id);
        let revision = existing
            .map(|pool| pool.config.config_revision.max(pool.status.config_revision))
            .unwrap_or(0);
        let environments: CoreEnvironmentList =
            serde_json::from_value(core_call(state, CoreMethod::ListEnvironments, json!({}))?)
                .map_err(|_| "invalid_environment_projection".to_owned())?;
        let (digest, signer) = pool_environment_metadata(
            existing,
            &environments,
            &environment_id,
            &environment_version,
        )?;
        let details = format!(
            "{environment_id}|{environment_version}|{desired}|{max_concurrency}|{digest}|{signer}"
        );
        let key = idempotency_key("apply", &pool_id_for_key, revision, &details);
        core_call(
            state,
            CoreMethod::ApplyJobPool,
            json!({
                "config": {
                    "pool_id": pool_id,
                    "desired_slots": desired,
                    "max_concurrency": max_concurrency,
                    "environment_id": environment_id,
                    "environment_version": environment_version,
                    "desired_state": "enabled",
                    "enabled": true,
                    "require_trusted": true,
                    "manifest_digest": digest,
                    "signer": signer
                },
                "expected_revision": revision,
                "idempotency_key": key,
                "actor": "windows-ui"
            }),
        )
    });
}

fn submit_job_pool_scale(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    pool_id: String,
    desired: i32,
) {
    let pool_id_for_key = pool_id.clone();
    start_job_pool_operation(ui, state, pool_id.clone(), move |state| {
        validate_text(&pool_id, "pool ID")?;
        if desired < 0 {
            return Err("invalid_argument".to_owned());
        }
        let revision = pool_revision(state, &pool_id)?;
        let key = idempotency_key("scale", &pool_id_for_key, revision, &desired.to_string());
        core_call(
            state,
            CoreMethod::ScaleJobPool,
            json!({
                "pool_id": pool_id,
                "desired_slots": desired,
                "expected_revision": revision,
                "idempotency_key": key,
                "actor": "windows-ui"
            }),
        )
    });
}

fn submit_job_pool_action(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    pool_id: String,
    action: String,
) {
    let method = match action.as_str() {
        "drain" => CoreMethod::DrainJobPool,
        "resume" => CoreMethod::ResumeJobPool,
        "delete" => CoreMethod::DeleteJobPool,
        _ => return,
    };
    let pool_id_for_key = pool_id.clone();
    start_job_pool_operation(ui, state, pool_id.clone(), move |state| {
        validate_text(&pool_id, "pool ID")?;
        let revision = pool_revision(state, &pool_id)?;
        let key = idempotency_key(&action, &pool_id_for_key, revision, "");
        core_call(
            state,
            method,
            json!({
                "pool_id": pool_id,
                "expected_revision": revision,
                "idempotency_key": key,
                "actor": "windows-ui"
            }),
        )
    });
}

fn submit_slot_session(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>, pool_id: String) {
    let pool_id_for_key = pool_id.clone();
    start_operation(
        ui,
        state,
        "slot-session",
        pool_id,
        move |state| {
            validate_text(&pool_id_for_key, "pool ID")?;
            let revision = pool_revision(state, &pool_id_for_key)?;
            let key = idempotency_key("start_slot_session", &pool_id_for_key, revision, "");
            let value = core_call(
                state,
                CoreMethod::StartSlotSession,
                json!({
                    "pool_id": pool_id_for_key,
                    "expected_revision": revision,
                    "idempotency_key": key,
                    "actor": "windows-ui"
                }),
            )
            .map_err(|error| {
                if error.contains("chuzi core: unavailable")
                    && core_status(state)
                        .map(|status| status.ready)
                        .unwrap_or(false)
                {
                    "slot_unavailable".to_owned()
                } else {
                    error
                }
            })?;
            slot_session_projection(state, value.clone())?;
            Ok(value)
        },
        move |state, operation_id| {
            let value = core_call(
                state,
                CoreMethod::GetSlotSessionOperation,
                json!({"operation_id": operation_id}),
            )
            .map_err(|error| {
                if error.contains("chuzi core: unavailable")
                    && core_status(state)
                        .map(|status| status.ready)
                        .unwrap_or(false)
                {
                    "session_unavailable".to_owned()
                } else {
                    error
                }
            })?;
            slot_session_projection(state, value)
        },
    );
}

fn start_job_pool_operation<F>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    pool_id: String,
    request: F,
) where
    F: FnOnce(&AppState) -> Result<Value, String> + Send + 'static,
{
    start_operation(
        ui,
        state,
        "job-pool",
        pool_id,
        request,
        |state, operation_id| {
            let value = core_call(
                state,
                CoreMethod::GetJobPoolOperation,
                json!({"operation_id": operation_id}),
            )?;
            decode_job_pool_operation(value)
                .map_err(|_| "invalid_operation_projection".to_owned())
                .map(|operation| {
                    (
                        operation.state,
                        operation.operation_id,
                        operation.failure_code,
                        operation.result,
                    )
                })
        },
    );
}

fn poll_job_pool_operation(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation_id: String,
) {
    start_operation_with_id(
        ui,
        state,
        "job-pool",
        operation_id,
        |state, operation_id| {
            let value = core_call(
                state,
                CoreMethod::GetJobPoolOperation,
                json!({"operation_id": operation_id}),
            )?;
            decode_job_pool_operation(value)
                .map_err(|_| "invalid_operation_projection".to_owned())
                .map(|operation| {
                    (
                        operation.state,
                        operation.operation_id,
                        operation.failure_code,
                        operation.result,
                    )
                })
        },
    );
}

fn poll_slot_session_operation(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation_id: String,
) {
    start_operation_with_id(
        ui,
        state,
        "slot-session",
        operation_id,
        |state, operation_id| {
            let value = core_call(
                state,
                CoreMethod::GetSlotSessionOperation,
                json!({"operation_id": operation_id}),
            )
            .map_err(|error| {
                if error.contains("chuzi core: unavailable")
                    && core_status(state)
                        .map(|status| status.ready)
                        .unwrap_or(false)
                {
                    "session_unavailable".to_owned()
                } else {
                    error
                }
            })?;
            slot_session_projection(state, value)
        },
    );
}

fn submit_environment_operation(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    environment_id: String,
    version: String,
    operation: String,
    package_ref: String,
    expected_revision: u64,
) {
    if !matches!(
        operation.as_str(),
        "install" | "upgrade" | "verify" | "trust" | "enable" | "disable" | "health" | "rollback"
    ) {
        return;
    }
    start_environment_operation(ui, state, environment_id.clone(), move |state| {
        validate_text(&environment_id, "environment ID")?;
        validate_text(&version, "environment version")?;
        if !package_ref.is_empty() && !valid_package_reference(&package_ref) {
            return Err("package_unavailable".to_owned());
        }
        if matches!(operation.as_str(), "install" | "upgrade") && package_ref.trim().is_empty() {
            return Err("package_unavailable".to_owned());
        }
        let revision = if expected_revision > 0 {
            expected_revision
        } else {
            environment_revision(state, &environment_id, &version)?
        };
        let details = format!("{version}|{operation}|{package_ref}");
        let key = idempotency_key(&operation, &environment_id, revision, &details);
        core_call(
            state,
            CoreMethod::EnvironmentOperation,
            json!({
                "environment_id": environment_id,
                "version": version,
                "operation": operation,
                "package_ref": package_ref,
                "expected_revision": revision,
                "idempotency_key": key,
                "actor": "windows-ui"
            }),
        )
    });
}

fn environment_revision(
    state: &AppState,
    environment_id: &str,
    version: &str,
) -> Result<u64, String> {
    let value = core_call(state, CoreMethod::ListEnvironments, json!({}))?;
    let list: CoreEnvironmentList =
        serde_json::from_value(value).map_err(|_| "invalid_environment_projection".to_owned())?;
    Ok(list
        .environments
        .iter()
        .find(|environment| {
            environment.environment_id == environment_id && environment.version == version
        })
        .map(|environment| environment.generation)
        .unwrap_or(0))
}

fn valid_package_reference(value: &str) -> bool {
    let trimmed = value.trim();
    !trimmed.is_empty()
        && trimmed == value
        && value.len() <= 256
        && value != "."
        && value != ".."
        && !value.chars().any(|character| {
            character.is_control() || matches!(character, '/' | '\\' | ' ' | ':' | '@')
        })
}

fn start_environment_operation<F>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    subject: String,
    request: F,
) where
    F: FnOnce(&AppState) -> Result<Value, String> + Send + 'static,
{
    start_operation(
        ui,
        state,
        "environment",
        subject,
        request,
        |state, operation_id| {
            let value = core_call(
                state,
                CoreMethod::GetEnvironmentOperation,
                json!({"operation_id": operation_id}),
            )?;
            decode_environment_operation(value)
                .map_err(|_| "invalid_operation_projection".to_owned())
                .map(|operation| {
                    (
                        operation.state,
                        operation.operation_id,
                        operation.failure_code,
                        String::new(),
                    )
                })
        },
    );
}

fn poll_environment_operation(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation_id: String,
) {
    start_operation_with_id(
        ui,
        state,
        "environment",
        operation_id,
        |state, operation_id| {
            let value = core_call(
                state,
                CoreMethod::GetEnvironmentOperation,
                json!({"operation_id": operation_id}),
            )?;
            decode_environment_operation(value)
                .map_err(|_| "invalid_operation_projection".to_owned())
                .map(|operation| {
                    (
                        operation.state,
                        operation.operation_id,
                        operation.failure_code,
                        String::new(),
                    )
                })
        },
    );
}

fn decode_job_pool_operation(value: Value) -> Result<CoreJobPoolOperation, serde_json::Error> {
    serde_json::from_value(value.get("operation").cloned().unwrap_or(Value::Null))
}

fn decode_slot_session_operation(
    value: Value,
) -> Result<CoreSlotSessionOperation, serde_json::Error> {
    let mut operation: CoreSlotSessionOperation =
        serde_json::from_value(value.get("operation").cloned().unwrap_or(Value::Null))?;
    if !matches!(
        operation.state.as_str(),
        "requested" | "provisioning" | "ready" | "failed" | "cancelled"
    ) {
        operation.state = "failed".to_owned();
        operation.failure_code = "invalid_operation_projection".to_owned();
    }
    if !matches!(
        operation.failure_code.as_str(),
        "" | "slot_quarantined"
            | "slot_not_found"
            | "slot_unavailable"
            | "session_unavailable"
            | "stale_revision"
            | "timeout"
            | "invalid_operation_projection"
    ) {
        operation.failure_code = "session_failed".to_owned();
    }
    Ok(operation)
}

fn slot_session_projection(
    state: &AppState,
    value: Value,
) -> Result<(String, String, String, String), String> {
    let operation = decode_slot_session_operation(value)
        .map_err(|_| "invalid_operation_projection".to_owned())?;
    let mut retry = state.slot_session_retry.lock().unwrap();
    let idempotent = operation.idempotent
        || retry
            .as_ref()
            .is_some_and(|(id, repeated)| id == &operation.operation_id && *repeated);
    *retry = Some((operation.operation_id.clone(), idempotent));
    Ok((
        operation.state.clone(),
        operation.operation_id.clone(),
        operation.failure_code.clone(),
        format_slot_session_detail(&operation, idempotent),
    ))
}

fn redacted_slot_state(value: &str) -> &str {
    match value {
        "unprovisioned" | "provisioning" | "ready" | "leased" | "quarantined" | "draining"
        | "retiring" | "deleted" | "unavailable" => value,
        _ => "unknown",
    }
}

fn format_slot_session_detail(
    operation: &CoreSlotSessionOperation,
    initial_idempotent: bool,
) -> String {
    let status = redacted_slot_state(&operation.status.status);
    let session_state = redacted_slot_state(&operation.status.session_state);
    let mode = execution_mode(&operation.status.execution_mode);
    let agent = match mode {
        "logical" => "no Windows Agent (logical test)",
        "windows" if operation.status.agent_ready => "Windows Agent ready (latest reconcile)",
        "windows" => "Windows Agent not ready",
        _ => "Agent readiness unknown",
    };
    let mut detail = format!(
        "slot #{} · mode {} · current status {} · session {} · {} · environment generation {}",
        operation.ordinal,
        mode,
        status,
        session_state,
        agent,
        operation.status.environment_generation,
    );
    if operation.idempotent || initial_idempotent {
        detail.push_str(" · idempotent");
    }
    detail
}

fn set_operation_kind(ui: &slint::Weak<MainWindow>, kind: &str) {
    let weak = ui.clone();
    let kind = kind.to_owned();
    let _ = slint::invoke_from_event_loop(move || {
        if let Some(window) = weak.upgrade() {
            window.set_operation_kind(kind.into());
            window.set_busy(true);
        }
    });
}

fn decode_environment_operation(
    value: Value,
) -> Result<CoreEnvironmentOperation, serde_json::Error> {
    serde_json::from_value(value.get("operation").cloned().unwrap_or(Value::Null))
}

fn start_operation<F, P>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    kind: &str,
    subject: String,
    request: F,
    poll: P,
) where
    F: FnOnce(&AppState) -> Result<Value, String> + Send + 'static,
    P: Fn(&AppState, &str) -> Result<(String, String, String, String), String>
        + Send
        + Sync
        + 'static,
{
    let weak = ui.clone();
    let poll = Arc::new(poll);
    {
        let mut guard = state.lock().unwrap();
        if guard.busy {
            return;
        }
        guard.busy = true;
    }
    set_operation_kind(ui, kind);
    set_operation_progress(
        &weak,
        "submitting",
        "",
        "requested",
        "Submitting operation…",
    );
    thread::spawn(move || {
        let result: Result<(String, String, String, String), String> =
            (|| -> Result<(String, String, String, String), String> {
                ensure_core_ready(&state.lock().unwrap())?;
                let value = request(&state.lock().unwrap())?;
                let operation_id = value
                    .get("operation")
                    .and_then(|operation| operation.get("operation_id"))
                    .and_then(Value::as_str)
                    .ok_or_else(|| "invalid_operation_projection".to_owned())?
                    .to_owned();
                Ok(poll_operation(&weak, &state, &operation_id, &poll)?)
            })();
        state.lock().unwrap().busy = false;
        finish_operation(&weak, result, subject, state);
    });
}

fn start_operation_with_id<P>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    kind: &str,
    operation_id: String,
    poll: P,
) where
    P: Fn(&AppState, &str) -> Result<(String, String, String, String), String>
        + Send
        + Sync
        + 'static,
{
    let weak = ui.clone();
    let poll = Arc::new(poll);
    {
        let mut guard = state.lock().unwrap();
        if guard.busy {
            return;
        }
        guard.busy = true;
    }
    set_operation_kind(ui, kind);
    set_operation_progress(
        &weak,
        "polling",
        &operation_id,
        "requested",
        "Polling operation…",
    );
    thread::spawn(move || {
        let result = poll_operation(&weak, &state, &operation_id, &poll);
        state.lock().unwrap().busy = false;
        finish_operation(&weak, result, operation_id, state);
    });
}

fn poll_operation<P>(
    ui: &slint::Weak<MainWindow>,
    state: &Arc<Mutex<AppState>>,
    operation_id: &str,
    poll: &Arc<P>,
) -> Result<(String, String, String, String), String>
where
    P: Fn(&AppState, &str) -> Result<(String, String, String, String), String>
        + Send
        + Sync
        + 'static,
{
    // Lease and platform cleanup can legitimately outlive the short UI
    // request path. Keep polling long enough for the default lease TTL, then
    // leave the operation ID available for an explicit refresh instead of
    // presenting a still-running delete as a failed operation.
    const MAX_POLL_ATTEMPTS: usize = 1200;
    for attempt in 0..MAX_POLL_ATTEMPTS {
        let value = match poll(&state.lock().unwrap(), operation_id) {
            Ok(value) => value,
            Err(error) if is_core_unavailable(&error) => {
                return Err("service_restarted".to_owned());
            }
            Err(error) => return Err(error),
        };
        let (state_name, id, failure, result) = value;
        set_operation_progress(
            ui,
            "polling",
            &id,
            &state_name,
            &format_operation_detail(&state_name, &failure, &result),
        );
        if is_terminal_operation(&state_name) {
            return Ok((state_name, id, failure, result));
        }
        if attempt + 1 == MAX_POLL_ATTEMPTS {
            return Err("operation_timeout".to_owned());
        }
        thread::sleep(Duration::from_millis(250));
    }
    Err("operation_timeout".to_owned())
}

fn is_terminal_operation(state: &str) -> bool {
    matches!(
        state,
        "applied" | "ready" | "failed" | "rolled_back" | "cancelled"
    )
}

fn format_operation_detail(state: &str, failure: &str, result: &str) -> String {
    if !failure.is_empty() && !result.is_empty() {
        return format!("{state} · {failure} · {result}");
    }
    if !failure.is_empty() {
        return format!("{state} · {failure}");
    }
    if !result.is_empty() {
        return format!("{state} · {result}");
    }
    state.to_owned()
}

fn set_operation_progress(
    ui: &slint::Weak<MainWindow>,
    phase: &str,
    operation_id: &str,
    state: &str,
    detail: &str,
) {
    let weak = ui.clone();
    let phase = phase.to_owned();
    let operation_id = operation_id.to_owned();
    let state = state.to_owned();
    let detail = detail.to_owned();
    let _ = slint::invoke_from_event_loop(move || {
        if let Some(window) = weak.upgrade() {
            window.set_operation_phase(phase.into());
            window.set_operation_id(operation_id.into());
            window.set_operation_state(state.into());
            window.set_operation_detail(detail.into());
        }
    });
}

fn finish_operation(
    ui: &slint::Weak<MainWindow>,
    result: Result<(String, String, String, String), String>,
    subject: String,
    state: Arc<Mutex<AppState>>,
) {
    let weak = ui.clone();
    let _ = slint::invoke_from_event_loop(move || {
        if let Some(window) = weak.upgrade() {
            window.set_busy(false);
            match result {
                Ok((operation_state, operation_id, failure, outcome)) => {
                    let failed = operation_state == "failed" || !failure.is_empty();
                    window.set_operation_phase(
                        if failed {
                            operation_error_phase(&failure)
                        } else if operation_state == "ready" {
                            "ready"
                        } else {
                            "applied"
                        }
                        .into(),
                    );
                    window.set_operation_id(operation_id.into());
                    window.set_operation_state(operation_state.clone().into());
                    window.set_operation_detail(
                        format_operation_detail(&operation_state, &failure, &outcome).into(),
                    );
                    window.set_message(
                        if failed {
                            friendly_error(&failure)
                        } else {
                            format!("Operation {operation_state} for {subject}.")
                        }
                        .into(),
                    );
                    window.set_message_kind(if failed { "error" } else { "success" }.into());
                    refresh_job_pools(&window.as_weak(), Arc::clone(&state));
                }
                Err(error) => {
                    if error == "operation_timeout" {
                        window.set_operation_phase("polling".into());
                        window.set_operation_detail(
                            "still running · use Operation to refresh status".into(),
                        );
                        window.set_message(
                            "Operation is still running. Use Operation to refresh its status."
                                .into(),
                        );
                        window.set_message_kind("info".into());
                        refresh_job_pools(&window.as_weak(), Arc::clone(&state));
                        return;
                    }
                    let phase = operation_error_phase(&error);
                    window.set_operation_phase(phase.into());
                    window.set_operation_state("failed".into());
                    window.set_operation_detail(friendly_error(&error).into());
                    window.set_message(friendly_error(&error).into());
                    window.set_message_kind("error".into());
                    refresh_job_pools(&window.as_weak(), Arc::clone(&state));
                }
            }
        }
    });
}

fn operation_error_phase(error: &str) -> &'static str {
    let value = error.to_ascii_lowercase();
    if value.contains("conflict") || value.contains("stale") {
        "stale revision"
    } else if value.contains("slot_unavailable") {
        "slot unavailable"
    } else if value.contains("session_unavailable") {
        "session unavailable"
    } else if value.contains("timeout") {
        "timeout"
    } else if value.contains("package_unavailable") {
        "package unavailable"
    } else if value.contains("environment_untrusted") {
        "environment untrusted"
    } else if value.contains("environment_unhealthy") {
        "environment unhealthy"
    } else if is_core_unavailable(error) {
        "unavailable"
    } else if value.contains("service_restarted") {
        "service restarted"
    } else {
        "failed"
    }
}

fn format_job_pool_summary(list: &CoreJobPoolList) -> String {
    list.job_pools
        .iter()
        .map(|pool| {
            format!(
                "{} · environment {} · ready {}/{} · leased {} · quarantined {} · draining {} · provisioning {} · retiring {} · effective {} · readiness {} · reconcile {} · failure {}",
                pool.config.pool_id,
                pool.config.environment_version,
                pool.status.ready,
                pool.status.desired,
                pool.status.leased,
                pool.status.quarantined,
                pool.status.draining,
                pool.status.provisioning,
                pool.status.retiring,
                pool.status.effective_capacity,
                if pool.status.environment_readiness.is_empty() {
                    "unknown"
                } else {
                    pool.status.environment_readiness.as_str()
                },
                if pool.status.reconcile_state.is_empty() {
                    "unknown"
                } else {
                    pool.status.reconcile_state.as_str()
                },
                if pool.status.last_failure_code.is_empty() {
                    "none"
                } else {
                    pool.status.last_failure_code.as_str()
                },
            )
        })
        .collect::<Vec<_>>()
        .join("\n")
}

#[cfg(test)]
mod job_pool_tests {
    use super::{
        format_job_pool_summary,
        models::{CoreJobPool, CoreJobPoolConfig, CoreJobPoolList, CoreJobPoolStatus},
    };

    #[test]
    fn summary_contains_safe_capacity_and_failure_fields() {
        let summary = format_job_pool_summary(&CoreJobPoolList {
            job_pools: vec![CoreJobPool {
                config: CoreJobPoolConfig {
                    pool_id: "pool-a".to_owned(),
                    desired_slots: 3,
                    max_concurrency: 0,
                    environment_id: "env-a".to_owned(),
                    environment_version: "1.2.3".to_owned(),
                    manifest_digest: String::new(),
                    signer: String::new(),
                    require_trusted: true,
                    desired_state: "enabled".to_owned(),
                    enabled: true,
                    config_revision: 1,
                },
                status: CoreJobPoolStatus {
                    execution_mode: "logical".to_owned(),
                    desired: 3,
                    ready: 2,
                    leased: 1,
                    quarantined: 0,
                    draining: 1,
                    provisioning: 1,
                    retiring: 0,
                    effective_capacity: 2,
                    environment_readiness: "ready".to_owned(),
                    reconcile_state: "failed".to_owned(),
                    last_failure_code: "package_unavailable".to_owned(),
                    operation_id: String::new(),
                    config_revision: 1,
                    environment_ready: false,
                },
            }],
        });
        for field in [
            "ready 2/3",
            "leased 1",
            "draining 1",
            "provisioning 1",
            "effective 2",
            "readiness ready",
            "reconcile failed",
            "failure package_unavailable",
        ] {
            assert!(summary.contains(field), "missing {field} in {summary}");
        }
        for secret in ["SID", "password", "profile", "pipe", "endpoint", "agent"] {
            assert!(
                !summary.to_ascii_lowercase().contains(secret),
                "summary leaked {secret}: {summary}"
            );
        }
    }
}

const SESSION_PAGE_SIZE: usize = 100;

fn refresh_sessions(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    if state.lock().unwrap().busy {
        return;
    }
    if let Some(window) = ui.upgrade() {
        let mut state_guard = state.lock().unwrap();
        state_guard.session_view_model.begin_loading();
        window.set_session_browser_view(slint::Image::default());
        window.set_session_browser_view_loaded(false);
        window.set_session_browser_view_status(SharedString::default());
        session_ui::render(&window, &state_guard.session_view_model);
    }

    let apply_state = Arc::clone(&state);
    let failure_state = Arc::clone(&state);
    run_background_with_failure(
        ui,
        state,
        |state| {
            ensure_core_ready(state)?;
            let result = core_call(
                state,
                CoreMethod::ListRequests,
                json!({"offset": 0, "limit": SESSION_PAGE_SIZE}),
            )?;
            let page: CoreRequestList = serde_json::from_value(result)
                .map_err(|_| "invalid_session_projection".to_owned())?;
            if page.requests.len() > SESSION_PAGE_SIZE {
                return Err("invalid_session_projection".to_owned());
            }
            let has_more = page.requests.len() == SESSION_PAGE_SIZE;
            Ok((
                "Session requests loaded.".to_owned(),
                (page.requests, has_more),
            ))
        },
        move |window, (requests, has_more): (Vec<CoreRequest>, bool)| {
            let mut state_guard = apply_state.lock().unwrap();
            state_guard
                .session_view_model
                .set_first_page(sessions_from_requests(&requests), has_more);
            session_ui::render(window, &state_guard.session_view_model);
        },
        move |window, error| {
            let mut state_guard = failure_state.lock().unwrap();
            let projection_error = if is_core_unavailable(error) {
                ProjectionError::CoreUnavailable
            } else if error.contains("invalid_session_projection") {
                ProjectionError::InvalidData
            } else {
                ProjectionError::Unknown
            };
            state_guard.session_view_model.set_error(projection_error);
            session_ui::render(window, &state_guard.session_view_model);
        },
    );
}

fn load_more_sessions(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    let offset = {
        let guard = state.lock().unwrap();
        if guard.busy || !guard.session_view_model.has_more() {
            return;
        }
        guard.session_view_model.next_offset()
    };
    if offset > 100_000 {
        return;
    }
    if let Some(window) = ui.upgrade() {
        let mut state_guard = state.lock().unwrap();
        state_guard.session_view_model.begin_loading();
        session_ui::render(&window, &state_guard.session_view_model);
    }

    let page_state = Arc::clone(&state);
    let failure_state = Arc::clone(&state);
    run_background_with_failure(
        ui,
        state,
        move |state| {
            ensure_core_ready(state)?;
            let result = core_call(
                state,
                CoreMethod::ListRequests,
                json!({"offset": offset, "limit": SESSION_PAGE_SIZE}),
            )?;
            let page: CoreRequestList = serde_json::from_value(result)
                .map_err(|_| "invalid_session_projection".to_owned())?;
            if page.requests.len() > SESSION_PAGE_SIZE {
                return Err("invalid_session_projection".to_owned());
            }
            let has_more = page.requests.len() == SESSION_PAGE_SIZE;
            Ok((
                "Older requests loaded.".to_owned(),
                (page.requests, has_more),
            ))
        },
        move |window, (requests, has_more): (Vec<CoreRequest>, bool)| {
            let mut state_guard = page_state.lock().unwrap();
            state_guard
                .session_view_model
                .append_page(sessions_from_requests(&requests).items, has_more);
            session_ui::render(window, &state_guard.session_view_model);
        },
        move |window, error| {
            let mut state_guard = failure_state.lock().unwrap();
            let projection_error = if is_core_unavailable(error) {
                ProjectionError::CoreUnavailable
            } else if error.contains("invalid_session_projection") {
                ProjectionError::InvalidData
            } else {
                ProjectionError::Unknown
            };
            state_guard.session_view_model.set_error(projection_error);
            session_ui::render(window, &state_guard.session_view_model);
        },
    );
}

fn run_session_action(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    request_id: String,
    action: String,
    is_more_action: bool,
) {
    let allowed = {
        let guard = state.lock().unwrap();
        let inspector = guard.session_view_model.inspector();
        let selected = inspector.selected.as_ref();
        let matches_selection = selected
            .and_then(|item| item.request_id.as_deref())
            .is_some_and(|selected_id| selected_id == request_id);
        if !matches_selection {
            false
        } else if is_more_action {
            inspector
                .actions
                .iter()
                .any(|entry| entry.enabled && entry.action.as_str() == action)
        } else {
            inspector.primary_enabled && inspector.primary_action.as_str() == action
        }
    };
    if !allowed {
        return;
    }

    let action_state = Arc::clone(&state);
    run_background_with(
        ui,
        state,
        move |state| {
            validate_text(&request_id, "request ID")?;
            match action.as_str() {
                "cancel" => {
                    let result = core_call(
                        state,
                        CoreMethod::CancelRequest,
                        json!({"request_id": request_id, "actor": "windows-ui"}),
                    )?;
                    let request: CoreRequest = serde_json::from_value(result)
                        .map_err(|_| "invalid_session_projection".to_owned())?;
                    Ok((
                        "Core updated the request status.".to_owned(),
                        SessionActionResult::Request(request),
                    ))
                }
                "refresh-status" => {
                    let result = core_call(
                        state,
                        CoreMethod::GetRequest,
                        json!({"request_id": request_id}),
                    )?;
                    let request: CoreRequest = serde_json::from_value(result)
                        .map_err(|_| "invalid_session_projection".to_owned())?;
                    Ok((
                        "Request status refreshed from Core.".to_owned(),
                        SessionActionResult::Request(request),
                    ))
                }
                "capture-view" => {
                    let result = core_call(
                        state,
                        CoreMethod::GetBrowserView,
                        json!({"request_id": request_id, "width": 640, "height": 360}),
                    )?;
                    let view: BrowserView = serde_json::from_value(result)
                        .map_err(|_| "browser_view_unavailable".to_owned())?;
                    if view.content_type != "image/jpeg"
                        || !(160..=1280).contains(&view.width)
                        || !(90..=720).contains(&view.height)
                        || view.data.len() > 2_000_000
                    {
                        return Err("browser_view_unavailable".to_owned());
                    }
                    Ok((
                        "Read-only browser preview captured.".to_owned(),
                        SessionActionResult::Browser(view),
                    ))
                }
                _ => Err("unsupported_session_action".to_owned()),
            }
        },
        move |window, result| match result {
            SessionActionResult::Request(request) => {
                let mut state_guard = action_state.lock().unwrap();
                state_guard.session_view_model.update_request(&request);
                session_ui::render(window, &state_guard.session_view_model);
            }
            SessionActionResult::Browser(view) => {
                let decoded = base64::engine::general_purpose::STANDARD.decode(&view.data);
                let Ok(bytes) = decoded else {
                    window.set_session_browser_view_loaded(false);
                    window
                        .set_session_browser_view_status("Preview data could not be read.".into());
                    return;
                };
                match Image::load_from_data(&bytes, Some("jpeg")) {
                    Ok(image) => {
                        window.set_session_browser_view(image);
                        window.set_session_browser_view_loaded(true);
                        window.set_session_browser_view_status(
                            format!("Read-only preview · {} × {}", view.width, view.height).into(),
                        );
                    }
                    Err(_) => {
                        window.set_session_browser_view_loaded(false);
                        window.set_session_browser_view_status(
                            "Preview image could not be read.".into(),
                        );
                    }
                }
            }
        },
    );
}

enum SessionActionResult {
    Request(CoreRequest),
    Browser(BrowserView),
}

fn apply_theme(ui: &MainWindow, theme: &str) {
    let scheme = match normalize_theme(theme) {
        "dark" => ColorScheme::Dark,
        "light" => ColorScheme::Light,
        _ => ColorScheme::Unknown,
    };
    ui.global::<Palette>().set_color_scheme(scheme);
}

fn normalize_theme(theme: &str) -> &'static str {
    match theme.trim().to_ascii_lowercase().as_str() {
        "dark" => "dark",
        "light" => "light",
        _ => "system",
    }
}

fn load_launcher_settings(ui: &MainWindow, state: Arc<Mutex<AppState>>) {
    let weak = ui.as_weak();
    thread::spawn(move || {
        let result = state.lock().unwrap().run_launcher("settings", &[]);
        let _ = slint::invoke_from_event_loop(move || {
            let Some(window) = weak.upgrade() else {
                return;
            };
            match result {
                Ok(output) => match serde_json::from_str::<BehaviorSettings>(output.trim()) {
                    Ok(settings) => {
                        let interval = if settings.check_interval <= 0 {
                            60
                        } else {
                            (settings.check_interval / (60 * 1_000_000_000)).max(5)
                        };
                        window.set_auto_check_updates(settings.auto_check_updates);
                        window.set_auto_repair(settings.auto_repair);
                        window.set_update_channel(settings.update_channel.clone().into());
                        window.set_launch_on_login(settings.launch_on_login);
                        window.set_close_to_tray(settings.close_to_tray);
                        window.set_start_core_on_launch(settings.start_core_on_launch);
                        window.set_update_interval(interval.min(i64::from(i32::MAX)) as i32);
                        window.set_settings_phase("ready".into());
                        let start_core = settings.start_core_on_launch;
                        state.lock().unwrap().settings = settings;
                        if start_core {
                            start_core_on_launch(&window.as_weak(), Arc::clone(&state));
                        } else {
                            refresh_core(&window.as_weak(), Arc::clone(&state));
                        }
                    }
                    Err(error) => {
                        window.set_settings_phase("error".into());
                        window.set_message(
                            format!("Saved launcher settings could not be read: {error}").into(),
                        );
                        window.set_message_kind("warning".into());
                    }
                },
                Err(error) => {
                    window.set_settings_phase("error".into());
                    window.set_message(
                        "Launcher settings are unavailable until the installed payload is ready."
                            .into(),
                    );
                    window.set_message_kind("warning".into());
                    let _ = error;
                    refresh_core(&window.as_weak(), Arc::clone(&state));
                }
            }
        });
    });
}

fn start_core_on_launch(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    let refresh_state = Arc::clone(&state);
    let failure_refresh_state = Arc::clone(&state);
    run_background_with_failure(
        ui,
        state,
        |state| {
            let status = core_status(state)?;
            if !status.installed {
                return Ok((
                    "Core is not installed; automatic start was skipped.".to_owned(),
                    (),
                ));
            }
            if !status.running || status.capability_status == "incompatible" {
                state.run_launcher("core-start", &[])?;
            }
            Ok(("Core started automatically.".to_owned(), ()))
        },
        move |window, _| {
            refresh_core(&window.as_weak(), refresh_state);
        },
        move |window, _| {
            refresh_core(&window.as_weak(), failure_refresh_state);
        },
    );
}

fn run_background_status<F>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation: F,
    core_ready: Option<bool>,
) where
    F: FnOnce(&mut AppState) -> Result<String, String> + Send + 'static,
{
    let refresh_state = Arc::clone(&state);
    run_background_with(
        ui,
        state,
        move |state| operation(state).map(|message| (message, core_ready)),
        move |window, ready: Option<bool>| {
            if ready.is_some() {
                refresh_core(&window.as_weak(), Arc::clone(&refresh_state));
            }
        },
    );
}

fn run_background_with<T, F, A>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation: F,
    apply: A,
) where
    T: Send + 'static,
    F: FnOnce(&mut AppState) -> Result<(String, T), String> + Send + 'static,
    A: FnOnce(&MainWindow, T) + Send + 'static,
{
    run_background_with_failure(ui, state, operation, apply, |_window, _error| {});
}

fn run_background_with_failure<T, F, A, E>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation: F,
    apply: A,
    on_failure: E,
) where
    T: Send + 'static,
    F: FnOnce(&mut AppState) -> Result<(String, T), String> + Send + 'static,
    A: FnOnce(&MainWindow, T) + Send + 'static,
    E: FnOnce(&MainWindow, &str) + Send + 'static,
{
    {
        let mut guard = state.lock().unwrap();
        if guard.busy {
            return;
        }
        guard.busy = true;
    }
    let weak = ui.clone();
    let _ = slint::invoke_from_event_loop({
        let weak = weak.clone();
        move || {
            if let Some(window) = weak.upgrade() {
                window.set_busy(true);
                window.set_busy_operation("Working...".into());
                window.set_message("Working...".into());
                window.set_message_kind("info".into());
            }
        }
    });
    thread::spawn(move || {
        let result = operation(&mut state.lock().unwrap());
        if let Err(ref error) = result {
            let mut guard = state.lock().unwrap();
            guard.diagnostic_error_class = diagnostic_error_class(error).to_owned();
            guard.diagnostic_operation = diagnostic_operation(error).to_owned();
        }
        state.lock().unwrap().busy = false;
        let _ = slint::invoke_from_event_loop(move || {
            if let Some(window) = weak.upgrade() {
                window.set_busy(false);
                window.set_busy_operation(SharedString::default());
                match result {
                    Ok((message, value)) => {
                        window.set_message(message.into());
                        window.set_message_kind("success".into());
                        apply(&window, value);
                    }
                    Err(error) => {
                        window.set_diagnostic_submitting(false);
                        if is_core_unavailable(&error) {
                            window.set_core_ready(false);
                            window.set_core_status("Core unavailable".into());
                            window.set_core_details(
                                "Core is installed but not accepting requests. Refresh or restart it."
                                    .into(),
                            );
                        }
                        window.set_message(friendly_error(&error).into());
                        window.set_message_kind("error".into());
                        show_diagnostic_consent(&window, "core", "error");
                        on_failure(&window, &error);
                    }
                }
            }
        });
    });
}

fn ensure_core_ready(state: &AppState) -> Result<(), String> {
    let status = core_status(state)?;
    if status.ready {
        Ok(())
    } else {
        Err("core_unavailable".to_owned())
    }
}

fn validate_text(value: &str, label: &str) -> Result<(), String> {
    if value.trim().is_empty() {
        return Err(format!("missing_{label}"));
    }
    Ok(())
}

fn set_feedback(ui: &slint::Weak<MainWindow>, message: String, kind: &'static str) {
    let weak = ui.clone();
    let _ = slint::invoke_from_event_loop(move || {
        if let Some(window) = weak.upgrade() {
            window.set_message(message.into());
            window.set_message_kind(kind.into());
            if kind == "error" || kind == "warning" {
                show_diagnostic_consent(
                    &window,
                    "ui",
                    if kind == "warning" {
                        "warning"
                    } else {
                        "error"
                    },
                );
            }
        }
    });
}

fn friendly_error(error: &str) -> String {
    let value = error.to_ascii_lowercase();
    for (code, message) in [
        ("core_stop_required", "Stop Core after all pools finish slot cleanup, then save the mode."),
        ("pool_cleanup_required", "Scale every pool to 0 and wait for all slots and leases to finish cleanup before stopping Core."),
        ("signed_environment_required", "Apply this pool with an installed, verified, trusted and healthy signed environment, then refresh before stopping Core."),
        ("environment_unavailable", "The signed environment runtime could not be verified. Start Core and check its environment gates."),
        ("core_config_invalid", "Core configuration is invalid. The existing file was preserved; repair the deployment configuration before starting Core."),
        ("windows_required", "Windows user pools can only be enabled on Windows."),
        ("pool_mode_save_failed", "The pool mode could not be saved. Refresh Core status and try again."),
    ] {
        if value.contains(code) { return message.to_owned(); }
    }
    if value.contains("core_capability_mismatch") || value.contains("core update required") {
        return "Installed Core is older than this UI. Reinstall Core to repair the shared Core payload.".to_owned();
    }
    if value.contains("package_unavailable") {
        return "The service-owned environment package is unavailable. Choose an approved package reference and try again.".to_owned();
    }
    if value.contains("environment_untrusted") || value.contains("not trusted") {
        return "The environment is not trusted. Trust it through the service before enabling the pool.".to_owned();
    }
    if value.contains("environment_unverified") || value.contains("not verified") {
        return "The environment has not passed verification. Verify it through the service before enabling the pool.".to_owned();
    }
    if value.contains("environment_unhealthy") || value.contains("not healthy") {
        return "The environment health gate is not ready. Check the environment and try again."
            .to_owned();
    }
    if value.contains("service_restarted") {
        return "Core restarted while this operation was running. Refresh the projection before retrying.".to_owned();
    }
    if value.contains("slot_unavailable") {
        return "No available execution slot was found in this pool. Refresh the pool and try again.".to_owned();
    }
    if value.contains("session_unavailable") {
        return "The execution slot session is unavailable. Refresh the operation and try again."
            .to_owned();
    }
    if value.contains("stale_revision")
        || value.contains("stale revision")
        || value.contains("conflict")
    {
        return "The item changed before this operation was applied. Refresh the projection and try again.".to_owned();
    }
    if value.contains("operation_timeout") {
        return "The operation is still running. Use its operation ID to check the final status."
            .to_owned();
    }
    if value.contains("timeout") {
        return "The basic session did not become ready before the timeout. Refresh the operation and try again.".to_owned();
    }
    if value.contains("invalid_operation_projection") {
        return "Core returned an operation status this client could not read. Refresh and try again.".to_owned();
    }
    if value.contains("browser_view_unavailable") {
        return "A read-only browser preview is not available for this request.".to_owned();
    }
    if value.contains("invalid_session_projection") {
        return "Core returned a request list this client could not read. Refresh and try again."
            .to_owned();
    }
    if value.contains("unsupported_session_action") {
        return "That action is not available for the selected request.".to_owned();
    }
    if value.contains("missing_request") {
        return "Enter an ID before trying this action.".to_owned();
    }
    if value.contains("not_found") || value.contains("not found") {
        return "Core could not find that item. Check the ID and try again.".to_owned();
    }
    if value.contains("core_not_installed") || value.contains("launcher is missing") {
        return "Core is not installed. Install Core, then try again.".to_owned();
    }
    if value.contains("core_start_timeout")
        || value.contains("connect core")
        || value.contains("unavailable")
    {
        return "Core is unavailable. Start Core and refresh its status.".to_owned();
    }
    if value.contains("forbidden") || value.contains("not allowed") {
        return "Core did not allow this operation.".to_owned();
    }
    if value.contains("conflict") || value.contains("already") {
        return "The item changed while this was running. Refresh and try again.".to_owned();
    }
    if value.contains("theme preference") {
        return "Theme preference could not be saved. The current appearance remains active."
            .to_owned();
    }
    "The operation could not be completed. Refresh and try again.".to_owned()
}

fn is_core_unavailable(error: &str) -> bool {
    let value = error.to_ascii_lowercase();
    value.contains("core_capability_mismatch")
        || value.contains("core_unavailable")
        || value.contains("core unavailable")
        || value.contains("core: unavailable")
        || value.contains("connect core pipe")
        || value.contains("core_start_timeout")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(unix)]
    #[test]
    fn slot_session_callbacks_use_launcher_polling_and_refresh() {
        use slint::Model;
        use std::cell::Cell;
        use std::os::unix::fs::PermissionsExt;
        use std::rc::Rc;
        use std::time::Instant;

        i_slint_backend_testing::init_integration_test_with_system_time();
        let root = std::env::temp_dir().join(format!(
            "chuzi-slot-ui-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&root).unwrap();
        let state = Arc::new(Mutex::new(AppState {
            data_root: root.clone(),
            payload_root: root.clone(),
            ..AppState::default()
        }));
        let launcher = state.lock().unwrap().launcher_path();
        fs::write(
            &launcher,
            include_str!("../tests/fixtures/slot_session_launcher.py"),
        )
        .unwrap();
        fs::set_permissions(&launcher, fs::Permissions::from_mode(0o700)).unwrap();
        fs::write(root.join("scenario"), "ready").unwrap();
        let ui = MainWindow::new().unwrap();
        connect_callbacks(&ui, Arc::clone(&state));
        let confirm = |window: &MainWindow| {
            window.set_pending_operation("start-slot-session".into());
            window.set_pending_pool_id("pool-a".into());
            window.set_operation_confirmation_visible(true);
            window.invoke_operation_confirmed();
            assert!(!window.get_operation_confirmation_visible());
        };
        confirm(&ui);
        // A rejected action must leave the active operation's refresh route alone.
        ui.invoke_job_pool_operation("unrelated-pool-operation".into());
        let stage = Rc::new(Cell::new(0));
        let stage_for_timer = Rc::clone(&stage);
        let weak = ui.as_weak();
        let state_for_timer = Arc::clone(&state);
        let fixture_root = root.clone();
        let began = Instant::now();
        let timer = slint::Timer::default();
        timer.start(
            slint::TimerMode::Repeated,
            Duration::from_millis(20),
            move || {
                assert!(
                    began.elapsed() < Duration::from_secs(20),
                    "UI callback fixture timed out"
                );
                let window = weak.upgrade().unwrap();
                if window.get_busy() || state_for_timer.lock().unwrap().busy {
                    return;
                }
                let expected = match stage_for_timer.get() {
                    0..=2 => "ready",
                    3..=4 => "failed",
                    5 => "stale revision",
                    _ => "slot unavailable",
                };
                if window.get_operation_phase() != expected {
                    return;
                }
                assert_eq!(window.get_operation_kind(), "slot-session");
                assert_eq!(window.get_session_rows().row_count(), 0);
                match stage_for_timer.get() {
                    0 => {
                        assert!(window
                            .get_operation_detail()
                            .contains("Windows Agent ready"));
                        assert!(!window.get_operation_detail().contains("idempotent"));
                        confirm(&window);
                    }
                    1 => {
                        assert!(window.get_operation_detail().contains("idempotent"));
                        window.invoke_refresh_operation(window.get_operation_id());
                    }
                    2 => {
                        assert!(window.get_operation_detail().contains("idempotent"));
                        fs::write(fixture_root.join("scenario"), "failed").unwrap();
                        confirm(&window);
                    }
                    3 => {
                        assert!(window.get_operation_detail().contains("slot_quarantined"));
                        assert!(!window.get_operation_detail().contains("idempotent"));
                        window.invoke_refresh_operation(window.get_operation_id());
                    }
                    4 => {
                        fs::write(fixture_root.join("scenario"), "stale_revision").unwrap();
                        confirm(&window);
                    }
                    5 => {
                        assert!(window.get_operation_detail().contains("changed"));
                        fs::write(fixture_root.join("scenario"), "slot_unavailable").unwrap();
                        confirm(&window);
                    }
                    _ => {
                        slint::quit_event_loop().unwrap();
                    }
                }
                stage_for_timer.set(stage_for_timer.get() + 1);
            },
        );
        slint::run_event_loop().unwrap();
        timer.stop();
        assert_eq!(stage.get(), 7);
        let calls = fs::read_to_string(root.join("calls.jsonl")).unwrap();
        let calls = calls
            .lines()
            .map(|line| serde_json::from_str::<Value>(line).unwrap())
            .collect::<Vec<_>>();
        assert!(!calls
            .iter()
            .any(|call| call["method"] == "get_job_pool_operation"));
        let starts = calls
            .iter()
            .filter(|call| call["method"] == "start_slot_session")
            .collect::<Vec<_>>();
        assert_eq!(starts.len(), 5);
        assert_eq!(starts[0]["params"], starts[1]["params"]);
        for call in starts {
            let params = call["params"].as_object().unwrap();
            assert_eq!(params.len(), 4);
            assert_eq!(params["pool_id"], "pool-a");
            assert_eq!(params["expected_revision"], 7);
            assert_eq!(params["actor"], "windows-ui");
            assert!(params["idempotency_key"]
                .as_str()
                .unwrap()
                .starts_with("windows-ui-start_slot_session-"));
        }
        fs::remove_dir_all(root).unwrap();
        drop(ui);
        pool_mode_confirmation_saves_offline_and_rejects_unsafe_states();
    }

    #[test]
    fn slot_session_strings_are_redacted_and_retry_is_scoped_to_operation() {
        let state = AppState::default();
        let value = json!({"operation": {
            "operation_id": "op-one", "state": "failed", "idempotent": true,
            "failure_code": "password=secret",
            "status": {"status": "C:\\private\\profile", "session_state": "cookie=secret"}
        }});
        let first = slot_session_projection(&state, value.clone()).unwrap();
        assert_eq!(first.2, "session_failed");
        assert!(first.3.contains("status unknown"));
        assert!(first.3.contains("session unknown"));
        assert!(!first.3.contains("secret"));
        let mut refresh = value;
        refresh["operation"]["idempotent"] = json!(false);
        assert!(slot_session_projection(&state, refresh.clone())
            .unwrap()
            .3
            .contains("idempotent"));
        refresh["operation"]["operation_id"] = json!("op-two");
        refresh["operation"]["state"] = json!("password=secret");
        let changed = slot_session_projection(&state, refresh).unwrap();
        assert_eq!(changed.0, "failed");
        assert_eq!(changed.2, "invalid_operation_projection");
        assert!(!changed.3.contains("idempotent"));
    }

    #[test]
    fn theme_values_fail_closed_to_system() {
        assert_eq!(normalize_theme("dark"), "dark");
        assert_eq!(normalize_theme(" LIGHT "), "light");
        assert_eq!(normalize_theme("unknown"), "system");
    }

    #[test]
    fn user_errors_are_classified_without_internal_details() {
        assert_eq!(
            friendly_error("not_found: request"),
            "Core could not find that item. Check the ID and try again."
        );
        assert!(is_core_unavailable("core_unavailable"));
        assert!(is_core_unavailable("chuzi core: unavailable"));
        assert!(is_core_unavailable("core_capability_mismatch"));
        assert_eq!(
            diagnostic_error_class("core_capability_mismatch"),
            "core_capability_mismatch"
        );
        assert!(friendly_error("core_capability_mismatch").contains("older"));
        assert!(!is_core_unavailable("not_found: request"));
        assert_eq!(
            friendly_error("connect Core pipe: C:\\private\\data"),
            "Core is unavailable. Start Core and refresh its status."
        );
        assert_eq!(
            friendly_error("unexpected stack and profile path"),
            "The operation could not be completed. Refresh and try again."
        );
    }

    #[test]
    fn core_method_names_are_fixed_and_package_refs_are_not_paths() {
        assert_eq!(CoreMethod::ListJobPools.wire_name(), "list_job_pools");
        assert_eq!(CoreMethod::DeleteJobPool.wire_name(), "delete_job_pool");
        assert_eq!(
            CoreMethod::StartSlotSession.wire_name(),
            "start_slot_session"
        );
        assert_eq!(
            CoreMethod::GetSlotSessionOperation.wire_name(),
            "get_slot_session_operation"
        );
        assert_eq!(
            CoreMethod::EnvironmentOperation.wire_name(),
            "environment_operation"
        );
        assert!(launcher_command_needs_manifest("settings"));
        assert!(!launcher_command_needs_manifest("core-call"));
        assert!(!launcher_command_needs_manifest("environment-list"));
        assert!("../package".contains('/'));
        assert!(r"C:\\package".contains('\\'));
    }

    #[cfg(unix)]
    fn pool_mode_confirmation_saves_offline_and_rejects_unsafe_states() {
        use std::cell::Cell;
        use std::os::unix::fs::PermissionsExt;
        use std::rc::Rc;
        use std::time::Instant;

        let root = std::env::temp_dir().join(format!(
            "chuzi-pool-mode-ui-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&root).unwrap();
        let state = Arc::new(Mutex::new(AppState {
            data_root: root.clone(),
            payload_root: root.clone(),
            ..AppState::default()
        }));
        let launcher = state.lock().unwrap().launcher_path();
        fs::write(
            &launcher,
            include_str!("../tests/fixtures/pool_mode_launcher.py"),
        )
        .unwrap();
        fs::set_permissions(&launcher, fs::Permissions::from_mode(0o700)).unwrap();
        fs::write(root.join("scenario"), "saved").unwrap();
        let ui = MainWindow::new().unwrap();
        connect_callbacks(&ui, Arc::clone(&state));
        let confirm = |window: &MainWindow, mode: &str| {
            window.set_pending_operation(format!("pool-mode:{mode}").into());
            window.set_pending_pool_id("pool-a".into());
            window.set_operation_confirmation_visible(true);
            window.invoke_operation_confirmed();
            assert!(!window.get_operation_confirmation_visible());
        };
        ui.set_core_status_known(true);
        ui.set_core_installed(true);
        ui.set_core_running(true);
        confirm(&ui, "windows");
        assert_eq!(ui.get_pool_mode_phase(), "failed");
        assert!(ui.get_pool_mode_detail().contains("Stop Core"));
        ui.set_core_running(false);
        ui.set_core_status_known(false);
        confirm(&ui, "windows");
        assert!(!root.join("calls.jsonl").exists());
        ui.set_pool_mode_phase("idle".into());
        state.lock().unwrap().busy = true;
        confirm(&ui, "windows");
        assert_eq!(ui.get_pool_mode_phase(), "idle");
        assert!(!root.join("calls.jsonl").exists());
        state.lock().unwrap().busy = false;
        ui.set_core_status_known(true);
        ui.set_job_pool_rows(ModelRc::from(
            [JobPoolRowData {
                pool_id: "pool-a".into(),
                config_revision: "7".into(),
                ..Default::default()
            }]
            .as_slice(),
        ));
        confirm(&ui, "windows");
        let stage = Rc::new(Cell::new(0));
        let timer_stage = Rc::clone(&stage);
        let weak = ui.as_weak();
        let fixture_root = root.clone();
        let timer_state = Arc::clone(&state);
        let began = Instant::now();
        let timer = slint::Timer::default();
        timer.start(
            slint::TimerMode::Repeated,
            Duration::from_millis(20),
            move || {
                assert!(
                    began.elapsed() < Duration::from_secs(20),
                    "mode callback timed out"
                );
                let window = weak.upgrade().unwrap();
                if window.get_busy() || timer_state.lock().unwrap().busy {
                    return;
                }
                let expected = if timer_stage.get() == 0 || timer_stage.get() == 5 {
                    "restart required"
                } else {
                    "failed"
                };
                if window.get_pool_mode_phase() != expected {
                    return;
                }
                match timer_stage.get() {
                    0 => {
                        assert_eq!(window.get_configured_pool_mode(), "windows");
                        fs::write(fixture_root.join("scenario"), "pool_cleanup_required").unwrap();
                        confirm(&window, "windows");
                    }
                    1 => {
                        assert!(window
                            .get_pool_mode_detail()
                            .contains("Scale every pool to 0"));
                        assert_eq!(window.get_configured_pool_mode(), "windows");
                        fs::write(fixture_root.join("scenario"), "running").unwrap();
                        confirm(&window, "windows");
                    }
                    2 => {
                        assert!(window.get_pool_mode_detail().contains("Stop Core"));
                        fs::write(fixture_root.join("scenario"), "saved").unwrap();
                        window.set_job_pool_rows(ModelRc::default());
                        confirm(&window, "windows");
                    }
                    3 => {
                        assert!(window.get_pool_mode_detail().contains("changed"));
                        fs::write(fixture_root.join("scenario"), "malformed").unwrap();
                        confirm(&window, "logical");
                    }
                    4 => {
                        assert!(window.get_pool_mode_detail().contains("could not be saved"));
                        assert_eq!(window.get_configured_pool_mode(), "windows");
                        fs::write(fixture_root.join("scenario"), "saved").unwrap();
                        confirm(&window, "logical");
                    }
                    _ => {
                        assert_eq!(window.get_configured_pool_mode(), "logical");
                        assert!(!window.get_core_running());
                        slint::quit_event_loop().unwrap();
                    }
                }
                timer_stage.set(timer_stage.get() + 1);
            },
        );
        slint::run_event_loop().unwrap();
        timer.stop();
        assert_eq!(stage.get(), 6);
        let calls = fs::read_to_string(root.join("calls.jsonl"))
            .unwrap()
            .lines()
            .map(|line| serde_json::from_str::<Value>(line).unwrap())
            .collect::<Vec<_>>();
        assert!(calls.iter().all(|call| matches!(
            call["command"].as_str(),
            Some("core-status" | "core-pool-mode-save")
        )));
        let saves = calls
            .iter()
            .filter(|call| call["command"] == "core-pool-mode-save")
            .collect::<Vec<_>>();
        assert_eq!(saves.len(), 4);
        let args = saves[0]["args"].as_array().unwrap();
        for (flag, expected) in [
            ("-pool-mode", "windows"),
            ("-pool-id", "pool-a"),
            ("-expected-revision", "7"),
        ] {
            let index = args.iter().position(|arg| arg == flag).unwrap();
            assert_eq!(args[index + 1], expected);
        }
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn logical_and_unknown_mode_never_claim_windows_agent_readiness() {
        for mode in ["logical", "", "unexpected"] {
            let operation = decode_slot_session_operation(json!({"operation": {
                "state": "ready", "environment_generation": 1,
                "status": {"execution_mode": mode, "agent_ready": true,
                           "status": "ready", "environment_generation": 9}
            }}))
            .unwrap();
            let detail = format_slot_session_detail(&operation, false);
            assert!(!detail.contains("Windows Agent ready"));
            assert!(detail.contains("generation 9"));
        }
    }

    #[test]
    fn pool_apply_preserves_or_resolves_signed_environment_identity() {
        let mut pools: CoreJobPoolList = serde_json::from_value(json!({"job_pools": [{
            "config": {"pool_id": "pool-a", "environment_id": "env", "environment_version": "1",
                       "manifest_digest": "a".repeat(64), "signer": "signed-by"},
            "status": {"execution_mode": "windows", "desired": 0, "ready": 0, "leased": 0,
                       "quarantined": 0, "draining": 0, "provisioning": 0, "retiring": 0, "effective_capacity": 0}
        }]})).unwrap();
        let mut envs: CoreEnvironmentList = serde_json::from_value(json!({"environments": [{
            "environment_id": "env", "version": "2", "ready": true, "installed": true,
            "verified": true, "trusted": true, "enabled": true, "healthy": true,
            "manifest_digest": "b".repeat(64), "signer": "new-signer"
        }]}))
        .unwrap();
        assert_eq!(
            pool_environment_metadata(Some(&pools.job_pools[0]), &envs, "env", "1").unwrap(),
            ("a".repeat(64), "signed-by".to_owned())
        );
        assert_eq!(
            pool_environment_metadata(Some(&pools.job_pools[0]), &envs, "env", "2").unwrap(),
            ("b".repeat(64), "new-signer".to_owned())
        );
        envs.environments[0].healthy = false;
        assert_eq!(
            pool_environment_metadata(Some(&pools.job_pools[0]), &envs, "env", "2").unwrap_err(),
            "signed_environment_required"
        );
        pools.job_pools[0].config.manifest_digest.clear();
        pools.job_pools[0].config.signer.clear();
        pools.job_pools[0].status.execution_mode = "logical".to_owned();
        assert_eq!(
            pool_environment_metadata(Some(&pools.job_pools[0]), &envs, "basic", "1").unwrap(),
            (String::new(), String::new())
        );
    }

    #[test]
    fn operation_errors_use_stable_user_states() {
        assert_eq!(operation_error_phase("stale_revision"), "stale revision");
        assert_eq!(
            operation_error_phase("package_unavailable"),
            "package unavailable"
        );
        assert_eq!(
            operation_error_phase("environment_untrusted"),
            "environment untrusted"
        );
        assert_eq!(
            operation_error_phase("environment_unhealthy"),
            "environment unhealthy"
        );
        assert_eq!(operation_error_phase("core_unavailable"), "unavailable");
        assert_eq!(
            operation_error_phase("slot_unavailable"),
            "slot unavailable"
        );
        assert_eq!(
            operation_error_phase("session_unavailable"),
            "session unavailable"
        );
        assert_eq!(operation_error_phase("timeout"), "timeout");
        assert_eq!(
            operation_error_phase("service_restarted"),
            "service restarted"
        );
        assert!(friendly_error("package_unavailable").contains("package"));
        assert!(friendly_error("stale_revision").contains("changed"));
        assert!(friendly_error("slot_unavailable").contains("execution slot"));
        assert!(friendly_error("session_unavailable").contains("session"));
        assert!(!friendly_error("slot_unavailable: C:\\private\\profile").contains("profile"));
    }

    #[test]
    fn operation_projections_decode_core_envelopes() {
        let pool = decode_job_pool_operation(serde_json::json!({
            "operation": {"operation_id": "op-1", "state": "applied"}
        }))
        .expect("job-pool operation envelope should decode");
        assert_eq!(pool.operation_id, "op-1");
        assert_eq!(pool.state, "applied");
        let environment = decode_environment_operation(serde_json::json!({
            "operation": {"operation_id": "envop-1", "state": "failed"}
        }))
        .expect("environment operation envelope should decode");
        assert_eq!(environment.operation_id, "envop-1");
        assert_eq!(environment.state, "failed");
    }

    #[test]
    fn slot_session_operation_projection_carries_readiness_and_idempotency() {
        let operation = decode_slot_session_operation(serde_json::json!({
            "operation": {
                "operation_id": "slotop-1",
                "pool_id": "pool-a",
                "slot_id": "pool-a-001",
                "ordinal": 1,
                "state": "ready",
                "environment_generation": 7,
                "status": {
                    "status": "ready",
                    "session_state": "ready",
                    "execution_mode": "windows",
                    "environment_generation": 7,
                    "agent_ready": true
                },
                "idempotent": true
            }
        }))
        .expect("slot-session operation envelope should decode");
        let detail = format_slot_session_detail(&operation, false);
        assert!(detail.contains("slot #1"));
        assert!(detail.contains("status ready"));
        assert!(detail.contains("session ready"));
        assert!(detail.contains("Windows Agent ready"));
        assert!(detail.contains("idempotent"));
        assert!(is_terminal_operation("ready"));
    }

    #[test]
    fn slot_session_failure_detail_retains_redacted_readiness() {
        let operation = decode_slot_session_operation(serde_json::json!({
            "operation": {
                "operation_id": "slotop-2",
                "ordinal": 2,
                "state": "failed",
                "failure_code": "slot_quarantined",
                "environment_generation": 3,
                "status": {
                    "status": "quarantined",
                    "session_state": "quarantined",
                    "execution_mode": "windows",
                    "environment_generation": 3,
                    "agent_ready": false
                }
            }
        }))
        .expect("failed slot-session operation should decode");
        let detail = format_operation_detail(
            &operation.state,
            &operation.failure_code,
            &format_slot_session_detail(&operation, false),
        );
        assert!(detail.contains("slot_quarantined"));
        assert!(detail.contains("slot #2"));
        assert!(detail.contains("Windows Agent not ready"));
    }

    #[test]
    fn package_references_are_opaque_tokens() {
        assert!(valid_package_reference("catalog-v1"));
        assert!(!valid_package_reference("../package"));
        assert!(!valid_package_reference("C:package"));
        assert!(!valid_package_reference("catalog:v1"));
        assert!(!valid_package_reference("catalog v1"));
    }

    #[test]
    fn idempotency_keys_are_stable_shape_without_user_paths() {
        let key = idempotency_key("scale", "pool/one", 7, "4");
        let repeated = idempotency_key("scale", "pool/one", 7, "4");
        let changed = idempotency_key("scale", "pool/one", 7, "5");
        assert!(key.starts_with("windows-ui-scale-pool-one-7-"));
        assert_eq!(key, repeated);
        assert_ne!(key, changed);
        assert!(!key.contains('/'));
        assert!(!key.contains('\\'));
    }

    #[test]
    fn local_diagnostic_fields_are_bounded_and_typed() {
        let value = bounded_diagnostic_field("core\npassword=secret");
        assert_eq!(value, "user-visible diagnostic message redacted");
        let snapshot = DiagnosticSnapshot {
            schema: "chuzi.diagnostic/v2".to_owned(),
            id: "diag-1".to_owned(),
            created_at: "unix:1".to_owned(),
            source: "ui".to_owned(),
            version: "test".to_owned(),
            platform: "windows".to_owned(),
            arch: "amd64".to_owned(),
            severity: "warning".to_owned(),
            category: "ui".to_owned(),
            summary: "safe".to_owned(),
            error_class: "ui_operation_failed".to_owned(),
            operation: "ui_operation".to_owned(),
            event_count: 0,
            events_truncated: false,
            capture_error_class: String::new(),
            capture_error_code: String::new(),
            capture_error_size: 0,
            capture_error_fingerprint: String::new(),
            capture_attempts: 0,
            legacy_retry_attempted: false,
            capture_request_shape: String::new(),
            capture_initial_error_class: String::new(),
            capture_initial_error_code: String::new(),
            capture_initial_error_size: 0,
            capture_initial_error_fingerprint: String::new(),
            capture_stage: String::new(),
            core_schema: String::new(),
            core_version: String::new(),
            core_status_error_class: String::new(),
            core_capability_status: String::new(),
            core_capability_error_code: String::new(),
            core_capability_error_fingerprint: String::new(),
            core_protocol_version: String::new(),
            core_method_supported: None,
            core_supported_methods: Vec::new(),
            response_kind: String::new(),
            response_size: 0,
            response_key_count: 0,
            response_fields: Vec::new(),
            response_fingerprint: String::new(),
            capture_duration_ms: 0,
            client_version: String::new(),
            core_status: DiagnosticCoreStatus::default(),
            events: Vec::new(),
        };
        let encoded = serde_json::to_string(&snapshot).expect("snapshot should encode");
        assert!(!encoded.contains("password"));
        assert!(!encoded.contains("Profile"));
    }

    fn valid_diagnostic_response() -> Value {
        json!({
            "schema": "chuzi.diagnostic/v2",
            "id": "diag-1",
            "created_at": "2026-10-07T00:00:00Z",
            "source": "core",
            "version": "test",
            "platform": "windows",
            "arch": "amd64",
            "severity": "error",
            "category": "core",
            "summary": "Core failed",
            "error_class": "internal",
            "operation": "core_call",
            "event_count": 0,
            "events_truncated": false,
            "core_status": {"installed": true, "running": true, "ready": true, "status": "ready"},
            "events": []
        })
    }

    #[test]
    fn diagnostic_response_missing_schema_is_classified() {
        let capture = inspect_diagnostic_response(json!({"id": "opaque", "result": {}}));
        assert_eq!(capture.capture_error_class, "missing_schema");
        assert_eq!(capture.capture_stage, "schema_validation");
        assert_eq!(capture.response_kind, "object");
        assert_eq!(capture.response_key_count, 2);
        assert!(capture.response_fingerprint.starts_with("fnv1a64:"));
    }

    #[test]
    fn diagnostic_response_shape_and_schema_type_are_classified() {
        let array_capture = inspect_diagnostic_response(json!([]));
        assert_eq!(array_capture.capture_error_class, "malformed_response");
        assert_eq!(array_capture.response_kind, "array");
        let schema_capture = inspect_diagnostic_response(json!({"schema": null}));
        assert_eq!(schema_capture.capture_error_class, "malformed_schema");
        assert_eq!(schema_capture.core_schema, "non_string");
    }

    #[test]
    fn diagnostic_response_old_schema_is_classified_without_raw_payload() {
        let capture = inspect_diagnostic_response(
            json!({"schema": "chuzi.diagnostic/v1", "version": "nightly-42", "summary": "secret-token"}),
        );
        assert_eq!(capture.capture_error_class, "unsupported_schema");
        assert_eq!(capture.core_schema, "chuzi.diagnostic/v1");
        assert_eq!(capture.core_version, "nightly-42");
        assert!(!capture.response_fingerprint.contains("secret-token"));
        assert!(capture.response_fingerprint.starts_with("fnv1a64:"));
    }

    #[test]
    fn diagnostic_response_malformed_v2_is_classified() {
        let mut response = valid_diagnostic_response();
        response["event_count"] = json!("not-a-number");
        let capture = inspect_diagnostic_response(response);
        assert_eq!(capture.capture_error_class, "malformed_snapshot");
        assert_eq!(capture.capture_stage, "snapshot_decode");
        assert!(capture.snapshot.is_none());
    }

    #[test]
    fn diagnostic_response_missing_required_status_is_classified() {
        let mut response = valid_diagnostic_response();
        response["core_status"] = json!({"installed": true, "running": true, "ready": true});
        let capture = inspect_diagnostic_response(response);
        assert_eq!(capture.capture_error_class, "malformed_snapshot");
        assert!(capture.snapshot.is_none());
    }

    #[test]
    fn diagnostic_response_valid_v2_is_accepted_and_fingerprinted() {
        let capture = inspect_diagnostic_response(valid_diagnostic_response());
        let snapshot = capture.snapshot.expect("valid snapshot");
        assert_eq!(capture.capture_stage, "complete");
        assert_eq!(capture.core_schema, "chuzi.diagnostic/v2");
        assert_eq!(capture.core_version, "test");
        assert_eq!(capture.response_kind, "object");
        assert!(capture
            .response_fields
            .iter()
            .any(|field| field == "schema"));
        assert!(capture.response_fingerprint.starts_with("fnv1a64:"));
        assert!(!capture.response_fingerprint.contains(&snapshot.summary));
    }

    #[test]
    fn diagnostic_legacy_params_keep_unicode_summary_and_drop_additive_fields() {
        let params = json!({
            "severity": "error",
            "category": "core",
            "summary": "Core 操作出现问题。可以保存一份脱敏诊断文件交给支持人员。",
            "error_class": "invalid_projection",
            "operation": "ui_operation",
        });
        let legacy = diagnostic_legacy_params(&params);
        assert_eq!(
            legacy,
            json!({
                "severity": "error",
                "category": "core",
                "summary": "Core 操作出现问题。可以保存一份脱敏诊断文件交给支持人员。",
            })
        );
    }

    #[test]
    fn diagnostic_legacy_response_is_normalized_to_v2_without_raw_error_text() {
        let status = DiagnosticCoreStatus {
            installed: true,
            running: true,
            ready: true,
            status: "ready".to_owned(),
            protocol: "chuzi.core/v1".to_owned(),
            capability_status: "supported".to_owned(),
        };
        let response = normalize_legacy_diagnostic_response(
            json!({
                "id": "diag-1",
                "created_at": "2026-10-08T00:00:00Z",
                "version": "0.1.0",
                "platform": "windows",
                "arch": "amd64",
                "severity": "error",
                "category": "core",
                "summary": "Core 操作出现问题。",
                "events": [],
            }),
            &status,
            "invalid_projection",
            "ui_operation",
        );
        assert_eq!(response["schema"], "chuzi.diagnostic/v2");
        assert_eq!(response["source"], "core");
        assert_eq!(response["error_class"], "invalid_projection");
        assert_eq!(response["operation"], "ui_operation");
        assert_eq!(response["core_status"]["status"], "ready");
        assert!(!response.to_string().contains("password"));
    }

    #[test]
    fn diagnostic_capabilities_record_method_support_without_raw_payload() {
        let capabilities = parse_diagnostic_capabilities(
            json!({
                "version": "chuzi.core/v1",
                "methods": ["get_diagnostic_snapshot", "hello", "bad method"]
            }),
            br#"{\"version\":\"chuzi.core/v1\",\"methods\":[] }"#,
        );
        assert_eq!(capabilities.status, "supported");
        assert_eq!(capabilities.protocol_version, "chuzi.core/v1");
        assert_eq!(capabilities.method_supported, Some(true));
        assert_eq!(
            capabilities.supported_methods,
            vec!["get_diagnostic_snapshot".to_owned(), "hello".to_owned()]
        );
        assert!(!capabilities.error_fingerprint.contains("methods"));
    }

    #[test]
    fn diagnostic_capabilities_distinguish_unsupported_and_malformed_hello() {
        let unsupported = parse_diagnostic_capabilities(
            json!({"version": "chuzi.core/v1", "methods": ["hello"]}),
            b"safe hello",
        );
        assert_eq!(unsupported.status, "unsupported");
        assert_eq!(unsupported.method_supported, Some(false));

        let malformed = parse_diagnostic_capabilities(
            json!({"version": "chuzi.core/v1"}),
            b"secret hello response",
        );
        assert_eq!(malformed.status, "malformed_response");
        assert_eq!(malformed.error_code, "malformed_hello");
        assert!(malformed.error_fingerprint.starts_with("fnv1a64:"));
        assert!(!malformed.error_fingerprint.contains("secret"));
    }

    #[test]
    fn diagnostic_capture_metadata_keeps_retry_and_capability_facts() {
        let mut target = inspect_diagnostic_response(valid_diagnostic_response());
        let mut source =
            diagnostic_call_failure("core_method_error", "chuzi core: invalid_argument", 0, &[]);
        source.capture_attempts = 2;
        source.legacy_retry_attempted = true;
        source.capture_request_shape = "legacy".to_owned();
        source.capture_initial_error_code = "invalid_argument".to_owned();
        source.core_capability_status = "supported".to_owned();
        source.core_protocol_version = CORE_PROTOCOL_VERSION.to_owned();
        source.core_method_supported = Some(true);
        source.core_supported_methods = vec![DIAGNOSTIC_METHOD.to_owned()];
        target.merge_call_metadata(source);
        assert_eq!(target.capture_attempts, 2);
        assert!(target.legacy_retry_attempted);
        assert_eq!(target.capture_request_shape, "legacy");
        assert_eq!(target.capture_initial_error_code, "invalid_argument");
        assert_eq!(target.core_capability_status, "supported");
        assert_eq!(target.core_method_supported, Some(true));
        assert_eq!(target.core_supported_methods, vec![DIAGNOSTIC_METHOD]);
    }

    #[test]
    fn diagnostic_capture_errors_use_stable_classes() {
        assert_eq!(
            diagnostic_capture_error_class("Core projection: expected object"),
            "malformed_response"
        );
        assert_eq!(
            diagnostic_capture_error_class("core transport: invalid request"),
            "core_method_error"
        );
        assert_eq!(
            diagnostic_capture_error_class("chuzi core: internal"),
            "core_method_error"
        );
        assert_eq!(
            diagnostic_capture_error_class("chuzi core: unavailable"),
            "core_method_error"
        );
        assert_eq!(
            diagnostic_capture_error_code("chuzi core: unavailable"),
            "unavailable"
        );
        assert_eq!(
            diagnostic_capture_error_code("core transport: invalid request"),
            "transport_error"
        );
        assert_eq!(
            diagnostic_capture_error_code("unsupported_method"),
            "unsupported_method"
        );
        let failure = diagnostic_call_failure(
            "malformed_response",
            "malformed_json",
            17,
            b"secret payload",
        );
        assert_eq!(failure.response_kind, "invalid_json");
        assert_eq!(failure.response_size, 17);
        assert!(failure.response_fingerprint.starts_with("fnv1a64:"));
        assert!(!failure.response_fingerprint.contains("secret payload"));
        assert_eq!(failure.capture_error_code, "malformed_json");
        assert!(failure.capture_error_fingerprint.starts_with("fnv1a64:"));
        assert_eq!(failure.capture_error_size, "malformed_json".len());
    }
}
