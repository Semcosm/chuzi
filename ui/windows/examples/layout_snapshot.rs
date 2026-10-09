use std::env;
use std::fs::{self, File};
use std::path::PathBuf;

use i_slint_backend_testing::{TestingBackend, TestingBackendOptions};
use slint::language::ColorScheme;
use slint::{ComponentHandle, ModelRc, PhysicalSize, SharedString};

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
            if is_settings_fixture(&session_state) {
                window.set_page("settings".into());
                window.set_settings_phase("ready".into());
                window.set_auto_check_updates(true);
                window.set_auto_repair(false);
                window.set_update_channel("stable".into());
                window.set_launch_on_login(false);
                window.set_close_to_tray(true);
                window.set_start_core_on_launch(true);
                window.set_update_interval(60);
                let pools = job_pool_fixture_for(&session_state);
                window.set_job_pool_rows(ModelRc::from(pools.as_slice()));
                let environments = environment_fixture();
                window.set_environment_rows(ModelRc::from(environments.as_slice()));
                if session_state == "operation-polling" {
                    window.set_operation_phase("polling".into());
                    window.set_operation_id("op-snapshot-0001".into());
                    window.set_operation_state("health_check".into());
                    window.set_operation_detail(
                        "health_check · waiting for trusted environment".into(),
                    );
                } else if session_state == "stale-revision" {
                    window.set_operation_phase("stale revision".into());
                    window.set_operation_id("op-stale-0001".into());
                    window.set_operation_state("failed".into());
                    window.set_operation_detail("stale revision · refresh required".into());
                } else if session_state == "package-unavailable" {
                    window.set_operation_phase("package unavailable".into());
                    window.set_operation_id("envop-package-0001".into());
                    window.set_operation_state("failed".into());
                    window.set_operation_detail("failed · package_unavailable".into());
                }
                if matches!(
                    session_state.as_str(),
                    "narrow-confirmation" | "delete-confirmation"
                ) {
                    let (operation, title, message) = if session_state == "delete-confirmation" {
                        (
                            "delete",
                            "Delete this pool?",
                            "New work will stop. The pool and managed slots will be removed after cleanup.",
                        )
                    } else {
                        (
                            "drain",
                            "Drain this pool?",
                            "New work will stop while leased slots finish safely.",
                        )
                    };
                    window.set_pending_operation(operation.into());
                    window.set_operation_confirmation_title(title.into());
                    window.set_operation_confirmation_message(message.into());
                    window.set_operation_confirmation_visible(true);
                }
            }
            window.window().set_size(PhysicalSize::new(width, height));
            window.show()?;
            slint::platform::update_timers_and_animations();

            let session_model = if is_settings_fixture(&session_state) {
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
    let page = if is_settings_fixture(state) {
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
        "pool-empty",
        "pool-single",
        "pool-mixed",
        "pool-provisioning",
        "pool-draining",
        "pool-quarantined",
        "pool-failed",
        "environment-untrusted",
        "operation-polling",
        "stale-revision",
        "package-unavailable",
        "unavailable-core",
        "narrow-confirmation",
        "delete-confirmation",
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

fn is_settings_fixture(state: &str) -> bool {
    matches!(
        state,
        "settings"
            | "pool-empty"
            | "pool-single"
            | "pool-mixed"
            | "pool-provisioning"
            | "pool-draining"
            | "pool-quarantined"
            | "pool-failed"
            | "environment-untrusted"
            | "operation-polling"
            | "stale-revision"
            | "package-unavailable"
            | "unavailable-core"
            | "narrow-confirmation"
            | "delete-confirmation"
    )
}

fn job_pool_fixture_for(state: &str) -> Vec<JobPoolRowData> {
    match state {
        "pool-empty" => Vec::new(),
        "pool-single" => job_pool_fixture().into_iter().take(1).collect(),
        "pool-provisioning" => job_pool_fixture()
            .into_iter()
            .filter(|row| row.reconcile_state == "provisioning")
            .collect(),
        "pool-draining" => job_pool_fixture()
            .into_iter()
            .filter(|row| row.reconcile_state == "draining")
            .collect(),
        "pool-quarantined" => job_pool_fixture()
            .into_iter()
            .filter(|row| row.pool_id == "pool-quarantined")
            .collect(),
        "pool-failed" | "package-unavailable" => job_pool_fixture()
            .into_iter()
            .filter(|row| row.reconcile_state == "failed")
            .collect(),
        "environment-untrusted" => job_pool_fixture()
            .into_iter()
            .filter(|row| row.pool_id == "pool-failed")
            .collect(),
        _ => job_pool_fixture(),
    }
}

fn job_pool_fixture() -> Vec<JobPoolRowData> {
    vec![
        pool_row(
            "pool-ready",
            "enabled",
            "ready",
            4,
            3,
            1,
            0,
            0,
            0,
            0,
            3,
            12,
            "1",
            "",
        ),
        pool_row(
            "pool-provisioning",
            "enabled",
            "provisioning",
            4,
            1,
            0,
            0,
            0,
            3,
            0,
            1,
            12,
            "2",
            "op-provisioning-0001",
        ),
        pool_row(
            "pool-draining",
            "draining",
            "draining",
            2,
            2,
            1,
            0,
            1,
            0,
            0,
            1,
            8,
            "3",
            "",
        ),
        pool_row(
            "pool-quarantined",
            "enabled",
            "ready",
            2,
            1,
            0,
            1,
            0,
            0,
            0,
            1,
            4,
            "4",
            "",
        ),
        pool_row(
            "pool-failed",
            "enabled",
            "failed",
            3,
            0,
            0,
            0,
            0,
            3,
            0,
            0,
            4,
            "5",
            "",
        ),
    ]
}

fn pool_row(
    id: &str,
    desired_state: &str,
    reconcile: &str,
    desired: i32,
    ready: i32,
    leased: i32,
    quarantined: i32,
    draining: i32,
    provisioning: i32,
    retiring: i32,
    effective: i32,
    max: i32,
    revision: &str,
    operation_id: &str,
) -> JobPoolRowData {
    JobPoolRowData {
        pool_id: id.into(),
        environment_id: "env/windows-slot".into(),
        environment_version: "2026.10".into(),
        desired_slots: desired,
        max_concurrency: max,
        desired_state: desired_state.into(),
        ready,
        leased,
        quarantined,
        draining,
        provisioning,
        retiring,
        effective_capacity: effective,
        environment_readiness: if reconcile == "failed" {
            "untrusted"
        } else {
            "ready"
        }
        .into(),
        reconcile_state: reconcile.into(),
        last_failure_code: if reconcile == "failed" {
            "package_unavailable"
        } else {
            ""
        }
        .into(),
        config_revision: revision.into(),
        operation_id: operation_id.into(),
        selected: false,
    }
}

fn environment_fixture() -> Vec<EnvironmentRowData> {
    vec![
        EnvironmentRowData {
            environment_id: "env/windows-slot".into(),
            version: "2026.10".into(),
            lifecycle: "ready".into(),
            generation: "7".into(),
            selected: false,
        },
        EnvironmentRowData {
            environment_id: "env/untrusted".into(),
            version: "2026.09".into(),
            lifecycle: "untrusted".into(),
            generation: "6".into(),
            selected: false,
        },
    ]
}
