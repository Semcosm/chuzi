import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";

import { PROTOCOL, type ProgressEvent, type ResultEvent } from "../state/types.js";

export interface UiRequest {
  protocol: typeof PROTOCOL;
  id: string;
  action: string;
  item?: string;
  target_id?: string;
  settings?: Record<string, unknown>;
}

export interface LauncherTransport {
  request(request: UiRequest): Promise<ResultEvent>;
  onProgress(listener: (event: ProgressEvent) => void): Promise<UnlistenFn>;
}

export class TauriLauncherTransport implements LauncherTransport {
  request(request: UiRequest): Promise<ResultEvent> {
    return invoke<ResultEvent>("launcher_request", { request });
  }

  onProgress(listener: (event: ProgressEvent) => void): Promise<UnlistenFn> {
    return listen<ProgressEvent>("launcher-progress", (event) => listener(event.payload));
  }
}

export type ResultListener = (event: ResultEvent, action: string) => void;
export type ProgressListener = (event: ProgressEvent) => void;

interface PendingRequest {
  request: UiRequest;
  action: string;
}

const MUTATING_ACTIONS = new Set([
  "component-install",
  "component-remove",
  "component-enable",
  "component-disable",
  "plugin-install",
  "plugin-remove",
  "plugin-enable",
  "plugin-disable",
  "plugin-trust",
  "plugin-untrust",
  "repair"
]);

export class LauncherClient {
  private readonly queue: PendingRequest[] = [];
  private readonly pending = new Map<string, PendingRequest>();
  private readonly resultListeners = new Set<ResultListener>();
  private readonly progressListeners = new Set<ProgressListener>();
  private active: PendingRequest | null = null;
  private sequence = 0;
  private progressUnlisten: UnlistenFn | null = null;

  constructor(private readonly transport: LauncherTransport) {}

  async start(): Promise<void> {
    this.progressUnlisten = await this.transport.onProgress((event) => {
      this.progressListeners.forEach((listener) => listener(event));
    });
  }

  stop(): void {
    this.progressUnlisten?.();
    this.progressUnlisten = null;
  }

  onResult(listener: ResultListener): () => void {
    this.resultListeners.add(listener);
    return () => this.resultListeners.delete(listener);
  }

  onProgress(listener: ProgressListener): () => void {
    this.progressListeners.add(listener);
    return () => this.progressListeners.delete(listener);
  }

  send(action: string, extra: Omit<UiRequest, "protocol" | "id" | "action"> = {}): string {
    const id = `${action}-${Date.now()}-${this.sequence++}`;
    const request: UiRequest = { protocol: PROTOCOL, id, action, ...extra };
    const pending = { request, action };
    this.pending.set(id, pending);
    if (action === "cancel") {
      void this.dispatch(pending);
    } else {
      this.queue.push(pending);
      this.pump();
    }
    return id;
  }

  hasPendingAction(action: string): boolean {
    return [...this.pending.values()].some((pending) => pending.action === action);
  }

  isMutating(action: string): boolean {
    return MUTATING_ACTIONS.has(action);
  }

  actionFor(id: string): string | undefined {
    return this.pending.get(id)?.action;
  }

  private pump(): void {
    if (this.active || this.queue.length === 0) return;
    this.active = this.queue.shift() ?? null;
    if (this.active) void this.dispatch(this.active);
  }

  private async dispatch(pending: PendingRequest): Promise<void> {
    let result: ResultEvent;
    try {
      result = await this.transport.request(pending.request);
    } catch (error) {
      result = {
        protocol: PROTOCOL,
        type: "result",
        id: pending.request.id,
        ok: false,
        error: error instanceof Error ? error.message : "launcher process failed"
      };
    }
    this.pending.delete(pending.request.id);
    if (this.active?.request.id === pending.request.id) this.active = null;
    this.resultListeners.forEach((listener) => listener(result, pending.action));
    this.pump();
  }
}
