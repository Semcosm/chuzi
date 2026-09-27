use std::env;
use std::fs::{self, File};
use std::path::PathBuf;

use i_slint_backend_testing::{TestingBackend, TestingBackendOptions};
use slint::{ComponentHandle, ModelRc, PhysicalSize, SharedString};

slint::include_modules!();

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let (output, sizes, page, adapter_state) = parse_args()?;
    fs::create_dir_all(&output)?;

    slint::platform::set_platform(Box::new(TestingBackend::new(TestingBackendOptions {
        mock_time: true,
        threading: false,
        renderer_name: Some(SharedString::from("software")),
    })))?;

    for (width, height) in sizes {
        if page == "rdp" {
            let window = DesktopRdpWindow::new()?;
            window.set_host("127.0.0.2".into());
            window.set_state("not-started".into());
            window.set_status("RDP 连接界面预览：FreeRDP 画面将在连接后显示。".into());
            window.window().set_size(PhysicalSize::new(width, height));
            let snapshot = window.window().take_snapshot()?;
            write_snapshot(&output, &page, width, height, &snapshot)?;
            window.window().hide()?;
            continue;
        }

        let window = MainWindow::new()?;
        window.set_core_installed(true);
        window.set_core_ready(true);
        window.set_core_status("Core is running".into());
        window.set_core_details("Headless layout snapshot".into());
        window.set_data_directory("Data directory: snapshot".into());
        configure_adapter_state(&window, &adapter_state);
        window.window().set_size(PhysicalSize::new(width, height));
        window.show()?;
        window.set_page("overview".into());
        slint::platform::update_timers_and_animations();
        let _ = window.window().take_snapshot()?;
        window.set_page(
            if page == "rdp-login" || page == "adapters" {
                "plugins"
            } else {
                &page
            }
            .into(),
        );
        window.window().set_size(PhysicalSize::new(width, height));
        slint::platform::update_timers_and_animations();
        let snapshot = window.window().take_snapshot()?;
        write_snapshot(&output, &page, width, height, &snapshot)?;
        window.window().hide()?;
    }
    Ok(())
}

fn configure_adapter_state(window: &MainWindow, state: &str) {
    match state {
        "builtin" => {
            window.set_plugin_summary("1 built-in adapter included with this release.".into());
            window.set_plugin_options(ModelRc::from([SharedString::from("chuzi.headless-cdp")]));
            window.set_plugin_input("chuzi.headless-cdp".into());
            window.set_plugin_loaded(true);
            window.set_plugin_display_name("Genshin Cloud Game".into());
            window.set_plugin_distribution("builtin".into());
            window.set_plugin_id("chuzi.headless-cdp".into());
            window.set_plugin_version("0.1.0".into());
            window.set_plugin_api("chuzi.adapter/v1".into());
            window.set_plugin_feature_capability("genshin-cloudgame@1".into());
            window.set_plugin_capabilities("genshin-cloudgame@1".into());
            window.set_plugin_health("included".into());
            window.set_plugin_installed(true);
            window.set_plugin_trusted(true);
            window.set_plugin_enabled(true);
        }
        "package" => {
            window.set_plugin_summary("1 installable adapter package available.".into());
            window.set_plugin_options(ModelRc::from([SharedString::from("demo.adapter")]));
            window.set_plugin_input("demo.adapter".into());
            window.set_plugin_loaded(true);
            window.set_plugin_display_name("Demo Adapter".into());
            window.set_plugin_distribution("package".into());
            window.set_plugin_id("demo.adapter".into());
            window.set_plugin_version("1.0.0".into());
            window.set_plugin_api("chuzi.adapter/v1".into());
            window.set_plugin_capabilities("demo.session@1".into());
            window.set_plugin_health("untrusted".into());
            window.set_plugin_installable(true);
        }
        "empty" => {
            window.set_plugin_summary("No adapters are available in this release.".into());
        }
        other => panic!("unsupported adapter state: {other}"),
    }
}

fn write_snapshot(
    output: &PathBuf,
    page: &str,
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
    let page_output = if page == "overview" {
        output.clone()
    } else {
        output.join(page)
    };
    fs::create_dir_all(&page_output)?;
    let path = page_output.join(format!("{width}x{height}.png"));
    write_png(&path, snapshot)
}

fn parse_args() -> Result<(PathBuf, Vec<(u32, u32)>, String, String), Box<dyn std::error::Error>> {
    let mut output = PathBuf::from("dist/windows-layout");
    let mut sizes = vec![(800, 600), (1120, 760), (1440, 900)];
    let mut sizes_explicit = false;
    let mut page = "overview".to_owned();
    let mut adapter_state = "empty".to_owned();
    let mut args = env::args().skip(1);
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--output" => output = PathBuf::from(args.next().ok_or("--output needs a path")?),
            "--sizes" => {
                sizes_explicit = true;
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
            "--page" => page = args.next().ok_or("--page needs a page name")?,
            "--adapter-state" => {
                adapter_state = args
                    .next()
                    .ok_or("--adapter-state needs empty, builtin, or package")?
            }
            other => return Err(format!("unknown argument: {other}").into()),
        }
    }
    match page.as_str() {
        "overview" | "plugins" | "adapters" | "accounts" | "tasks" | "rdp" | "rdp-login"
        | "settings" => {
            if page == "rdp" && !sizes_explicit {
                sizes = vec![(1280, 752), (800, 600), (500, 281)];
            }
            if !["empty", "builtin", "package"].contains(&adapter_state.as_str()) {
                return Err(format!("unknown adapter state: {adapter_state}").into());
            }
            Ok((output, sizes, page, adapter_state))
        }
        other => Err(format!("unknown page: {other}").into()),
    }
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
