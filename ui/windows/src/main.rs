use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use slint::language::ColorScheme;
use slint::{ComponentHandle, SharedString};
use std::fs::{self, File, OpenOptions};
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::thread;

slint::include_modules!();

const CORE_PROTOCOL: &str = "chuzi.core/v1";

#[derive(Default)]
struct AppState {
    data_root: PathBuf,
    payload_root: PathBuf,
    service: Option<Child>,
}

#[derive(Debug, Deserialize)]
struct CoreAccount {
    account: String,
    state: String,
    #[serde(default)]
    request_id: String,
    revision: u64,
}

#[derive(Debug, Deserialize)]
struct CoreRequest {
    request_id: String,
    account: String,
    state: String,
    attempt: i32,
    #[serde(default)]
    last_failure: String,
}

#[derive(Debug, Deserialize)]
struct SubmitResult {
    request: CoreRequest,
    idempotent: bool,
}

#[derive(Debug, Deserialize)]
struct WireEnvelope {
    protocol: String,
    id: String,
    #[serde(rename = "type")]
    kind: Option<String>,
    result: Option<Value>,
    error: Option<WireError>,
}

#[derive(Debug, Deserialize)]
struct WireError {
    code: String,
    message: String,
}

#[derive(Debug, Serialize, Deserialize)]
struct BehaviorSettings {
    auto_check_updates: bool,
    auto_repair: bool,
    update_channel: String,
    launch_on_login: bool,
    close_to_tray: bool,
    check_interval: i64,
}

#[derive(Debug, Serialize, Deserialize)]
struct UiPreferences {
    #[serde(default = "default_theme")]
    theme: String,
}

fn default_theme() -> String {
    "system".to_owned()
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
            service: None,
        })
    }

    fn launcher_path(&self) -> PathBuf {
        self.payload_root.join(if cfg!(windows) {
            "chuzi-launcher.exe"
        } else {
            "chuzi-launcher"
        })
    }

    fn service_path(&self) -> PathBuf {
        self.data_root
            .join(if cfg!(windows) { "chuzi.exe" } else { "chuzi" })
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

fn connect_callbacks(ui: &MainWindow, state: Arc<Mutex<AppState>>) {
    let weak = ui.as_weak();
    let install_state = Arc::clone(&state);
    ui.on_install_core(move || {
        run_background_status(
            &weak,
            Arc::clone(&install_state),
            |state| {
                state.run_launcher("component-install", &["-item", "service"])?;
                start_service(state)?;
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
                start_service(state)?;
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
                stop_service(state)?;
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
        let (auto_check, auto_repair, launch, tray, interval, theme) =
            (weak.upgrade().map(|window| {
                (
                    window.get_auto_check_updates(),
                    window.get_auto_repair(),
                    window.get_launch_on_login(),
                    window.get_close_to_tray(),
                    window.get_update_interval(),
                    window.get_theme().to_string(),
                )
            }),)
                .0
                .unwrap_or((false, false, false, false, 60, default_theme()));
        run_background(&weak, state, move |state| {
            let settings = BehaviorSettings {
                auto_check_updates: auto_check,
                auto_repair,
                update_channel: "nightly".to_owned(),
                launch_on_login: launch,
                close_to_tray: tray,
                check_interval: i64::from(interval) * 60_000_000_000,
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
        if let Err(error) = theme_state.lock().unwrap().save_ui_theme(&theme) {
            if let Some(window) = weak.upgrade() {
                window.set_message(format!("Theme could not be saved: {error}").into());
            }
        }
    });

    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_refresh_plugins(move || {
        run_background(&weak, Arc::clone(&plugin_state), |state| {
            let output = state.run_launcher("plugin-list", &[])?;
            Ok(format_plugin_summary(&output))
        })
    });
    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_install_plugin(move || plugin_action(&weak, Arc::clone(&plugin_state), "plugin-install"));
    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_trust_plugin(move || plugin_action(&weak, Arc::clone(&plugin_state), "plugin-trust"));
    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_enable_plugin(move || plugin_action(&weak, Arc::clone(&plugin_state), "plugin-enable"));
    let weak = ui.as_weak();
    let plugin_state = Arc::clone(&state);
    ui.on_remove_plugin(move || plugin_action(&weak, Arc::clone(&plugin_state), "plugin-remove"));

    let weak = ui.as_weak();
    let account_state = Arc::clone(&state);
    ui.on_lookup_account(move |account| {
        let account = account.to_string();
        run_background(&weak, Arc::clone(&account_state), move |state| {
            let result = core_call(state, "get_account", json!({"account_id": account}))?;
            let parsed: CoreAccount =
                serde_json::from_value(result).map_err(|error| error.to_string())?;
            Ok(format!(
                "{} · state={} · request={} · revision={}",
                parsed.account, parsed.state, parsed.request_id, parsed.revision
            ))
        });
    });

    let weak = ui.as_weak();
    let submit_state = Arc::clone(&state);
    ui.on_submit_task(move |account| {
        let account = account.to_string();
        run_background(&weak, Arc::clone(&submit_state), move |state| {
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
            Ok(format!(
                "submitted {} · state={} · idempotent={}",
                parsed.request.request_id, parsed.request.state, parsed.idempotent
            ))
        });
    });

    let weak = ui.as_weak();
    let task_state = Arc::clone(&state);
    ui.on_refresh_task(move |request| {
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
        request_action(
            &weak,
            Arc::clone(&cancel_state),
            request.to_string(),
            "cancel_request",
        )
    });
}

fn refresh_core(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>) {
    let weak = ui.clone();
    let data = state.lock().unwrap().data_root.clone();
    let service = state.lock().unwrap().service.is_some();
    let _ = slint::invoke_from_event_loop(move || {
        if let Some(window) = weak.upgrade() {
            window.set_core_ready(service);
            window.set_core_status(if service {
                "Core is running".into()
            } else if data
                .join(if cfg!(windows) { "chuzi.exe" } else { "chuzi" })
                .is_file()
            {
                "Core is stopped".into()
            } else {
                "Core is not installed".into()
            });
            window.set_core_details(if service {
                "Core process is running.".into()
            } else {
                "Install Core to unlock plugins and tasks.".into()
            });
        }
    });
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
        if let Ok(output) = result {
            if let Ok(settings) = serde_json::from_str::<BehaviorSettings>(&output) {
                let _ = slint::invoke_from_event_loop(move || {
                    if let Some(window) = weak.upgrade() {
                        window.set_auto_check_updates(settings.auto_check_updates);
                        window.set_auto_repair(settings.auto_repair);
                        window.set_launch_on_login(settings.launch_on_login);
                        window.set_close_to_tray(settings.close_to_tray);
                        window
                            .set_update_interval((settings.check_interval / 60_000_000_000) as i32);
                    }
                });
            }
        }
    });
}

fn run_background<F>(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>, operation: F)
where
    F: FnOnce(&mut AppState) -> Result<String, String> + Send + 'static,
{
    run_background_status(ui, state, operation, None);
}

fn run_background_status<F>(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    operation: F,
    core_ready: Option<bool>,
) where
    F: FnOnce(&mut AppState) -> Result<String, String> + Send + 'static,
{
    let weak = ui.clone();
    let _ = slint::invoke_from_event_loop({
        let weak = weak.clone();
        move || {
            if let Some(window) = weak.upgrade() {
                window.set_busy(true);
                window.set_message("Working...".into());
            }
        }
    });
    thread::spawn(move || {
        let result = operation(&mut state.lock().unwrap());
        let _ = slint::invoke_from_event_loop(move || {
            if let Some(window) = weak.upgrade() {
                window.set_busy(false);
                match result {
                    Ok(message) => {
                        window.set_message(message.into());
                        if let Some(ready) = core_ready {
                            window.set_core_ready(ready);
                            window.set_core_status(
                                if ready {
                                    "Core is running"
                                } else {
                                    "Core is stopped"
                                }
                                .into(),
                            );
                        }
                    }
                    Err(error) => {
                        window.set_message(format!("Core operation failed: {error}").into())
                    }
                }
            }
        });
    });
}

fn start_service(state: &mut AppState) -> Result<(), String> {
    if state
        .service
        .as_mut()
        .is_some_and(|process| process.try_wait().ok().flatten().is_none())
    {
        return Ok(());
    }
    if !state.service_path().is_file() {
        return Err(format!(
            "Core is not installed: {}",
            state.service_path().display()
        ));
    }
    fs::create_dir_all(&state.data_root).map_err(|error| error.to_string())?;
    let config = state.data_root.join("core-config.json");
    let content = json!({
        "data_dir": state.data_root,
        "credentials": {"key_env": "CHUZI_CREDENTIAL_KEY", "key_id_env": "CHUZI_CREDENTIAL_KEY_ID", "history_env": "CHUZI_CREDENTIAL_KEYS"},
        "health": {"listen": ""},
        "observability": {"metrics_listen": "", "log_path": state.data_root.join("core-service.log"), "log_max_bytes": 10485760, "log_max_files": 5}
    });
    fs::write(
        &config,
        serde_json::to_vec_pretty(&content).map_err(|error| error.to_string())?,
    )
    .map_err(|error| error.to_string())?;
    let process = Command::new(state.service_path())
        .arg("-config")
        .arg(config)
        .current_dir(&state.data_root)
        .env("CHUZI_DATA_DIR", &state.data_root)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|error| error.to_string())?;
    fs::write(state.data_root.join(".core.pid"), process.id().to_string())
        .map_err(|error| error.to_string())?;
    state.service = Some(process);
    Ok(())
}

fn stop_service(state: &mut AppState) -> Result<(), String> {
    if let Some(mut process) = state.service.take() {
        let _ = process.kill();
        let _ = process.wait();
    }
    let _ = fs::remove_file(state.data_root.join(".core.pid"));
    Ok(())
}

fn core_call(state: &AppState, method: &str, params: Value) -> Result<Value, String> {
    let pipe = pipe_name(&state.data_root);
    let mut stream = OpenOptions::new()
        .read(true)
        .write(true)
        .open(&pipe)
        .map_err(|error| format!("connect Core pipe: {error}"))?;
    let id = unique_id();
    let hello = json!({"protocol": CORE_PROTOCOL, "id": format!("hello-{id}"), "method": "hello", "params": {"version": CORE_PROTOCOL}});
    write_json_line(&mut stream, &hello)?;
    let _ = read_response(&mut stream, &format!("hello-{id}"))?;
    let request = json!({"protocol": CORE_PROTOCOL, "id": id, "method": method, "params": params});
    write_json_line(&mut stream, &request)?;
    read_response(&mut stream, &id)
}

fn write_json_line(stream: &mut File, value: &Value) -> Result<(), String> {
    let mut bytes = serde_json::to_vec(value).map_err(|error| error.to_string())?;
    bytes.push(b'\n');
    stream.write_all(&bytes).map_err(|error| error.to_string())
}

fn read_response(stream: &mut File, expected_id: &str) -> Result<Value, String> {
    let mut reader = BufReader::new(stream.try_clone().map_err(|error| error.to_string())?);
    let mut line = String::new();
    reader
        .read_line(&mut line)
        .map_err(|error| error.to_string())?;
    let response: WireEnvelope = serde_json::from_str(&line).map_err(|error| error.to_string())?;
    if response.protocol != CORE_PROTOCOL || response.id != expected_id {
        return Err("invalid Core response".to_owned());
    }
    if response.kind.as_deref() == Some("error") {
        let error = response
            .error
            .ok_or_else(|| "Core returned an unknown error".to_owned())?;
        return Err(format!("{}: {}", error.code, error.message));
    }
    response
        .result
        .ok_or_else(|| "Core returned an empty result".to_owned())
}

fn request_action(
    ui: &slint::Weak<MainWindow>,
    state: Arc<Mutex<AppState>>,
    request: String,
    method: &'static str,
) {
    run_background(ui, state, move |state| {
        let params = if method == "cancel_request" {
            json!({"request_id": request, "actor": "windows-ui"})
        } else {
            json!({"request_id": request})
        };
        let result = core_call(state, method, params)?;
        let parsed: CoreRequest =
            serde_json::from_value(result).map_err(|error| error.to_string())?;
        Ok(format!(
            "{} · account={} · state={} · attempt={} {}",
            parsed.request_id, parsed.account, parsed.state, parsed.attempt, parsed.last_failure
        ))
    });
}

fn plugin_action(ui: &slint::Weak<MainWindow>, state: Arc<Mutex<AppState>>, command: &'static str) {
    run_background(ui, state, move |state| {
        state.run_launcher(command, &["-item", "default"])?;
        Ok(format!("{command} completed."))
    });
}

fn format_plugin_summary(output: &str) -> String {
    match serde_json::from_str::<Value>(output) {
        Ok(Value::Array(items)) if items.is_empty() => {
            "No plugins are included in this Core release.".to_owned()
        }
        Ok(Value::Array(items)) => format!("{} plugin(s) reported by Core.", items.len()),
        _ => "Plugin state refreshed.".to_owned(),
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

fn pipe_name(data_root: &Path) -> String {
    let cleaned = data_root.to_string_lossy().replace('/', "\\");
    let digest = Sha256::digest(cleaned.as_bytes());
    format!(r"\\.\pipe\chuzi-core-{}", hex_encode(&digest[..8]))
}

fn hex_encode(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}
