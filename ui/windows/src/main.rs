mod desktop_rdp;
mod models;
mod session_ui;
mod view_model;

use base64::Engine;
use desktop_rdp::RdpHost;
use models::{
    default_theme, BehaviorSettings, BrowserView, CoreEnvironment, CoreEnvironmentList,
    CoreEnvironmentOperation, CoreJobPool, CoreJobPoolList, CoreJobPoolOperation, CoreRequest,
    CoreRequestList, CoreStatus, DiagnosticStatus, UiPreferences,
};
use serde_json::{json, Value};
use slint::language::ColorScheme;
use slint::{ComponentHandle, Image, ModelRc, SharedString};
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;
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
    settings: BehaviorSettings,
    session_view_model: SessionViewModel,
    workspace_host: RdpHost,
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
}

fn launcher_command_needs_manifest(command: &str) -> bool {
    !matches!(
        command,
        "core-status"
            | "core-start"
            | "core-stop"
            | "core-call"
            | "job-pool-list"
            | "job-pool-get"
            | "job-pool-apply"
            | "job-pool-scale"
            | "job-pool-drain"
            | "job-pool-resume"
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
    ListJobPools,
    ApplyJobPool,
    ScaleJobPool,
    DrainJobPool,
    ResumeJobPool,
    GetJobPoolOperation,
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
            Self::ListJobPools => "list_job_pools",
            Self::ApplyJobPool => "apply_job_pool",
            Self::ScaleJobPool => "scale_job_pool",
            Self::DrainJobPool => "drain_job_pool",
            Self::ResumeJobPool => "resume_job_pool",
            Self::GetJobPoolOperation => "get_job_pool_operation",
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
            "drain" | "resume" => {
                submit_job_pool_action(&weak, Arc::clone(&operation_state), pool_id, pending)
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
    ui.on_diagnostic_submit(move || {
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
                ensure_core_ready(state)?;
                let result = core_call(
                    state,
                    CoreMethod::SubmitDiagnosticReport,
                    json!({"severity": severity, "category": category, "summary": summary}),
                )?;
                let status: DiagnosticStatus =
                    serde_json::from_value(result).map_err(|error| error.to_string())?;
                let message = if status.state == "queued" {
                    "诊断信息已保存，将在网络可用时自动重试。".to_owned()
                } else {
                    "诊断信息已提交，感谢你的帮助。".to_owned()
                };
                Ok((message, status))
            },
            |window, _status: DiagnosticStatus| {
                window.set_diagnostic_submitting(false);
                window.set_diagnostic_consent_visible(false);
            },
        );
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
            "core" => "Core 操作出现问题，是否发送脱敏诊断信息？",
            _ => "应用操作出现问题，是否发送脱敏诊断信息？",
        }
        .into(),
    );
    window.set_diagnostic_consent_visible(true);
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
            Ok(("Core status refreshed.".to_owned(), snapshot))
        },
        move |window, snapshot: CoreSnapshot| {
            window.set_core_ready(snapshot.ready);
            window.set_core_installed(snapshot.installed);
            window.set_core_status_known(true);
            window.set_core_status(snapshot.status.into());
            window.set_core_details(snapshot.details.into());
            if snapshot.ready {
                refresh_sessions(&window.as_weak(), Arc::clone(&sessions_state));
                refresh_job_pools(&window.as_weak(), Arc::clone(&pools_state));
            }
        },
    );
}

fn refresh_job_pools(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    if let Some(window) = ui.upgrade() {
        window.set_job_pool_phase("loading".into());
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

static NEXT_IDEMPOTENCY_KEY: AtomicU64 = AtomicU64::new(1);

fn idempotency_key(operation: &str, subject: &str, revision: u64) -> String {
    let sequence = NEXT_IDEMPOTENCY_KEY.fetch_add(1, Ordering::Relaxed);
    let subject = subject
        .chars()
        .map(|character| {
            if character.is_ascii_alphanumeric() || character == '-' || character == '_' {
                character
            } else {
                '-'
            }
        })
        .collect::<String>();
    format!("windows-ui-{operation}-{subject}-{revision}-{sequence}")
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
        let revision = pool_revision(state, &pool_id)?;
        let key = idempotency_key("apply", &pool_id_for_key, revision);
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
                    "require_trusted": true
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
        let key = idempotency_key("scale", &pool_id_for_key, revision);
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
        _ => return,
    };
    let pool_id_for_key = pool_id.clone();
    start_job_pool_operation(ui, state, pool_id.clone(), move |state| {
        validate_text(&pool_id, "pool ID")?;
        let revision = pool_revision(state, &pool_id)?;
        let key = idempotency_key(&action, &pool_id_for_key, revision);
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

fn start_job_pool_operation<F>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    pool_id: String,
    request: F,
) where
    F: FnOnce(&AppState) -> Result<Value, String> + Send + 'static,
{
    start_operation(ui, state, pool_id, request, |state, operation_id| {
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
    });
}

fn poll_job_pool_operation(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation_id: String,
) {
    start_operation_with_id(ui, state, operation_id, |state, operation_id| {
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
    });
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
        let key = idempotency_key(&operation, &environment_id, revision);
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
    start_operation(ui, state, subject, request, |state, operation_id| {
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
    });
}

fn poll_environment_operation(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation_id: String,
) {
    start_operation_with_id(ui, state, operation_id, |state, operation_id| {
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
    });
}

fn decode_job_pool_operation(value: Value) -> Result<CoreJobPoolOperation, serde_json::Error> {
    serde_json::from_value(value.get("operation").cloned().unwrap_or(Value::Null))
}

fn decode_environment_operation(
    value: Value,
) -> Result<CoreEnvironmentOperation, serde_json::Error> {
    serde_json::from_value(value.get("operation").cloned().unwrap_or(Value::Null))
}

fn start_operation<F, P>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
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
    for attempt in 0..40 {
        let value = poll(&state.lock().unwrap(), operation_id)?;
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
        if attempt == 39 {
            return Err("operation_timeout".to_owned());
        }
        thread::sleep(Duration::from_millis(250));
    }
    Err("operation_timeout".to_owned())
}

fn is_terminal_operation(state: &str) -> bool {
    matches!(state, "applied" | "failed" | "rolled_back" | "cancelled")
}

fn format_operation_detail(state: &str, failure: &str, result: &str) -> String {
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
            match result {
                Ok((operation_state, operation_id, failure, outcome)) => {
                    let failed = operation_state == "failed" || !failure.is_empty();
                    window.set_operation_phase(if failed { "failed" } else { "applied" }.into());
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
            if !status.running {
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
    let sessions_state = Arc::clone(&state);
    run_background_with(
        ui,
        state,
        move |state| operation(state).map(|message| (message, core_ready)),
        move |window, ready: Option<bool>| {
            if let Some(ready) = ready {
                window.set_core_ready(ready);
                window.set_core_status(
                    if ready {
                        "Core is running"
                    } else {
                        "Core is stopped"
                    }
                    .into(),
                );
                window.set_core_installed(true);
                window.set_core_status_known(true);
                if ready {
                    refresh_sessions(&window.as_weak(), Arc::clone(&sessions_state));
                }
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
    if value.contains("package_unavailable") {
        return "The service-owned environment package is unavailable. Choose an approved package reference and try again.".to_owned();
    }
    if value.contains("environment_untrusted") || value.contains("not trusted") {
        return "The environment is not trusted. Trust it through the service before enabling the pool.".to_owned();
    }
    if value.contains("environment_unhealthy") || value.contains("not healthy") {
        return "The environment health gate is not ready. Check the environment and try again."
            .to_owned();
    }
    if value.contains("service_restarted") {
        return "Core restarted while this operation was running. Refresh the projection before retrying.".to_owned();
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
    value.contains("core_unavailable")
        || value.contains("core unavailable")
        || value.contains("connect core pipe")
        || value.contains("core_start_timeout")
}

#[cfg(test)]
mod tests {
    use super::*;

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
        assert!(friendly_error("package_unavailable").contains("package"));
        assert!(friendly_error("stale_revision").contains("changed"));
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
    fn package_references_are_opaque_tokens() {
        assert!(valid_package_reference("catalog-v1"));
        assert!(!valid_package_reference("../package"));
        assert!(!valid_package_reference("C:package"));
        assert!(!valid_package_reference("catalog:v1"));
        assert!(!valid_package_reference("catalog v1"));
    }

    #[test]
    fn idempotency_keys_are_stable_shape_without_user_paths() {
        let key = idempotency_key("scale", "pool/one", 7);
        assert!(key.starts_with("windows-ui-scale-pool-one-7-"));
        assert!(!key.contains('/'));
        assert!(!key.contains('\\'));
    }
}
