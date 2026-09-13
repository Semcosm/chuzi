#[cfg(all(
    feature = "desktop-webview",
    any(target_os = "windows", target_os = "macos", target_os = "linux")
))]
fn main() -> std::io::Result<()> {
    chuzi_launcher_ui::desktop::run()
}

#[cfg(not(all(
    feature = "desktop-webview",
    any(target_os = "windows", target_os = "macos", target_os = "linux")
)))]
fn main() -> ! {
    eprintln!(
        "chuzi-launcher-ui requires the desktop-webview feature on a supported desktop target"
    );
    std::process::exit(2)
}
