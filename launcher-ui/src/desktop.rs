use std::error::Error;

use tauri::{App, Runtime, WebviewWindowBuilder};

const MAIN_WINDOW_LABEL: &str = "main";

fn platform_name() -> &'static str {
    if cfg!(target_os = "windows") {
        "windows"
    } else if cfg!(target_os = "macos") {
        "macos"
    } else if cfg!(target_os = "linux") {
        "linux"
    } else {
        "unknown"
    }
}

/// Inject host capabilities before the document and its bundled application run.
///
/// The payload is intentionally limited to presentation capabilities. It is not
/// part of the launcher business protocol and contains no paths or user data.
pub fn initialization_script() -> String {
    let platform = serde_json::to_string(platform_name()).expect("platform name is valid JSON");
    format!(
        r#"(() => {{
  const capabilities = Object.freeze({{
    platform: {platform},
    titlebar: "custom",
    transparentWindow: true,
    desktopBackdrop: true,
    environmentSource: "desktop-compositor",
    compositedOpacity: true
  }});
  Object.defineProperty(window, "__CHUZI_CAPABILITIES__", {{
    value: capabilities,
    configurable: false,
    enumerable: false,
    writable: false
  }});
}})();"#,
        platform = platform
    )
}

pub fn setup<R: Runtime>(app: &mut App<R>) -> Result<(), Box<dyn Error>> {
    let config = app
        .config()
        .app
        .windows
        .iter()
        .find(|window| window.label == MAIN_WINDOW_LABEL)
        .ok_or("main window configuration is missing")?;

    WebviewWindowBuilder::from_config(app.handle(), config)?
        .initialization_script(initialization_script())
        .build()?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn initialization_script_exposes_only_host_presentation_capabilities() {
        let script = initialization_script();

        assert!(script.contains("__CHUZI_CAPABILITIES__"));
        assert!(script.contains("transparentWindow: true"));
        assert!(script.contains("environmentSource: \"desktop-compositor\""));
        assert!(script.contains(&format!("platform: \"{}\"", platform_name())));
        assert!(!script.contains("release_index"));
        assert!(!script.contains("manifest"));
    }
}
