use std::env;
use std::fs::{self, File};
use std::path::PathBuf;

use i_slint_backend_testing::{TestingBackend, TestingBackendOptions};
use slint::{ComponentHandle, PhysicalSize, SharedString};

slint::include_modules!();

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let (output, sizes) = parse_args()?;
    fs::create_dir_all(&output)?;

    slint::platform::set_platform(Box::new(TestingBackend::new(TestingBackendOptions {
        mock_time: true,
        threading: false,
        renderer_name: Some(SharedString::from("software")),
    })))?;

    for (width, height) in sizes {
        let window = MainWindow::new()?;
        window.set_core_status("Core is running".into());
        window.set_core_details("Headless layout snapshot".into());
        window.set_data_directory("Data directory: snapshot".into());
        window.window().set_size(PhysicalSize::new(width, height));
        let snapshot = window.window().take_snapshot()?;
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
        let path = output.join(format!("{width}x{height}.png"));
        write_png(&path, &snapshot)?;
        window.window().hide()?;
    }
    Ok(())
}

fn parse_args() -> Result<(PathBuf, Vec<(u32, u32)>), Box<dyn std::error::Error>> {
    let mut output = PathBuf::from("dist/windows-layout");
    let mut sizes = vec![(800, 600), (1120, 760), (1440, 900)];
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
            other => return Err(format!("unknown argument: {other}").into()),
        }
    }
    Ok((output, sizes))
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
