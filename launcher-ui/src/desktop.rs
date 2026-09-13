use std::{
    collections::{HashMap, HashSet},
    env,
    ffi::OsString,
    io::{self, BufRead, Read, Write},
    panic::{catch_unwind, AssertUnwindSafe},
    path::PathBuf,
    process::{Child, Command as ProcessCommand, Stdio},
    sync::{Arc, Mutex},
    thread,
    time::Duration,
};

use serde_json::Value;
#[cfg(target_os = "linux")]
use tao::platform::unix::WindowExtUnix;
use tao::{
    dpi::LogicalSize,
    event::{Event, WindowEvent},
    event_loop::{ControlFlow, EventLoop, EventLoopBuilder, EventLoopProxy},
    window::{Window, WindowBuilder},
};
#[cfg(any(target_os = "windows", target_os = "linux"))]
use wry::WebContext;
#[cfg(target_os = "linux")]
use wry::WebViewBuilderExtUnix;
use wry::{NewWindowResponse, PermissionResponse, WebView, WebViewBuilder};

use crate::{
    command_spec, parse_progress_line, LauncherConfig, ProgressEvent, ProgressMessage, ResultEvent,
    UiRequest, UI_HTML,
};

#[derive(Debug)]
enum UserEvent {
    Ipc(String),
    Progress {
        id: String,
        event: ProgressEvent,
    },
    Finished {
        id: String,
        result: Result<Value, String>,
    },
}

#[derive(Default)]
struct OperationState {
    children: HashMap<String, Arc<Mutex<Child>>>,
    cancelled: HashSet<String>,
}

#[derive(Clone, Default)]
struct OperationRegistry {
    state: Arc<Mutex<OperationState>>,
}

impl OperationRegistry {
    fn reserve(&self, id: &str, child: Arc<Mutex<Child>>) -> bool {
        let Ok(mut state) = self.state.lock() else {
            return false;
        };
        if state.children.contains_key(id) {
            return false;
        }
        state.children.insert(id.to_owned(), child);
        true
    }

    fn cancel(&self, id: &str) -> Result<bool, String> {
        let Ok(mut state) = self.state.lock() else {
            return Err("operation state unavailable".to_owned());
        };
        let Some(child) = state.children.get(id).cloned() else {
            return Ok(false);
        };
        state.cancelled.insert(id.to_owned());
        drop(state);
        let Ok(mut child) = child.lock() else {
            return Err("operation process state unavailable".to_owned());
        };
        match child.kill() {
            Ok(()) => Ok(true),
            Err(error) if error.kind() == io::ErrorKind::InvalidInput => Ok(true),
            Err(_) => Err("operation cancellation failed".to_owned()),
        }
    }

    fn cancelled(&self, id: &str) -> bool {
        self.state
            .lock()
            .map(|mut state| state.cancelled.remove(id))
            .unwrap_or(false)
    }

    fn finish(&self, id: &str) {
        if let Ok(mut state) = self.state.lock() {
            state.children.remove(id);
            state.cancelled.remove(id);
        }
    }

    fn cancel_all(&self) {
        let Ok(mut state) = self.state.lock() else {
            return;
        };
        let children: Vec<_> = state
            .children
            .iter()
            .map(|(id, child)| (id.clone(), Arc::clone(child)))
            .collect();
        for (id, _) in &children {
            state.cancelled.insert(id.clone());
        }
        drop(state);
        for (_, child) in children {
            if let Ok(mut child) = child.lock() {
                let _ = child.kill();
            }
        }
    }
}

struct UiHost {
    window: Window,
    webview: WebView,
    #[cfg(any(target_os = "windows", target_os = "linux"))]
    _context: WebContext,
    config: LauncherConfig,
    operations: OperationRegistry,
}

impl UiHost {
    fn dispatch(&self, event: impl serde::Serialize) {
        let Ok(encoded) = serde_json::to_string(&event) else {
            return;
        };
        let script = format!("window.onLauncherEvent({encoded});");
        let _ = self.webview.evaluate_script(&script);
    }

    fn error_for_request(&self, id: String, error: impl Into<String>) {
        self.dispatch(ResultEvent::failure(id, error));
    }

    fn handle_ipc(&mut self, body: String, proxy: &EventLoopProxy<UserEvent>) {
        let fallback_id = serde_json::from_str::<Value>(&body)
            .ok()
            .and_then(|value| value.get("id").and_then(Value::as_str).map(str::to_owned))
            .unwrap_or_else(|| "unknown".to_owned());
        let request = match UiRequest::parse(&body) {
            Ok(request) => request,
            Err(error) => {
                self.error_for_request(fallback_id, error);
                return;
            }
        };
        if request.action == "cancel" {
            let target = request.target_id.as_deref().unwrap_or_default();
            match self.operations.cancel(target) {
                Ok(cancelled) => {
                    self.dispatch(ResultEvent::success(
                        request.id,
                        Some(serde_json::json!({
                            "cancelled": cancelled,
                            "target_id": target,
                        })),
                    ));
                }
                Err(error) => self.error_for_request(request.id, error),
            }
            return;
        }
        if let Err(error) = spawn_operation(
            self.config.clone(),
            request,
            self.operations.clone(),
            proxy.clone(),
        ) {
            self.error_for_request(fallback_id, error);
        }
    }

    fn handle_finished(&self, id: String, result: Result<Value, String>) {
        let cancelled = self.operations.cancelled(&id);
        self.operations.finish(&id);
        if cancelled {
            self.dispatch(ResultEvent::failure(id, "operation_cancelled"));
        } else {
            match result {
                Ok(data) => self.dispatch(ResultEvent::success(id, Some(data))),
                Err(error) => self.dispatch(ResultEvent::failure(id, error)),
            }
        }
    }
}

fn spawn_operation(
    config: LauncherConfig,
    request: UiRequest,
    operations: OperationRegistry,
    proxy: EventLoopProxy<UserEvent>,
) -> Result<(), String> {
    let id = request.id.clone();
    let spec = command_spec(&config, &request)?;
    let has_stdin = spec.stdin.is_some();
    let mut command = ProcessCommand::new(&config.launcher);
    command
        .args(spec.args)
        .current_dir(&config.root)
        .stdin(if has_stdin {
            Stdio::piped()
        } else {
            Stdio::null()
        })
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = command
        .spawn()
        .map_err(|_| "launcher_unavailable".to_owned())?;
    if let Some(input) = spec.stdin {
        let Some(mut stdin) = child.stdin.take() else {
            let _ = child.kill();
            return Err("launcher_input_failed".to_owned());
        };
        if stdin.write_all(&input).is_err() {
            let _ = child.kill();
            return Err("launcher_input_failed".to_owned());
        }
    }
    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let child = Arc::new(Mutex::new(child));
    if !operations.reserve(&id, Arc::clone(&child)) {
        if let Ok(mut child) = child.lock() {
            let _ = child.kill();
        }
        return Err("duplicate operation id".to_owned());
    }

    let stderr_proxy = proxy.clone();
    let stderr_id = id.clone();
    let stderr_thread = thread::Builder::new()
        .name("chuzi-launcher-ui-stderr".to_owned())
        .spawn(move || {
            let Some(stderr) = stderr else {
                return;
            };
            for line in io::BufReader::new(stderr).lines().map_while(Result::ok) {
                if let Some(event) = parse_progress_line(&line) {
                    let _ = stderr_proxy.send_event(UserEvent::Progress {
                        id: stderr_id.clone(),
                        event,
                    });
                }
            }
        })
        .map_err(|_| {
            operations.finish(&id);
            if let Ok(mut child) = child.lock() {
                let _ = child.kill();
            }
            "launcher_output_failed".to_owned()
        })?;
    let operation_child = Arc::clone(&child);
    let stdout_thread = thread::Builder::new()
        .name("chuzi-launcher-ui-stdout".to_owned())
        .spawn(move || {
            let mut output = Vec::new();
            if let Some(stdout) = stdout {
                let _ = io::BufReader::new(stdout).read_to_end(&mut output);
            }
            output
        })
        .map_err(|_| {
            operations.finish(&id);
            if let Ok(mut child) = operation_child.lock() {
                let _ = child.kill();
            }
            "launcher_output_failed".to_owned()
        })?;

    let operation_child = Arc::clone(&child);
    let operation_id = id.clone();
    thread::Builder::new()
        .name("chuzi-launcher-ui-operation".to_owned())
        .spawn(move || {
            let status = loop {
                let result = match child.lock() {
                    Ok(mut child) => child.try_wait(),
                    Err(_) => Err(io::Error::other("operation process state unavailable")),
                };
                match result {
                    Ok(Some(status)) => break Ok(status),
                    Ok(None) => thread::sleep(Duration::from_millis(25)),
                    Err(error) => break Err(error),
                }
            };
            let output = stdout_thread.join().unwrap_or_default();
            let _ = stderr_thread.join();
            let result = match status {
                Ok(status) if status.success() => {
                    let text = String::from_utf8_lossy(&output);
                    if text.trim().is_empty() {
                        Ok(Value::Null)
                    } else {
                        serde_json::from_str(text.trim())
                            .map_err(|_| "invalid_launcher_response".to_owned())
                    }
                }
                Ok(_) => Err("launcher_operation_failed".to_owned()),
                Err(_) => Err("launcher_process_failed".to_owned()),
            };
            let _ = proxy.send_event(UserEvent::Finished { id, result });
        })
        .map_err(|_| {
            operations.finish(&operation_id);
            if let Ok(mut child) = operation_child.lock() {
                let _ = child.kill();
            }
            "launcher_process_failed".to_owned()
        })?;
    Ok(())
}

fn parse_config() -> Result<Option<LauncherConfig>, String> {
    let executable = env::current_exe().map_err(|_| "launcher_path_unavailable".to_owned())?;
    let executable_dir = executable
        .parent()
        .ok_or_else(|| "launcher_path_unavailable".to_owned())?
        .to_path_buf();
    let mut root: Option<PathBuf> = None;
    let mut launcher: Option<PathBuf> = None;
    let mut manifest: Option<PathBuf> = None;
    let mut release_index = None;
    let mut source_root = None;
    let mut download_dir = None;
    let mut allow_http_loopback = false;
    let mut arguments = env::args_os().skip(1);
    while let Some(argument) = arguments.next() {
        let name = argument.to_string_lossy();
        match name.as_ref() {
            "--help" | "-h" => {
                println!(
                    "usage: chuzi-launcher-ui [--root PATH] [--launcher PATH] [--manifest PATH] [--release-index URL] [--source-root PATH] [--download-dir PATH] [--allow-http-loopback]"
                );
                return Ok(None);
            }
            "--root" => root = Some(next_path(&mut arguments, "--root")?),
            "--launcher" => launcher = Some(next_path(&mut arguments, "--launcher")?),
            "--manifest" => manifest = Some(next_path(&mut arguments, "--manifest")?),
            "--source-root" => source_root = Some(next_path(&mut arguments, "--source-root")?),
            "--download-dir" => download_dir = Some(next_path(&mut arguments, "--download-dir")?),
            "--release-index" => {
                release_index = Some(
                    arguments
                        .next()
                        .ok_or_else(|| "--release-index requires a URL".to_owned())?
                        .to_string_lossy()
                        .into_owned(),
                );
            }
            "--allow-http-loopback" => allow_http_loopback = true,
            other => return Err(format!("unknown option: {other}")),
        }
    }
    let root = root.unwrap_or_else(|| executable_dir.clone());
    let launcher_default = if cfg!(target_os = "windows") {
        "chuzi-launcher.exe"
    } else {
        "chuzi-launcher"
    };
    let launcher = launcher.unwrap_or_else(|| executable_dir.join(launcher_default));
    let manifest = manifest.unwrap_or_else(|| root.join("release-manifest.json"));
    let config = LauncherConfig {
        launcher,
        root,
        manifest,
        release_index,
        source_root,
        download_dir,
        allow_http_loopback,
    };
    config.validate().map_err(|error| error.to_owned())?;
    Ok(Some(config))
}

fn next_path(
    arguments: &mut impl Iterator<Item = OsString>,
    option: &str,
) -> Result<PathBuf, String> {
    arguments
        .next()
        .map(PathBuf::from)
        .ok_or_else(|| format!("{option} requires a path"))
}

fn build_event_loop(
    builder: &mut EventLoopBuilder<UserEvent>,
) -> Result<EventLoop<UserEvent>, String> {
    catch_native_panic(|| builder.build()).map_err(|_| "gui_session_unavailable".to_owned())
}

fn catch_native_panic<T>(function: impl FnOnce() -> T) -> Result<T, ()> {
    let previous_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(|_| {}));
    let result = catch_unwind(AssertUnwindSafe(function));
    std::panic::set_hook(previous_hook);
    result.map_err(|_| ())
}

fn configure_builder<'a>(
    builder: WebViewBuilder<'a>,
    proxy: EventLoopProxy<UserEvent>,
) -> WebViewBuilder<'a> {
    builder
        .with_visible(true)
        .with_html(UI_HTML)
        .with_clipboard(false)
        .with_autoplay(false)
        .with_general_autofill_enabled(false)
        .with_navigation_handler(|url| url == "about:blank")
        .with_new_window_req_handler(|_, _| NewWindowResponse::Deny)
        .with_permission_handler(|_| PermissionResponse::Deny)
        .with_download_started_handler(|_, _| false)
        .with_ipc_handler(move |request| {
            let _ = proxy.send_event(UserEvent::Ipc(request.body().clone()));
        })
}

fn build_host(
    config: LauncherConfig,
    proxy: EventLoopProxy<UserEvent>,
    target: &tao::event_loop::EventLoopWindowTarget<UserEvent>,
) -> Result<UiHost, String> {
    let window = WindowBuilder::new()
        .with_title("chuzi launcher")
        .with_inner_size(LogicalSize::new(980.0, 680.0))
        .with_min_inner_size(LogicalSize::new(720.0, 480.0))
        .with_visible(true)
        .build(target)
        .map_err(|_| "gui_session_unavailable".to_owned())?;
    #[cfg(any(target_os = "windows", target_os = "linux"))]
    {
        let profile = config.root.join(".chuzi").join("launcher-ui");
        std::fs::create_dir_all(&profile).map_err(|_| "ui_profile_unavailable".to_owned())?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&profile, std::fs::Permissions::from_mode(0o700));
        }
        let mut context = WebContext::new(Some(profile));
        let builder = configure_builder(WebViewBuilder::new_with_web_context(&mut context), proxy);
        #[cfg(target_os = "linux")]
        let webview = {
            let vbox = window
                .default_vbox()
                .ok_or_else(|| "gui_session_unavailable".to_owned())?;
            builder
                .build_gtk(vbox)
                .map_err(|_| "webview_build_failed".to_owned())?
        };
        #[cfg(target_os = "windows")]
        let webview = builder
            .build(&window)
            .map_err(|_| "webview_build_failed".to_owned())?;
        return Ok(UiHost {
            window,
            webview,
            _context: context,
            config,
            operations: OperationRegistry::default(),
        });
    }
    #[cfg(target_os = "macos")]
    {
        let builder = configure_builder(WebViewBuilder::new().with_incognito(true), proxy);
        let webview = builder
            .build(&window)
            .map_err(|_| "webview_build_failed".to_owned())?;
        return Ok(UiHost {
            window,
            webview,
            config,
            operations: OperationRegistry::default(),
        });
    }
    #[allow(unreachable_code)]
    Err("unsupported_desktop_target".to_owned())
}

pub fn run() -> io::Result<()> {
    let Some(config) = parse_config().map_err(io::Error::other)? else {
        return Ok(());
    };
    if wry::webview_version().is_err() {
        return Err(io::Error::other("webview_runtime_unavailable"));
    }
    let mut builder = EventLoopBuilder::<UserEvent>::with_user_event();
    let event_loop = build_event_loop(&mut builder).map_err(io::Error::other)?;
    let proxy = event_loop.create_proxy();
    let mut host: Option<UiHost> = None;
    event_loop.run(move |event, target, control_flow| {
        *control_flow = ControlFlow::Wait;
        if host.is_none() {
            match build_host(config.clone(), proxy.clone(), target) {
                Ok(created) => host = Some(created),
                Err(error) => {
                    eprintln!("{error}");
                    *control_flow = ControlFlow::Exit;
                    return;
                }
            }
        }
        let Some(host) = host.as_mut() else {
            *control_flow = ControlFlow::Exit;
            return;
        };
        match event {
            Event::UserEvent(UserEvent::Ipc(body)) => host.handle_ipc(body, &proxy),
            Event::UserEvent(UserEvent::Progress { id, event }) => {
                host.dispatch(ProgressMessage {
                    protocol: crate::UI_PROTOCOL_VERSION,
                    kind: "progress",
                    id,
                    operation: event.operation,
                    stage: event.stage,
                    item: event.item,
                    completed: event.completed,
                    total: event.total,
                });
            }
            Event::UserEvent(UserEvent::Finished { id, result }) => {
                host.handle_finished(id, result)
            }
            Event::WindowEvent {
                event: WindowEvent::CloseRequested,
                window_id,
                ..
            } if host.window.id() == window_id => {
                host.operations.cancel_all();
                *control_flow = ControlFlow::Exit;
            }
            _ => {}
        }
    })
}
