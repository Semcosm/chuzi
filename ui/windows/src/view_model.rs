//! Pure, redacted projections used by the Windows UI.
//!
//! The Core owns business state. This module only translates the stable Core
//! strings into a small display vocabulary and deliberately drops fields that
//! are not safe or useful to render. It has no launcher, storage, browser, or
//! credential dependencies, so the mapping can be tested deterministically.

use crate::models::CoreRequest;

/// Business states that the first Sessions surface can explain to a user.
///
/// Unknown Core values stay `Unknown`; the UI must not infer a transition from
/// a new or malformed state.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum DisplayStatus {
    Running,
    Queued,
    Succeeded,
    Failed,
    Expired,
    Cancelled,
    Blocked,
    NoRequest,
    Unknown,
}

impl DisplayStatus {
    pub(crate) const fn as_str(self) -> &'static str {
        match self {
            Self::Running => "running",
            Self::Queued => "queued",
            Self::Succeeded => "succeeded",
            Self::Failed => "failed",
            Self::Expired => "expired",
            Self::Cancelled => "cancelled",
            Self::Blocked => "blocked",
            Self::NoRequest => "no-request",
            Self::Unknown => "unknown",
        }
    }

    pub(crate) const fn label(self) -> &'static str {
        match self {
            Self::Running => "In progress",
            Self::Queued => "Queued",
            Self::Succeeded => "Succeeded",
            Self::Failed => "Failed",
            Self::Expired => "Expired",
            Self::Cancelled => "Cancelled",
            Self::Blocked => "Blocked",
            Self::NoRequest => "No request",
            Self::Unknown => "Unknown state",
        }
    }
}

/// The one action the current context may promote.
///
/// `None` is intentional for an unknown state: hiding an action is safer than
/// guessing which Core transition a new state permits.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum PrimaryAction {
    CaptureView,
    Cancel,
    RefreshStatus,
    None,
}

impl PrimaryAction {
    pub(crate) const fn as_str(self) -> &'static str {
        match self {
            Self::CaptureView => "capture-view",
            Self::Cancel => "cancel",
            Self::RefreshStatus => "refresh-status",
            Self::None => "none",
        }
    }

    pub(crate) const fn label(self) -> &'static str {
        match self {
            Self::CaptureView => "Capture view",
            Self::Cancel => "Cancel request",
            Self::RefreshStatus => "Refresh status",
            Self::None => "",
        }
    }
}

/// A bounded failure vocabulary. Core failure text is never copied into the
/// display model because it may contain implementation or sensitive details.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum FailureSummary {
    Transient,
    Credential,
    Permission,
    Configuration,
    Unknown,
}

/// Safe projection for one account/request-backed Session row.
///
/// `account_label` is retained only when it already has Core's redacted
/// `id_<12 hex characters>` form. `request_id` is an opaque, bounded action
/// key; it is not copied from arbitrary invalid input. No credentials, paths,
/// room IDs, browser facts, or raw failure messages exist in this type.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct SessionDisplay {
    pub(crate) request_id: Option<String>,
    pub(crate) account_label: Option<String>,
    pub(crate) status: DisplayStatus,
    pub(crate) primary_action: PrimaryAction,
    pub(crate) attempt: Option<u32>,
    pub(crate) failure: Option<FailureSummary>,
    pub(crate) created_at: Option<String>,
    pub(crate) updated_at: Option<String>,
}

/// Collection-level projection state. Existing safe items may remain visible
/// while a refresh is unavailable or fails.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum ProjectionPhase {
    Loading,
    Ready,
    Empty,
    Unavailable,
    Error,
}

impl ProjectionPhase {
    pub(crate) const fn as_str(self) -> &'static str {
        match self {
            Self::Loading => "loading",
            Self::Ready => "ready",
            Self::Empty => "empty",
            Self::Unavailable => "unavailable",
            Self::Error => "error",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum ProjectionError {
    CoreUnavailable,
    InvalidData,
    Unknown,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct SessionListProjection {
    pub(crate) phase: ProjectionPhase,
    pub(crate) items: Vec<SessionDisplay>,
    pub(crate) error: Option<ProjectionError>,
}

impl SessionListProjection {
    pub(crate) fn loading(previous: Vec<SessionDisplay>) -> Self {
        Self {
            phase: ProjectionPhase::Loading,
            items: previous,
            error: None,
        }
    }

    pub(crate) fn empty() -> Self {
        Self {
            phase: ProjectionPhase::Empty,
            items: Vec::new(),
            error: None,
        }
    }

    pub(crate) fn from_items(items: Vec<SessionDisplay>) -> Self {
        if items.is_empty() {
            Self::empty()
        } else {
            Self {
                phase: ProjectionPhase::Ready,
                items,
                error: None,
            }
        }
    }

    pub(crate) fn unavailable(previous: Vec<SessionDisplay>) -> Self {
        Self {
            phase: ProjectionPhase::Unavailable,
            items: previous,
            error: Some(ProjectionError::CoreUnavailable),
        }
    }

    pub(crate) fn error(previous: Vec<SessionDisplay>, error: ProjectionError) -> Self {
        Self {
            phase: ProjectionPhase::Error,
            items: previous,
            error: Some(error),
        }
    }
}

/// The bounded set of Sessions filters exposed by the shell. Filtering is
/// local to the already loaded redacted projection; it never broadens the Core
/// query or causes the view model to read storage.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub(crate) enum SessionFilter {
    #[default]
    All,
    Running,
    Queued,
    Failed,
}

impl SessionFilter {
    pub(crate) fn from_str(value: &str) -> Option<Self> {
        match value {
            "all" => Some(Self::All),
            "running" => Some(Self::Running),
            "queued" => Some(Self::Queued),
            "failed" => Some(Self::Failed),
            _ => None,
        }
    }

    pub(crate) const fn as_str(self) -> &'static str {
        match self {
            Self::All => "all",
            Self::Running => "running",
            Self::Queued => "queued",
            Self::Failed => "failed",
        }
    }

    pub(crate) fn matches(self, status: DisplayStatus) -> bool {
        match self {
            Self::All => true,
            Self::Running => status == DisplayStatus::Running,
            Self::Queued => status == DisplayStatus::Queued,
            Self::Failed => status == DisplayStatus::Failed,
        }
    }
}

/// Keyboard movement is intentionally expressed as a view concern. It never
/// implies a Core transition.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SelectionMove {
    Up,
    Down,
    Home,
    End,
}

/// Low-frequency actions shown by a Session More menu. The action is an
/// intent for the launcher/Core controller; this module only determines which
/// bounded intents are valid for the projected state.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum MoreAction {
    RefreshStatus,
}

impl MoreAction {
    pub(crate) const fn as_str(self) -> &'static str {
        match self {
            Self::RefreshStatus => "refresh-status",
        }
    }

    pub(crate) const fn is_destructive(self) -> bool {
        false
    }
}

/// A safe action descriptor consumed by a row or Inspector. `enabled` is
/// derived only from the Core-projected business state.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct ActionDescriptor {
    pub(crate) action: MoreAction,
    pub(crate) enabled: bool,
    pub(crate) destructive: bool,
}

impl ActionDescriptor {
    const fn new(action: MoreAction, enabled: bool) -> Self {
        Self {
            action,
            enabled,
            destructive: action.is_destructive(),
        }
    }
}

/// The in-place Inspector projection. Keeping this separate from the list
/// lets a selection change update one region without navigating or reloading
/// the window.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct SessionInspector {
    pub(crate) selected: Option<SessionDisplay>,
    pub(crate) primary_action: PrimaryAction,
    pub(crate) primary_enabled: bool,
    pub(crate) actions: Vec<ActionDescriptor>,
}

impl SessionInspector {
    fn empty() -> Self {
        Self {
            selected: None,
            primary_action: PrimaryAction::None,
            primary_enabled: false,
            actions: Vec::new(),
        }
    }

    fn from_session(session: SessionDisplay) -> Self {
        let status = session.status;
        let actions = more_actions(status);
        Self {
            primary_action: session.primary_action,
            primary_enabled: session.primary_action != PrimaryAction::None,
            selected: Some(session),
            actions,
        }
    }
}

/// Pure list/selection state for the Sessions vertical slice. A controller can
/// call [`SessionViewModel::projection`] to render the collection and
/// [`SessionViewModel::inspector`] for the selected object.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct SessionViewModel {
    projection: SessionListProjection,
    filter: SessionFilter,
    search: String,
    selected_key: Option<String>,
    focused_index: Option<usize>,
    has_more: bool,
    next_offset: usize,
}

impl SessionViewModel {
    pub(crate) fn new(projection: SessionListProjection) -> Self {
        Self {
            projection,
            filter: SessionFilter::All,
            search: String::new(),
            selected_key: None,
            focused_index: None,
            has_more: false,
            next_offset: 0,
        }
    }

    pub(crate) fn projection(&self) -> &SessionListProjection {
        &self.projection
    }

    pub(crate) fn filter(&self) -> SessionFilter {
        self.filter
    }

    pub(crate) fn search(&self) -> &str {
        &self.search
    }

    pub(crate) fn begin_loading(&mut self) {
        self.projection = SessionListProjection::loading(self.projection.items.clone());
    }

    pub(crate) fn set_first_page(&mut self, projection: SessionListProjection, has_more: bool) {
        self.projection = projection;
        self.has_more = has_more;
        self.next_offset = self.projection.items.len();
        self.reconcile_selection();
    }

    pub(crate) fn append_page(&mut self, items: Vec<SessionDisplay>, has_more: bool) {
        self.projection.items.extend(items);
        self.has_more = has_more;
        self.next_offset = self.projection.items.len();
        self.projection.phase = if self.projection.items.is_empty() {
            ProjectionPhase::Empty
        } else {
            ProjectionPhase::Ready
        };
        self.projection.error = None;
        self.reconcile_selection();
    }

    pub(crate) fn set_error(&mut self, error: ProjectionError) {
        self.projection = match error {
            ProjectionError::CoreUnavailable => {
                SessionListProjection::unavailable(self.projection.items.clone())
            }
            other => SessionListProjection::error(self.projection.items.clone(), other),
        };
    }

    pub(crate) fn next_offset(&self) -> usize {
        self.next_offset
    }

    pub(crate) fn has_more(&self) -> bool {
        self.has_more
    }

    pub(crate) fn update_request(&mut self, request: &CoreRequest) {
        let updated = session_from_request(request);
        if let Some(key) = updated.request_id.as_deref() {
            if let Some(existing) = self
                .projection
                .items
                .iter_mut()
                .find(|item| item.request_id.as_deref() == Some(key))
            {
                *existing = updated;
            }
        }
        self.reconcile_selection();
    }

    pub(crate) fn set_filter(&mut self, filter: SessionFilter) {
        self.filter = filter;
        self.reconcile_selection();
    }

    /// Search is deliberately bounded and case-insensitive. It searches only
    /// request IDs and already-redacted account labels.
    pub(crate) fn set_search(&mut self, search: &str) {
        self.search = search.trim().chars().take(128).collect();
        self.reconcile_selection();
    }

    pub(crate) fn visible_items(&self) -> Vec<&SessionDisplay> {
        self.projection
            .items
            .iter()
            .filter(|item| self.matches(item))
            .collect()
    }

    pub(crate) fn selected(&self) -> Option<&SessionDisplay> {
        let selected = self.selected_key.as_deref()?;
        self.visible_items()
            .into_iter()
            .find(|item| session_key(item).as_deref() == Some(selected))
    }

    pub(crate) fn selected_key(&self) -> Option<&str> {
        self.selected_key.as_deref()
    }

    pub(crate) fn inspector(&self) -> SessionInspector {
        self.selected()
            .cloned()
            .map(SessionInspector::from_session)
            .unwrap_or_else(SessionInspector::empty)
    }

    pub(crate) fn select_visible(&mut self, index: usize) -> bool {
        let visible = self.visible_items();
        let Some(item) = visible.get(index) else {
            return false;
        };
        let Some(key) = session_key(item) else {
            return false;
        };
        self.selected_key = Some(key);
        self.focused_index = Some(index);
        true
    }

    pub(crate) fn select_key(&mut self, key: &str) -> bool {
        let Some(index) = self
            .visible_items()
            .iter()
            .position(|item| session_key(item).as_deref() == Some(key))
        else {
            return false;
        };
        self.select_visible(index)
    }

    pub(crate) fn move_selection(&mut self, movement: SelectionMove) -> bool {
        let count = self.visible_items().len();
        if count == 0 {
            self.selected_key = None;
            self.focused_index = None;
            return false;
        }
        let Some(current) = self.focused_index else {
            let initial = if movement == SelectionMove::End {
                count - 1
            } else {
                0
            };
            return self.select_visible(initial);
        };
        let current = current.min(count - 1);
        let next = match movement {
            SelectionMove::Up => current.saturating_sub(1),
            SelectionMove::Down => (current + 1).min(count - 1),
            SelectionMove::Home => 0,
            SelectionMove::End => count - 1,
        };
        self.select_visible(next)
    }

    fn matches(&self, item: &SessionDisplay) -> bool {
        if !self.filter.matches(item.status) {
            return false;
        }
        let needle = self.search.to_ascii_lowercase();
        if needle.is_empty() {
            return true;
        }
        [item.request_id.as_deref(), item.account_label.as_deref()]
            .into_iter()
            .flatten()
            .any(|value| value.to_ascii_lowercase().contains(&needle))
    }

    fn reconcile_selection(&mut self) {
        let visible_len = self.visible_items().len();
        if let Some(key) = self.selected_key.as_deref() {
            if let Some(index) = self
                .visible_items()
                .iter()
                .position(|item| session_key(item).as_deref() == Some(key))
            {
                self.focused_index = Some(index);
                return;
            }
        }
        self.selected_key = None;
        self.focused_index = self.focused_index.filter(|index| *index < visible_len);
    }
}

impl Default for SessionViewModel {
    fn default() -> Self {
        Self::new(SessionListProjection::loading(Vec::new()))
    }
}

fn session_key(session: &SessionDisplay) -> Option<String> {
    session
        .request_id
        .clone()
        .or_else(|| session.account_label.clone())
}

fn more_actions(status: DisplayStatus) -> Vec<ActionDescriptor> {
    // Refresh is the only secondary command supported by the current Core
    // contract; unsupported retry/restart/stop operations are not advertised.
    match status {
        DisplayStatus::Running | DisplayStatus::Queued => {
            vec![ActionDescriptor::new(MoreAction::RefreshStatus, true)]
        }
        DisplayStatus::Succeeded
        | DisplayStatus::Failed
        | DisplayStatus::Expired
        | DisplayStatus::Cancelled
        | DisplayStatus::Blocked
        | DisplayStatus::NoRequest
        | DisplayStatus::Unknown => Vec::new(),
    }
}

/// Maps every account/request state declared by the Core state machine.
pub(crate) fn display_status(state: &str) -> DisplayStatus {
    match state {
        "NO_REQUEST" => DisplayStatus::NoRequest,
        "QUEUED" => DisplayStatus::Queued,
        "STARTING" | "LOGGING_IN" => DisplayStatus::Running,
        "LOGIN_SUCCEEDED" => DisplayStatus::Succeeded,
        "LOGIN_FAILED" => DisplayStatus::Failed,
        "EXPIRED" => DisplayStatus::Expired,
        "CANCELLED" => DisplayStatus::Cancelled,
        "BLOCKED" => DisplayStatus::Blocked,
        _ => DisplayStatus::Unknown,
    }
}

pub(crate) const fn primary_action(status: DisplayStatus) -> PrimaryAction {
    match status {
        DisplayStatus::Running => PrimaryAction::CaptureView,
        DisplayStatus::Queued => PrimaryAction::Cancel,
        DisplayStatus::NoRequest => PrimaryAction::None,
        DisplayStatus::Succeeded
        | DisplayStatus::Failed
        | DisplayStatus::Expired
        | DisplayStatus::Cancelled
        | DisplayStatus::Blocked
        | DisplayStatus::Unknown => PrimaryAction::RefreshStatus,
    }
}

pub(crate) fn session_from_request(request: &CoreRequest) -> SessionDisplay {
    let status = display_status(&request.state);
    SessionDisplay {
        request_id: safe_identifier(&request.request_id),
        account_label: safe_redacted_label(&request.account),
        status,
        primary_action: primary_action(status),
        attempt: u32::try_from(request.attempt).ok(),
        failure: failure_summary(&request.last_failure),
        created_at: non_empty(&request.created_at),
        updated_at: non_empty(&request.updated_at),
    }
}

pub(crate) fn sessions_from_requests(requests: &[CoreRequest]) -> SessionListProjection {
    SessionListProjection::from_items(requests.iter().map(session_from_request).collect())
}

fn failure_summary(value: &str) -> Option<FailureSummary> {
    if value.is_empty() {
        return None;
    }
    Some(match value {
        "transient" => FailureSummary::Transient,
        "credential" => FailureSummary::Credential,
        "permission" => FailureSummary::Permission,
        "configuration" => FailureSummary::Configuration,
        _ => FailureSummary::Unknown,
    })
}

fn non_empty(value: &str) -> Option<String> {
    (!value.is_empty()).then(|| value.to_owned())
}

fn safe_identifier(value: &str) -> Option<String> {
    let value = value.trim();
    if value.is_empty() || value.len() > 128 || value.chars().any(char::is_control) {
        return None;
    }
    Some(value.to_owned())
}

fn safe_redacted_label(value: &str) -> Option<String> {
    let value = value.trim();
    let suffix = value.strip_prefix("id_")?;
    if suffix.len() != 12 || !suffix.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return None;
    }
    Some(value.to_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request(state: &str, id: &str) -> CoreRequest {
        CoreRequest {
            request_id: id.to_owned(),
            account: "id_0123456789ab".to_owned(),
            state: state.to_owned(),
            attempt: 2,
            last_failure: String::new(),
            created_at: "2026-09-01T10:00:00Z".to_owned(),
            updated_at: "2026-09-01T10:05:00Z".to_owned(),
        }
    }

    fn fixtures() -> SessionListProjection {
        SessionListProjection::from_items(vec![
            session_from_request(&request("QUEUED", "fixture-queued")),
            session_from_request(&request("STARTING", "fixture-running")),
            session_from_request(&request("LOGIN_FAILED", "fixture-failed")),
            session_from_request(&request("LOGIN_SUCCEEDED", "fixture-succeeded")),
        ])
    }

    #[test]
    fn request_states_are_preserved_without_inventing_running_or_actions() {
        let cases = [
            ("NO_REQUEST", DisplayStatus::NoRequest, PrimaryAction::None),
            ("QUEUED", DisplayStatus::Queued, PrimaryAction::Cancel),
            (
                "STARTING",
                DisplayStatus::Running,
                PrimaryAction::CaptureView,
            ),
            (
                "LOGGING_IN",
                DisplayStatus::Running,
                PrimaryAction::CaptureView,
            ),
            (
                "LOGIN_SUCCEEDED",
                DisplayStatus::Succeeded,
                PrimaryAction::RefreshStatus,
            ),
            (
                "LOGIN_FAILED",
                DisplayStatus::Failed,
                PrimaryAction::RefreshStatus,
            ),
            (
                "EXPIRED",
                DisplayStatus::Expired,
                PrimaryAction::RefreshStatus,
            ),
            (
                "CANCELLED",
                DisplayStatus::Cancelled,
                PrimaryAction::RefreshStatus,
            ),
            (
                "BLOCKED",
                DisplayStatus::Blocked,
                PrimaryAction::RefreshStatus,
            ),
            (
                "future_state",
                DisplayStatus::Unknown,
                PrimaryAction::RefreshStatus,
            ),
        ];
        for (state, expected_status, expected_action) in cases {
            let projected = session_from_request(&request(state, "request-1"));
            assert_eq!(projected.status, expected_status, "state {state}");
            assert_eq!(projected.primary_action, expected_action, "state {state}");
        }
    }

    #[test]
    fn projection_keeps_only_redacted_labels_safe_failure_class_and_core_times() {
        let mut input = request("LOGIN_FAILED", "request-1");
        input.account = "/var/lib/chuzi/profiles/account-password".to_owned();
        input.last_failure = "password=super-secret room_id=!private:example.org".to_owned();
        let projected = session_from_request(&input);
        assert_eq!(projected.account_label, None);
        assert_eq!(projected.failure, Some(FailureSummary::Unknown));
        assert_eq!(
            projected.updated_at.as_deref(),
            Some("2026-09-01T10:05:00Z")
        );
        let debug = format!("{projected:?}");
        assert!(!debug.contains("super-secret"));
        assert!(!debug.contains("private:example.org"));
        assert!(!debug.contains("/var/lib/chuzi"));
    }

    #[test]
    fn loading_empty_error_and_paged_results_are_deterministic() {
        let mut model = SessionViewModel::new(SessionListProjection::empty());
        model.begin_loading();
        assert_eq!(model.projection().phase, ProjectionPhase::Loading);
        model.set_first_page(fixtures(), true);
        assert_eq!(model.next_offset(), 4);
        assert!(model.has_more());
        model.append_page(
            vec![session_from_request(&request("CANCELLED", "fixture-last"))],
            false,
        );
        assert_eq!(model.projection().items.len(), 5);
        assert_eq!(model.next_offset(), 5);
        assert!(!model.has_more());
        model.set_error(ProjectionError::CoreUnavailable);
        assert_eq!(model.projection().phase, ProjectionPhase::Unavailable);
        assert_eq!(model.projection().items.len(), 5);
        assert_eq!(
            model.projection().error,
            Some(ProjectionError::CoreUnavailable)
        );
    }

    #[test]
    fn filter_search_keyboard_and_selection_update_inspector_in_place() {
        let mut model = SessionViewModel::new(fixtures());
        model.set_filter(SessionFilter::Failed);
        assert_eq!(model.visible_items().len(), 1);
        assert_eq!(model.visible_items()[0].status, DisplayStatus::Failed);
        model.set_filter(SessionFilter::All);
        model.set_search("RUNNING");
        assert_eq!(model.visible_items().len(), 1);
        assert!(model.select_visible(0));
        assert_eq!(
            model
                .inspector()
                .selected
                .as_ref()
                .unwrap()
                .request_id
                .as_deref(),
            Some("fixture-running")
        );
        model.set_search("");
        assert_eq!(model.selected_key(), Some("fixture-running"));
        assert!(model.move_selection(SelectionMove::End));
        assert_eq!(model.selected_key(), Some("fixture-succeeded"));
        assert_eq!(
            model.inspector().selected.as_ref().unwrap().status,
            DisplayStatus::Succeeded
        );
        model.set_filter(SessionFilter::Queued);
        assert_eq!(model.inspector().selected, None);
    }

    #[test]
    fn only_core_supported_actions_are_exposed_and_updates_keep_selection() {
        let mut model = SessionViewModel::new(fixtures());
        assert!(model.select_key("fixture-running"));
        let running = model.inspector();
        assert_eq!(running.primary_action, PrimaryAction::CaptureView);
        assert_eq!(running.actions.len(), 1);
        assert_eq!(running.actions[0].action, MoreAction::RefreshStatus);
        assert!(!running.actions[0].destructive);

        assert!(model.select_key("fixture-queued"));
        assert_eq!(model.inspector().primary_action, PrimaryAction::Cancel);
        let mut cancelled = request("CANCELLED", "fixture-queued");
        cancelled.updated_at = "2026-09-01T10:06:00Z".to_owned();
        model.update_request(&cancelled);
        assert_eq!(model.selected_key(), Some("fixture-queued"));
        assert_eq!(
            model.inspector().selected.unwrap().status,
            DisplayStatus::Cancelled
        );
        assert!(model.inspector().actions.is_empty());

        assert_eq!(
            primary_action(display_status("NO_REQUEST")),
            PrimaryAction::None
        );
    }

    #[test]
    fn invalid_keys_and_future_states_fail_closed() {
        let mut value = request("future_state", "request-1");
        value.account = "raw-account-id".to_owned();
        let item = session_from_request(&value);
        assert_eq!(item.status, DisplayStatus::Unknown);
        assert_eq!(item.account_label, None);
        assert_eq!(item.primary_action, PrimaryAction::RefreshStatus);
        let mut model = SessionViewModel::new(SessionListProjection::from_items(vec![item]));
        assert!(!model.select_key("not-in-list"));
        assert_eq!(model.inspector().selected, None);
    }
}
