#[cfg(not(all(
    feature = "desktop-webview",
    any(target_os = "windows", target_os = "macos")
)))]
use std::io::{self, BufRead, Write};

#[cfg(not(all(
    feature = "desktop-webview",
    any(target_os = "windows", target_os = "macos")
)))]
use chuzi_browser_runtime::{Action, Envelope, Runtime};

#[cfg(not(all(
    feature = "desktop-webview",
    any(target_os = "windows", target_os = "macos")
)))]
fn write_responses(
    stdout: &mut io::BufWriter<io::Stdout>,
    responses: Vec<Envelope>,
) -> io::Result<()> {
    for response in responses {
        serde_json::to_writer(&mut *stdout, &response)?;
        stdout.write_all(b"\n")?;
    }
    stdout.flush()
}

#[cfg(all(
    feature = "desktop-webview",
    any(target_os = "windows", target_os = "macos")
))]
fn main() -> std::io::Result<()> {
    chuzi_browser_runtime::desktop::run()
}

#[cfg(not(all(
    feature = "desktop-webview",
    any(target_os = "windows", target_os = "macos")
)))]
fn main() -> io::Result<()> {
    let stdin = io::stdin();
    let mut stdout = io::BufWriter::new(io::stdout());
    let mut runtime = Runtime::default();

    for line in stdin.lock().lines() {
        let line = line?;
        if line.trim().is_empty() {
            continue;
        }

        let request = match serde_json::from_str::<Envelope>(&line) {
            Ok(request) => request,
            Err(_) => {
                let response = Envelope {
                    protocol: chuzi_browser_runtime::PROTOCOL_VERSION.to_owned(),
                    id: "unknown".to_owned(),
                    kind: "error".to_owned(),
                    payload: Default::default(),
                    error: Some("invalid JSON".to_owned()),
                };
                write_responses(&mut stdout, vec![response])?;
                continue;
            }
        };

        let (responses, action) = runtime.handle(request);
        write_responses(&mut stdout, responses)?;
        match action {
            Action::Continue => {}
            Action::Shutdown => break,
            Action::Crash(code) => std::process::exit(code),
        }
    }

    Ok(())
}
