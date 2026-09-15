import { setAppearanceAttributes } from "./material.js";
import type { Material, Theme } from "../state/types.js";

const MATERIALS: Material[] = ["solid", "frosted", "mica", "liquid"];

function validTheme(value: unknown): Theme {
  return value === "dark" ? "dark" : "light";
}

function validMaterial(value: unknown): Material {
  return MATERIALS.includes(value as Material) ? (value as Material) : "solid";
}

export function readAppearance(): { theme: Theme; material: Material } {
  const fallback = { theme: "light" as Theme, material: "solid" as Material };
  try {
    const stored = window.localStorage?.getItem("chuzi.appearance");
    if (!stored) return fallback;
    const value = JSON.parse(stored) as { theme?: unknown; material?: unknown };
    return { theme: validTheme(value?.theme), material: validMaterial(value?.material) };
  } catch {
    return fallback;
  }
}

export function applyAppearance(theme: unknown, material: unknown, persist: boolean): void {
  const nextTheme = validTheme(theme);
  const nextMaterial = validMaterial(material);
  const root = document.documentElement;
  if (!root) return;
  setAppearanceAttributes(root, nextTheme, nextMaterial);
  document.querySelectorAll<HTMLElement>("[data-optical=\"interactive\"]").forEach(clearPointerSurface);
  document.querySelectorAll<HTMLElement>("[data-theme-control]").forEach((control) => {
    control.setAttribute("aria-pressed", String(control.dataset.themeControl === nextTheme));
  });
  document.querySelectorAll<HTMLElement>("[data-material-control]").forEach((control) => {
    control.setAttribute("aria-pressed", String(control.dataset.materialControl === nextMaterial));
  });
  if (persist) {
    try {
      window.localStorage?.setItem("chuzi.appearance", JSON.stringify({ theme: nextTheme, material: nextMaterial }));
    } catch {
      // Local appearance is a best-effort preference and must not block the UI.
    }
  }
}

export function initializeAppearance(): void {
  const saved = readAppearance();
  let theme = saved.theme;
  try {
    if (!window.localStorage?.getItem("chuzi.appearance") && window.matchMedia?.("(prefers-color-scheme: dark)").matches) {
      theme = "dark";
    }
  } catch {
    // The default Light + Solid combination remains valid without WebView storage.
  }
  applyAppearance(theme, saved.material, false);
}

export function clearPointerSurface(surface: HTMLElement): void {
  surface.style.removeProperty("--cz-pointer-x");
  surface.style.removeProperty("--cz-pointer-y");
}
