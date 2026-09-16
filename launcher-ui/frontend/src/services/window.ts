import { getCurrentWindow } from "@tauri-apps/api/window";

export type WindowAction = "minimize" | "maximize" | "close";
export type ResizeDirection =
  | "East"
  | "North"
  | "NorthEast"
  | "NorthWest"
  | "South"
  | "SouthEast"
  | "SouthWest"
  | "West";

function currentWindow() {
  return getCurrentWindow();
}

export async function performWindowAction(action: WindowAction): Promise<void> {
  const window = currentWindow();
  if (action === "minimize") {
    await window.minimize();
  } else if (action === "maximize") {
    await window.toggleMaximize();
  } else {
    await window.close();
  }
}

export function startWindowDrag(): Promise<void> {
  return currentWindow().startDragging();
}

export function startWindowResize(direction: ResizeDirection): Promise<void> {
  return currentWindow().startResizeDragging(direction);
}

export async function readMaximizedState(): Promise<boolean | null> {
  try {
    return await currentWindow().isMaximized();
  } catch {
    // The static browser fallback has no Tauri window. It remains usable.
    return null;
  }
}
