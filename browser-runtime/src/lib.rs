use std::collections::{BTreeMap, HashMap};

use serde::{Deserialize, Serialize};

pub const PROTOCOL_VERSION: &str = "v1";
pub const BROWSER_RUNTIME: &str = "deferred";

#[derive(Debug, Clone, Deserialize, Serialize, PartialEq, Eq)]
pub struct Envelope {
    #[serde(default)]
    pub protocol: String,
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub payload: BTreeMap<String, String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

impl Envelope {
    pub fn request(id: impl Into<String>, kind: impl Into<String>) -> Self {
        Self {
            protocol: PROTOCOL_VERSION.to_owned(),
            id: id.into(),
            kind: kind.into(),
            payload: BTreeMap::new(),
            error: None,
        }
    }

    fn response(
        request: &Self,
        kind: impl Into<String>,
        payload: BTreeMap<String, String>,
    ) -> Self {
        Self {
            protocol: PROTOCOL_VERSION.to_owned(),
            id: request.id.clone(),
            kind: kind.into(),
            payload,
            error: None,
        }
    }

    fn error(request: &Self, message: impl Into<String>) -> Self {
        Self {
            protocol: PROTOCOL_VERSION.to_owned(),
            id: if request.id.is_empty() {
                "unknown".to_owned()
            } else {
                request.id.clone()
            },
            kind: "error".to_owned(),
            payload: BTreeMap::new(),
            error: Some(message.into()),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Action {
    Continue,
    Shutdown,
    Crash(i32),
}

#[derive(Debug, Clone)]
struct Session {
    request_id: String,
    session_id: String,
}

#[derive(Debug, Default)]
pub struct Runtime {
    sessions: HashMap<String, Session>,
}

impl Runtime {
    pub fn handle(&mut self, request: Envelope) -> (Vec<Envelope>, Action) {
        if request.protocol != PROTOCOL_VERSION {
            let protocol = if request.protocol.is_empty() {
                "missing"
            } else {
                request.protocol.as_str()
            };
            return (
                vec![Envelope::error(
                    &request,
                    format!("unsupported protocol: {protocol}"),
                )],
                Action::Continue,
            );
        }

        match request.kind.as_str() {
            "hello" => {
                let mut payload = BTreeMap::new();
                payload.insert("service".to_owned(), "chuzi-browser-runtime".to_owned());
                payload.insert("browserRuntime".to_owned(), BROWSER_RUNTIME.to_owned());
                payload.insert(
                    "capabilities".to_owned(),
                    "protocol.v1,browser-runtime.contract,browser-runtime.deferred".to_owned(),
                );
                (
                    vec![Envelope::response(&request, "hello_ack", payload)],
                    Action::Continue,
                )
            }
            "ping" => (
                vec![Envelope::response(&request, "pong", BTreeMap::new())],
                Action::Continue,
            ),
            "session_start" => self.start_session(request),
            "session_cancel" => self.cancel_session(request),
            "shutdown" => (
                vec![Envelope::response(
                    &request,
                    "shutdown_ack",
                    BTreeMap::new(),
                )],
                Action::Shutdown,
            ),
            other => (
                vec![Envelope::error(
                    &request,
                    format!(
                        "unsupported message type: {}",
                        if other.is_empty() { "missing" } else { other }
                    ),
                )],
                Action::Continue,
            ),
        }
    }

    fn start_session(&mut self, request: Envelope) -> (Vec<Envelope>, Action) {
        let required = ["session_id", "account_id", "request_id", "profile_dir"];
        if let Some(field) = required
            .iter()
            .find(|field| match request.payload.get(**field) {
                None => true,
                Some(value) => value.trim().is_empty(),
            })
        {
            return (
                vec![Envelope::error(
                    &request,
                    format!("session_start requires {field}"),
                )],
                Action::Continue,
            );
        }

        let session_id = request.payload["session_id"].clone();
        if self.sessions.contains_key(&session_id) {
            return (
                vec![Envelope::error(&request, "session is already running")],
                Action::Continue,
            );
        }

        self.sessions.insert(
            session_id.clone(),
            Session {
                request_id: request.id.clone(),
                session_id: session_id.clone(),
            },
        );
        let mut started_payload = BTreeMap::new();
        started_payload.insert("session_id".to_owned(), session_id.clone());
        let mut responses = vec![Envelope::response(
            &request,
            "session_started",
            started_payload,
        )];

        match request
            .payload
            .get("mode")
            .map(String::as_str)
            .unwrap_or(BROWSER_RUNTIME)
        {
            "hold" => (responses, Action::Continue),
            "crash" => (responses, Action::Crash(42)),
            "success" => {
                self.sessions.remove(&session_id);
                responses.push(self.session_result(&request, "session_succeeded", None));
                (responses, Action::Continue)
            }
            "failure" => {
                self.sessions.remove(&session_id);
                responses.push(self.session_result(&request, "session_failed", Some("transient")));
                (responses, Action::Continue)
            }
            _ => {
                self.sessions.remove(&session_id);
                responses.push(self.session_result(
                    &request,
                    "session_failed",
                    Some("configuration"),
                ));
                (responses, Action::Continue)
            }
        }
    }

    fn session_result(&self, request: &Envelope, kind: &str, failure: Option<&str>) -> Envelope {
        let mut payload = BTreeMap::new();
        if let Some(session_id) = request.payload.get("session_id") {
            payload.insert("session_id".to_owned(), session_id.clone());
        }
        if let Some(failure) = failure {
            payload.insert("failure".to_owned(), failure.to_owned());
            payload.insert(
                "reason".to_owned(),
                "browser runtime is deferred".to_owned(),
            );
        }
        Envelope::response(request, kind, payload)
    }

    fn cancel_session(&mut self, request: Envelope) -> (Vec<Envelope>, Action) {
        let Some(session_id) = request.payload.get("session_id").cloned() else {
            return (
                vec![Envelope::error(
                    &request,
                    "session_cancel requires session_id",
                )],
                Action::Continue,
            );
        };

        if let Some(session) = self.sessions.remove(&session_id) {
            let mut payload = BTreeMap::new();
            payload.insert("session_id".to_owned(), session.session_id);
            return (
                vec![Envelope {
                    protocol: PROTOCOL_VERSION.to_owned(),
                    id: session.request_id,
                    kind: "session_cancelled".to_owned(),
                    payload,
                    error: None,
                }],
                Action::Continue,
            );
        }

        let mut payload = BTreeMap::new();
        payload.insert("session_id".to_owned(), session_id);
        payload.insert("already_stopped".to_owned(), "true".to_owned());
        (
            vec![Envelope::response(&request, "session_cancelled", payload)],
            Action::Continue,
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request(kind: &str, payload: &[(&str, &str)]) -> Envelope {
        let mut envelope = Envelope::request("request-1", kind);
        envelope.payload = payload
            .iter()
            .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
            .collect();
        envelope
    }

    #[test]
    fn hello_reports_deferred_capabilities() {
        let mut runtime = Runtime::default();
        let (responses, action) = runtime.handle(Envelope::request("hello-1", "hello"));

        assert_eq!(action, Action::Continue);
        assert_eq!(responses[0].kind, "hello_ack");
        assert_eq!(responses[0].payload["browserRuntime"], BROWSER_RUNTIME);
        assert!(responses[0].payload["capabilities"].contains("browser-runtime.contract"));
    }

    #[test]
    fn session_modes_are_explicit() {
        let mut runtime = Runtime::default();
        let (success, _) = runtime.handle(request(
            "session_start",
            &[
                ("session_id", "session-success"),
                ("account_id", "account-1"),
                ("request_id", "request-1"),
                ("profile_dir", "/service/profile"),
                ("mode", "success"),
            ],
        ));
        assert_eq!(success[0].kind, "session_started");
        assert_eq!(success[1].kind, "session_succeeded");

        let (deferred, _) = runtime.handle(request(
            "session_start",
            &[
                ("session_id", "session-deferred"),
                ("account_id", "account-1"),
                ("request_id", "request-2"),
                ("profile_dir", "/service/profile"),
            ],
        ));
        assert_eq!(deferred[1].kind, "session_failed");
        assert_eq!(deferred[1].payload["failure"], "configuration");
    }

    #[test]
    fn held_session_can_be_cancelled() {
        let mut runtime = Runtime::default();
        let (started, _) = runtime.handle(request(
            "session_start",
            &[
                ("session_id", "session-hold"),
                ("account_id", "account-1"),
                ("request_id", "request-1"),
                ("profile_dir", "/service/profile"),
                ("mode", "hold"),
            ],
        ));
        assert_eq!(started[0].kind, "session_started");

        let (cancelled, _) =
            runtime.handle(request("session_cancel", &[("session_id", "session-hold")]));
        assert_eq!(cancelled[0].kind, "session_cancelled");
        assert_eq!(cancelled[0].id, "request-1");
    }

    #[test]
    fn shutdown_is_an_explicit_action() {
        let mut runtime = Runtime::default();
        let (responses, action) = runtime.handle(Envelope::request("shutdown-1", "shutdown"));
        assert_eq!(responses[0].kind, "shutdown_ack");
        assert_eq!(action, Action::Shutdown);
    }

    #[test]
    fn missing_protocol_is_a_classified_protocol_error() {
        let mut runtime = Runtime::default();
        let request = serde_json::from_str::<Envelope>(r#"{"id":"bad-1","type":"hello"}"#)
            .expect("missing protocol should still be an envelope");
        let (responses, action) = runtime.handle(request);

        assert_eq!(action, Action::Continue);
        assert_eq!(responses[0].kind, "error");
        assert_eq!(
            responses[0].error.as_deref(),
            Some("unsupported protocol: missing")
        );
    }
}
