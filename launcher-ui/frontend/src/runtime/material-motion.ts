import { clearPointerSurface } from "./appearance.js";

const pointerMotion = new WeakMap<HTMLElement, { x: number; y: number; frame?: number; cancelled: boolean }>();

export function installMaterialMotion(): void {
  const writePointer = (surface: HTMLElement, x: number, y: number): void => {
    surface.style.setProperty("--cz-pointer-x", `${x.toFixed(2)}%`);
    surface.style.setProperty("--cz-pointer-y", `${y.toFixed(2)}%`);
  };
  const schedulePointer = (surface: HTMLElement, x: number, y: number): void => {
    const pending = pointerMotion.get(surface) ?? { x, y, cancelled: false };
    pending.x = x;
    pending.y = y;
    pending.cancelled = false;
    pointerMotion.set(surface, pending);
    if (pending.frame !== undefined || typeof window.requestAnimationFrame !== "function") {
      if (typeof window.requestAnimationFrame !== "function") writePointer(surface, x, y);
      return;
    }
    pending.frame = window.requestAnimationFrame(() => {
      pending.frame = undefined;
      if (!pending.cancelled && pointerMotion.get(surface) === pending) {
        writePointer(surface, pending.x, pending.y);
        pointerMotion.delete(surface);
      }
    });
  };
  document.addEventListener("pointermove", (event) => {
    const root = document.documentElement;
    if (!root || root.dataset.material !== "liquid" || root.dataset.reducedEffects === "true" || root.dataset.reducedMotion === "true" || root.dataset.contrastGuard === "true" || root.dataset.windowInactive === "true" || root.dataset.resolvedMaterial === "mica" || root.dataset.resolvedMaterial === "solid") return;
    if (event.pointerType && event.pointerType !== "mouse" && event.pointerType !== "pen") return;
    const target = event.target;
    const surface = target instanceof Element ? target.closest<HTMLElement>("[data-optical=\"interactive\"]") : null;
    if (!surface) return;
    const rect = surface.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    schedulePointer(surface, Math.max(0, Math.min(100, ((event.clientX - rect.left) / rect.width) * 100)), Math.max(0, Math.min(100, ((event.clientY - rect.top) / rect.height) * 100)));
  });
  const clearFromEvent = (event: Event): void => {
    const target = event.target;
    if (target instanceof Element) {
      const surface = target.closest<HTMLElement>("[data-optical=\"interactive\"]");
      if (surface) {
        const pending = pointerMotion.get(surface);
        if (pending) pending.cancelled = true;
        clearPointerSurface(surface);
      }
    }
  };
  document.addEventListener("pointercancel", clearFromEvent);
  document.addEventListener("focusin", clearFromEvent);
  document.addEventListener("pointerout", (event) => {
    const target = event.target;
    const related = event.relatedTarget;
    if (target instanceof Element) {
      const surface = target.closest<HTMLElement>("[data-optical=\"interactive\"]");
      if (surface && (!(related instanceof Node) || !surface.contains(related))) clearFromEvent(event);
    }
  });
  const resetWindow = (inactive: boolean): void => {
    document.documentElement.dataset.windowInactive = String(inactive);
    document.querySelectorAll<HTMLElement>("[data-optical=\"interactive\"]").forEach((surface) => {
      const pending = pointerMotion.get(surface);
      if (pending) pending.cancelled = true;
      clearPointerSurface(surface);
    });
  };
  document.addEventListener("visibilitychange", () => resetWindow(document.visibilityState === "hidden"));
  window.addEventListener("blur", () => resetWindow(true));
  window.addEventListener("focus", () => resetWindow(false));
}
