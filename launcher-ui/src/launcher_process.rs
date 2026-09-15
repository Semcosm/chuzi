use std::{
    collections::{HashMap, HashSet},
    io::{self, BufRead, Read, Write},
    process::{Child, Command, Stdio},
    sync::{Arc, Mutex},
    thread,
    time::Duration,
};

use serde_json::Value;
use tauri::{AppHandle, Emitter};

use crate::{
    config::LauncherConfig,
    protocol::{
        command_spec, parse_progress_line, ProgressEvent, ProgressMessage, ResultEvent, UiRequest,
        UI_PROTOCOL_VERSION,
    },
};

#[derive(Default)]
struct OperationState {
    children: HashMap<String, Arc<Mutex<Child>>>,
    cancelled: HashSet<String>,
}

#[derive(Clone, Default)]
pub struct OperationRegistry {
    state: Arc<Mutex<OperationState>>,
}

impl OperationRegistry {
    fn reserve(&self, id: &str, child: Arc<Mutex<Child>>) -> bool {
        let Ok(mut state) = self.state.lock() else {
            return false;
        };
        if state.children.contains_key(id) {
            return false;
        }
        state.children.insert(id.to_owned(), child);
        true
    }

    pub fn cancel(&self, id: &str) -> Result<bool, String> {
        let Ok(mut state) = self.state.lock() else {
            return Err("operation state unavailable".to_owned());
        };
        let Some(child) = state.children.get(id).cloned() else {
            return Ok(false);
        };
        state.cancelled.insert(id.to_owned());
        drop(state);

        let Ok(mut child) = child.lock() else {
            return Err("operation process state unavailable".to_owned());
        };
        match child.kill() {
            Ok(()) => Ok(true),
            Err(error) if error.kind() == io::ErrorKind::InvalidInput => Ok(true),
            Err(_) => Err("operation cancellation failed".to_owned()),
        }
    }

    fn was_cancelled(&self, id: &str) -> bool {
        self.state
            .lock()
            .map(|mut state| state.cancelled.remove(id))
            .unwrap_or(false)
    }

    fn finish(&self, id: &str) {
        if let Ok(mut state) = self.state.lock() {
            state.children.remove(id);
            state.cancelled.remove(id);
        }
    }

    pub fn cancel_all(&self) {
        let Ok(mut state) = self.state.lock() else {
            return;
        };
        let children: Vec<_> = state
            .children
            .iter()
            .map(|(id, child)| (id.clone(), Arc::clone(child)))
            .collect();
        for (id, _) in &children {
            state.cancelled.insert(id.clone());
        }
        drop(state);
        for (_, child) in children {
            if let Ok(mut child) = child.lock() {
                let _ = child.kill();
            }
        }
    }
}

pub fn execute(
    app: AppHandle,
    config: LauncherConfig,
    request: UiRequest,
    operations: OperationRegistry,
) -> ResultEvent {
    let id = request.id.clone();
    if let Err(error) = request.validate() {
        return ResultEvent::failure(id, error);
    }
    if request.action == "cancel" {
        let target = request.target_id.as_deref().unwrap_or_default();
        return match operations.cancel(target) {
            Ok(cancelled) => ResultEvent::success(
                id,
                Some(serde_json::json!({
                    "cancelled": cancelled,
                    "target_id": target,
                })),
            ),
            Err(error) => ResultEvent::failure(id, error),
        };
    }

    let spec = match command_spec(&config, &request) {
        Ok(spec) => spec,
        Err(error) => return ResultEvent::failure(id, error),
    };
    match run_command(app, config, id.clone(), spec, operations.clone()) {
        Ok(data) => ResultEvent::success(id, Some(data)),
        Err(error) => ResultEvent::failure(id, error),
    }
}

fn run_command(
    app: AppHandle,
    config: LauncherConfig,
    id: String,
    spec: crate::protocol::CommandSpec,
    operations: OperationRegistry,
) -> Result<Value, String> {
    let has_stdin = spec.stdin.is_some();
    let mut command = Command::new(&config.launcher);
    command
        .args(spec.args)
        .current_dir(&config.root)
        .stdin(if has_stdin {
            Stdio::piped()
        } else {
            Stdio::null()
        })
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    let mut child = command
        .spawn()
        .map_err(|_| "launcher_unavailable".to_owned())?;
    if let Some(input) = spec.stdin {
        let Some(mut stdin) = child.stdin.take() else {
            let _ = child.kill();
            return Err("launcher_input_failed".to_owned());
        };
        if stdin.write_all(&input).is_err() {
            let _ = child.kill();
            return Err("launcher_input_failed".to_owned());
        }
    }

    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let child = Arc::new(Mutex::new(child));
    if !operations.reserve(&id, Arc::clone(&child)) {
        if let Ok(mut child) = child.lock() {
            let _ = child.kill();
        }
        return Err("duplicate operation id".to_owned());
    }

    let stderr_app = app.clone();
    let stderr_id = id.clone();
    let stderr_thread = thread::Builder::new()
        .name("chuzi-launcher-ui-stderr".to_owned())
        .spawn(move || {
            let Some(stderr) = stderr else {
                return;
            };
            for line in io::BufReader::new(stderr).lines().map_while(Result::ok) {
                if let Some(event) = parse_progress_line(&line) {
                    emit_progress(&stderr_app, &stderr_id, event);
                }
            }
        })
        .map_err(|_| {
            operations.finish(&id);
            if let Ok(mut child) = child.lock() {
                let _ = child.kill();
            }
            "launcher_output_failed".to_owned()
        })?;

    let stdout_thread = thread::Builder::new()
        .name("chuzi-launcher-ui-stdout".to_owned())
        .spawn(move || {
            let mut output = Vec::new();
            if let Some(stdout) = stdout {
                let _ = io::BufReader::new(stdout).read_to_end(&mut output);
            }
            output
        })
        .map_err(|_| {
            operations.finish(&id);
            if let Ok(mut child) = child.lock() {
                let _ = child.kill();
            }
            "launcher_output_failed".to_owned()
        })?;

    let status = loop {
        let result = match child.lock() {
            Ok(mut child) => child.try_wait(),
            Err(_) => Err(io::Error::other("operation process state unavailable")),
        };
        match result {
            Ok(Some(status)) => break Ok(status),
            Ok(None) => thread::sleep(Duration::from_millis(25)),
            Err(error) => break Err(error),
        }
    };
    let output = stdout_thread.join().unwrap_or_default();
    let _ = stderr_thread.join();
    let cancelled = operations.was_cancelled(&id);
    operations.finish(&id);
    if cancelled {
        return Err("operation_cancelled".to_owned());
    }

    match status {
        Ok(status) if status.success() => {
            let text = String::from_utf8_lossy(&output);
            if text.trim().is_empty() {
                Ok(Value::Null)
            } else {
                serde_json::from_str(text.trim())
                    .map_err(|_| "invalid_launcher_response".to_owned())
            }
        }
        Ok(_) => Err("launcher_operation_failed".to_owned()),
        Err(_) => Err("launcher_process_failed".to_owned()),
    }
}

fn emit_progress(app: &AppHandle, id: &str, event: ProgressEvent) {
    let message = ProgressMessage {
        protocol: UI_PROTOCOL_VERSION,
        kind: "progress",
        id: id.to_owned(),
        operation: event.operation,
        stage: event.stage,
        item: event.item,
        completed: event.completed,
        total: event.total,
    };
    let _ = app.emit("launcher-progress", message);
}
