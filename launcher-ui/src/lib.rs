use std::{
    ffi::OsString,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize};
use serde_json::Value;

pub const UI_PROTOCOL_VERSION: &str = "chuzi.launcher-ui/v1";
pub const UI_HTML: &str = include_str!("ui.html");

#[cfg(all(
    feature = "desktop-webview",
    any(target_os = "windows", target_os = "macos", target_os = "linux")
))]
pub mod desktop;

#[derive(Debug, Clone)]
pub struct LauncherConfig {
    pub launcher: PathBuf,
    pub root: PathBuf,
    pub manifest: PathBuf,
    pub release_index: Option<String>,
    pub update_manifest: Option<PathBuf>,
    pub source_root: Option<PathBuf>,
    pub download_dir: Option<PathBuf>,
    pub trusted_signers: Vec<String>,
    pub allow_http_loopback: bool,
}

impl LauncherConfig {
    pub fn validate(&self) -> Result<(), String> {
        validate_absolute("launcher", &self.launcher)?;
        validate_absolute("root", &self.root)?;
        validate_absolute("manifest", &self.manifest)?;
        if let Some(path) = &self.update_manifest {
            validate_absolute("update_manifest", path)?;
        }
        if let Some(path) = &self.source_root {
            validate_absolute("source_root", path)?;
        }
        if let Some(path) = &self.download_dir {
            validate_absolute("download_dir", path)?;
        }
        if self.allow_http_loopback && self.release_index.is_none() {
            return Err("allow_http_loopback requires release_index".to_owned());
        }
        Ok(())
    }

    pub fn common_args(&self) -> Vec<OsString> {
        let mut args = vec![
            OsString::from("-manifest"),
            self.manifest.as_os_str().to_owned(),
            OsString::from("-root"),
            self.root.as_os_str().to_owned(),
        ];
        if let Some(source_root) = &self.source_root {
            args.extend([
                OsString::from("-source-root"),
                source_root.as_os_str().to_owned(),
            ]);
        }
        if let Some(download_dir) = &self.download_dir {
            args.extend([
                OsString::from("-download-dir"),
                download_dir.as_os_str().to_owned(),
            ]);
        }
        if let Some(release_index) = &self.release_index {
            args.extend([
                OsString::from("-release-index"),
                OsString::from(release_index),
            ]);
        }
        if let Some(update_manifest) = &self.update_manifest {
            args.extend([
                OsString::from("-update-manifest"),
                update_manifest.as_os_str().to_owned(),
            ]);
        }
        if !self.trusted_signers.is_empty() {
            args.extend([
                OsString::from("-trusted-signers"),
                OsString::from(self.trusted_signers.join(",")),
            ]);
        }
        if self.allow_http_loopback {
            args.push(OsString::from("-allow-http-loopback"));
        }
        args
    }
}

fn validate_absolute(name: &str, path: &Path) -> Result<(), String> {
    if !path.is_absolute() || path.to_string_lossy().trim().is_empty() {
        return Err(format!("{name} must be an absolute path"));
    }
    Ok(())
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct UiRequest {
    pub protocol: String,
    pub id: String,
    pub action: String,
    #[serde(default)]
    pub item: Option<String>,
    #[serde(default)]
    pub target_id: Option<String>,
    #[serde(default)]
    pub settings: Option<Value>,
}

impl UiRequest {
    pub fn parse(body: &str) -> Result<Self, String> {
        let request: Self =
            serde_json::from_str(body).map_err(|_| "invalid UI request".to_owned())?;
        request.validate()?;
        Ok(request)
    }

    pub fn validate(&self) -> Result<(), String> {
        if self.protocol != UI_PROTOCOL_VERSION {
            return Err("unsupported UI protocol".to_owned());
        }
        if !valid_id(&self.id) {
            return Err("request id is required".to_owned());
        }
        match self.action.as_str() {
            "initialize"
            | "initialize-complete"
            | "component-list"
            | "plugin-list"
            | "settings"
            | "check-update"
            | "repair" => {}
            "component-install" | "component-remove" | "component-enable" | "component-disable" => {
                require_item(self.item.as_deref())?
            }
            "plugin-install" | "plugin-remove" | "plugin-enable" | "plugin-disable"
            | "plugin-trust" | "plugin-untrust" => require_item(self.item.as_deref())?,
            "settings-save" => {
                if !self.settings.as_ref().is_some_and(Value::is_object) {
                    return Err("settings-save requires an object".to_owned());
                }
            }
            "cancel" => {
                if !valid_id(self.target_id.as_deref().unwrap_or_default()) {
                    return Err("cancel requires target_id".to_owned());
                }
            }
            other => return Err(format!("unsupported UI action: {other}")),
        }
        Ok(())
    }
}

fn require_item(item: Option<&str>) -> Result<(), String> {
    if item.is_some_and(|value| !value.trim().is_empty()) {
        Ok(())
    } else {
        Err("action requires item".to_owned())
    }
}

fn valid_id(value: &str) -> bool {
    !value.trim().is_empty()
        && value.len() <= 128
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.'))
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandSpec {
    pub args: Vec<OsString>,
    pub stdin: Option<Vec<u8>>,
}

pub fn command_spec(config: &LauncherConfig, request: &UiRequest) -> Result<CommandSpec, String> {
    config.validate()?;
    request.validate()?;
    if request.action == "cancel" {
        return Err("cancel is handled by the UI process".to_owned());
    }
    let command = match request.action.as_str() {
        "initialize"
        | "initialize-complete"
        | "component-list"
        | "component-install"
        | "component-remove"
        | "component-enable"
        | "component-disable"
        | "settings"
        | "settings-save"
        | "check-update"
        | "plugin-list"
        | "plugin-install"
        | "plugin-remove"
        | "plugin-enable"
        | "plugin-disable"
        | "plugin-trust"
        | "plugin-untrust"
        | "repair" => request.action.as_str(),
        _ => return Err("unsupported UI action".to_owned()),
    };
    let mut args = config.common_args();
    args.extend([OsString::from("-command"), OsString::from(command)]);
    if let Some(item) = &request.item {
        args.extend([OsString::from("-item"), OsString::from(item)]);
    }
    let stdin = if command == "settings-save" {
        let settings = request
            .settings
            .as_ref()
            .ok_or_else(|| "settings-save requires settings".to_owned())?;
        let mut data =
            serde_json::to_vec(settings).map_err(|_| "encode settings failed".to_owned())?;
        data.push(b'\n');
        args.extend([OsString::from("-settings-input"), OsString::from("-")]);
        Some(data)
    } else {
        None
    };
    if matches!(
        command,
        "component-install"
            | "component-remove"
            | "component-enable"
            | "component-disable"
            | "plugin-install"
            | "plugin-remove"
            | "plugin-enable"
            | "plugin-disable"
            | "plugin-trust"
            | "plugin-untrust"
            | "repair"
    ) {
        args.push(OsString::from("-progress"));
    }
    Ok(CommandSpec { args, stdin })
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct ProgressEvent {
    pub operation: String,
    pub stage: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub item: Option<String>,
    pub completed: u64,
    pub total: u64,
}

pub fn parse_progress_line(line: &str) -> Option<ProgressEvent> {
    let line = line.trim();
    let rest = line.strip_prefix("progress ")?;
    let operation = field(rest, "operation")?;
    let stage = field(rest, "stage")?;
    let completed = field(rest, "completed")?.parse().ok()?;
    let total = field(rest, "total")?.parse().ok()?;
    let item = field(rest, "item").filter(|value| !value.is_empty());
    Some(ProgressEvent {
        operation,
        stage,
        item,
        completed,
        total,
    })
}

fn field(input: &str, name: &str) -> Option<String> {
    let marker = format!("{name}=");
    let start = input.find(&marker)? + marker.len();
    let value = &input[start..];
    if let Some(quoted) = value.strip_prefix('\"') {
        let mut escaped = false;
        for (offset, byte) in quoted.bytes().enumerate() {
            if escaped {
                escaped = false;
                continue;
            }
            if byte == b'\\' {
                escaped = true;
                continue;
            }
            if byte == b'\"' {
                let encoded = format!("\"{}\"", &quoted[..offset]);
                return serde_json::from_str(&encoded)
                    .ok()
                    .or_else(|| Some(quoted[..offset].to_owned()));
            }
        }
        None
    } else {
        Some(value.split_whitespace().next()?.to_owned())
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct ResultEvent {
    pub protocol: &'static str,
    #[serde(rename = "type")]
    pub kind: &'static str,
    pub id: String,
    pub ok: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

impl ResultEvent {
    pub fn success(id: impl Into<String>, data: Option<Value>) -> Self {
        Self {
            protocol: UI_PROTOCOL_VERSION,
            kind: "result",
            id: id.into(),
            ok: true,
            data,
            error: None,
        }
    }

    pub fn failure(id: impl Into<String>, error: impl Into<String>) -> Self {
        Self {
            protocol: UI_PROTOCOL_VERSION,
            kind: "result",
            id: id.into(),
            ok: false,
            data: None,
            error: Some(error.into()),
        }
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct ProgressMessage {
    pub protocol: &'static str,
    #[serde(rename = "type")]
    pub kind: &'static str,
    pub id: String,
    pub operation: String,
    pub stage: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub item: Option<String>,
    pub completed: u64,
    pub total: u64,
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_root() -> PathBuf {
        #[cfg(windows)]
        {
            PathBuf::from(r"C:\opt\chuzi")
        }
        #[cfg(not(windows))]
        {
            PathBuf::from("/opt/chuzi")
        }
    }

    fn config() -> LauncherConfig {
        let root = test_root();
        LauncherConfig {
            launcher: root.join("chuzi-launcher"),
            root: root.clone(),
            manifest: root.join("release-manifest.json"),
            release_index: None,
            update_manifest: None,
            source_root: None,
            download_dir: None,
            trusted_signers: Vec::new(),
            allow_http_loopback: false,
        }
    }

    fn request(action: &str) -> UiRequest {
        UiRequest {
            protocol: UI_PROTOCOL_VERSION.to_owned(),
            id: "req-1".to_owned(),
            action: action.to_owned(),
            item: None,
            target_id: None,
            settings: None,
        }
    }

    #[test]
    fn command_spec_is_shell_free_and_includes_root() {
        let config = config();
        let spec = command_spec(&config, &request("component-list")).expect("command spec");
        assert_eq!(spec.args[0], "-manifest");
        assert!(spec.args.iter().any(|arg| arg == config.root.as_os_str()));
        assert!(!spec.args.iter().any(|arg| arg == "-progress"));
    }

    #[test]
    fn command_spec_passes_update_source_and_explicit_signers() {
        let mut config = config();
        config.update_manifest = Some(config.root.join("candidate.json"));
        config.trusted_signers = vec!["release-key".to_owned(), "ops-key".to_owned()];
        let spec = command_spec(&config, &request("check-update")).expect("command spec");
        assert!(spec.args.windows(2).any(|pair| {
            pair[0] == "-update-manifest"
                && pair[1] == config.root.join("candidate.json").as_os_str()
        }));
        assert!(spec
            .args
            .windows(2)
            .any(|pair| { pair[0] == "-trusted-signers" && pair[1] == "release-key,ops-key" }));
    }

    #[test]
    fn settings_save_uses_stdin_instead_of_a_persistent_file() {
        let mut request = request("settings-save");
        request.settings = Some(serde_json::json!({"update_channel":"nightly"}));
        let spec = command_spec(&config(), &request).expect("command spec");
        assert_eq!(spec.args.last().and_then(|value| value.to_str()), Some("-"));
        assert!(spec.stdin.expect("stdin").ends_with(b"\n"));
    }

    #[test]
    fn config_rejects_relative_paths_and_unpaired_loopback_flag() {
        let mut relative = config();
        relative.root = PathBuf::from("relative");
        assert!(relative.validate().is_err());

        let mut loopback = config();
        loopback.allow_http_loopback = true;
        assert!(loopback.validate().is_err());
        loopback.release_index = Some("http://127.0.0.1:8080/index.json".to_owned());
        assert!(loopback.validate().is_ok());
    }

    #[test]
    fn request_parser_denies_unknown_fields_and_non_ascii_ids() {
        let unknown = format!(
            r#"{{"protocol":"{UI_PROTOCOL_VERSION}","id":"req-1","action":"settings","extra":true}}"#
        );
        assert!(UiRequest::parse(&unknown).is_err());

        let mut invalid = request("settings");
        invalid.id = "\u{8bf7}".to_owned();
        assert!(invalid.validate().is_err());
    }

    #[test]
    fn progress_parser_handles_quoted_items() {
        let event = parse_progress_line(
            r#"progress operation=repair stage=verify item="service/main with space" completed=1 total=2"#,
        )
        .expect("progress");
        assert_eq!(event.item.as_deref(), Some("service/main with space"));
        assert_eq!(event.completed, 1);
    }

    #[test]
    fn progress_parser_rejects_non_progress_and_incomplete_lines() {
        assert!(parse_progress_line("launcher output").is_none());
        assert!(
            parse_progress_line("progress operation=repair stage=verify completed=no total=2")
                .is_none()
        );
        assert!(
            parse_progress_line("progress operation=repair stage=verify completed=1").is_none()
        );
    }

    #[test]
    fn requests_reject_unknown_actions_and_invalid_cancel_targets() {
        let mut unknown = request("exec");
        assert!(unknown.validate().is_err());
        unknown.action = "cancel".to_owned();
        assert!(unknown.validate().is_err());
    }

    #[test]
    fn requests_require_items_for_plugin_mutations() {
        let mut plugin_request = request("plugin-enable");
        assert!(plugin_request.validate().is_err());
        plugin_request.item = Some("demo".to_owned());
        assert!(plugin_request.validate().is_ok());
        assert!(request("plugin-list").validate().is_ok());
        assert!(request("check-update").validate().is_ok());
    }

    #[test]
    fn html_contains_the_supported_user_actions() {
        for action in [
            "initialize",
            "component-list",
            "component-install",
            "settings-save",
            "repair",
            "check-update",
            "plugin-list",
            "plugin-trust",
        ] {
            assert!(UI_HTML.contains(action), "missing action {action}");
        }
    }
}
