import { LauncherClient, TauriLauncherTransport } from "../services/ipc.js";
import { applyAppearance, getAppearance, initializeAppearance, updateAppearance } from "../runtime/appearance.js";
import { installMaterialMotion } from "../runtime/material-motion.js";
import { disposeLiquidGlassRenderer, installLiquidGlassRenderer } from "../runtime/liquid-glass.js";
import { syncMaterialSurfaces } from "../runtime/material.js";
import type { AppState, MaterialOpacityKey } from "../state/types.js";
import { byId } from "../utilities/dom.js";
import { renderComponents } from "../views/component-view.js";
import { renderOnboarding, renderOverviewComponents, renderUpdate, renderWorkspaceStatus } from "../views/overview-view.js";
import { renderPlugins } from "../views/plugin-view.js";
import { renderSettings } from "../views/settings-view.js";
import { LauncherController } from "../features/launcher-controller.js";
import { installWindowControls } from "../features/window-controls.js";

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
  // Rows and onboarding panels are rendered after the initial appearance pass.
  // Bind their material datasets through the same resolver before the optical
  // renderer observes them.
  syncMaterialSurfaces(document.documentElement, getAppearance());
}

function currentMaterial(): string {
  return document.documentElement.dataset.material ?? "solid";
}

let demoToastTimer: number | undefined;

function showDemoToast(): void {
  const toast = byId("toast");
  toast.textContent = "Glass controls are responding without moving the title bar.";
  toast.classList.add("show");
  if (demoToastTimer !== undefined) window.clearTimeout(demoToastTimer);
  demoToastTimer = window.setTimeout(() => {
    toast.classList.remove("show");
    toast.textContent = "";
    demoToastTimer = undefined;
  }, 2600);
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
    const chromeMaterialButton = target.closest<HTMLElement>("[data-chrome-material-control]");
    if (chromeMaterialButton) {
      updateAppearance({ chromeMaterial: chromeMaterialButton.dataset.chromeMaterialControl }, true);
      return;
    }
    const demoAction = target.closest<HTMLElement>("[data-demo-action]");
    if (demoAction?.dataset.demoAction === "toast") {
      showDemoToast();
      return;
    }
    if (demoAction?.dataset.demoAction === "magnifier") {
      demoAction.closest<HTMLElement>(".control-sample")?.classList.toggle("magnifier-active");
      return;
    }
    const dialogOpen = target.closest<HTMLElement>("[data-dialog-open]");
    if (dialogOpen?.dataset.dialogOpen) {
      const dialog = document.getElementById(dialogOpen.dataset.dialogOpen);
      if (dialog instanceof HTMLDialogElement) dialog.showModal();
      return;
    }
    const dialogClose = target.closest<HTMLElement>("[data-dialog-close]");
    if (dialogClose) {
      const dialog = dialogClose.closest("dialog");
      if (dialog instanceof HTMLDialogElement) dialog.close();
      return;
    }
    const demoTab = target.closest<HTMLElement>("[data-demo-tab]");
    if (demoTab) {
      const tablist = demoTab.closest("[role=\"tablist\"]");
      tablist?.querySelectorAll<HTMLElement>("[data-demo-tab]").forEach((tab) => {
        tab.setAttribute("aria-selected", String(tab === demoTab));
      });
      const panelId = demoTab.getAttribute("aria-controls");
      const panelRoot = tablist?.parentElement;
      panelRoot?.querySelectorAll<HTMLElement>("[role=\"tabpanel\"]").forEach((panel) => {
        panel.hidden = panel.id !== panelId;
      });
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
  document.addEventListener("input", (event) => {
    const target = event.target;
    if (!(target instanceof HTMLInputElement)) return;
    const key = target.dataset.opacityControl;
    if (target.dataset.demoSlider === "true") {
      const output = document.getElementById("demo-slider-value");
      if (output) output.textContent = `${target.value}%`;
    }
    if (key !== "frosted" && key !== "mica" && key !== "liquid") return;
    const opacity: Partial<Record<MaterialOpacityKey, number>> = {};
    opacity[key] = Number(target.value) / 100;
    updateAppearance({ opacity }, true);
  });
}

export async function bootstrap(): Promise<void> {
  initializeAppearance();
  installMaterialMotion();
  installLiquidGlassRenderer();
  installWindowControls();
  const client = new LauncherClient(new TauriLauncherTransport());
  const controller = new LauncherController(client, render);
  installInteractions(controller);
  window.addEventListener("unload", () => {
    disposeLiquidGlassRenderer();
    client.stop();
  }, { once: true });
  await controller.start();
}

void bootstrap().catch((error: unknown) => {
  const message = error instanceof Error ? error.message : "launcher startup failed";
  const target = document.getElementById("global-error");
  if (target) target.textContent = message;
});
