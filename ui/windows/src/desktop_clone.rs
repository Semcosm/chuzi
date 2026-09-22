use crate::DesktopCloneWindow;
use slint::{CloseRequestResponse, ComponentHandle};
#[cfg(windows)]
use std::time::Duration;

/// UI-only owner for the desktop-clone window.
///
/// The window intentionally has no RDP, Win32, MSTSC, or child-session
/// integration yet. It is a standalone shell that will host the future
/// desktop surface once that transport is designed separately.
pub struct DesktopCloneController {
    _window: DesktopCloneWindow,
}

impl DesktopCloneController {
    pub fn new(host: String) -> Result<Self, String> {
        let window = DesktopCloneWindow::new().map_err(|error| error.to_string())?;
        window.set_host(host.into());
        window.set_state("not-started".into());
        window
            .show()
            .map_err(|error| format!("desktop_clone_window_show: {error}"))?;

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

        Ok(Self { _window: window })
    }
}

#[cfg(windows)]
fn apply_windows_window_chrome(window: &DesktopCloneWindow) {
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

    let hwnd = win32.hwnd.get() as *mut core::ffi::c_void;
    let corner_preference = DWMWCP_ROUND;
    let dark_mode: u32 = 1;
    let border_color: u32 = 0x00141414;

    unsafe {
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_WINDOW_CORNER_PREFERENCE,
            (&corner_preference as *const _).cast(),
            std::mem::size_of_val(&corner_preference) as u32,
        );
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_USE_IMMERSIVE_DARK_MODE,
            (&dark_mode as *const _).cast(),
            std::mem::size_of_val(&dark_mode) as u32,
        );
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_BORDER_COLOR,
            (&border_color as *const _).cast(),
            std::mem::size_of_val(&border_color) as u32,
        );
    }
}
