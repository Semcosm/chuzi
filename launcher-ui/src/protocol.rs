use std::ffi::OsString;

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::config::LauncherConfig;

pub const UI_PROTOCOL_VERSION: &str = "chuzi.launcher-ui/v1";

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
        return Err("cancel is handled by the launcher process service".to_owned());
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

#[derive(Debug, Clone, Serialize)]
pub struct ProgressEvent {
    pub operation: String,
    pub stage: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub item: Option<String>,
    pub completed: u64,
    pub total: u64,
}

pub fn parse_progress_line(line: &str) -> Option<ProgressEvent> {
    let rest = line.trim().strip_prefix("progress ")?;
    Some(ProgressEvent {
        operation: field(rest, "operation")?,
        stage: field(rest, "stage")?,
        completed: field(rest, "completed")?.parse().ok()?,
        total: field(rest, "total")?.parse().ok()?,
        item: field(rest, "item").filter(|value| !value.is_empty()),
    })
}

fn field(input: &str, name: &str) -> Option<String> {
    let marker = format!("{name}=");
    let value = &input[input.find(&marker)? + marker.len()..];
    if let Some(quoted) = value.strip_prefix('"') {
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
            if byte == b'"' {
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
    use std::path::PathBuf;

    fn root() -> PathBuf {
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
        let root = root();
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
        let spec = command_spec(&config(), &request("component-list")).expect("command spec");
        assert_eq!(spec.args[0], "-manifest");
        assert!(spec.args.iter().any(|arg| arg == config().root.as_os_str()));
        assert!(!spec.args.iter().any(|arg| arg == "-progress"));
    }

    #[test]
    fn settings_save_uses_stdin_and_paths_are_validated() {
        let mut value = request("settings-save");
        value.settings = Some(serde_json::json!({"update_channel":"nightly"}));
        let spec = command_spec(&config(), &value).expect("command spec");
        assert_eq!(spec.args.last().and_then(|arg| arg.to_str()), Some("-"));
        assert!(spec.stdin.expect("stdin").ends_with(b"\n"));
        let mut invalid = config();
        invalid.root = PathBuf::from("relative");
        assert!(invalid.validate().is_err());
    }

    #[test]
    fn request_parser_rejects_unknown_actions_and_fields() {
        assert!(request("exec").validate().is_err());
        let unknown = format!(
            r#"{{"protocol":"{UI_PROTOCOL_VERSION}","id":"req-1","action":"settings","extra":true}}"#
        );
        assert!(serde_json::from_str::<UiRequest>(&unknown).is_err());
    }

    #[test]
    fn progress_parser_handles_quotes_and_incomplete_lines() {
        let event = parse_progress_line(r#"progress operation=repair stage=verify item="service/main with space" completed=1 total=2"#).expect("progress");
        assert_eq!(event.item.as_deref(), Some("service/main with space"));
        assert!(
            parse_progress_line("progress operation=repair stage=verify completed=no total=2")
                .is_none()
        );
    }
}
