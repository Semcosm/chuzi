use crate::DesktopCloneWindow;
use slint::{CloseRequestResponse, ComponentHandle, Timer, TimerMode};
use std::cell::RefCell;
use std::rc::Rc;
use std::time::Duration;

#[cfg(windows)]
mod native {
    use crate::DesktopCloneWindow;
    use raw_window_handle::{HasWindowHandle, RawWindowHandle};
    use slint::ComponentHandle;
    use std::process::{Child, Command};
    use windows_sys::Win32::Foundation::{HWND, RECT};
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        CreateWindowExW, DestroyWindow, EnumWindows, FindWindowW, GetClassNameW, GetClientRect,
        GetWindow, GetWindowLongPtrW, GetWindowThreadProcessId, IsWindow, IsWindowVisible,
        SendMessageW, SetParent, SetWindowLongPtrW, SetWindowPos, ShowWindow, GWL_EXSTYLE,
        GWL_STYLE, GW_OWNER, HWND_TOP, SWP_NOACTIVATE, SWP_NOZORDER, SWP_SHOWWINDOW, SW_SHOW,
        WM_CLOSE, WS_CAPTION, WS_CHILD, WS_CLIPCHILDREN, WS_CLIPSIBLINGS, WS_EX_APPWINDOW,
        WS_EX_TOOLWINDOW, WS_MAXIMIZEBOX, WS_MINIMIZEBOX, WS_POPUP, WS_SYSMENU, WS_THICKFRAME,
        WS_VISIBLE,
    };

    const RDP_WINDOW_CLASS: &str = "TscShellContainerClass";

    pub struct NativeRdpEmbed {
        parent_hwnd: HWND,
        host_hwnd: HWND,
        remote_hwnd: HWND,
        process: Child,
        process_id: u32,
        content_top: i32,
    }

    impl NativeRdpEmbed {
        pub fn start(parent_hwnd: HWND, host: &str, content_top: i32) -> Result<Self, String> {
            let host_hwnd = create_host_window(parent_hwnd)?;
            let endpoint = format!("/v:{host}");
            let process = Command::new("mstsc.exe")
                .arg(endpoint)
                .arg("/f")
                .arg("/prompt")
                .spawn()
                .map_err(|error| format!("remote_client_start: {error}"))?;
            let process_id = process.id();

            Ok(Self {
                parent_hwnd,
                host_hwnd,
                remote_hwnd: std::ptr::null_mut(),
                process,
                process_id,
                content_top,
            })
        }

        pub fn poll(&mut self) -> EmbedState {
            if !self.remote_hwnd.is_null() && unsafe { IsWindow(self.remote_hwnd) == 0 } {
                self.remote_hwnd = std::ptr::null_mut();
                return EmbedState::Closed;
            }

            if self.remote_hwnd.is_null() {
                let candidate = find_rdp_window(self.process_id);
                if !candidate.is_null() {
                    if attach_rdp_window(candidate, self.host_hwnd).is_ok() {
                        self.remote_hwnd = candidate;
                    }
                }
            }

            self.resize();

            if !self.remote_hwnd.is_null() {
                EmbedState::Connected
            } else if self.process.try_wait().ok().flatten().is_some() {
                EmbedState::Closed
            } else {
                EmbedState::Waiting
            }
        }

        pub fn resize(&self) {
            if self.host_hwnd.is_null() {
                return;
            }

            let mut rect = RECT {
                left: 0,
                top: 0,
                right: 0,
                bottom: 0,
            };
            if unsafe { GetClientRect(self.parent_hwnd, &mut rect) == 0 } {
                return;
            }

            let width = (rect.right - rect.left).max(1);
            let height = (rect.bottom - rect.top - self.content_top).max(1);
            unsafe {
                let _ = SetWindowPos(
                    self.host_hwnd,
                    HWND_TOP,
                    0,
                    self.content_top,
                    width,
                    height,
                    SWP_NOACTIVATE | SWP_NOZORDER | SWP_SHOWWINDOW,
                );
            }
            if !self.remote_hwnd.is_null() {
                unsafe {
                    let _ = SetWindowPos(
                        self.remote_hwnd,
                        HWND_TOP,
                        0,
                        0,
                        width,
                        height,
                        SWP_NOACTIVATE | SWP_NOZORDER | SWP_SHOWWINDOW,
                    );
                }
            }
        }

        pub fn disconnect(&mut self) {
            if !self.remote_hwnd.is_null() && unsafe { IsWindow(self.remote_hwnd) != 0 } {
                unsafe {
                    let _ = SendMessageW(self.remote_hwnd, WM_CLOSE, 0, 0);
                }
                self.remote_hwnd = std::ptr::null_mut();
            }
            if !self.host_hwnd.is_null() && unsafe { IsWindow(self.host_hwnd) != 0 } {
                unsafe {
                    let _ = DestroyWindow(self.host_hwnd);
                }
                self.host_hwnd = std::ptr::null_mut();
            }
            if self.process.try_wait().ok().flatten().is_none() {
                let _ = self.process.kill();
            }
        }
    }

    impl Drop for NativeRdpEmbed {
        fn drop(&mut self) {
            self.disconnect();
        }
    }

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    pub enum EmbedState {
        Waiting,
        Connected,
        Closed,
    }

    pub fn parent_hwnd(window: &DesktopCloneWindow) -> Result<HWND, String> {
        let handle = window
            .window()
            .window_handle()
            .window_handle()
            .map_err(|error| format!("desktop_clone_window_handle: {error}"))?;
        match handle.as_raw() {
            RawWindowHandle::Win32(value) => Ok(value.hwnd.get() as HWND),
            _ => Err("desktop_clone_requires_windows_window".to_owned()),
        }
    }

    fn create_host_window(parent_hwnd: HWND) -> Result<HWND, String> {
        let class_name = wide("STATIC");
        let hwnd = unsafe {
            CreateWindowExW(
                0,
                class_name.as_ptr(),
                std::ptr::null(),
                WS_CHILD | WS_VISIBLE | WS_CLIPCHILDREN | WS_CLIPSIBLINGS,
                0,
                0,
                1,
                1,
                parent_hwnd,
                std::ptr::null_mut(),
                std::ptr::null_mut(),
                std::ptr::null(),
            )
        };
        if hwnd.is_null() {
            Err("remote_host_window_create_failed".to_owned())
        } else {
            Ok(hwnd)
        }
    }

    fn find_rdp_window(process_id: u32) -> HWND {
        let mut state = FindWindowState {
            process_id,
            result: std::ptr::null_mut(),
        };
        unsafe {
            let _ = EnumWindows(Some(enum_windows_callback), &mut state as *mut _ as isize);
        }
        if !state.result.is_null() {
            return state.result;
        }

        let class_name = wide(RDP_WINDOW_CLASS);
        unsafe { FindWindowW(class_name.as_ptr(), std::ptr::null()) }
    }

    struct FindWindowState {
        process_id: u32,
        result: HWND,
    }

    unsafe extern "system" fn enum_windows_callback(hwnd: HWND, state: isize) -> i32 {
        let state = &mut *(state as *mut FindWindowState);
        if IsWindowVisible(hwnd) == 0 || !GetWindow(hwnd, GW_OWNER).is_null() {
            return 1;
        }

        let mut process_id = 0;
        GetWindowThreadProcessId(hwnd, &mut process_id);
        if process_id == state.process_id {
            state.result = hwnd;
            return 0;
        }

        let mut class_buffer = [0u16; 128];
        let length = GetClassNameW(hwnd, class_buffer.as_mut_ptr(), class_buffer.len() as i32);
        if length > 0
            && String::from_utf16_lossy(&class_buffer[..length as usize]) == RDP_WINDOW_CLASS
        {
            state.result = hwnd;
            return 0;
        }
        1
    }

    fn attach_rdp_window(remote_hwnd: HWND, host_hwnd: HWND) -> Result<(), String> {
        let style = unsafe { GetWindowLongPtrW(remote_hwnd, GWL_STYLE) } as u32;
        let style = style
            & !(WS_POPUP
                | WS_CAPTION
                | WS_THICKFRAME
                | WS_SYSMENU
                | WS_MINIMIZEBOX
                | WS_MAXIMIZEBOX);
        let _ = unsafe { SetWindowLongPtrW(remote_hwnd, GWL_STYLE, (style | WS_CHILD) as isize) };

        let ex_style = unsafe { GetWindowLongPtrW(remote_hwnd, GWL_EXSTYLE) } as u32;
        let ex_style = ex_style & !(WS_EX_APPWINDOW | WS_EX_TOOLWINDOW);
        let _ = unsafe { SetWindowLongPtrW(remote_hwnd, GWL_EXSTYLE, ex_style as isize) };

        // A top-level mstsc window has a null previous parent, so a null return
        // value from SetParent is not itself a failure signal here.
        let _ = unsafe { SetParent(remote_hwnd, host_hwnd) };
        unsafe {
            ShowWindow(remote_hwnd, SW_SHOW);
        }
        Ok(())
    }

    fn wide(value: &str) -> Vec<u16> {
        value.encode_utf16().chain(std::iter::once(0)).collect()
    }
}

#[cfg(windows)]
use native::{parent_hwnd, EmbedState, NativeRdpEmbed};

#[cfg(not(windows))]
mod native {
    pub struct NativeRdpEmbed;
}

#[cfg(not(windows))]
use native::NativeRdpEmbed;

pub struct DesktopCloneController {
    _window: DesktopCloneWindow,
    _native: Rc<RefCell<Option<NativeRdpEmbed>>>,
    _timer: Timer,
}

impl DesktopCloneController {
    pub fn new(host: String) -> Result<Self, String> {
        let window = DesktopCloneWindow::new().map_err(|error| error.to_string())?;
        window.set_host(host.clone().into());
        window.set_status("正在准备 RDP 连接…".into());
        window.set_connected(false);
        window.show().map_err(|error| error.to_string())?;

        #[cfg(windows)]
        let native = Rc::new(RefCell::new(None));

        #[cfg(not(windows))]
        let native = Rc::new(RefCell::new(None));

        let _poll_native = native.clone();
        let poll_window = window.as_weak();
        let poll_host = host.clone();
        let timer = Timer::default();
        timer.start(TimerMode::Repeated, Duration::from_millis(100), move || {
            let Some(poll_window) = poll_window.upgrade() else {
                return;
            };
            #[cfg(windows)]
            {
                if _poll_native.borrow().is_none() {
                    if let Ok(parent) = parent_hwnd(&poll_window) {
                        match NativeRdpEmbed::start(
                            parent,
                            &poll_host,
                            (36.0 * poll_window.window().scale_factor()) as i32,
                        ) {
                            Ok(embed) => {
                                *_poll_native.borrow_mut() = Some(embed);
                                poll_window.set_status("正在等待 RDP 登录窗口…".into());
                            }
                            Err(error) => {
                                poll_window.set_status(error.into());
                            }
                        }
                    }
                }

                if let Some(embed) = _poll_native.borrow_mut().as_mut() {
                    match embed.poll() {
                        EmbedState::Waiting => {
                            poll_window.set_status("正在连接 RDP…".into());
                        }
                        EmbedState::Connected => {
                            poll_window.set_connected(true);
                            poll_window.set_status("RDP 已连接".into());
                        }
                        EmbedState::Closed => {
                            poll_window.set_connected(false);
                            poll_window.set_status("RDP 已断开".into());
                        }
                    }
                }
            }

            #[cfg(not(windows))]
            {
                let _ = &poll_host;
                poll_window.set_status("桌面分身只支持 Windows RDP".into());
            }
        });

        let hide_window = window.as_weak();
        window.on_hide_window(move || {
            if let Some(window) = hide_window.upgrade() {
                let _ = window.hide();
            }
        });

        let _disconnect_native = native.clone();
        let disconnect_window = window.as_weak();
        window.on_disconnect_window(move || {
            #[cfg(windows)]
            if let Some(embed) = _disconnect_native.borrow_mut().as_mut() {
                embed.disconnect();
            }
            if let Some(window) = disconnect_window.upgrade() {
                let _ = window.hide();
            }
        });

        let _close_native = native.clone();
        window.window().on_close_requested(move || {
            #[cfg(windows)]
            if let Some(embed) = _close_native.borrow_mut().as_mut() {
                embed.disconnect();
            }
            CloseRequestResponse::HideWindow
        });

        Ok(Self {
            _window: window,
            _native: native,
            _timer: timer,
        })
    }
}
