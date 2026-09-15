import type { LauncherClient } from "../services/ipc.js";
import type { AppState, ComponentState, InitializationStatus, LauncherSettings, PluginState, ResultEvent, UpdateInfo } from "../state/types.js";
import { initialState } from "../state/types.js";
import { createStore, type Store } from "../state/store.js";

const REFRESH_ACTIONS = ["initialize", "component-list", "settings", "plugin-list"] as const;

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" ? value as Record<string, unknown> : null;
}

function components(value: unknown): ComponentState[] | null {
  return Array.isArray(value) ? value as ComponentState[] : null;
}

function plugins(value: unknown): PluginState[] | null {
  return Array.isArray(value) ? value as PluginState[] : null;
}

function initialization(value: unknown): InitializationStatus | null {
  const data = record(value);
  return data && typeof data.first_run === "boolean" ? data as InitializationStatus : null;
}

function settings(value: unknown): LauncherSettings | null {
  const data = record(value);
  return data && Object.prototype.hasOwnProperty.call(data, "update_channel") ? data as unknown as LauncherSettings : null;
}

export type RenderState = (state: AppState) => void;

export class LauncherController {
  readonly state: Store<AppState>;

  constructor(private readonly client: LauncherClient, render: RenderState) {
    this.state = createStore(initialState());
    this.state.subscribe(render);
    client.onProgress((event) => {
      this.state.update((current) => ({ ...current, progress: `${event.stage}${event.item ? ` | ${event.item}` : ""}` }));
    });
    client.onResult((event, action) => this.handleResult(event, action));
  }

  async start(): Promise<void> {
    await this.client.start();
    this.refresh();
  }

  refresh(): void {
    if (this.state.get().refreshInFlight) return;
    this.state.update((state) => ({
      ...state,
      refreshInFlight: true,
      autoCheckRequested: false,
      refreshPending: new Set()
    }));
    REFRESH_ACTIONS.forEach((action) => {
      const id = this.send(action);
      this.state.update((state) => {
        const refreshPending = new Set(state.refreshPending);
        refreshPending.add(id);
        return { ...state, refreshPending };
      });
    });
  }

  checkUpdate(): void {
    if (!this.client.hasPendingAction("check-update")) this.send("check-update");
  }

  startAction(action: string, item?: string): void {
    this.send(action, item ? { item } : undefined);
  }

  finishOnboarding(): void {
    const selected = Array.from(document.querySelectorAll<HTMLInputElement>("[data-choice]:checked")).map((input) => input.dataset.choice).filter((id): id is string => Boolean(id));
    this.state.update((state) => ({ ...state, installQueue: selected }));
    this.installNext(selected);
  }

  cancelOperation(): void {
    const operation = this.state.get().currentOperation;
    if (operation) this.send("cancel", { target_id: operation });
  }

  saveSettings(): void {
    const minutes = Math.max(0, Number(document.getElementById("interval") instanceof HTMLInputElement ? (document.getElementById("interval") as HTMLInputElement).value : 0));
    this.send("settings-save", {
      settings: {
        auto_check_updates: (document.getElementById("auto-check") as HTMLInputElement).checked,
        auto_repair: (document.getElementById("auto-repair") as HTMLInputElement).checked,
        update_channel: (document.getElementById("channel") as HTMLSelectElement).value,
        launch_on_login: (document.getElementById("login") as HTMLInputElement).checked,
        close_to_tray: (document.getElementById("tray") as HTMLInputElement).checked,
        check_interval: Math.round(minutes * 60000000000)
      }
    });
  }

  setView(view: AppState["activeView"]): void {
    this.state.update((state) => ({ ...state, activeView: view }));
  }

  private send(action: string, extra?: { item?: string; target_id?: string; settings?: Record<string, unknown> }): string {
    const id = this.client.send(action, extra);
    this.state.update((state) => ({
      ...state,
      currentOperation: this.client.isMutating(action) ? id : state.currentOperation
    }));
    return id;
  }

  private installNext(queue = this.state.get().installQueue): void {
    const [item, ...rest] = queue;
    if (!item) {
      this.state.update((state) => ({ ...state, installQueue: [], installing: null }));
      this.send("initialize-complete");
      return;
    }
    this.state.update((state) => ({ ...state, installQueue: rest, installing: item }));
    this.send("component-install", { item });
  }

  private handleResult(event: ResultEvent, action: string): void {
    const current = this.state.get();
    let next = { ...current };
    if (event.id === current.currentOperation) next.currentOperation = null;
    if (current.refreshPending.has(event.id)) {
      const refreshPending = new Set(current.refreshPending);
      refreshPending.delete(event.id);
      next.refreshPending = refreshPending;
      next.refreshInFlight = refreshPending.size > 0;
    }
    if (!event.ok) {
      if (current.installing && action === "component-install") next = { ...next, installing: null, installQueue: [] };
      this.state.update(() => ({ ...next, error: event.error ?? "launcher operation failed", progress: "" }));
      return;
    }
    next.error = "";
    next.progress = "";
    const data = event.data;
    if (action === "initialize") {
      const info = initialization(data);
      if (info) next = { ...next, initialization: info, components: info.components ?? next.components };
    } else if (action === "component-list") {
      const list = components(data);
      if (list) next.components = list;
    } else if (action === "plugin-list") {
      const list = plugins(data);
      if (list) next.plugins = list;
    } else if (action === "settings") {
      const value = settings(data);
      if (value) {
        next.settings = value;
        const shouldCheckUpdate = value.auto_check_updates && !next.autoCheckRequested;
        if (shouldCheckUpdate) next.autoCheckRequested = true;
        this.state.update(() => next);
        if (shouldCheckUpdate) this.checkUpdate();
        return;
      }
    } else if (action === "check-update") {
      next.update = (record(data) ?? null) as UpdateInfo | null;
    } else if (action === "initialize-complete") {
      this.state.update(() => next);
      if (record(data)?.initialized === true) this.refresh();
      return;
    }
    this.state.update(() => next);
    if (next.installing && action === "component-install") this.installNext();
    else if (this.client.isMutating(action)) this.refresh();
  }
}
