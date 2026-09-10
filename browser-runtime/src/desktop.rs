//! Wry-backed desktop WebView runtime.
//!
//! The module is target-gated to the platforms with a Wry desktop backend.
//! Linux uses WebKitGTK through Tao's GTK container path, which supports both
//! X11 and Wayland when a graphical session is available.

use std::{
    io::{self, BufRead, Write},
    panic::{catch_unwind, AssertUnwindSafe},
    path::{Component, Path},
    thread,
};

#[cfg(any(target_os = "windows", target_os = "linux"))]
use std::path::PathBuf;

use serde::Deserialize;
#[cfg(target_os = "linux")]
use tao::platform::unix::WindowExtUnix;
use tao::{
    dpi::LogicalSize,
    event::{Event, WindowEvent},
    event_loop::{ControlFlow, EventLoop, EventLoopBuilder, EventLoopProxy},
    window::{Window, WindowBuilder},
};
#[cfg(target_os = "linux")]
use wry::PageLoadEvent;
#[cfg(target_os = "linux")]
use wry::WebViewBuilderExtUnix;
use wry::{NewWindowResponse, PermissionResponse, WebContext, WebView, WebViewBuilder};

use crate::{Envelope, PROTOCOL_VERSION};

const BROWSER_RUNTIME: &str = "wry-desktop";
#[cfg(target_os = "linux")]
const CAPABILITIES: &str = "protocol.v1,browser-runtime.contract,browser-runtime.desktop-webview,browser-runtime.linux-webkitgtk,browser-runtime.linux-x11,browser-runtime.linux-wayland";
#[cfg(not(target_os = "linux"))]
const CAPABILITIES: &str = "protocol.v1,browser-runtime.contract,browser-runtime.desktop-webview";
const TEST_PAGE_RESULT: &str = "local-test-page-ready";
const TEST_PAGE_HTML: &str = r#"<!doctype html>
<meta charset="utf-8">
<title>chuzi local runtime test</title>
<body data-chuzi-runtime="ready">chuzi</body>
<script>
  window.addEventListener("load", () => {
    window.ipc.postMessage(JSON.stringify({event: "ready", result: "ok"}));
  });
</script>"#;

#[derive(Debug)]
enum Command {
    Line(String),
    Ipc(String),
    InputClosed,
}

#[derive(Debug, Deserialize)]
struct IpcMessage {
    event: String,
    result: String,
}

struct ActiveSession {
    request_id: String,
    session_id: String,
    hidden: bool,
    window: Window,
    _webview: WebView,
    #[cfg(any(target_os = "windows", target_os = "linux"))]
    _context: WebContext,
}

struct DesktopSession {
    window: Window,
    webview: WebView,
    #[cfg(any(target_os = "windows", target_os = "linux"))]
    context: WebContext,
}

#[derive(Debug, Clone, Copy)]
enum BuildError {
    RuntimeUnavailable,
    GuiSessionUnavailable,
    WebViewUnavailable,
}

struct DesktopHost {
    proxy: EventLoopProxy<Command>,
    active: Option<ActiveSession>,
    input_closed: bool,
}

impl DesktopHost {
    fn new(proxy: EventLoopProxy<Command>) -> Self {
        Self {
            proxy,
            active: None,
            input_closed: false,
        }
    }

    fn handle_line(
        &mut self,
        line: String,
        stdout: &mut io::BufWriter<io::Stdout>,
        event_loop_window: &tao::event_loop::EventLoopWindowTarget<Command>,
        control_flow: &mut ControlFlow,
    ) -> io::Result<()> {
        if line.trim().is_empty() {
            return Ok(());
        }

        let request = match serde_json::from_str::<Envelope>(&line) {
            Ok(request) => request,
            Err(_) => Envelope {
                protocol: PROTOCOL_VERSION.to_owned(),
                id: "unknown".to_owned(),
                kind: "error".to_owned(),
                payload: Default::default(),
                error: Some("invalid JSON".to_owned()),
            },
        };

        if request.error.is_some() || (request.kind == "error" && request.id == "unknown") {
            return write_responses(stdout, vec![request]);
        }

        if request.protocol != PROTOCOL_VERSION {
            return write_responses(
                stdout,
                vec![Envelope::error(
                    &request,
                    format!(
                        "unsupported protocol: {}",
                        if request.protocol.is_empty() {
                            "missing"
                        } else {
                            request.protocol.as_str()
                        }
                    ),
                )],
            );
        }

        match request.kind.as_str() {
            "hello" => write_responses(stdout, vec![self.hello(&request)]),
            "ping" => write_responses(
                stdout,
                vec![Envelope::response(&request, "pong", Default::default())],
            ),
            "session_start" => self.start_session(request, stdout, event_loop_window),
            "session_cancel" => self.cancel_session(request, stdout),
            "shutdown" => {
                self.active.take();
                let result = write_responses(
                    stdout,
                    vec![Envelope::response(
                        &request,
                        "shutdown_ack",
                        Default::default(),
                    )],
                );
                *control_flow = ControlFlow::Exit;
                result
            }
            _ => write_responses(
                stdout,
                vec![Envelope::error(
                    &request,
                    format!(
                        "unsupported message type: {}",
                        if request.kind.is_empty() {
                            "missing"
                        } else {
                            request.kind.as_str()
                        }
                    ),
                )],
            ),
        }
    }

    fn handle_ipc(
        &mut self,
        body: String,
        stdout: &mut io::BufWriter<io::Stdout>,
    ) -> io::Result<()> {
        let Ok(message) = serde_json::from_str::<IpcMessage>(&body) else {
            return Ok(());
        };
        if message.event != "ready" || message.result != "ok" {
            return Ok(());
        }

        let Some(active) = self.active.take() else {
            return Ok(());
        };
        if active.hidden {
            let _ = active._webview.set_visible(false);
            active.window.set_visible(false);
        }
        let mut payload = std::collections::BTreeMap::new();
        payload.insert("session_id".to_owned(), active.session_id);
        payload.insert("result".to_owned(), TEST_PAGE_RESULT.to_owned());
        write_responses(
            stdout,
            vec![Envelope {
                protocol: PROTOCOL_VERSION.to_owned(),
                id: active.request_id,
                kind: "session_succeeded".to_owned(),
                payload,
                error: None,
            }],
        )
    }

    fn handle_window_close(
        &mut self,
        window_id: tao::window::WindowId,
        stdout: &mut io::BufWriter<io::Stdout>,
    ) -> io::Result<()> {
        let Some(active) = self.active.as_ref() else {
            return Ok(());
        };
        if active.window.id() != window_id {
            return Ok(());
        }

        let active = self.active.take().expect("active session was checked");
        let mut payload = std::collections::BTreeMap::new();
        payload.insert("session_id".to_owned(), active.session_id);
        payload.insert("already_stopped".to_owned(), "false".to_owned());
        write_responses(
            stdout,
            vec![Envelope {
                protocol: PROTOCOL_VERSION.to_owned(),
                id: active.request_id,
                kind: "session_cancelled".to_owned(),
                payload,
                error: None,
            }],
        )
    }

    fn hello(&self, request: &Envelope) -> Envelope {
        let mut payload = std::collections::BTreeMap::new();
        payload.insert("service".to_owned(), "chuzi-browser-runtime".to_owned());
        payload.insert("browserRuntime".to_owned(), BROWSER_RUNTIME.to_owned());
        payload.insert("capabilities".to_owned(), CAPABILITIES.to_owned());
        match wry::webview_version() {
            Ok(version) => {
                payload.insert("runtimeAvailable".to_owned(), "true".to_owned());
                payload.insert("runtimeVersion".to_owned(), version);
            }
            Err(_) => {
                payload.insert("runtimeAvailable".to_owned(), "false".to_owned());
                payload.insert(
                    "runtimeError".to_owned(),
                    "webview_runtime_unavailable".to_owned(),
                );
            }
        }
        Envelope::response(request, "hello_ack", payload)
    }

    fn start_session(
        &mut self,
        request: Envelope,
        stdout: &mut io::BufWriter<io::Stdout>,
        event_loop_window: &tao::event_loop::EventLoopWindowTarget<Command>,
    ) -> io::Result<()> {
        for field in ["session_id", "account_id", "request_id", "profile_dir"] {
            if request
                .payload
                .get(field)
                .is_none_or(|value| value.trim().is_empty())
            {
                return write_responses(
                    stdout,
                    vec![Envelope::error(
                        &request,
                        format!("session_start requires {field}"),
                    )],
                );
            }
        }

        let session_id = request.payload["session_id"].clone();
        if self.active.is_some() {
            return write_responses(
                stdout,
                vec![Envelope::error(&request, "session is already running")],
            );
        }

        let visible = match request.payload.get("visibility").map(String::as_str) {
            None | Some("hidden") => false,
            Some("visible") => true,
            Some(_) => {
                return write_responses(
                    stdout,
                    vec![Envelope::error(
                        &request,
                        "visibility must be visible or hidden",
                    )],
                )
            }
        };
        let profile_dir = request.payload["profile_dir"].clone();
        if !valid_profile_dir(&profile_dir) {
            return write_responses(
                stdout,
                vec![Envelope::error(
                    &request,
                    "profile_dir must be an absolute service-derived path",
                )],
            );
        }

        let mut responses = vec![Envelope::response(
            &request,
            "session_started",
            [("session_id".to_owned(), session_id.clone())]
                .into_iter()
                .collect(),
        )];
        if let Err(error) = write_responses(stdout, responses.drain(..).collect()) {
            return Err(error);
        }

        let build_result = catch_native_panic(
            || self.build_session(&session_id, &profile_dir, visible, event_loop_window),
            BuildError::WebViewUnavailable,
        )
        .and_then(|result| result);
        match build_result {
            Ok(session) => {
                self.active = Some(ActiveSession {
                    request_id: request.id,
                    session_id,
                    hidden: !visible,
                    window: session.window,
                    _webview: session.webview,
                    #[cfg(any(target_os = "windows", target_os = "linux"))]
                    _context: session.context,
                });
                Ok(())
            }
            Err(error) => write_responses(
                stdout,
                vec![session_failure(&request, build_error_reason(error))],
            ),
        }
    }

    fn cancel_session(
        &mut self,
        request: Envelope,
        stdout: &mut io::BufWriter<io::Stdout>,
    ) -> io::Result<()> {
        let Some(session_id) = request.payload.get("session_id").cloned() else {
            return write_responses(
                stdout,
                vec![Envelope::error(
                    &request,
                    "session_cancel requires session_id",
                )],
            );
        };

        if self
            .active
            .as_ref()
            .is_some_and(|active| active.session_id == session_id)
        {
            let active = self.active.take().expect("active session was checked");
            return write_responses(
                stdout,
                vec![Envelope {
                    protocol: PROTOCOL_VERSION.to_owned(),
                    id: active.request_id,
                    kind: "session_cancelled".to_owned(),
                    payload: [("session_id".to_owned(), session_id)]
                        .into_iter()
                        .collect(),
                    error: None,
                }],
            );
        }

        let mut payload = std::collections::BTreeMap::new();
        payload.insert("session_id".to_owned(), session_id);
        payload.insert("already_stopped".to_owned(), "true".to_owned());
        write_responses(
            stdout,
            vec![Envelope::response(&request, "session_cancelled", payload)],
        )
    }

    #[cfg(target_os = "windows")]
    fn build_session(
        &self,
        _session_id: &str,
        profile_dir: &str,
        visible: bool,
        event_loop_window: &tao::event_loop::EventLoopWindowTarget<Command>,
    ) -> Result<DesktopSession, BuildError> {
        ensure_runtime()?;
        let mut context = WebContext::new(Some(PathBuf::from(profile_dir)));
        let builder = WebViewBuilder::new_with_web_context(&mut context);
        let (window, webview) =
            self.build_window_and_webview(builder, visible, event_loop_window)?;
        Ok(DesktopSession {
            window,
            webview,
            context,
        })
    }

    #[cfg(target_os = "linux")]
    fn build_session(
        &self,
        _session_id: &str,
        profile_dir: &str,
        visible: bool,
        event_loop_window: &tao::event_loop::EventLoopWindowTarget<Command>,
    ) -> Result<DesktopSession, BuildError> {
        ensure_runtime()?;
        let mut context = WebContext::new(Some(PathBuf::from(profile_dir)));
        let builder = WebViewBuilder::new_with_web_context(&mut context);
        let (window, webview) =
            self.build_window_and_webview(builder, visible, event_loop_window)?;
        Ok(DesktopSession {
            window,
            webview,
            context,
        })
    }

    #[cfg(target_os = "macos")]
    fn build_session(
        &self,
        _session_id: &str,
        _profile_dir: &str,
        visible: bool,
        event_loop_window: &tao::event_loop::EventLoopWindowTarget<Command>,
    ) -> Result<DesktopSession, BuildError> {
        ensure_runtime()?;
        let builder = WebViewBuilder::new().with_incognito(true);
        let (window, webview) =
            self.build_window_and_webview(builder, visible, event_loop_window)?;
        Ok(DesktopSession { window, webview })
    }

    fn build_window_and_webview<'a>(
        &self,
        builder: wry::WebViewBuilder<'a>,
        visible: bool,
        event_loop_window: &tao::event_loop::EventLoopWindowTarget<Command>,
    ) -> Result<(Window, WebView), BuildError> {
        let window = WindowBuilder::new()
            .with_title("chuzi browser runtime")
            .with_inner_size(LogicalSize::new(800.0, 600.0))
            // GTK/WebKitGTK needs a realized visible pass before a hidden
            // WebView reliably starts loading its local document and emits
            // readiness IPC. Hidden mode is applied after that IPC arrives.
            .with_visible(if cfg!(target_os = "linux") {
                true
            } else {
                visible
            })
            .build(event_loop_window)
            .map_err(|_| BuildError::GuiSessionUnavailable)?;

        let proxy = self.proxy.clone();
        #[cfg(target_os = "linux")]
        let load_proxy = self.proxy.clone();
        let builder = builder
            .with_visible(if cfg!(target_os = "linux") {
                true
            } else {
                visible
            })
            .with_html(TEST_PAGE_HTML)
            .with_clipboard(false)
            .with_autoplay(false)
            .with_general_autofill_enabled(false)
            .with_navigation_handler(allow_embedded_navigation)
            .with_new_window_req_handler(|_, _| NewWindowResponse::Deny)
            .with_permission_handler(|_| PermissionResponse::Deny)
            .with_download_started_handler(|_, _| false)
            .with_ipc_handler(move |request| {
                let _ = proxy.send_event(Command::Ipc(request.body().clone()));
            });
        #[cfg(target_os = "linux")]
        let builder = builder.with_on_page_load_handler(move |event, _url| {
            if matches!(event, PageLoadEvent::Finished) {
                let _ = load_proxy.send_event(Command::Ipc(
                    r#"{"event":"ready","result":"ok"}"#.to_owned(),
                ));
            }
        });
        #[cfg(target_os = "linux")]
        let webview = {
            let vbox = window
                .default_vbox()
                .ok_or(BuildError::GuiSessionUnavailable)?;
            builder
                .build_gtk(vbox)
                .map_err(|_| BuildError::WebViewUnavailable)?
        };
        #[cfg(not(target_os = "linux"))]
        let webview = builder
            .build(&window)
            .map_err(|_| BuildError::WebViewUnavailable)?;
        Ok((window, webview))
    }
}

fn ensure_runtime() -> Result<(), BuildError> {
    wry::webview_version()
        .map(|_| ())
        .map_err(|_| BuildError::RuntimeUnavailable)
}

fn allow_embedded_navigation(url: String) -> bool {
    // WebKitGTK reports the null-base `load_html` document as about:blank.
    // Allow that synthetic initial navigation, while keeping external and
    // user-triggered navigations denied by default.
    url == "about:blank"
}

fn valid_profile_dir(value: &str) -> bool {
    let path = Path::new(value);
    !value.trim().is_empty()
        && path.is_absolute()
        && path
            .components()
            .all(|component| !matches!(component, Component::ParentDir))
}

fn build_error_reason(error: BuildError) -> &'static str {
    match error {
        BuildError::RuntimeUnavailable => "webview_runtime_unavailable",
        BuildError::GuiSessionUnavailable => "gui_session_unavailable",
        BuildError::WebViewUnavailable => "webview_build_failed",
    }
}

fn session_failure(request: &Envelope, reason: &str) -> Envelope {
    let mut payload = std::collections::BTreeMap::new();
    if let Some(session_id) = request.payload.get("session_id") {
        payload.insert("session_id".to_owned(), session_id.clone());
    }
    payload.insert("failure".to_owned(), "configuration".to_owned());
    payload.insert("reason".to_owned(), reason.to_owned());
    Envelope::response(request, "session_failed", payload)
}

fn write_responses(
    stdout: &mut io::BufWriter<io::Stdout>,
    responses: Vec<Envelope>,
) -> io::Result<()> {
    for response in responses {
        serde_json::to_writer(&mut *stdout, &response)?;
        stdout.write_all(b"\n")?;
    }
    stdout.flush()
}

pub fn run() -> io::Result<()> {
    let mut event_loop_builder = EventLoopBuilder::<Command>::with_user_event();
    let event_loop = match build_event_loop(&mut event_loop_builder) {
        Ok(event_loop) => event_loop,
        Err(error) => {
            let mut stdout = io::BufWriter::new(io::stdout());
            write_responses(&mut stdout, vec![startup_failure(error)])?;
            return Ok(());
        }
    };
    let proxy = event_loop.create_proxy();
    let stdin_proxy = proxy.clone();
    thread::Builder::new()
        .name("chuzi-browser-runtime-stdin".to_owned())
        .spawn(move || {
            let stdin = io::stdin();
            for line in stdin.lock().lines() {
                let Ok(line) = line else {
                    break;
                };
                if stdin_proxy.send_event(Command::Line(line)).is_err() {
                    return;
                }
            }
            let _ = stdin_proxy.send_event(Command::InputClosed);
        })?;

    let mut host = DesktopHost::new(proxy);
    let mut stdout = io::BufWriter::new(io::stdout());
    event_loop.run(move |event, event_loop_window, control_flow| {
        *control_flow = ControlFlow::Wait;
        let result = match event {
            Event::UserEvent(Command::Line(line)) => {
                host.handle_line(line, &mut stdout, event_loop_window, control_flow)
            }
            Event::UserEvent(Command::Ipc(body)) => host.handle_ipc(body, &mut stdout),
            Event::UserEvent(Command::InputClosed) => {
                // A pipe writer may close stdin immediately after sending a
                // start request. Keep the WebView alive long enough for its
                // local readiness IPC to arrive; exit once the session has a
                // terminal result.
                host.input_closed = true;
                if host.active.is_none() {
                    *control_flow = ControlFlow::Exit;
                }
                Ok(())
            }
            Event::WindowEvent {
                event: WindowEvent::CloseRequested,
                window_id,
                ..
            } => host.handle_window_close(window_id, &mut stdout),
            _ => Ok(()),
        };
        if host.input_closed && host.active.is_none() {
            *control_flow = ControlFlow::Exit;
        }
        if result.is_err() {
            *control_flow = ControlFlow::Exit;
        }
    });
}

fn build_event_loop(
    builder: &mut EventLoopBuilder<Command>,
) -> Result<EventLoop<Command>, BuildError> {
    // tao exposes an infallible build API, but its platform backends can panic
    // when no GUI session/display is available. Convert that implementation
    // detail into the stable runtime fact used by the control service and do
    // not leak native panic text to stderr.
    catch_native_panic(|| builder.build(), BuildError::GuiSessionUnavailable)
}

fn catch_native_panic<T>(
    function: impl FnOnce() -> T,
    panic_error: BuildError,
) -> Result<T, BuildError> {
    let previous_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(|_| {}));
    let result = catch_unwind(AssertUnwindSafe(function));
    std::panic::set_hook(previous_hook);
    result.map_err(|_| panic_error)
}

fn startup_failure(error: BuildError) -> Envelope {
    Envelope {
        protocol: PROTOCOL_VERSION.to_owned(),
        id: "unknown".to_owned(),
        kind: "error".to_owned(),
        payload: Default::default(),
        error: Some(build_error_reason(error).to_owned()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn profile_directory_accepts_only_absolute_paths_without_parent_segments() {
        assert!(valid_profile_dir(if cfg!(target_os = "windows") {
            r"C:\\service\\profiles\\account"
        } else {
            "/service/profiles/account"
        }));
        assert!(!valid_profile_dir("service/profiles/account"));
        assert!(!valid_profile_dir(""));
        assert!(!valid_profile_dir(if cfg!(target_os = "windows") {
            r"C:\\service\\profiles\\..\\account"
        } else {
            "/service/profiles/../account"
        }));
    }

    #[test]
    fn startup_failures_are_redacted_classified_errors() {
        let failure = startup_failure(BuildError::GuiSessionUnavailable);
        assert_eq!(failure.kind, "error");
        assert_eq!(failure.id, "unknown");
        assert_eq!(failure.error.as_deref(), Some("gui_session_unavailable"));
        assert!(failure.payload.is_empty());
    }

    #[test]
    fn native_panics_are_mapped_without_exposing_the_panic_message() {
        let result = catch_native_panic(
            || -> Result<(), BuildError> { panic!("native detail must stay private") },
            BuildError::WebViewUnavailable,
        )
        .and_then(|result| result);
        assert!(matches!(result, Err(BuildError::WebViewUnavailable)));
    }

    #[test]
    fn navigation_policy_allows_only_the_embedded_initial_document() {
        assert!(allow_embedded_navigation("about:blank".to_owned()));
        assert!(!allow_embedded_navigation(
            "https://example.invalid".to_owned()
        ));
        assert!(!allow_embedded_navigation(
            "file:///tmp/page.html".to_owned()
        ));
    }
}
