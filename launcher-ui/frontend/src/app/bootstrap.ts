import { LauncherClient, TauriLauncherTransport } from "../services/ipc.js";
import { applyAppearance, initializeAppearance } from "../runtime/appearance.js";
import { installMaterialMotion } from "../runtime/material-motion.js";
import type { AppState } from "../state/types.js";
import { byId } from "../utilities/dom.js";
import { renderComponents } from "../views/component-view.js";
import { renderOnboarding, renderOverviewComponents, renderUpdate, renderWorkspaceStatus } from "../views/overview-view.js";
import { renderPlugins } from "../views/plugin-view.js";
import { renderSettings } from "../views/settings-view.js";
import { LauncherController } from "../features/launcher-controller.js";

const viewTitles: Record<AppState["activeView"], string> = {
  overview: "Overview",
  components: "Components",
  plugins: "Plugins",
  settings: "Settings"
};

let renderedSettings: AppState["settings"] | undefined;

function render(state: AppState): void {
  renderComponents(byId("components"), state.components);
  renderOverviewComponents(byId("overview-components"), state.components);
  renderPlugins(byId("plugins"), state.plugins);
  if (state.settings !== renderedSettings) {
    renderSettings(state.settings);
    renderedSettings = state.settings;
  }
  renderUpdate(byId("update-status"), state.update);
  renderOnboarding(byId("onboarding"), state.initialization, state.components);
  renderWorkspaceStatus(byId("workspace-readiness"), state.initialization?.first_run ? "setup" : state.refreshInFlight ? "syncing" : "ready");
  byId("version").textContent = state.initialization
    ? (state.initialization.first_run ? "first launch" : "ready")
    : "loading manifest";
  byId("global-progress").textContent = state.progress;
  byId("component-progress").textContent = state.progress;
  byId("plugin-progress").textContent = state.progress;
  byId("cancel-operation").hidden = !state.currentOperation;
  byId("global-error").textContent = state.error;
  const toast = byId("toast");
  toast.textContent = state.error;
  toast.classList.toggle("show", Boolean(state.error));
  document.querySelectorAll<HTMLElement>("[data-view]").forEach((button) => {
    button.classList.toggle("active", button.dataset.view === state.activeView);
  });
  document.querySelectorAll<HTMLElement>(".view").forEach((view) => {
    view.classList.toggle("active", view.id === `view-${state.activeView}`);
  });
  byId("page-title").textContent = viewTitles[state.activeView];
}

function currentMaterial(): string {
  return document.documentElement.dataset.material ?? "solid";
}

function installInteractions(controller: LauncherController): void {
  document.addEventListener("click", (event) => {
    const target = event.target;
    if (!(target instanceof Element)) return;
    const themeButton = target.closest<HTMLElement>("[data-theme-control]");
    if (themeButton) {
      applyAppearance(themeButton.dataset.themeControl, currentMaterial(), true);
      return;
    }
    const materialButton = target.closest<HTMLElement>("[data-material-control]");
    if (materialButton) {
      applyAppearance(document.documentElement.dataset.theme, materialButton.dataset.materialControl, true);
      return;
    }
    const viewButton = target.closest<HTMLElement>("[data-view]");
    if (viewButton && viewButton.dataset.view) {
      controller.setView(viewButton.dataset.view as AppState["activeView"]);
      return;
    }
    const actionButton = target.closest<HTMLElement>("[data-action]");
    const action = actionButton?.dataset.action;
    if (!action) return;
    if (action === "refresh" || action === "refresh-plugins") controller.refresh();
    else if (action === "check-update") controller.checkUpdate();
    else if (action === "finish-onboarding") controller.finishOnboarding();
    else if (action === "save-settings") controller.saveSettings();
    else if (action === "cancel-operation") controller.cancelOperation();
    else controller.startAction(action, actionButton?.dataset.item);
  });
}

export async function bootstrap(): Promise<void> {
  initializeAppearance();
  installMaterialMotion();
  const client = new LauncherClient(new TauriLauncherTransport());
  const controller = new LauncherController(client, render);
  installInteractions(controller);
  window.addEventListener("unload", () => client.stop(), { once: true });
  await controller.start();
}

void bootstrap().catch((error: unknown) => {
  const message = error instanceof Error ? error.message : "launcher startup failed";
  const target = document.getElementById("global-error");
  if (target) target.textContent = message;
});
