use crate::DesktopCloneWindow;
use slint::{CloseRequestResponse, ComponentHandle};

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
