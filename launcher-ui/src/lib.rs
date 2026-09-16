mod config;
mod desktop;
mod launcher_process;
mod protocol;

use tauri::{AppHandle, Manager, State, WindowEvent};

use config::LauncherConfig;
use launcher_process::{execute, OperationRegistry};
use protocol::{ResultEvent, UiRequest};

pub struct AppState {
    config: LauncherConfig,
    operations: OperationRegistry,
}

#[tauri::command]
async fn launcher_request(
    app: AppHandle,
    state: State<'_, AppState>,
    request: UiRequest,
) -> Result<ResultEvent, String> {
    let id = request.id.clone();
    let config = state.config.clone();
    let operations = state.operations.clone();
    Ok(
        tauri::async_runtime::spawn_blocking(move || execute(app, config, request, operations))
            .await
            .unwrap_or_else(|_| ResultEvent::failure(id, "launcher_process_failed")),
    )
}

pub fn run() {
    let config = match LauncherConfig::from_env() {
        Ok(Some(config)) => config,
        Ok(None) => return,
        Err(error) => {
            eprintln!("[chuzi-launcher-ui] {error}");
            std::process::exit(2);
        }
    };

    tauri::Builder::default()
        .manage(AppState {
            config,
            operations: OperationRegistry::default(),
        })
        .setup(desktop::setup)
        .invoke_handler(tauri::generate_handler![launcher_request])
        .on_window_event(|window, event| {
            if matches!(event, WindowEvent::Destroyed) {
                window
                    .app_handle()
                    .state::<AppState>()
                    .operations
                    .cancel_all();
            }
        })
        .run(tauri::generate_context!())
        .expect("error while running chuzi launcher UI");
}
