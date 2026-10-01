//! Conversion from the pure Session view model into the Slint presentation
//! model. This module has no launcher or Core dependencies and is shared by
//! production rendering and deterministic layout fixtures.

use crate::view_model::{FailureSummary, SessionDisplay, SessionViewModel};
use crate::{MainWindow, SessionRowData};
use slint::{ModelRc, SharedString};

pub(crate) fn render(window: &MainWindow, model: &SessionViewModel) {
    let rows = model
        .visible_items()
        .into_iter()
        .map(|item| row_data(item, model.selected_key() == item.request_id.as_deref()))
        .collect::<Vec<_>>();
    window.set_session_rows(ModelRc::from(rows.as_slice()));
    window.set_session_filter(model.filter().as_str().into());
    window.set_session_search(model.search().into());
    window.set_session_has_more_requests(model.has_more());

    let inspector = model.inspector();
    if let Some(item) = inspector.selected {
        window.set_session_inspector_selected(true);
        window.set_session_inspector_request_id(
            item.request_id
                .as_deref()
                .unwrap_or("Request unavailable")
                .into(),
        );
        window.set_session_inspector_account_label(
            item.account_label
                .as_deref()
                .unwrap_or("Account unavailable")
                .into(),
        );
        window.set_session_inspector_status_label(item.status.label().into());
        window.set_session_inspector_attempt_label(attempt_label(&item).into());
        window.set_session_inspector_created_at(item.created_at.as_deref().unwrap_or("").into());
        window.set_session_inspector_updated_at(item.updated_at.as_deref().unwrap_or("").into());
        window.set_session_inspector_failure_label(failure_label(item.failure).into());
        window.set_session_inspector_primary_action(item.primary_action.as_str().into());
        window.set_session_inspector_primary_label(item.primary_action.label().into());
        window.set_session_inspector_has_more_action(!inspector.actions.is_empty());
    } else {
        window.set_session_inspector_selected(false);
        window.set_session_inspector_request_id(SharedString::default());
        window.set_session_inspector_account_label(SharedString::default());
        window.set_session_inspector_status_label(SharedString::default());
        window.set_session_inspector_attempt_label(SharedString::default());
        window.set_session_inspector_created_at(SharedString::default());
        window.set_session_inspector_updated_at(SharedString::default());
        window.set_session_inspector_failure_label(SharedString::default());
        window.set_session_inspector_primary_action("none".into());
        window.set_session_inspector_primary_label(SharedString::default());
        window.set_session_inspector_has_more_action(false);
    }

    let message = match model.projection().phase {
        crate::view_model::ProjectionPhase::Loading => "Loading requests…",
        crate::view_model::ProjectionPhase::Empty => "No requests yet",
        crate::view_model::ProjectionPhase::Unavailable => {
            "Core is unavailable. Start Core and refresh the list."
        }
        crate::view_model::ProjectionPhase::Error => {
            "Requests could not be loaded. Refresh the list to try again."
        }
        crate::view_model::ProjectionPhase::Ready if rows.is_empty() => {
            "No loaded requests match this search and filter."
        }
        crate::view_model::ProjectionPhase::Ready => "",
    };
    window.set_session_collection_message(message.into());
    window.set_session_phase(model.projection().phase.as_str().into());
}

fn row_data(item: &SessionDisplay, selected: bool) -> SessionRowData {
    let request_id = item.request_id.as_deref().unwrap_or("");
    SessionRowData {
        request_id: request_id.into(),
        request_label: if request_id.is_empty() {
            "Request identifier unavailable".into()
        } else {
            format!("Request  ·  {request_id}").into()
        },
        account_label: item
            .account_label
            .as_deref()
            .unwrap_or("Account unavailable")
            .into(),
        status: item.status.as_str().into(),
        status_label: item.status.label().into(),
        attempt_label: attempt_label(item).into(),
        updated_at: item
            .updated_at
            .as_deref()
            .map(row_timestamp_label)
            .unwrap_or_else(|| "Updated time unavailable".to_owned())
            .into(),
        primary_action: item.primary_action.as_str().into(),
        primary_label: item.primary_action.label().into(),
        selected,
    }
}

fn row_timestamp_label(value: &str) -> String {
    let Some((date, time_and_zone)) = value.split_once('T') else {
        return value.to_owned();
    };
    let Some(month_day) = date.get(5..10) else {
        return value.to_owned();
    };
    let Some(time) = time_and_zone.get(..5) else {
        return value.to_owned();
    };
    let zone = if time_and_zone.ends_with('Z') {
        "Z"
    } else {
        ""
    };
    format!("{month_day} {time}{zone}")
}

fn attempt_label(item: &SessionDisplay) -> String {
    item.attempt
        .map(|attempt| format!("Attempt {attempt}"))
        .unwrap_or_else(|| "Attempt unavailable".to_owned())
}

fn failure_label(failure: Option<FailureSummary>) -> String {
    match failure {
        Some(FailureSummary::Transient) => "Failure class  ·  Transient".to_owned(),
        Some(FailureSummary::Credential) => "Failure class  ·  Credential".to_owned(),
        Some(FailureSummary::Permission) => "Failure class  ·  Permission".to_owned(),
        Some(FailureSummary::Configuration) => "Failure class  ·  Configuration".to_owned(),
        Some(FailureSummary::Unknown) => "Failure class  ·  Other".to_owned(),
        None => String::new(),
    }
}
