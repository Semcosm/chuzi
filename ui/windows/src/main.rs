mod desktop_rdp;
mod models;

use base64::Engine;
use desktop_rdp::{DesktopRdpController, RdpTarget};
use models::{
    default_theme, BehaviorSettings, BrowserView, CoreAccount, CoreComponent, CorePlugin,
    CoreRequest, CoreStatus, DiagnosticStatus, SubmitResult, UiPreferences,
};
use serde_json::{json, Value};
use slint::language::ColorScheme;
use slint::{ComponentHandle, Image, ModelRc, SharedString};
use std::cell::RefCell;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::rc::Rc;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

slint::include_modules!();

#[derive(Default)]
struct AppState {
    data_root: PathBuf,
    payload_root: PathBuf,
    busy: bool,
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
    let data_directory = state.lock().unwrap().data_root.display().to_string();
    ui.set_data_directory(SharedString::from(format!(
        "Data directory: {data_directory}"
    )));

    connect_callbacks(&ui, Arc::clone(&state));
    refresh_core(&ui.as_weak(), Arc::clone(&state));
    load_settings(&ui, Arc::clone(&state));
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
            busy: false,
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
        if !self.manifest_path().is_file() {
            return Err(format!(
                "Core release manifest is missing: {}",
                self.manifest_path().display()
            ));
        }
        fs::create_dir_all(&self.data_root).map_err(|error| error.to_string())?;
        let output = Command::new(self.launcher_path())
            .arg("-root")
            .arg(&self.data_root)
            .arg("-source-root")
            .arg(&self.payload_root)
            .arg("-manifest")
            .arg(self.manifest_path())
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

fn unique_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos()
        .to_string()
}

fn core_call(state: &AppState, method: &str, params: Value) -> Result<Value, String> {
    let params_json = serde_json::to_string(&params).map_err(|error| error.to_string())?;
    let output = state.run_launcher(
        "core-call",
        &[
            "-core-method",
            method,
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
    let weak = ui.as_weak();
    let install_state = Arc::clone(&state);
    ui.on_install_core(move || {
        run_background_status(
            &weak,
            Arc::clone(&install_state),
            |state| {
                state.run_launcher("component-install", &["-item", "service"])?;
                state.run_launcher("core-start", &[])?;
                Ok("Core installed.".to_owned())
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
    let settings_state = Arc::clone(&state);
    ui.on_save_settings(move || {
        let weak = weak.clone();
        let state = Arc::clone(&settings_state);
        let (auto_check, auto_repair, channel, launch, tray, interval, theme) =
            (weak.upgrade().map(|window| {
                (
                    window.get_auto_check_updates(),
                    window.get_auto_repair(),
                    window.get_update_channel().to_string(),
                    window.get_launch_on_login(),
                    window.get_close_to_tray(),
                    window.get_update_interval(),
                    window.get_theme().to_string(),
                )
            }),)
                .0
                .unwrap_or((
                    false,
                    false,
                    "nightly".to_owned(),
                    false,
                    false,
                    60,
                    default_theme(),
                ));
        run_background(&weak, state, move |state| {
            let settings = BehaviorSettings {
                auto_check_updates: auto_check,
                auto_repair,
                update_channel: if channel == "stable" {
                    "stable".to_owned()
                } else {
                    "nightly".to_owned()
                },
                launch_on_login: launch,
                close_to_tray: tray,
                check_interval: i64::from(interval.max(5)) * 60_000_000_000,
            };
            let input = state.data_root.join(".chuzi-settings-input.json");
            fs::write(
                &input,
                serde_json::to_vec(&settings).map_err(|error| error.to_string())?,
            )
            .map_err(|error| error.to_string())?;
            let result = state.run_launcher(
                "settings-save",
                &["-settings-input", input.to_string_lossy().as_ref()],
            );
            let _ = fs::remove_file(input);
            result?;
            state.save_ui_theme(&theme)?;
            Ok("Settings saved.".to_owned())
        });
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
                    friendly_error(&format!("settings: {error}")),
                    "error",
                );
            }
        });
    });

    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_refresh_plugins(move || refresh_plugins(&weak, Arc::clone(&plugin_state)));
    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_install_plugin(move |plugin| {
        plugin_action(
            &weak,
            Arc::clone(&plugin_state),
            "plugin-install",
            plugin.to_string(),
        )
    });
    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_trust_plugin(move |plugin| {
        plugin_action(
            &weak,
            Arc::clone(&plugin_state),
            "plugin-trust",
            plugin.to_string(),
        )
    });
    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_enable_plugin(move |plugin| {
        plugin_action(
            &weak,
            Arc::clone(&plugin_state),
            "plugin-enable",
            plugin.to_string(),
        )
    });
    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_remove_plugin(move |plugin| {
        plugin_action(
            &weak,
            Arc::clone(&plugin_state),
            "plugin-remove",
            plugin.to_string(),
        )
    });

    let weak = ui.as_weak();
    let component_state = Arc::clone(&state);
    ui.on_refresh_components(move || refresh_components(&weak, Arc::clone(&component_state)));
    let weak = ui.as_weak();
    let component_state = Arc::clone(&state);
    ui.on_install_component(move |component| {
        component_action(
            &weak,
            Arc::clone(&component_state),
            "component-install",
            component.to_string(),
        )
    });
    let weak = ui.as_weak();
    let component_state = Arc::clone(&state);
    ui.on_enable_component(move |component| {
        component_action(
            &weak,
            Arc::clone(&component_state),
            "component-enable",
            component.to_string(),
        )
    });
    let weak = ui.as_weak();
    let component_state = Arc::clone(&state);
    ui.on_disable_component(move |component| {
        component_action(
            &weak,
            Arc::clone(&component_state),
            "component-disable",
            component.to_string(),
        )
    });
    let weak = ui.as_weak();
    let component_state = Arc::clone(&state);
    ui.on_remove_component(move |component| {
        component_action(
            &weak,
            Arc::clone(&component_state),
            "component-remove",
            component.to_string(),
        )
    });

    let weak = ui.as_weak();
    let account_state = Arc::clone(&state);
    ui.on_lookup_account(move |account| {
        let account = account.to_string();
        if let Some(window) = weak.upgrade() {
            window.set_account_loaded(false);
        }
        run_background_with(
            &weak,
            Arc::clone(&account_state),
            move |state| {
                validate_text(&account, "account ID")?;
                let result = core_call(state, "get_account", json!({"account_id": account}))?;
                let parsed: CoreAccount =
                    serde_json::from_value(result).map_err(|error| error.to_string())?;
                Ok(("Account status loaded.".to_owned(), parsed))
            },
            |window, account: CoreAccount| {
                window.set_account_loaded(true);
                window.set_account_status(format!("Account {}", account.account).into());
                window.set_account_state(account.state.into());
                window.set_account_revision(account.revision.to_string().into());
                window.set_account_request(account.request_id.into());
            },
        );
    });

    let weak = ui.as_weak();
    let submit_state = Arc::clone(&state);
    ui.on_submit_task(move |account| {
        let account = account.to_string();
        if let Some(window) = weak.upgrade() {
            window.set_task_loaded(false);
        }
        run_background_with(
            &weak,
            Arc::clone(&submit_state),
            move |state| {
                validate_text(&account, "account ID")?;
                let id = format!("ui-{}", unique_id());
                let result = core_call(
                    state,
                    "submit_request",
                    json!({
                        "request_id": id,
                        "account_id": account,
                        "idempotency_key": format!("ui-{id}"),
                        "actor": "windows-ui"
                    }),
                )?;
                let parsed: SubmitResult =
                    serde_json::from_value(result).map_err(|error| error.to_string())?;
                Ok((
                    if parsed.idempotent {
                        "Request already existed; showing the existing request.".to_owned()
                    } else {
                        "Request submitted.".to_owned()
                    },
                    parsed,
                ))
            },
            |window, result: SubmitResult| {
                let request = result.request;
                window.set_request_input(request.request_id.clone().into());
                window.set_task_request_id(request.request_id.clone().into());
                window.set_task_loaded(true);
                window.set_task_status("Request submitted".into());
                window.set_task_account(request.account.into());
                window.set_task_state(request.state.into());
                window.set_task_attempt(request.attempt.to_string().into());
                window.set_task_failure(request.last_failure.into());
                window.set_page("tasks".into());
            },
        );
    });

    let weak = ui.as_weak();
    let task_state = Arc::clone(&state);
    ui.on_refresh_task(move |request| {
        if let Some(window) = weak.upgrade() {
            window.set_task_loaded(false);
        }
        request_action(
            &weak,
            Arc::clone(&task_state),
            request.to_string(),
            "get_request",
        )
    });
    let weak = ui.as_weak();
    let cancel_state = Arc::clone(&state);
    ui.on_cancel_task(move |request| {
        if let Some(window) = weak.upgrade() {
            window.set_task_loaded(false);
        }
        request_action(
            &weak,
            Arc::clone(&cancel_state),
            request.to_string(),
            "cancel_request",
        )
    });

    let weak = ui.as_weak();
    let view_state = Arc::clone(&state);
    ui.on_refresh_view(move |request| {
        let request = request.to_string();
        if let Some(window) = weak.upgrade() {
            window.set_browser_view_status("Capturing the active browser page...".into());
        }
        run_background_with(
            &weak,
            Arc::clone(&view_state),
            move |state| {
                validate_text(&request, "request ID")?;
                let result = core_call(
                    state,
                    "get_browser_view",
                    json!({"request_id": request, "width": 640, "height": 360}),
                )?;
                let parsed: BrowserView =
                    serde_json::from_value(result).map_err(|error| error.to_string())?;
                Ok(("Browser view captured.".to_owned(), parsed))
            },
            |window, view: BrowserView| {
                let bytes = match base64::engine::general_purpose::STANDARD.decode(&view.data) {
                    Ok(bytes) => bytes,
                    Err(error) => {
                        window.set_browser_view_status(
                            format!("Invalid browser frame: {error}").into(),
                        );
                        return;
                    }
                };
                match Image::load_from_data(&bytes, Some("jpeg")) {
                    Ok(image) => {
                        window.set_browser_view(image);
                        window.set_browser_view_loaded(true);
                        window.set_browser_view_status(
                            format!(
                                "{}x{} {} captured for {} at {}",
                                view.width,
                                view.height,
                                view.content_type,
                                view.request_id,
                                view.captured_at
                            )
                            .into(),
                        );
                    }
                    Err(error) => window.set_browser_view_status(
                        format!("Unable to decode browser frame: {error:?}").into(),
                    ),
                }
            },
        );
    });

    let weak = ui.as_weak();
    let desktop_rdp_state: Rc<RefCell<Option<DesktopRdpController>>> = Rc::new(RefCell::new(None));
    schedule_rdp_status_poll(ui.as_weak(), desktop_rdp_state.clone());
    let desktop_rdp_slot = desktop_rdp_state.clone();
    ui.on_connect_rdp(
        move |host, port, domain, username, password, allow_untrusted_certificate| {
            let host = host.to_string().trim().to_owned();
            let port_text = port.to_string();
            let domain = domain.to_string().trim().to_owned();
            let username = username.to_string().trim().to_owned();
            let password = password.to_string();
            if let Some(window) = weak.upgrade() {
                if host.is_empty() {
                    set_rdp_error(&window, "请输入 RDP 服务器地址。".to_owned());
                    return;
                }
                let port = match port_text.trim().parse::<u16>() {
                    Ok(port) if port != 0 => port,
                    _ => {
                        set_rdp_error(&window, "RDP 端口必须是 1 到 65535 之间的数字。".to_owned());
                        return;
                    }
                };
                if username.is_empty() {
                    set_rdp_error(&window, "请输入 RDP 用户名。".to_owned());
                    return;
                }
                if password.is_empty() {
                    set_rdp_error(&window, "请输入 RDP 密码。".to_owned());
                    return;
                }

                let mut target = RdpTarget::with_credentials(
                    host.clone(),
                    username,
                    password,
                    (!domain.is_empty()).then_some(domain),
                );
                target.port = port;
                target.allow_untrusted_certificate = allow_untrusted_certificate;
                window.set_rdp_password("".into());
                window.set_rdp_status(format!("正在打开 RDP 窗体（目标：{host}:{port}）…").into());
                match DesktopRdpController::new_with_target(target) {
                    Ok(controller) => {
                        *desktop_rdp_slot.borrow_mut() = Some(controller);
                        window.set_message("RDP 窗体已打开，FreeRDP 正在连接…".into());
                        window.set_message_kind("success".into());
                    }
                    Err(error) => {
                        set_rdp_error(&window, error);
                    }
                }
            }
        },
    );

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
                    "submit_diagnostic_report",
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

fn set_rdp_error(window: &MainWindow, message: String) {
    window.set_rdp_status(message.clone().into());
    window.set_message(format!("RDP 连接失败：{message}").into());
    window.set_message_kind("error".into());
    show_diagnostic_consent(window, "rdp", "error");
}

fn show_diagnostic_consent(window: &MainWindow, category: &str, severity: &str) {
    window.set_diagnostic_consent_category(category.into());
    window.set_diagnostic_consent_severity(severity.into());
    window.set_diagnostic_consent_summary(
        match category {
            "rdp" => "RDP 连接出现问题，是否发送脱敏诊断信息？",
            "core" => "Core 操作出现问题，是否发送脱敏诊断信息？",
            _ => "应用操作出现问题，是否发送脱敏诊断信息？",
        }
        .into(),
    );
    window.set_diagnostic_consent_visible(true);
}

fn schedule_rdp_status_poll(
    weak: slint::Weak<MainWindow>,
    slot: Rc<RefCell<Option<DesktopRdpController>>>,
) {
    slint::Timer::single_shot(Duration::from_millis(100), move || {
        let Some(window) = weak.upgrade() else {
            return;
        };

        let snapshot = slot
            .borrow()
            .as_ref()
            .map(DesktopRdpController::status_snapshot);
        if let Some((state, status)) = snapshot {
            window.set_rdp_status(status.clone().into());
            match state.as_str() {
                "connected" => {
                    window.set_message("RDP 已连接，远程画面正在接收。".into());
                    window.set_message_kind("success".into());
                }
                "failed" => set_rdp_error(&window, status),
                "closed" => {
                    window.set_message("RDP 连接已断开。".into());
                    window.set_message_kind("info".into());
                }
                _ => {}
            }
        }

        schedule_rdp_status_poll(weak, slot);
    });
}

#[derive(Debug)]
struct CoreSnapshot {
    ready: bool,
    installed: bool,
    status: String,
    details: String,
}

fn refresh_core(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
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
                    details: "Start Core to enable accounts, tasks, and plugins.".to_owned(),
                }
            } else {
                CoreSnapshot {
                    ready: false,
                    installed: false,
                    status: "Core is not installed".to_owned(),
                    details: "Install Core from Settings > Components to begin using Chuzi."
                        .to_owned(),
                }
            };
            Ok(("Core status refreshed.".to_owned(), snapshot))
        },
        |window, snapshot: CoreSnapshot| {
            window.set_core_ready(snapshot.ready);
            window.set_core_installed(snapshot.installed);
            window.set_core_status(snapshot.status.into());
            window.set_core_details(snapshot.details.into());
        },
    );
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

fn load_settings(ui: &MainWindow, state: Arc<Mutex<AppState>>) {
    let weak = ui.as_weak();
    thread::spawn(move || {
        let result = state.lock().unwrap().run_launcher("settings", &[]);
        let feedback = match result {
            Ok(output) => match serde_json::from_str::<BehaviorSettings>(&output) {
                Ok(settings) => {
                    let settings_weak = weak.clone();
                    let _ = slint::invoke_from_event_loop(move || {
                        if let Some(window) = settings_weak.upgrade() {
                            window.set_auto_check_updates(settings.auto_check_updates);
                            window.set_auto_repair(settings.auto_repair);
                            window.set_update_channel(settings.update_channel.into());
                            window.set_launch_on_login(settings.launch_on_login);
                            window.set_close_to_tray(settings.close_to_tray);
                            window.set_update_interval(
                                (settings.check_interval / 60_000_000_000).max(5) as i32,
                            );
                        }
                    });
                    None
                }
                Err(_) => Some("Saved launcher settings could not be read. Defaults are in use."),
            },
            Err(_) => Some("Launcher settings are unavailable until Core is installed."),
        };
        if let Some(message) = feedback {
            let _ = slint::invoke_from_event_loop(move || {
                if let Some(window) = weak.upgrade() {
                    window.set_message(message.into());
                    window.set_message_kind("info".into());
                }
            });
        }
    });
}

fn run_background<F>(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>, operation: F)
where
    F: FnOnce(&mut AppState) -> Result<String, String> + Send + 'static,
{
    run_background_with(
        ui,
        state,
        move |state| operation(state).map(|message| (message, ())),
        |_window, _| {},
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
    run_background_with(
        ui,
        state,
        move |state| operation(state).map(|message| (message, core_ready)),
        |window, ready: Option<bool>| {
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

fn request_action(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    request: String,
    method: &'static str,
) {
    run_background_with(
        ui,
        state,
        move |state| {
            validate_text(&request, "request ID")?;
            let params = if method == "cancel_request" {
                json!({"request_id": request, "actor": "windows-ui"})
            } else {
                json!({"request_id": request})
            };
            let result = core_call(state, method, params)?;
            let parsed: CoreRequest =
                serde_json::from_value(result).map_err(|error| error.to_string())?;
            Ok((
                if method == "cancel_request" {
                    "Cancellation requested.".to_owned()
                } else {
                    "Request status refreshed.".to_owned()
                },
                parsed,
            ))
        },
        |window, request: CoreRequest| {
            window.set_task_loaded(true);
            window.set_task_status(
                if request.state == "cancelled" {
                    "Request cancelled"
                } else {
                    "Request loaded"
                }
                .into(),
            );
            window.set_task_request_id(request.request_id.clone().into());
            window.set_request_input(request.request_id.into());
            window.set_task_account(request.account.into());
            window.set_task_state(request.state.into());
            window.set_task_attempt(request.attempt.to_string().into());
            window.set_task_failure(request.last_failure.into());
        },
    );
}

fn refresh_components(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    run_background_with(
        ui,
        state,
        |state| {
            let output = state.run_launcher("component-list", &[])?;
            let components = parse_components(&output)?;
            Ok((format_component_summary(&components), components))
        },
        apply_components,
    );
}

fn component_action(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    command: &'static str,
    component: String,
) {
    if let Err(error) = validate_text(&component, "component ID") {
        set_feedback(ui, friendly_error(&error), "error");
        return;
    }
    run_background_with(
        ui,
        state,
        move |state| {
            state.run_launcher(command, &["-item", component.as_str()])?;
            let output = state.run_launcher("component-list", &[])?;
            let components = parse_components(&output)?;
            Ok((component_action_message(command), components))
        },
        apply_components,
    );
}

fn parse_components(output: &str) -> Result<Vec<CoreComponent>, String> {
    serde_json::from_str(output).map_err(|error| format!("component projection: {error}"))
}

fn format_component_summary(components: &[CoreComponent]) -> String {
    if components.is_empty() {
        "No components are included in this Core release.".to_owned()
    } else {
        let entries = components
            .iter()
            .map(|component| format!("{} ({})", component.id, component.health))
            .collect::<Vec<_>>();
        format!("{} component(s): {}", components.len(), entries.join(", "))
    }
}

fn component_action_message(command: &str) -> String {
    match command {
        "component-install" => "Component installed.".to_owned(),
        "component-enable" => "Component enabled.".to_owned(),
        "component-disable" => "Component disabled.".to_owned(),
        "component-remove" => "Component removed.".to_owned(),
        _ => "Component state updated.".to_owned(),
    }
}

fn apply_components(window: &MainWindow, components: Vec<CoreComponent>) {
    window.set_component_summary(format_component_summary(&components).into());
    let options = components
        .iter()
        .map(|component| SharedString::from(component.id.clone()))
        .collect::<Vec<_>>();
    window.set_component_options(ModelRc::from(options.as_slice()));
    let selected = window.get_component_input().to_string();
    let component = components
        .iter()
        .find(|component| component.id == selected)
        .or_else(|| components.first());
    let Some(component) = component else {
        window.set_component_loaded(false);
        window.set_component_input(SharedString::default());
        window.set_component_id(SharedString::default());
        window.set_component_version(SharedString::default());
        window.set_component_health(SharedString::default());
        window.set_component_installed(false);
        window.set_component_required(false);
        window.set_component_enabled(false);
        return;
    };
    window.set_component_loaded(true);
    window.set_component_input(component.id.clone().into());
    window.set_component_id(component.id.clone().into());
    window.set_component_version(component.version.clone().into());
    window.set_component_health(component.health.clone().into());
    window.set_component_installed(component.installed);
    window.set_component_required(component.required);
    window.set_component_enabled(component.enabled);
}

fn refresh_plugins(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    run_background_with(
        ui,
        state,
        |state| {
            ensure_core_ready(state)?;
            let output = state.run_launcher("plugin-list", &[])?;
            let plugins = parse_plugins(&output)?;
            Ok((format_plugin_summary(&plugins), plugins))
        },
        apply_plugins,
    );
}

fn plugin_action(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    command: &'static str,
    plugin: String,
) {
    if let Err(error) = validate_text(&plugin, "plugin ID") {
        set_feedback(ui, friendly_error(&error), "error");
        return;
    }
    run_background_with(
        ui,
        state,
        move |state| {
            ensure_core_ready(state)?;
            state.run_launcher(command, &["-item", plugin.as_str()])?;
            let output = state.run_launcher("plugin-list", &[])?;
            let plugins = parse_plugins(&output)?;
            Ok((plugin_action_message(command), plugins))
        },
        apply_plugins,
    );
}

fn parse_plugins(output: &str) -> Result<Vec<CorePlugin>, String> {
    serde_json::from_str(output).map_err(|error| format!("plugin projection: {error}"))
}

fn format_plugin_summary(plugins: &[CorePlugin]) -> String {
    if plugins.is_empty() {
        "No plugins are included in this Core release.".to_owned()
    } else {
        format!("{} plugin(s) reported by the launcher.", plugins.len())
    }
}

fn plugin_action_message(command: &str) -> String {
    match command {
        "plugin-install" => {
            "Plugin installed; it remains untrusted until explicitly trusted.".to_owned()
        }
        "plugin-trust" => "Plugin trust updated. Review the signer before enabling it.".to_owned(),
        "plugin-enable" => "Plugin enabled.".to_owned(),
        "plugin-remove" => "Plugin removed.".to_owned(),
        _ => "Plugin state updated.".to_owned(),
    }
}

fn apply_plugins(window: &MainWindow, plugins: Vec<CorePlugin>) {
    window.set_plugin_summary(format_plugin_summary(&plugins).into());
    let selected = window.get_plugin_input().to_string();
    let plugin = plugins
        .iter()
        .find(|plugin| plugin.descriptor.id == selected)
        .or_else(|| plugins.first());
    let Some(plugin) = plugin else {
        window.set_plugin_loaded(false);
        window.set_plugin_id(SharedString::default());
        window.set_plugin_installed(false);
        window.set_plugin_trusted(false);
        window.set_plugin_enabled(false);
        window.set_plugin_health(SharedString::default());
        return;
    };
    window.set_plugin_loaded(true);
    window.set_plugin_input(plugin.descriptor.id.clone().into());
    window.set_plugin_id(plugin.descriptor.id.clone().into());
    window.set_plugin_version(plugin.descriptor.version.clone().into());
    window.set_plugin_installed(plugin.installed);
    window.set_plugin_trusted(plugin.trusted);
    window.set_plugin_enabled(plugin.trusted && plugin.enabled);
    window.set_plugin_health(plugin.health.clone().into());
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
    if value.contains("rdp_requires_windows") || value.contains("remote_desktop_requires_windows") {
        return "RDP is only available in the Windows client.".to_owned();
    }
    if value.contains("rdp_client_start") || value.contains("remote_client_start") {
        return "Windows could not start the RDP client.".to_owned();
    }
    if value.contains("missing_account")
        || value.contains("missing_request")
        || value.contains("missing_plugin")
        || value.contains("missing_component")
    {
        return "Enter an ID before trying this action.".to_owned();
    }
    if value.contains("not_found") || value.contains("not found") {
        return "Core could not find that item. Check the ID and try again.".to_owned();
    }
    if value.contains("core_not_installed") || value.contains("launcher is missing") {
        return "Core is not installed. Use Settings > Components, then try again.".to_owned();
    }
    if value.contains("core_stop_unavailable") {
        return "Core is running, but this client cannot identify its process. Restart Core from its owning service.".to_owned();
    }
    if value.contains("core_stop_timeout") {
        return "Core did not stop within the expected time. Refresh its status before trying again."
            .to_owned();
    }
    if value.contains("core_start_timeout")
        || value.contains("connect core")
        || value.contains("unavailable")
    {
        return "Core is unavailable. Start Core and refresh its status.".to_owned();
    }
    if value.contains("not trusted") || value.contains("forbidden") || value.contains("not allowed")
    {
        return "Core did not allow this operation. Review plugin trust and permissions."
            .to_owned();
    }
    if value.contains("conflict") || value.contains("already") {
        return "The item changed while this was running. Refresh and try again.".to_owned();
    }
    if value.contains("settings") {
        return "Settings could not be saved. Check the launcher and try again.".to_owned();
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
            friendly_error("not_found: account"),
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
        assert_eq!(
            friendly_error("rdp_client_start: access denied"),
            "Windows could not start the RDP client."
        );
    }

    #[test]
    fn plugin_projection_never_promotes_untrusted_state() {
        let plugins: Vec<CorePlugin> = serde_json::from_str(
            r#"[{"descriptor":{"id":"demo","version":"1"},"installed":true,"enabled":true,"trusted":false,"health":"untrusted"}]"#,
        )
        .expect("valid plugin projection");
        let plugin = &plugins[0];
        assert!(plugin.enabled);
        assert!(!plugin.trusted);
        assert!(!(plugin.trusted && plugin.enabled));
    }

    #[test]
    fn component_projection_preserves_lifecycle_state() {
        let components = parse_components(
            r#"[{"id":"service","installed":true,"version":"1","enabled":true,"required":true,"health":"healthy"}]"#,
        )
        .expect("valid component projection");
        assert_eq!(components.len(), 1);
        assert_eq!(components[0].id, "service");
        assert!(components[0].installed);
        assert!(components[0].required);
        assert_eq!(
            format_component_summary(&components),
            "1 component(s): service (healthy)"
        );
    }

    #[test]
    fn unavailable_core_error_is_user_actionable() {
        assert_eq!(
            friendly_error("core_stop_timeout"),
            "Core did not stop within the expected time. Refresh its status before trying again."
        );
        assert_eq!(
            friendly_error("core_stop_unavailable"),
            "Core is running, but this client cannot identify its process. Restart Core from its owning service."
        );
    }
}
