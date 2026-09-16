import { setAppearanceAttributes } from "./material.js";
import type {
  AppearancePreferences,
  Material,
  MaterialOpacityKey,
  Theme
} from "../state/types.js";

const APPEARANCE_STORAGE_KEY = "chuzi.appearance";
const MATERIALS: Material[] = ["solid", "frosted", "mica", "liquid"];
const OPACITY_KEYS: MaterialOpacityKey[] = ["frosted", "mica", "liquid"];
const DEFAULT_APPEARANCE: AppearancePreferences = {
  theme: "light",
  material: "solid",
  chromeMaterial: "mica",
  opacity: {
    frosted: 0.86,
    mica: 0.92,
    liquid: 0.78
  }
};
const OPACITY_LIMITS: Record<MaterialOpacityKey, readonly [number, number]> = {
  frosted: [0.6, 0.96],
  mica: [0.75, 1],
  liquid: [0.55, 0.92]
};

export interface AppearancePatch {
  theme?: unknown;
  material?: unknown;
  chromeMaterial?: unknown;
  opacity?: Partial<Record<MaterialOpacityKey, unknown>>;
}

let activeAppearance: AppearancePreferences = cloneAppearance(DEFAULT_APPEARANCE);

function cloneAppearance(value: AppearancePreferences): AppearancePreferences {
  return {
    theme: value.theme,
    material: value.material,
    chromeMaterial: value.chromeMaterial,
    opacity: { ...value.opacity }
  };
}

function validTheme(value: unknown, fallback: Theme = DEFAULT_APPEARANCE.theme): Theme {
  return value === "dark" || value === "light" ? value : fallback;
}

function validMaterial(value: unknown, fallback: Material = DEFAULT_APPEARANCE.material): Material {
  return MATERIALS.includes(value as Material) ? (value as Material) : fallback;
}

function validOpacity(value: unknown, key: MaterialOpacityKey, fallback: number): number {
  const numeric = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(numeric)) return fallback;
  const [minimum, maximum] = OPACITY_LIMITS[key];
  return Math.round(Math.min(maximum, Math.max(minimum, numeric)) * 100) / 100;
}

function normalizeAppearance(value: AppearancePatch = {}): AppearancePreferences {
  const opacity = value.opacity ?? {};
  return {
    theme: validTheme(value.theme),
    material: validMaterial(value.material),
    chromeMaterial: validMaterial(value.chromeMaterial, DEFAULT_APPEARANCE.chromeMaterial),
    opacity: {
      frosted: validOpacity(opacity.frosted, "frosted", DEFAULT_APPEARANCE.opacity.frosted),
      mica: validOpacity(opacity.mica, "mica", DEFAULT_APPEARANCE.opacity.mica),
      liquid: validOpacity(opacity.liquid, "liquid", DEFAULT_APPEARANCE.opacity.liquid)
    }
  };
}

export function readAppearance(): AppearancePreferences {
  try {
    const stored = window.localStorage?.getItem(APPEARANCE_STORAGE_KEY);
    if (!stored) return cloneAppearance(DEFAULT_APPEARANCE);
    const value = JSON.parse(stored) as AppearancePatch & { chromeMaterial?: unknown };
    // Migrate the previous two-field preference by applying its material to
    // both content and the shared window chrome.
    const hasChromeMaterial = Object.prototype.hasOwnProperty.call(value, "chromeMaterial");
    return normalizeAppearance({
      theme: value.theme,
      material: value.material,
      chromeMaterial: hasChromeMaterial ? value.chromeMaterial : value.material,
      opacity: value.opacity
    });
  } catch {
    return cloneAppearance(DEFAULT_APPEARANCE);
  }
}

export function getAppearance(): AppearancePreferences {
  return cloneAppearance(activeAppearance);
}

function opacityKey(value: string | undefined): MaterialOpacityKey | null {
  return value && OPACITY_KEYS.includes(value as MaterialOpacityKey) ? (value as MaterialOpacityKey) : null;
}

function syncAppearanceControls(appearance: AppearancePreferences): void {
  document.querySelectorAll<HTMLElement>("[data-theme-control]").forEach((control) => {
    control.setAttribute("aria-pressed", String(control.dataset.themeControl === appearance.theme));
  });
  document.querySelectorAll<HTMLElement>("[data-material-control]").forEach((control) => {
    control.setAttribute("aria-pressed", String(control.dataset.materialControl === appearance.material));
  });
  document.querySelectorAll<HTMLElement>("[data-chrome-material-control]").forEach((control) => {
    control.setAttribute("aria-pressed", String(control.dataset.chromeMaterialControl === appearance.chromeMaterial));
  });
  document.querySelectorAll<HTMLInputElement>("[data-opacity-control]").forEach((control) => {
    const key = opacityKey(control.dataset.opacityControl);
    if (!key) return;
    const percentage = Math.round(appearance.opacity[key] * 100);
    control.value = String(percentage);
    control.setAttribute("aria-valuenow", String(percentage));
    control.setAttribute("aria-valuetext", `${percentage}%`);
    const output = document.getElementById(`opacity-${key}-value`);
    if (output) output.textContent = `${percentage}%`;
  });
}

export function updateAppearance(changes: AppearancePatch, persist: boolean): void {
  const next = normalizeAppearance({
    theme: changes.theme ?? activeAppearance.theme,
    material: changes.material ?? activeAppearance.material,
    chromeMaterial: changes.chromeMaterial ?? activeAppearance.chromeMaterial,
    opacity: {
      ...activeAppearance.opacity,
      ...(changes.opacity ?? {})
    }
  });
  activeAppearance = next;
  const root = document.documentElement;
  if (!root) return;
  setAppearanceAttributes(root, next);
  window.dispatchEvent(new CustomEvent("chuzi:appearance-change", { detail: next }));
  document.querySelectorAll<HTMLElement>("[data-optical=\"interactive\"]").forEach(clearPointerSurface);
  syncAppearanceControls(next);
  if (persist) {
    try {
      window.localStorage?.setItem(APPEARANCE_STORAGE_KEY, JSON.stringify(next));
    } catch {
      // Local appearance is a best-effort preference and must not block the UI.
    }
  }
}

/** Backward-compatible convenience for the two primary appearance buttons. */
export function applyAppearance(theme: unknown, material: unknown, persist: boolean): void {
  updateAppearance({ theme, material }, persist);
}

export function initializeAppearance(): void {
  const saved = readAppearance();
  let theme = saved.theme;
  try {
    if (!window.localStorage?.getItem(APPEARANCE_STORAGE_KEY) && window.matchMedia?.("(prefers-color-scheme: dark)").matches) {
      theme = "dark";
    }
  } catch {
    // The default Light + Solid combination remains valid without WebView storage.
  }
  updateAppearance({ ...saved, theme }, false);
}

export function clearPointerSurface(surface: HTMLElement): void {
  surface.style.removeProperty("--cz-pointer-x");
  surface.style.removeProperty("--cz-pointer-y");
}
