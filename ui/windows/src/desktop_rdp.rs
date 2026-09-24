use crate::DesktopRdpWindow;
use slint::{CloseRequestResponse, ComponentHandle, Timer};
#[cfg(windows)]
use slint::{Image, Rgba8Pixel, SharedPixelBuffer};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, Sender};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::Instant;

#[cfg(windows)]
use slint::TimerMode;
#[cfg(windows)]
use std::time::Duration;

/// Parameters for one in-memory RDP connection.
///
/// The UI supplies all connection parameters for one in-memory session. The
/// target is never serialized or logged.
#[cfg_attr(not(windows), allow(dead_code))]
pub struct RdpTarget {
    pub host: String,
    pub port: u16,
    pub username: Option<String>,
    pub password: Option<String>,
    pub domain: Option<String>,
    /// Accept a certificate that FreeRDP cannot validate for this session only.
    ///
    /// This is deliberately not persisted and never maps to
    /// `FreeRDP_IgnoreCertificate`, which would silently disable certificate
    /// verification for every connection.
    pub allow_untrusted_certificate: bool,
    pub width: u32,
    pub height: u32,
}

impl RdpTarget {
    pub fn with_credentials(
        host: String,
        username: String,
        password: String,
        domain: Option<String>,
    ) -> Self {
        Self {
            host,
            port: 3389,
            username: Some(username),
            password: Some(password),
            domain,
            allow_untrusted_certificate: false,
            width: 1920,
            height: 1080,
        }
    }
}

impl Drop for RdpTarget {
    fn drop(&mut self) {
        if let Some(password) = &mut self.password {
            wipe_string(password);
        }
    }
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug)]
struct RdpFrame {
    width: u32,
    height: u32,
    /// A private copy in RGBA8, converted off the Slint UI thread.
    #[cfg(windows)]
    pixels: SharedPixelBuffer<Rgba8Pixel>,
    #[cfg(not(windows))]
    pixels: Vec<u8>,
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone, Copy)]
enum PointerAction {
    Move,
    Down,
    Up,
    Cancel,
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone, Copy)]
enum PointerButton {
    Left,
    Right,
    Middle,
    Other,
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone, Copy)]
enum RdpInput {
    Pointer {
        action: PointerAction,
        button: PointerButton,
        x: f32,
        y: f32,
        viewport_width: f32,
        viewport_height: f32,
    },
}

#[cfg_attr(not(windows), allow(dead_code))]
struct PendingUpdate {
    state: String,
    status: String,
    frame: Option<RdpFrame>,
}

#[cfg_attr(not(windows), allow(dead_code))]
struct RdpPerfStats {
    enabled: bool,
    started: Instant,
    last_report: Mutex<Instant>,
    last_frame: Mutex<Option<Instant>>,
    paint_batches: AtomicU64,
    frame_intervals: AtomicU64,
    frame_interval_us: AtomicU64,
    coalesced_frames: AtomicU64,
    copied_bytes: AtomicU64,
    copy_time_us: AtomicU64,
    ui_handoffs: AtomicU64,
    ui_handoff_time_us: AtomicU64,
}

#[cfg_attr(not(windows), allow(dead_code))]
impl RdpPerfStats {
    fn new() -> Self {
        let now = Instant::now();
        Self {
            enabled: std::env::var("CHUZI_RDP_PERF")
                .map(|value| {
                    matches!(
                        value.trim().to_ascii_lowercase().as_str(),
                        "1" | "true" | "yes"
                    )
                })
                .unwrap_or(false),
            started: now,
            last_report: Mutex::new(now),
            last_frame: Mutex::new(None),
            paint_batches: AtomicU64::new(0),
            frame_intervals: AtomicU64::new(0),
            frame_interval_us: AtomicU64::new(0),
            coalesced_frames: AtomicU64::new(0),
            copied_bytes: AtomicU64::new(0),
            copy_time_us: AtomicU64::new(0),
            ui_handoffs: AtomicU64::new(0),
            ui_handoff_time_us: AtomicU64::new(0),
        }
    }

    fn record_copy(&self, bytes: usize, elapsed: std::time::Duration) {
        if !self.enabled {
            return;
        }
        if let Ok(mut last_frame) = self.last_frame.lock() {
            let now = Instant::now();
            if let Some(previous) = *last_frame {
                self.frame_intervals.fetch_add(1, Ordering::Relaxed);
                self.frame_interval_us
                    .fetch_add(previous.elapsed().as_micros() as u64, Ordering::Relaxed);
            }
            *last_frame = Some(now);
        }
        self.paint_batches.fetch_add(1, Ordering::Relaxed);
        self.copied_bytes.fetch_add(bytes as u64, Ordering::Relaxed);
        self.copy_time_us
            .fetch_add(elapsed.as_micros() as u64, Ordering::Relaxed);
        self.report_if_due(false);
    }

    fn record_coalesced(&self) {
        if !self.enabled {
            return;
        }
        self.coalesced_frames.fetch_add(1, Ordering::Relaxed);
    }

    fn record_ui_handoff(&self, elapsed: std::time::Duration) {
        if !self.enabled {
            return;
        }
        self.ui_handoffs.fetch_add(1, Ordering::Relaxed);
        self.ui_handoff_time_us
            .fetch_add(elapsed.as_micros() as u64, Ordering::Relaxed);
        self.report_if_due(false);
    }

    fn report_if_due(&self, force: bool) {
        if !self.enabled {
            return;
        }
        let Ok(mut last_report) = self.last_report.lock() else {
            return;
        };
        if !force && last_report.elapsed() < std::time::Duration::from_secs(1) {
            return;
        }
        *last_report = Instant::now();
        eprintln!(
            "rdp_perf {{\"elapsed_ms\":{},\"paint_batches\":{},\"frame_intervals\":{},\"frame_interval_us\":{},\"coalesced_frames\":{},\"copied_bytes\":{},\"copy_time_us\":{},\"ui_handoffs\":{},\"ui_handoff_time_us\":{}}}",
            self.started.elapsed().as_millis(),
            self.paint_batches.load(Ordering::Relaxed),
            self.frame_intervals.load(Ordering::Relaxed),
            self.frame_interval_us.load(Ordering::Relaxed),
            self.coalesced_frames.load(Ordering::Relaxed),
            self.copied_bytes.load(Ordering::Relaxed),
            self.copy_time_us.load(Ordering::Relaxed),
            self.ui_handoffs.load(Ordering::Relaxed),
            self.ui_handoff_time_us.load(Ordering::Relaxed),
        );
    }
}

#[cfg_attr(not(windows), allow(dead_code))]
struct SharedState {
    update: Mutex<PendingUpdate>,
    certificate_failure: Mutex<Option<String>>,
    perf: RdpPerfStats,
    pending: AtomicBool,
    stop: AtomicBool,
}

impl SharedState {
    fn new() -> Self {
        Self {
            update: Mutex::new(PendingUpdate {
                state: "connecting".to_owned(),
                status: "正在连接 RDP…".to_owned(),
                frame: None,
            }),
            certificate_failure: Mutex::new(None),
            perf: RdpPerfStats::new(),
            pending: AtomicBool::new(false),
            stop: AtomicBool::new(false),
        }
    }

    fn set_state(&self, state: impl Into<String>, status: impl Into<String>) {
        let state = state.into();
        let status = status.into();
        let mut changed = false;
        if let Ok(mut update) = self.update.lock() {
            if update.state != state || update.status != status {
                update.state = state;
                update.status = status;
                changed = true;
            }
        }
        if changed {
            self.pending.store(true, Ordering::Release);
        }
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn set_certificate_failure(&self, status: impl Into<String>) {
        let status = status.into();
        if let Ok(mut failure) = self.certificate_failure.lock() {
            *failure = Some(status.clone());
        }
        self.set_state("connecting", status);
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn certificate_failure(&self) -> Option<String> {
        self.certificate_failure
            .lock()
            .ok()
            .and_then(|failure| failure.clone())
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn set_frame(&self, frame: RdpFrame) {
        if let Ok(mut update) = self.update.lock() {
            // Keep the newest complete framebuffer. A stale frame must not
            // block a newer RDP update from reaching the UI.
            if update.frame.is_some() {
                self.perf.record_coalesced();
            }
            update.frame = Some(frame);
            self.pending.store(true, Ordering::Release);
        }
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn poll(&self) -> Option<PendingUpdate> {
        if !self.pending.swap(false, Ordering::AcqRel) {
            return None;
        }

        let Ok(mut update) = self.update.lock() else {
            return Some(PendingUpdate {
                state: "failed".to_owned(),
                status: "RDP 状态不可用。".to_owned(),
                frame: None,
            });
        };

        Some(PendingUpdate {
            state: update.state.clone(),
            status: update.status.clone(),
            frame: update.frame.take(),
        })
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn has_pending(&self) -> bool {
        self.pending.load(Ordering::Acquire)
    }

    fn status_snapshot(&self) -> (String, String) {
        let Ok(update) = self.update.lock() else {
            return ("failed".to_owned(), "RDP 状态不可用。".to_owned());
        };
        (update.state.clone(), update.status.clone())
    }
}

pub struct DesktopRdpController {
    _window: DesktopRdpWindow,
    shared: Arc<SharedState>,
    worker: Option<JoinHandle<()>>,
    frame_timer: Timer,
    #[cfg_attr(not(windows), allow(dead_code))]
    input_tx: Sender<RdpInput>,
}

impl DesktopRdpController {
    pub fn new_with_target(mut target: RdpTarget) -> Result<Self, String> {
        target.host = target.host.trim().to_owned();
        if target.host.is_empty() {
            return Err("RDP host must not be empty".to_owned());
        }
        if target.host.contains('\0') {
            return Err("RDP host contains an invalid NUL character".to_owned());
        }
        if target.port == 0 {
            return Err("RDP port must be between 1 and 65535".to_owned());
        }
        if target
            .username
            .as_deref()
            .map(str::trim)
            .unwrap_or_default()
            .is_empty()
        {
            return Err("RDP username is required".to_owned());
        }
        if target.password.as_deref().unwrap_or_default().is_empty() {
            return Err("RDP password is required".to_owned());
        }

        let window = DesktopRdpWindow::new().map_err(|error| error.to_string())?;
        window.set_host(target.host.clone().into());
        window.set_state("connecting".into());
        window.set_status("正在连接 RDP，等待远程桌面画面…".into());
        window
            .show()
            .map_err(|error| format!("desktop_rdp_window_show: {error}"))?;

        #[cfg(windows)]
        {
            let weak = window.as_weak();
            slint::Timer::single_shot(Duration::from_millis(1), move || {
                if let Some(window) = weak.upgrade() {
                    apply_windows_window_chrome(&window);
                }
            });
        }

        let weak = window.as_weak();
        window.on_hide_window(move || {
            if let Some(window) = weak.upgrade() {
                let _ = window.hide();
            }
        });

        window
            .window()
            .on_close_requested(move || CloseRequestResponse::HideWindow);

        let shared = Arc::new(SharedState::new());
        let (input_tx, input_rx) = mpsc::channel();
        let input_callback_tx = input_tx.clone();
        window.on_rdp_pointer_event(move |kind, button, x, y, viewport_width, viewport_height| {
            let action = match kind.as_str() {
                "move" => PointerAction::Move,
                "down" => PointerAction::Down,
                "up" => PointerAction::Up,
                _ => PointerAction::Cancel,
            };
            let button = match button.as_str() {
                "left" => PointerButton::Left,
                "right" => PointerButton::Right,
                "middle" => PointerButton::Middle,
                _ => PointerButton::Other,
            };
            let _ = input_callback_tx.send(RdpInput::Pointer {
                action,
                button,
                x,
                y,
                viewport_width,
                viewport_height,
            });
        });

        #[cfg(windows)]
        let worker = {
            let shared = Arc::clone(&shared);
            Some(std::thread::spawn(move || {
                freerdp::run(target, shared, input_rx)
            }))
        };

        #[cfg(not(windows))]
        let worker = {
            let _ = &input_rx;
            let _ = target;
            shared.set_state(
                "failed",
                "FreeRDP 仅随 Windows 构建提供；当前平台没有 RDP backend。",
            );
            None
        };

        let frame_timer = Timer::default();
        #[cfg(windows)]
        {
            let timer_shared = Arc::clone(&shared);
            let timer_window = window.as_weak();
            frame_timer.start(TimerMode::Repeated, Duration::from_millis(16), move || {
                if timer_shared.has_pending() {
                    apply_pending_update(&timer_window, &timer_shared);
                }
            });
        }

        Ok(Self {
            _window: window,
            shared,
            worker,
            frame_timer,
            input_tx,
        })
    }

    pub fn status_snapshot(&self) -> (String, String) {
        self.shared.status_snapshot()
    }
}

impl Drop for DesktopRdpController {
    fn drop(&mut self) {
        self.shared.stop.store(true, Ordering::SeqCst);
        self.frame_timer.stop();
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

#[cfg(windows)]
fn apply_pending_update(window: &slint::Weak<DesktopRdpWindow>, shared: &SharedState) {
    let Some(window) = window.upgrade() else {
        return;
    };

    let Some(update) = shared.poll() else {
        return;
    };
    window.set_state(update.state.into());
    window.set_status(update.status.into());

    if let Some(frame) = update.frame {
        let started = Instant::now();
        let image = frame_to_image(frame);
        match image {
            Some(image) => {
                window.set_frame(image);
                shared.perf.record_ui_handoff(started.elapsed());
            }
            None => shared.set_state("failed", "收到的 RDP framebuffer 尺寸无效。"),
        }
    }
}

#[cfg(windows)]
fn frame_to_image(frame: RdpFrame) -> Option<Image> {
    if frame.width == 0
        || frame.height == 0
        || frame.pixels.width() != frame.width
        || frame.pixels.height() != frame.height
    {
        return None;
    }
    Some(Image::from_rgba8(frame.pixels))
}

#[cfg(windows)]
mod freerdp {
    use super::{PointerAction, PointerButton, RdpFrame, RdpInput, RdpTarget, SharedState};
    use slint::{Rgba8Pixel, SharedPixelBuffer};
    use std::ffi::{c_char, CStr, CString};
    use std::mem::{size_of, zeroed};
    use std::sync::mpsc::Receiver;
    use std::sync::Arc;
    use windows_sys::Win32::Foundation::{BOOL, HANDLE, WAIT_FAILED, WAIT_TIMEOUT};
    use windows_sys::Win32::Networking::WinSock::{WSACleanup, WSAStartup, WSADATA};
    use windows_sys::Win32::System::Threading::WaitForMultipleObjects;

    #[allow(
        non_camel_case_types,
        non_snake_case,
        non_upper_case_globals,
        dead_code
    )]
    mod ffi {
        include!(concat!(env!("OUT_DIR"), "/freerdp_bindings.rs"));
    }

    #[repr(C)]
    struct AppContext {
        base: ffi::rdpContext,
        shared: *const SharedState,
        allow_untrusted_certificate: u8,
    }

    pub fn run(target: RdpTarget, shared: Arc<SharedState>, input_rx: Receiver<RdpInput>) {
        shared.set_state("connecting", "正在准备 FreeRDP 连接…");
        let result = unsafe { run_session(&target, &shared, &input_rx) };
        if shared.stop.load(std::sync::atomic::Ordering::SeqCst) {
            shared.set_state("closed", "RDP 连接已关闭。\n");
        } else if let Err(error) = result {
            let error = match shared.certificate_failure() {
                Some(certificate_failure) => format!("{certificate_failure}\n{error}"),
                None => error,
            };
            shared.set_state("failed", error);
        } else {
            shared.set_state("closed", "RDP 连接已断开。\n");
        }
        shared.perf.report_if_due(true);
    }

    unsafe fn run_session(
        target: &RdpTarget,
        shared: &Arc<SharedState>,
        input_rx: &Receiver<RdpInput>,
    ) -> Result<(), String> {
        // FreeRDP's standalone Windows clients initialize Winsock in their
        // process-level startup hook. Embedded clients must do that
        // explicitly before FreeRDP calls getaddrinfo or creates sockets.
        let _winsock = WinsockGuard::initialize()?;

        let instance = ffi::freerdp_new();
        if instance.is_null() {
            return Err("FreeRDP instance allocation failed".to_owned());
        }

        (*instance).ContextSize = size_of::<AppContext>();
        (*instance).ContextNew = Some(context_new);
        (*instance).ContextFree = Some(context_free);
        (*instance).PostConnect = Some(post_connect);
        (*instance).PostDisconnect = Some(post_disconnect);

        if ffi::freerdp_context_new(instance) == 0 {
            ffi::freerdp_free(instance);
            return Err("FreeRDP context initialization failed".to_owned());
        }

        let context = (*instance).context;
        if context.is_null() {
            ffi::freerdp_free(instance);
            return Err("FreeRDP returned a null context".to_owned());
        }
        let app_context = context.cast::<AppContext>();
        (*app_context).shared = Arc::as_ptr(shared);
        (*app_context).allow_untrusted_certificate = u8::from(target.allow_untrusted_certificate);

        // FreeRDP invokes these callbacks when its normal certificate store
        // cannot validate a certificate. Returning 2 from the callbacks means
        // accept for this connection only; returning 1 would persist trust.
        (*instance).VerifyCertificateEx = Some(verify_certificate);
        (*instance).VerifyChangedCertificateEx = Some(verify_changed_certificate);

        if let Err(error) = configure(instance, target) {
            ffi::freerdp_context_free(instance);
            ffi::freerdp_free(instance);
            return Err(error);
        }

        if ffi::freerdp_connect(instance) == 0 {
            let error = last_error(instance, "FreeRDP connection failed");
            ffi::freerdp_context_free(instance);
            ffi::freerdp_free(instance);
            return Err(error);
        }

        let event_result = event_loop(instance, shared, input_rx);
        ffi::freerdp_disconnect(instance);
        ffi::freerdp_context_free(instance);
        ffi::freerdp_free(instance);
        event_result
    }

    unsafe fn configure(instance: *mut ffi::freerdp, target: &RdpTarget) -> Result<(), String> {
        let context = (*instance).context;
        if context.is_null() || (*context).settings.is_null() {
            return Err("FreeRDP settings are unavailable".to_owned());
        }
        let settings = (*context).settings;
        let host =
            CString::new(target.host.as_str()).map_err(|_| "RDP host is invalid".to_owned())?;
        let username = CString::new(target.username.as_deref().unwrap_or_default())
            .map_err(|_| "RDP username is invalid".to_owned())?;
        let password = CString::new(target.password.as_deref().unwrap_or_default())
            .map_err(|_| "RDP password is invalid".to_owned())?;
        let domain = CString::new(target.domain.as_deref().unwrap_or_default())
            .map_err(|_| "RDP domain is invalid".to_owned())?;

        if ffi::freerdp_settings_set_string(
            settings,
            ffi::CHUZI_FREERDP_SERVER_HOSTNAME as _,
            host.as_ptr(),
        ) == 0
            || ffi::freerdp_settings_set_uint32(
                settings,
                ffi::CHUZI_FREERDP_SERVER_PORT as _,
                u32::from(target.port),
            ) == 0
            || ffi::freerdp_settings_set_string(
                settings,
                ffi::CHUZI_FREERDP_USERNAME as _,
                username.as_ptr(),
            ) == 0
            || ffi::freerdp_settings_set_string(
                settings,
                ffi::CHUZI_FREERDP_PASSWORD as _,
                password.as_ptr(),
            ) == 0
            || ffi::freerdp_settings_set_string(
                settings,
                ffi::CHUZI_FREERDP_DOMAIN as _,
                domain.as_ptr(),
            ) == 0
            || ffi::freerdp_settings_set_uint32(
                settings,
                ffi::CHUZI_FREERDP_DESKTOP_WIDTH as _,
                target.width,
            ) == 0
            || ffi::freerdp_settings_set_uint32(
                settings,
                ffi::CHUZI_FREERDP_DESKTOP_HEIGHT as _,
                target.height,
            ) == 0
            || ffi::freerdp_settings_set_uint32(
                settings,
                ffi::CHUZI_FREERDP_TCP_CONNECT_TIMEOUT as _,
                10_000,
            ) == 0
        {
            return Err("FreeRDP settings initialization failed".to_owned());
        }
        Ok(())
    }

    unsafe fn event_loop(
        instance: *mut ffi::freerdp,
        shared: &SharedState,
        input_rx: &Receiver<RdpInput>,
    ) -> Result<(), String> {
        let context = (*instance).context;
        if context.is_null() {
            return Err("FreeRDP context disappeared".to_owned());
        }

        loop {
            drain_input(instance, input_rx);
            if shared.stop.load(std::sync::atomic::Ordering::SeqCst)
                || ffi::freerdp_shall_disconnect_context(context) != 0
            {
                return Ok(());
            }

            let mut handles: [HANDLE; 64] = zeroed();
            let count =
                ffi::freerdp_get_event_handles(context, handles.as_mut_ptr(), handles.len() as u32);
            if count == 0 || count > handles.len() as u32 {
                return Err(last_error(
                    instance,
                    "FreeRDP returned invalid event handles",
                ));
            }

            let wait_result = WaitForMultipleObjects(count, handles.as_ptr(), 0, 16);
            if wait_result == WAIT_FAILED {
                return Err("Windows event wait for the RDP connection failed".to_owned());
            }
            if wait_result != WAIT_TIMEOUT && ffi::freerdp_check_event_handles(context) == 0 {
                return Err(last_error(instance, "FreeRDP event processing failed"));
            }
            drain_input(instance, input_rx);
        }
    }

    unsafe fn drain_input(instance: *mut ffi::freerdp, input_rx: &Receiver<RdpInput>) {
        let Some(context) = instance.as_ref().map(|instance| instance.context) else {
            return;
        };
        let Some(context) = context.as_ref() else {
            return;
        };
        let Some(input) = context.input.as_mut() else {
            return;
        };
        let Some(gdi) = context.gdi.as_ref() else {
            return;
        };
        let Some(width) = u32::try_from(gdi.width).ok().filter(|width| *width > 0) else {
            return;
        };
        let Some(height) = u32::try_from(gdi.height).ok().filter(|height| *height > 0) else {
            return;
        };

        while let Ok(RdpInput::Pointer {
            action,
            button,
            x,
            y,
            viewport_width,
            viewport_height,
        }) = input_rx.try_recv()
        {
            let Some((x, y)) = map_pointer(x, y, viewport_width, viewport_height, width, height)
            else {
                continue;
            };
            let flags = match action {
                PointerAction::Move => ffi::CHUZI_PTR_FLAGS_MOVE as u16,
                PointerAction::Down => {
                    let Some(button_flag) = pointer_button_flag(button) else {
                        continue;
                    };
                    button_flag | ffi::CHUZI_PTR_FLAGS_DOWN as u16
                }
                PointerAction::Up => {
                    let Some(button_flag) = pointer_button_flag(button) else {
                        continue;
                    };
                    button_flag
                }
                PointerAction::Cancel => continue,
            };
            let _ = ffi::freerdp_input_send_mouse_event(input, flags, x, y);
        }
    }

    fn pointer_button_flag(button: PointerButton) -> Option<u16> {
        match button {
            PointerButton::Left => Some(ffi::CHUZI_PTR_FLAGS_BUTTON1 as u16),
            PointerButton::Right => Some(ffi::CHUZI_PTR_FLAGS_BUTTON2 as u16),
            PointerButton::Middle => Some(ffi::CHUZI_PTR_FLAGS_BUTTON3 as u16),
            PointerButton::Other => None,
        }
    }

    fn map_pointer(
        x: f32,
        y: f32,
        viewport_width: f32,
        viewport_height: f32,
        frame_width: u32,
        frame_height: u32,
    ) -> Option<(u16, u16)> {
        if !x.is_finite()
            || !y.is_finite()
            || !viewport_width.is_finite()
            || !viewport_height.is_finite()
            || viewport_width <= 0.0
            || viewport_height <= 0.0
            || frame_width == 0
            || frame_height == 0
        {
            return None;
        }
        let frame_width = frame_width as f32;
        let frame_height = frame_height as f32;
        let scale = (viewport_width / frame_width).min(viewport_height / frame_height);
        if !scale.is_finite() || scale <= 0.0 {
            return None;
        }
        let rendered_width = frame_width * scale;
        let rendered_height = frame_height * scale;
        let offset_x = (viewport_width - rendered_width) / 2.0;
        let offset_y = (viewport_height - rendered_height) / 2.0;
        if x < offset_x
            || y < offset_y
            || x >= offset_x + rendered_width
            || y >= offset_y + rendered_height
        {
            return None;
        }
        let mapped_x = ((x - offset_x) / scale)
            .floor()
            .clamp(0.0, frame_width - 1.0);
        let mapped_y = ((y - offset_y) / scale)
            .floor()
            .clamp(0.0, frame_height - 1.0);
        Some((mapped_x as u16, mapped_y as u16))
    }

    unsafe extern "C" fn context_new(
        _instance: *mut ffi::freerdp,
        _context: *mut ffi::rdpContext,
    ) -> BOOL {
        1
    }

    unsafe extern "C" fn context_free(
        _instance: *mut ffi::freerdp,
        _context: *mut ffi::rdpContext,
    ) {
    }

    unsafe extern "C" fn verify_certificate(
        instance: *mut ffi::freerdp,
        host: *const c_char,
        port: u16,
        _common_name: *const c_char,
        _subject: *const c_char,
        _issuer: *const c_char,
        fingerprint: *const c_char,
        _flags: u32,
    ) -> u32 {
        handle_certificate_verification(instance, host, port, fingerprint, false)
    }

    unsafe extern "C" fn verify_changed_certificate(
        instance: *mut ffi::freerdp,
        host: *const c_char,
        port: u16,
        _common_name: *const c_char,
        _subject: *const c_char,
        _issuer: *const c_char,
        fingerprint: *const c_char,
        _old_subject: *const c_char,
        _old_issuer: *const c_char,
        _old_fingerprint: *const c_char,
        _flags: u32,
    ) -> u32 {
        handle_certificate_verification(instance, host, port, fingerprint, true)
    }

    unsafe fn handle_certificate_verification(
        instance: *mut ffi::freerdp,
        host: *const c_char,
        port: u16,
        fingerprint: *const c_char,
        changed: bool,
    ) -> u32 {
        let Some(app_context) = app_context_from_instance(instance) else {
            return 0;
        };
        let app_context = &*app_context;
        let Some(shared) = app_context.shared.as_ref() else {
            return 0;
        };

        let host = c_string_or_unknown(host);
        if app_context.allow_untrusted_certificate != 0 {
            let certificate_kind = if changed {
                "已变化或名称不匹配的"
            } else {
                "未受信任的"
            };
            shared.set_state(
                "connecting",
                format!("正在接受{certificate_kind} RDP 证书（仅本次连接）：{host}:{port}…"),
            );
            return 2;
        }

        let certificate_kind = if changed {
            "服务器证书已变化或名称不匹配"
        } else {
            "服务器证书未受信任或名称不匹配"
        };
        let mut message = format!("RDP {certificate_kind}，已拒绝连接（目标：{host}:{port}）。");
        if let Some(fingerprint) = certificate_value(fingerprint) {
            message.push_str(&format!("\n证书指纹：{fingerprint}"));
        }
        message.push_str("\n请先核对指纹；确认目标可信后，勾选“仅本次接受不受信任证书”再重试。");
        shared.set_certificate_failure(message);
        0
    }

    unsafe fn app_context_from_instance(instance: *mut ffi::freerdp) -> Option<*const AppContext> {
        let context = instance.as_ref()?.context;
        (!context.is_null()).then(|| context.cast::<AppContext>().cast_const())
    }

    unsafe fn c_string_or_unknown(value: *const c_char) -> String {
        if value.is_null() {
            return "<unknown>".to_owned();
        }
        CStr::from_ptr(value).to_string_lossy().into_owned()
    }

    unsafe fn certificate_value(value: *const c_char) -> Option<String> {
        if value.is_null() {
            return None;
        }
        let value = CStr::from_ptr(value).to_string_lossy();
        let value = value.trim();
        if value.is_empty() || value.contains("BEGIN CERTIFICATE") {
            return None;
        }
        let mut value = value.chars().take(256).collect::<String>();
        if value.chars().count() == 256 {
            value.push('…');
        }
        Some(value)
    }

    struct WinsockGuard;

    impl WinsockGuard {
        unsafe fn initialize() -> Result<Self, String> {
            let mut data: WSADATA = zeroed();
            let result = WSAStartup(0x0202, &mut data);
            if result != 0 {
                return Err(format!("Winsock initialization failed ({result})"));
            }
            Ok(Self)
        }
    }

    impl Drop for WinsockGuard {
        fn drop(&mut self) {
            unsafe {
                let _ = WSACleanup();
            }
        }
    }

    unsafe extern "C" fn post_connect(instance: *mut ffi::freerdp) -> BOOL {
        if instance.is_null() || (*instance).context.is_null() {
            return 0;
        }
        let context = (*instance).context;
        if ffi::gdi_init(instance, ffi::CHUZI_PIXEL_FORMAT_RGBA32 as u32) == 0 {
            return 0;
        }
        if ffi::freerdp_settings_set_bool(
            (*context).settings,
            ffi::CHUZI_FREERDP_DEACTIVATE_CLIENT_DECODING as _,
            0,
        ) == 0
        {
            return 0;
        }
        let Some(update) = (*context).update.as_mut() else {
            return 0;
        };
        update.BeginPaint = Some(begin_paint);
        update.EndPaint = Some(end_paint);
        update.DesktopResize = Some(desktop_resize);
        if let Some(shared) = shared_from_context(&*context) {
            shared.set_state("connected", "RDP 已连接，正在接收远程桌面画面…");
        }
        1
    }

    unsafe extern "C" fn post_disconnect(instance: *mut ffi::freerdp) {
        if !instance.is_null() {
            ffi::gdi_free(instance);
        }
    }

    unsafe extern "C" fn begin_paint(context: *mut ffi::rdpContext) -> BOOL {
        if let Some(gdi) = context.as_ref().and_then(|context| context.gdi.as_ref()) {
            // Start a fresh invalidation batch. FreeRDP accumulates dirty
            // rectangles between BeginPaint and EndPaint.
            reset_dirty_region(gdi);
        }
        1
    }

    unsafe extern "C" fn end_paint(context: *mut ffi::rdpContext) -> BOOL {
        let Some(context) = context.as_ref() else {
            return 0;
        };
        let Some(gdi) = context.gdi.as_ref() else {
            return 0;
        };
        let has_dirty_region = gdi
            .primary
            .as_ref()
            .and_then(|primary| primary.hdc.as_ref())
            .and_then(|hdc| hdc.hwnd.as_ref())
            .and_then(|hwnd| hwnd.invalid.as_ref())
            .map(|invalid| invalid.null == 0)
            .unwrap_or(false);
        if !has_dirty_region {
            reset_dirty_region(gdi);
            return 1;
        }
        if gdi.width <= 0 || gdi.height <= 0 || gdi.stride == 0 || gdi.primary_buffer.is_null() {
            reset_dirty_region(gdi);
            return 0;
        }

        let Ok(width) = usize::try_from(gdi.width) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let Ok(height) = usize::try_from(gdi.height) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let Ok(stride) = usize::try_from(gdi.stride) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let Some(row_bytes) = width.checked_mul(4) else {
            reset_dirty_region(gdi);
            return 0;
        };
        if stride < row_bytes {
            reset_dirty_region(gdi);
            return 0;
        }
        let Some(bytes) = stride.checked_mul(height) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let Some(pixel_bytes) = row_bytes.checked_mul(height) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let source = std::slice::from_raw_parts(gdi.primary_buffer, bytes);
        let mut pixels = SharedPixelBuffer::<Rgba8Pixel>::new(width as u32, height as u32);
        let destination = pixels.make_mut_bytes();
        let copy_started = std::time::Instant::now();
        if stride == row_bytes {
            // FreeRDP was initialized with PIXEL_FORMAT_RGBA32, so the
            // framebuffer is already in the byte order Slint expects.
            destination.copy_from_slice(source);
        } else {
            for row in 0..height {
                let source = &source[row * stride..row * stride + row_bytes];
                let destination = &mut destination[row * row_bytes..(row + 1) * row_bytes];
                destination.copy_from_slice(source);
            }
        }

        if let Some(shared) = shared_from_context(context) {
            shared.perf.record_copy(pixel_bytes, copy_started.elapsed());
            shared.set_frame(RdpFrame {
                width: width as u32,
                height: height as u32,
                pixels,
            });
        }
        reset_dirty_region(gdi);
        1
    }

    unsafe fn reset_dirty_region(gdi: &ffi::rdpGdi) {
        let Some(primary) = gdi.primary.as_ref() else {
            return;
        };
        let Some(hdc) = primary.hdc.as_ref() else {
            return;
        };
        let Some(hwnd) = hdc.hwnd.as_mut() else {
            return;
        };
        if let Some(invalid) = hwnd.invalid.as_mut() {
            invalid.null = 1;
        }
        hwnd.ninvalid = 0;
    }

    unsafe extern "C" fn desktop_resize(context: *mut ffi::rdpContext) -> BOOL {
        let Some(context) = context.as_ref() else {
            return 0;
        };
        let gdi = context.gdi;
        if gdi.is_null() {
            return 0;
        }
        let settings = context.settings;
        if settings.is_null() {
            return 0;
        }
        let width =
            ffi::freerdp_settings_get_uint32(settings, ffi::CHUZI_FREERDP_DESKTOP_WIDTH as _);
        let height =
            ffi::freerdp_settings_get_uint32(settings, ffi::CHUZI_FREERDP_DESKTOP_HEIGHT as _);
        ffi::gdi_resize(gdi, width, height)
    }

    unsafe fn shared_from_context(context: &ffi::rdpContext) -> Option<&SharedState> {
        let app_context = (context as *const ffi::rdpContext).cast::<AppContext>();
        app_context.as_ref()?.shared.as_ref()
    }

    unsafe fn last_error(instance: *mut ffi::freerdp, prefix: &str) -> String {
        let Some(context) = instance
            .as_ref()
            .and_then(|instance| instance.context.as_ref())
        else {
            return prefix.to_owned();
        };
        let context = context as *const ffi::rdpContext as *mut ffi::rdpContext;
        let code = ffi::freerdp_get_last_error(context);
        let name = ffi::freerdp_get_last_error_name(code);
        if name.is_null() {
            return format!("{prefix} (error code {code})");
        }
        let name = CStr::from_ptr(name).to_string_lossy();
        format!("{prefix} ({name})")
    }
}

#[cfg(windows)]
fn apply_windows_window_chrome(window: &DesktopRdpWindow) {
    use raw_window_handle::{HasWindowHandle, RawWindowHandle};
    use windows_sys::Win32::Graphics::Dwm::{
        DwmSetWindowAttribute, DWMWA_BORDER_COLOR, DWMWA_USE_IMMERSIVE_DARK_MODE,
        DWMWA_WINDOW_CORNER_PREFERENCE, DWMWCP_ROUND,
    };

    let handle = window.window().window_handle();
    let Ok(handle) = handle.window_handle() else {
        return;
    };
    let RawWindowHandle::Win32(win32) = handle.as_raw() else {
        return;
    };

    let hwnd = win32.hwnd.get() as *mut c_void;
    let corner_preference: u32 = DWMWCP_ROUND as u32;
    let dark_mode: u32 = 1;
    let border_color: u32 = 0x00141414;

    unsafe {
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_WINDOW_CORNER_PREFERENCE as u32,
            (&corner_preference as *const u32).cast::<c_void>(),
            size_of_val(&corner_preference) as u32,
        );
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_USE_IMMERSIVE_DARK_MODE as u32,
            (&dark_mode as *const u32).cast::<c_void>(),
            size_of_val(&dark_mode) as u32,
        );
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_BORDER_COLOR as u32,
            (&border_color as *const u32).cast::<c_void>(),
            size_of_val(&border_color) as u32,
        );
    }
}

#[cfg(windows)]
use std::ffi::c_void;
#[cfg(windows)]
use std::mem::size_of_val;

fn wipe_string(value: &mut String) {
    for index in 0..value.len() {
        unsafe {
            std::ptr::write_volatile(value.as_mut_ptr().add(index), 0);
        }
    }
    value.clear();
}

#[cfg(test)]
mod tests {
    use super::{RdpFrame, SharedState};

    #[test]
    fn shared_state_replaces_stale_frame_with_latest_frame() {
        let shared = SharedState::new();
        shared.set_frame(RdpFrame {
            width: 1,
            height: 1,
            pixels: vec![1, 2, 3, 4],
        });
        shared.set_frame(RdpFrame {
            width: 1,
            height: 1,
            pixels: vec![5, 6, 7, 8],
        });

        let update = shared.poll().expect("latest update should be available");
        let frame = update.frame.expect("latest frame should be available");
        assert_eq!(frame.pixels, vec![5, 6, 7, 8]);
        assert!(shared.poll().is_none());
    }

    #[test]
    fn shared_state_only_marks_changed_state_as_pending() {
        let shared = SharedState::new();
        assert!(!shared.has_pending());
        assert!(shared.poll().is_none());

        shared.set_state("connecting", "正在连接 RDP…");
        assert!(!shared.has_pending());

        shared.set_state("connected", "RDP 已连接");
        assert!(shared.has_pending());
        let update = shared.poll().expect("changed state should be available");
        assert_eq!(update.state, "connected");
        assert_eq!(update.status, "RDP 已连接");
        assert!(!shared.has_pending());
    }
}
