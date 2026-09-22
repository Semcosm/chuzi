use crate::DesktopRdpWindow;
use slint::{CloseRequestResponse, ComponentHandle, Timer};
#[cfg(windows)]
use slint::{Image, Rgba8Pixel, SharedPixelBuffer};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;

#[cfg(windows)]
use slint::TimerMode;
#[cfg(windows)]
use std::time::Duration;

/// Parameters for one in-memory RDP connection.
///
/// The UI currently supplies only `host`. Credentials can be injected by a
/// caller that owns them, and otherwise the Windows credential dialog is used
/// for this connection only. The target is never serialized or logged.
#[cfg_attr(not(windows), allow(dead_code))]
pub struct RdpTarget {
    pub host: String,
    pub port: u16,
    pub username: Option<String>,
    pub password: Option<String>,
    pub domain: Option<String>,
    pub width: u32,
    pub height: u32,
}

impl RdpTarget {
    pub fn new(host: String) -> Self {
        Self {
            host,
            port: 3389,
            username: None,
            password: None,
            domain: None,
            width: 1920,
            height: 1080,
        }
    }

    #[allow(dead_code)]
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
    stride: u32,
    /// FreeRDP owns its primary buffer. This is a private copy in BGRX32.
    pixels: Vec<u8>,
}

#[cfg_attr(not(windows), allow(dead_code))]
struct PendingUpdate {
    state: String,
    status: String,
    frame: Option<RdpFrame>,
}

#[cfg_attr(not(windows), allow(dead_code))]
struct SharedState {
    update: Mutex<PendingUpdate>,
    stop: std::sync::atomic::AtomicBool,
}

impl SharedState {
    fn new() -> Self {
        Self {
            update: Mutex::new(PendingUpdate {
                state: "connecting".to_owned(),
                status: "正在连接 RDP…".to_owned(),
                frame: None,
            }),
            stop: std::sync::atomic::AtomicBool::new(false),
        }
    }

    fn set_state(&self, state: impl Into<String>, status: impl Into<String>) {
        if let Ok(mut update) = self.update.lock() {
            update.state = state.into();
            update.status = status.into();
        }
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn set_frame(&self, frame: RdpFrame) {
        if let Ok(mut update) = self.update.lock() {
            update.frame = Some(frame);
        }
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn poll(&self) -> PendingUpdate {
        let Ok(mut update) = self.update.lock() else {
            return PendingUpdate {
                state: "failed".to_owned(),
                status: "RDP 状态不可用。".to_owned(),
                frame: None,
            };
        };

        PendingUpdate {
            state: update.state.clone(),
            status: update.status.clone(),
            frame: update.frame.take(),
        }
    }
}

pub struct DesktopRdpController {
    _window: DesktopRdpWindow,
    shared: Arc<SharedState>,
    worker: Option<JoinHandle<()>>,
    frame_timer: Timer,
}

impl DesktopRdpController {
    pub fn new(host: String) -> Result<Self, String> {
        Self::new_with_target(RdpTarget::new(host))
    }

    pub fn new_with_target(mut target: RdpTarget) -> Result<Self, String> {
        target.host = target.host.trim().to_owned();
        if target.host.is_empty() {
            return Err("RDP host must not be empty".to_owned());
        }
        if target.host.contains('\0') {
            return Err("RDP host contains an invalid NUL character".to_owned());
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

        #[cfg(windows)]
        let worker = {
            let shared = Arc::clone(&shared);
            Some(std::thread::spawn(move || freerdp::run(target, shared)))
        };

        #[cfg(not(windows))]
        let worker = {
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
            frame_timer.start(TimerMode::Repeated, Duration::from_millis(33), move || {
                apply_pending_update(&timer_window, &timer_shared);
            });
        }

        Ok(Self {
            _window: window,
            shared,
            worker,
            frame_timer,
        })
    }
}

impl Drop for DesktopRdpController {
    fn drop(&mut self) {
        self.shared
            .stop
            .store(true, std::sync::atomic::Ordering::SeqCst);
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

    let update = shared.poll();
    window.set_state(update.state.into());
    window.set_status(update.status.into());

    if let Some(frame) = update.frame {
        match frame_to_image(&frame) {
            Some(image) => window.set_frame(image),
            None => shared.set_state("failed", "收到的 RDP framebuffer 尺寸无效。"),
        }
    }
}

#[cfg(windows)]
fn frame_to_image(frame: &RdpFrame) -> Option<Image> {
    let width = usize::try_from(frame.width).ok()?;
    let height = usize::try_from(frame.height).ok()?;
    let stride = usize::try_from(frame.stride).ok()?;
    let row_bytes = width.checked_mul(4)?;
    let frame_bytes = stride.checked_mul(height)?;
    if width == 0 || height == 0 || stride < row_bytes || frame.pixels.len() < frame_bytes {
        return None;
    }

    let mut rgba = vec![0u8; row_bytes.checked_mul(height)?];
    for row in 0..height {
        let source = &frame.pixels[row * stride..row * stride + row_bytes];
        let destination = &mut rgba[row * row_bytes..(row + 1) * row_bytes];
        for (source, destination) in source.chunks_exact(4).zip(destination.chunks_exact_mut(4)) {
            // FreeRDP was initialized with PIXEL_FORMAT_BGRX32.
            destination.copy_from_slice(&[source[2], source[1], source[0], 0xff]);
        }
    }

    let buffer =
        SharedPixelBuffer::<Rgba8Pixel>::clone_from_slice(&rgba, frame.width, frame.height);
    Some(Image::from_rgba8(buffer))
}

#[cfg(windows)]
fn prompt_for_credentials(target: &RdpTarget) -> Result<RdpCredentials, String> {
    use std::mem::size_of;
    use std::ptr::{null, null_mut};
    use windows_sys::Win32::Foundation::BOOL;
    use windows_sys::Win32::Security::Credentials::{
        CredUIParseUserNameW, CredUIPromptForCredentialsW, CREDUI_FLAGS_DO_NOT_PERSIST,
        CREDUI_FLAGS_EXCLUDE_CERTIFICATES, CREDUI_FLAGS_USERNAME_TARGET_CREDENTIALS, CREDUI_INFOW,
    };

    if let (Some(username), Some(password)) = (&target.username, &target.password) {
        if !username.is_empty() && !password.is_empty() {
            return Ok(RdpCredentials {
                username: username.clone(),
                password: password.clone(),
                domain: target.domain.clone().unwrap_or_default(),
            });
        }
    }

    let target_name = to_wide(&target.host);
    let caption = to_wide("Chuzi RDP");
    let message = to_wide("请输入此 RDP 连接的凭据。凭据只用于本次连接，不会保存。");
    let mut username = [0u16; 512];
    let mut password = [0u16; 512];
    if let Some(initial_username) = &target.username {
        copy_wide(initial_username, &mut username);
    }
    let mut save: BOOL = 0;
    let info = CREDUI_INFOW {
        cbSize: size_of::<CREDUI_INFOW>() as u32,
        hwndParent: null_mut(),
        pszMessageText: message.as_ptr(),
        pszCaptionText: caption.as_ptr(),
        hbmBanner: null_mut(),
    };
    let flags = CREDUI_FLAGS_DO_NOT_PERSIST
        | CREDUI_FLAGS_EXCLUDE_CERTIFICATES
        | CREDUI_FLAGS_USERNAME_TARGET_CREDENTIALS;
    let result = unsafe {
        CredUIPromptForCredentialsW(
            &info,
            target_name.as_ptr(),
            null(),
            0,
            username.as_mut_ptr(),
            username.len() as u32,
            password.as_mut_ptr(),
            password.len() as u32,
            &mut save,
            flags,
        )
    };
    if result != 0 {
        username.fill(0);
        password.fill(0);
        return Err("RDP credential dialog was cancelled or failed".to_owned());
    }

    let entered_username = wide_string(&username);
    let entered_password = wide_string(&password);
    username.fill(0);
    password.fill(0);

    if entered_username.is_empty() || entered_password.is_empty() {
        return Err("RDP username and password are required".to_owned());
    }

    let mut parsed_user = [0u16; 512];
    let mut parsed_domain = [0u16; 256];
    let entered_username_wide = to_wide(&entered_username);
    let parse_result = unsafe {
        CredUIParseUserNameW(
            entered_username_wide.as_ptr(),
            parsed_user.as_mut_ptr(),
            parsed_user.len() as u32,
            parsed_domain.as_mut_ptr(),
            parsed_domain.len() as u32,
        )
    };
    let (username, domain) = if parse_result == 0 {
        (wide_string(&parsed_user), wide_string(&parsed_domain))
    } else {
        (entered_username, target.domain.clone().unwrap_or_default())
    };

    Ok(RdpCredentials {
        username,
        password: entered_password,
        domain,
    })
}

#[cfg(windows)]
struct RdpCredentials {
    username: String,
    password: String,
    domain: String,
}

#[cfg(windows)]
impl Drop for RdpCredentials {
    fn drop(&mut self) {
        wipe_string(&mut self.password);
    }
}

#[cfg(windows)]
fn to_wide(value: &str) -> Vec<u16> {
    value.encode_utf16().chain(std::iter::once(0)).collect()
}

#[cfg(windows)]
fn copy_wide(value: &str, destination: &mut [u16]) {
    let encoded = value
        .encode_utf16()
        .take(destination.len().saturating_sub(1));
    for (slot, value) in destination.iter_mut().zip(encoded) {
        *slot = value;
    }
}

#[cfg(windows)]
fn wide_string(value: &[u16]) -> String {
    let end = value
        .iter()
        .position(|character| *character == 0)
        .unwrap_or(value.len());
    String::from_utf16_lossy(&value[..end])
}

#[cfg(windows)]
mod freerdp {
    use super::{prompt_for_credentials, RdpFrame, RdpTarget, SharedState};
    use std::ffi::{CStr, CString};
    use std::mem::{size_of, zeroed};
    use std::sync::Arc;
    use windows_sys::Win32::Foundation::{BOOL, HANDLE, WAIT_FAILED};
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
    }

    pub fn run(target: RdpTarget, shared: Arc<SharedState>) {
        shared.set_state("connecting", "正在准备 FreeRDP 连接…");
        let result = unsafe { run_session(&target, &shared) };
        if shared.stop.load(std::sync::atomic::Ordering::SeqCst) {
            shared.set_state("closed", "RDP 连接已关闭。\n");
        } else if let Err(error) = result {
            shared.set_state("failed", error);
        } else {
            shared.set_state("closed", "RDP 连接已断开。\n");
        }
    }

    unsafe fn run_session(target: &RdpTarget, shared: &Arc<SharedState>) -> Result<(), String> {
        let credentials = prompt_for_credentials(target)?;
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

        if let Err(error) = configure(instance, target, &credentials) {
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

        let event_result = event_loop(instance, shared);
        ffi::freerdp_disconnect(instance);
        ffi::freerdp_context_free(instance);
        ffi::freerdp_free(instance);
        event_result
    }

    unsafe fn configure(
        instance: *mut ffi::freerdp,
        target: &RdpTarget,
        credentials: &super::RdpCredentials,
    ) -> Result<(), String> {
        let context = (*instance).context;
        if context.is_null() || (*context).settings.is_null() {
            return Err("FreeRDP settings are unavailable".to_owned());
        }
        let settings = (*context).settings;
        let host =
            CString::new(target.host.as_str()).map_err(|_| "RDP host is invalid".to_owned())?;
        let username = CString::new(credentials.username.as_str())
            .map_err(|_| "RDP username is invalid".to_owned())?;
        let password = CString::new(credentials.password.as_str())
            .map_err(|_| "RDP password is invalid".to_owned())?;
        let domain = CString::new(credentials.domain.as_str())
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

    unsafe fn event_loop(instance: *mut ffi::freerdp, shared: &SharedState) -> Result<(), String> {
        let context = (*instance).context;
        if context.is_null() {
            return Err("FreeRDP context disappeared".to_owned());
        }

        loop {
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

            let wait_result = WaitForMultipleObjects(count, handles.as_ptr(), 0, 250);
            if wait_result == WAIT_FAILED {
                return Err("Windows event wait for the RDP connection failed".to_owned());
            }
            if ffi::freerdp_check_event_handles(context) == 0 {
                return Err(last_error(instance, "FreeRDP event processing failed"));
            }
        }
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

    unsafe extern "C" fn post_connect(instance: *mut ffi::freerdp) -> BOOL {
        if instance.is_null() || (*instance).context.is_null() {
            return 0;
        }
        let context = (*instance).context;
        if ffi::gdi_init(instance, ffi::CHUZI_PIXEL_FORMAT_BGRX32 as u32) == 0 {
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
            if let Some(primary) = gdi.primary.as_ref() {
                if let Some(hdc) = primary.hdc.as_ref() {
                    if let Some(hwnd) = hdc.hwnd.as_ref() {
                        if let Some(invalid) = hwnd.invalid.as_mut() {
                            // FreeRDP's GDI renderer uses this flag to
                            // accumulate a complete update before EndPaint
                            // copies the current primary buffer.
                            invalid.null = 1;
                        }
                    }
                }
            }
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
        if gdi.width <= 0 || gdi.height <= 0 || gdi.stride == 0 || gdi.primary_buffer.is_null() {
            return 0;
        }

        let Ok(width) = usize::try_from(gdi.width) else {
            return 0;
        };
        let Ok(height) = usize::try_from(gdi.height) else {
            return 0;
        };
        let Ok(stride) = usize::try_from(gdi.stride) else {
            return 0;
        };
        let Some(row_bytes) = width.checked_mul(4) else {
            return 0;
        };
        if stride < row_bytes {
            return 0;
        }
        let Some(bytes) = stride.checked_mul(height) else {
            return 0;
        };
        let source = std::slice::from_raw_parts(gdi.primary_buffer, bytes);
        let pixels = source.to_vec();

        if let Some(shared) = shared_from_context(context) {
            shared.set_frame(RdpFrame {
                width: width as u32,
                height: height as u32,
                stride: stride as u32,
                pixels,
            });
        }
        1
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
