use crate::models::WireEnvelope;
use crate::{AppState, CORE_PROTOCOL};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::fs::{File, OpenOptions};
use std::io::{BufRead, BufReader, Write};
use std::path::Path;

pub(crate) fn core_call(state: &AppState, method: &str, params: Value) -> Result<Value, String> {
    let mut stream = connect(&state.data_root)?;
    let id = unique_id();
    let hello = hello_request(&id);
    write_json_line(&mut stream, &hello)?;
    let _ = read_response(&mut stream, &format!("hello-{id}"))?;
    let request = serde_json::json!({"protocol": CORE_PROTOCOL, "id": id, "method": method, "params": params});
    write_json_line(&mut stream, &request)?;
    read_response(&mut stream, &id)
}

pub(crate) fn core_handshake(data_root: &Path) -> Result<(), String> {
    let mut stream = connect(data_root)?;
    let id = unique_id();
    write_json_line(&mut stream, &hello_request(&id))?;
    let _ = read_response(&mut stream, &format!("hello-{id}"))?;
    Ok(())
}

fn connect(data_root: &Path) -> Result<File, String> {
    OpenOptions::new()
        .read(true)
        .write(true)
        .open(pipe_name(data_root))
        .map_err(|error| format!("connect Core pipe: {error}"))
}

fn hello_request(id: &str) -> Value {
    serde_json::json!({
        "protocol": CORE_PROTOCOL,
        "id": format!("hello-{id}"),
        "method": "hello",
        "params": {"version": CORE_PROTOCOL}
    })
}

fn write_json_line(stream: &mut File, value: &Value) -> Result<(), String> {
    let mut bytes = serde_json::to_vec(value).map_err(|error| error.to_string())?;
    bytes.push(b'\n');
    stream.write_all(&bytes).map_err(|error| error.to_string())
}

fn read_response(stream: &mut File, expected_id: &str) -> Result<Value, String> {
    let mut reader = BufReader::new(stream.try_clone().map_err(|error| error.to_string())?);
    let mut line = String::new();
    reader
        .read_line(&mut line)
        .map_err(|error| error.to_string())?;
    let response: WireEnvelope = serde_json::from_str(&line).map_err(|error| error.to_string())?;
    if response.protocol != CORE_PROTOCOL || response.id != expected_id {
        return Err("invalid Core response".to_owned());
    }
    if response.kind.as_deref() == Some("error") {
        let error = response
            .error
            .ok_or_else(|| "Core returned an unknown error".to_owned())?;
        return Err(format!("{}: {}", error.code, error.message));
    }
    response
        .result
        .ok_or_else(|| "Core returned an empty result".to_owned())
}

pub(crate) fn unique_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos()
        .to_string()
}

pub(crate) fn pipe_name(data_root: &Path) -> String {
    let cleaned = data_root.to_string_lossy().replace('/', "\\");
    let digest = Sha256::digest(cleaned.as_bytes());
    format!(r"\\.\pipe\chuzi-core-{}", hex_encode(&digest[..8]))
}

fn hex_encode(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}
