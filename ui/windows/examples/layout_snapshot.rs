use std::env;
use std::fs::{self, File};
use std::path::PathBuf;

use i_slint_backend_testing::{TestingBackend, TestingBackendOptions};
use slint::language::ColorScheme;
use slint::{ComponentHandle, PhysicalSize, SharedString};

#[path = "../src/models.rs"]
#[allow(dead_code)]
mod models;
#[path = "../src/session_ui.rs"]
#[allow(dead_code)]
mod session_ui;
#[path = "../src/view_model.rs"]
#[allow(dead_code)]
mod view_model;

slint::include_modules!();

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let (output, sizes, session_state, theme) = parse_args()?;
    fs::create_dir_all(&output)?;

    slint::platform::set_platform(Box::new(TestingBackend::new(TestingBackendOptions {
        mock_time: true,
        threading: false,
        renderer_name: Some(SharedString::from("software")),
    })))?;

    let themes = if theme == "both" {
        vec!["light", "dark"]
    } else {
        vec![theme.as_str()]
    };
    for (width, height) in sizes {
        for theme in &themes {
            let window = MainWindow::new()?;
            configure_window(&window, theme);
            if session_state == "settings" {
                window.set_page("settings".into());
                window.set_settings_phase("ready".into());
                window.set_auto_check_updates(true);
                window.set_auto_repair(false);
                window.set_update_channel("stable".into());
                window.set_launch_on_login(false);
                window.set_close_to_tray(true);
                window.set_start_core_on_launch(true);
                window.set_update_interval(60);
            }
            window.window().set_size(PhysicalSize::new(width, height));
            window.show()?;
            slint::platform::update_timers_and_animations();

            let session_model = if session_state == "settings" {
                view_model::SessionViewModel::default()
            } else {
                session_fixture(&session_state)
            };
            let has_selection = session_model.inspector().selected.is_some();
            session_ui::render(&window, &session_model);
            window.window().set_size(PhysicalSize::new(width, height));
            slint::platform::update_timers_and_animations();
            // The testing backend may create a Window at its final size
            // without delivering the intermediate resize event.
            let force_compact = matches!(
                session_state.as_str(),
                "compact-inspector" | "cancel-confirmation"
            );
            window.set_compact_shell(width < 940);
            window.set_inspector_collapsed(force_compact || width < 1200);
            window.set_inspector_open((force_compact || width < 1200) && has_selection);
            window.set_snapshot_focus(session_state == "keyboard-focus");
            window.set_busy(session_state == "disabled-action");
            window.set_session_inspector_menu_open(session_state == "more-menu");
            if session_state == "cancel-confirmation" {
                window.set_cancel_confirmation_visible(true);
                window.set_cancel_confirmation_request_id("req-0001".into());
                window.set_cancel_confirmation_account_label("id_000000000001".into());
            }
            slint::platform::update_timers_and_animations();

            let snapshot = window.window().take_snapshot()?;
            write_snapshot(&output, &session_state, theme, width, height, &snapshot)?;
            window.window().hide()?;
        }
    }
    Ok(())
}

fn configure_window(window: &MainWindow, theme: &str) {
    window.set_theme(theme.into());
    window.set_core_installed(true);
    window.set_core_ready(true);
    window.set_core_status_known(true);
    window.set_core_status("Core is running".into());
    window.set_core_details("Headless layout snapshot".into());
    window
        .global::<Palette>()
        .set_color_scheme(color_scheme(theme));
}

fn color_scheme(theme: &str) -> ColorScheme {
    match theme {
        "light" => ColorScheme::Light,
        "dark" => ColorScheme::Dark,
        _ => ColorScheme::Unknown,
    }
}

fn session_fixture(state: &str) -> view_model::SessionViewModel {
    use view_model::{ProjectionError, SessionListProjection, SessionViewModel};

    if state == "loading" {
        return SessionViewModel::default();
    }
    if state == "empty" {
        return SessionViewModel::new(SessionListProjection::empty());
    }

    let requests = session_requests();
    let mut model = SessionViewModel::default();
    model.set_first_page(view_model::sessions_from_requests(&requests), true);
    model.select_key(if state == "cancel-confirmation" {
        "req-0001"
    } else {
        "req-0000"
    });
    match state {
        "mixed"
        | "mixed-selected"
        | "compact-inspector"
        | "more-menu"
        | "keyboard-focus"
        | "disabled-action"
        | "cancel-confirmation" => {}
        "error" => model.set_error(ProjectionError::Unknown),
        "unavailable" => model.set_error(ProjectionError::CoreUnavailable),
        other => panic!("unsupported session fixture: {other}"),
    }
    model
}

fn session_requests() -> Vec<models::CoreRequest> {
    (0..100)
        .map(|index| {
            let (state, failure) = match index {
                0 => ("LOGGING_IN", ""),
                1 => ("QUEUED", ""),
                2 => ("LOGIN_SUCCEEDED", ""),
                3 => ("LOGIN_FAILED", "credential"),
                4 => ("BLOCKED", "permission"),
                _ => ("QUEUED", ""),
            };
            let minute = index % 60;
            models::CoreRequest {
                request_id: format!("req-{index:04}"),
                account: format!("id_{index:012x}"),
                state: state.to_owned(),
                attempt: if index == 3 { 2 } else { 1 },
                last_failure: failure.to_owned(),
                created_at: format!("2026-09-29T09:{minute:02}:00Z"),
                updated_at: format!("2026-09-29T10:{minute:02}:00Z"),
            }
        })
        .collect()
}

fn write_snapshot(
    output: &PathBuf,
    state: &str,
    theme: &str,
    width: u32,
    height: u32,
    snapshot: &slint::SharedPixelBuffer<slint::Rgba8Pixel>,
) -> Result<(), Box<dyn std::error::Error>> {
    if snapshot.width() != width || snapshot.height() != height {
        return Err(format!(
            "snapshot size mismatch: expected {width}x{height}, got {}x{}",
            snapshot.width(),
            snapshot.height()
        )
        .into());
    }
    if snapshot
        .as_bytes()
        .chunks_exact(4)
        .all(|pixel| pixel == [247, 247, 248, 255])
    {
        return Err(format!("snapshot is blank at {width}x{height}").into());
    }
    let page = if state == "settings" {
        "settings"
    } else {
        "sessions"
    };
    let page_output = output.join(page).join(state).join(theme);
    fs::create_dir_all(&page_output)?;
    let path = page_output.join(format!("{width}x{height}.png"));
    write_png(&path, snapshot)
}

fn parse_args() -> Result<(PathBuf, Vec<(u32, u32)>, String, String), Box<dyn std::error::Error>> {
    let mut output = PathBuf::from("dist/windows-layout");
    let mut sizes = vec![(800, 600), (1120, 760), (1440, 900)];
    let mut session_state = "mixed".to_owned();
    let mut theme = "both".to_owned();
    let mut args = env::args().skip(1);
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--output" => output = PathBuf::from(args.next().ok_or("--output needs a path")?),
            "--sizes" => {
                sizes = args
                    .next()
                    .ok_or("--sizes needs a comma-separated list")?
                    .split(',')
                    .map(|size| {
                        let (width, height) = size.split_once('x').ok_or("size must be WxH")?;
                        Ok((width.parse()?, height.parse()?))
                    })
                    .collect::<Result<Vec<_>, Box<dyn std::error::Error>>>()?;
            }
            "--session-state" => {
                session_state = args
                    .next()
                    .ok_or("--session-state needs mixed, empty, loading, error, or unavailable")?;
            }
            "--theme" => {
                theme = args.next().ok_or("--theme needs light, dark, or both")?;
            }
            other => return Err(format!("unknown argument: {other}").into()),
        }
    }
    if ![
        "mixed",
        "mixed-selected",
        "compact-inspector",
        "cancel-confirmation",
        "more-menu",
        "keyboard-focus",
        "disabled-action",
        "empty",
        "loading",
        "error",
        "unavailable",
        "settings",
    ]
    .contains(&session_state.as_str())
    {
        return Err(format!("unknown session state: {session_state}").into());
    }
    if !["light", "dark", "both"].contains(&theme.as_str()) {
        return Err(format!("unknown theme: {theme}").into());
    }
    Ok((output, sizes, session_state, theme))
}

fn write_png(
    path: &PathBuf,
    snapshot: &slint::SharedPixelBuffer<slint::Rgba8Pixel>,
) -> Result<(), Box<dyn std::error::Error>> {
    let file = File::create(path)?;
    let mut encoder = png::Encoder::new(file, snapshot.width(), snapshot.height());
    encoder.set_color(png::ColorType::Rgba);
    encoder.set_depth(png::BitDepth::Eight);
    let mut writer = encoder.write_header()?;
    writer.write_image_data(snapshot.as_bytes())?;
    Ok(())
}
