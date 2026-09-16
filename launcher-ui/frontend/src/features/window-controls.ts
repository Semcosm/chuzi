import {
  performWindowAction,
  readMaximizedState,
  startWindowDrag,
  startWindowResize,
  type ResizeDirection,
  type WindowAction
} from "../services/window.js";

const WINDOW_ACTIONS = new Set<WindowAction>(["minimize", "maximize", "close"]);
const RESIZE_DIRECTIONS = new Set<ResizeDirection>([
  "East",
  "North",
  "NorthEast",
  "NorthWest",
  "South",
  "SouthEast",
  "SouthWest",
  "West"
]);

function setMaximizedState(root: HTMLElement, state: boolean | null): void {
  if (state !== null) root.dataset.windowMaximized = String(state);
}

export function installWindowControls(): void {
  const root = document.documentElement;
  const updateMaximizedState = (): void => {
    void readMaximizedState().then((state) => setMaximizedState(root, state));
  };

  document.querySelectorAll<HTMLElement>("[data-window-action]").forEach((control) => {
    control.addEventListener("click", (event) => {
      event.preventDefault();
      const action = control.dataset.windowAction;
      if (!action || !WINDOW_ACTIONS.has(action as WindowAction)) return;
      void performWindowAction(action as WindowAction).then(updateMaximizedState).catch(() => undefined);
    });
  });

  document.querySelector<HTMLElement>("[data-window-drag]")?.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    event.preventDefault();
    void startWindowDrag().catch(() => undefined);
  });

  document.querySelector<HTMLElement>("[data-window-drag]")?.addEventListener("dblclick", (event) => {
    if (event.button !== 0) return;
    event.preventDefault();
    void performWindowAction("maximize").then(updateMaximizedState).catch(() => undefined);
  });

  document.querySelectorAll<HTMLElement>("[data-window-resize]").forEach((handle) => {
    handle.addEventListener("pointerdown", (event) => {
      if (event.button !== 0) return;
      const direction = handle.dataset.windowResize;
      if (!direction || !RESIZE_DIRECTIONS.has(direction as ResizeDirection)) return;
      event.preventDefault();
      void startWindowResize(direction as ResizeDirection).catch(() => undefined);
    });
  });

  window.addEventListener("resize", updateMaximizedState);
  updateMaximizedState();
}
