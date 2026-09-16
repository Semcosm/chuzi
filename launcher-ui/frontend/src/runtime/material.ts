import type { AppearancePreferences, Material } from "../state/types.js";

export interface MaterialCapabilities {
  backdropFilter: boolean;
  compositedOpacity: boolean;
  webgl: boolean;
  environmentSource: "page" | "host-backdrop" | "desktop-compositor" | "none";
  transparentWindow: boolean;
  refraction: boolean;
  dispersion: boolean;
  environmentContrastRisk: boolean;
  reduceMotion: boolean;
  reduceTransparency: boolean;
  increaseContrast: boolean;
  forcedColors: boolean;
}

export type HostPlatform = "windows" | "macos" | "linux" | "unknown";

export interface HostCapabilities {
  platform?: HostPlatform;
  titlebar?: "native" | "custom";
  desktopBackdrop?: boolean;
  backdropFilter?: boolean;
  compositedOpacity?: boolean;
  webgl?: boolean;
  environmentSource?: MaterialCapabilities["environmentSource"];
  transparentWindow?: boolean;
  refraction?: boolean;
  dispersion?: boolean;
  environmentContrastRisk?: boolean;
}

export interface ResolvedMaterial {
  name: "solid" | "mica" | "frosted" | "liquid-basic";
  capabilities: MaterialCapabilities;
  reducedEffects?: boolean;
}

const MATERIALS: readonly Material[] = ["solid", "frosted", "mica", "liquid"];

export const LIQUID_CONTROL_SELECTOR =
  "button, select, input[type='range'], .glass-toggle-track, .glass-tabs, .glass-progress, .glass-scroll-demo, .glass-lens-preview";

export interface MaterialSurfaceSync {
  content: ResolvedMaterial;
  chrome: ResolvedMaterial;
}

function isMaterial(value: unknown): value is Material {
  return typeof value === "string" && MATERIALS.includes(value as Material);
}

function hasEnvironmentSource(capabilities: MaterialCapabilities): boolean {
  // `none` means there is no input that a liquid renderer can sample. WebGL
  // support alone is not an environment; it must be paired with a real source.
  return capabilities.environmentSource !== "none";
}

/**
 * Reports whether a liquid request may expose optical effects.
 *
 * `resolveMaterial` intentionally retains the historical `liquid-basic`
 * result for compatibility with the CSS resolver. This second check keeps
 * that result from enabling refraction/dispersion when no environment input
 * exists (for example, a WebGL context with no texture).
 */
export function canRenderLiquid(
  requestedOrResolved: Material | ResolvedMaterial,
  resolvedArgument?: ResolvedMaterial
): boolean {
  const requested = typeof requestedOrResolved === "string" ? requestedOrResolved : "liquid";
  const resolved = typeof requestedOrResolved === "string"
    ? resolvedArgument ?? resolveMaterial(requestedOrResolved)
    : requestedOrResolved;
  if (!resolved) return false;
  return requested === "liquid" &&
    resolved.name === "liquid-basic" &&
    !resolved.reducedEffects &&
    !resolved.capabilities.environmentContrastRisk &&
    !resolved.capabilities.reduceTransparency &&
    !resolved.capabilities.increaseContrast &&
    !resolved.capabilities.forcedColors &&
    hasEnvironmentSource(resolved.capabilities);
}

function canRenderDispersion(requested: Material, resolved: ResolvedMaterial): boolean {
  return canRenderLiquid(requested, resolved) &&
    (resolved.capabilities.dispersion || resolved.capabilities.webgl) &&
    !resolved.capabilities.reduceMotion;
}

function mediaMatches(query: string): boolean {
  try {
    return Boolean(window.matchMedia?.(query).matches);
  } catch {
    return false;
  }
}

export function detectHostCapabilities(): HostCapabilities {
  const host = (window as Window & { __CHUZI_CAPABILITIES__?: HostCapabilities })
    .__CHUZI_CAPABILITIES__;
  return host ?? {};
}

export function detectMaterialCapabilities(): MaterialCapabilities {
  const supports = typeof CSS !== "undefined" && typeof CSS.supports === "function";
  const host = detectHostCapabilities();
  const browserBackdropFilter = Boolean(
    supports &&
      (CSS.supports("backdrop-filter", "blur(1px)") || CSS.supports("-webkit-backdrop-filter", "blur(1px)"))
  );
  const backdropFilter = typeof host.backdropFilter === "boolean" ? host.backdropFilter : browserBackdropFilter;
  let browserWebgl = false;
  try {
    const canvas = document.createElement?.("canvas");
    browserWebgl = Boolean(canvas?.getContext("webgl") || canvas?.getContext("experimental-webgl"));
  } catch {
    browserWebgl = false;
  }
  const validSources = ["page", "host-backdrop", "desktop-compositor", "none"] as const;
  let environmentSource = validSources.includes(host.environmentSource as (typeof validSources)[number])
    ? (host.environmentSource as (typeof validSources)[number])
    : document.body
      ? "page"
      : "none";
  if (environmentSource === "desktop-compositor" && host.transparentWindow !== true) environmentSource = "none";
  return {
    backdropFilter,
    compositedOpacity: host.compositedOpacity !== false,
    webgl: typeof host.webgl === "boolean" ? host.webgl : browserWebgl,
    environmentSource,
    transparentWindow: host.transparentWindow === true,
    refraction: host.refraction === true,
    dispersion: host.dispersion === true,
    environmentContrastRisk: host.environmentContrastRisk === true,
    reduceMotion: mediaMatches("(prefers-reduced-motion: reduce)"),
    reduceTransparency: mediaMatches("(prefers-reduced-transparency: reduce)"),
    increaseContrast: mediaMatches("(prefers-contrast: more)"),
    forcedColors: mediaMatches("(forced-colors: active)")
  };
}

export function resolveMaterial(
  requested: Material,
  capabilities: MaterialCapabilities = detectMaterialCapabilities()
): ResolvedMaterial {
  if (requested === "solid") return { name: "solid", capabilities };
  if (capabilities.forcedColors || capabilities.increaseContrast) {
    return { name: "solid", capabilities, reducedEffects: true };
  }
  if (capabilities.reduceTransparency) return { name: "mica", capabilities, reducedEffects: true };
  if (requested === "mica") return { name: "mica", capabilities };
  if (requested === "frosted") {
    return { name: capabilities.backdropFilter && capabilities.compositedOpacity ? "frosted" : "mica", capabilities };
  }
  if (requested === "liquid") {
    return {
      name: (capabilities.backdropFilter && capabilities.compositedOpacity) || capabilities.webgl ? "liquid-basic" : "mica",
      capabilities
    };
  }
  return { name: "solid", capabilities };
}

function setData(element: HTMLElement, key: string, value: string): void {
  if (element.dataset[key] !== value) element.dataset[key] = value;
}

function queryWithin(root: HTMLElement, selector: string): HTMLElement[] {
  // Tests and embedders may provide a document-like root without the DOM
  // query API. Keep the document fallback for that boundary while using the
  // supplied subtree in the browser.
  if (typeof root.querySelectorAll !== "function") {
    return Array.from(document.querySelectorAll<HTMLElement>(selector));
  }
  const matches = Array.from(root.querySelectorAll<HTMLElement>(selector));
  if (root.matches(selector)) matches.unshift(root);
  return matches;
}

function surfaceIsChrome(surface: HTMLElement): boolean {
  return surface.dataset.surfaceArea === "chrome";
}

function resolveSurfaceRequest(
  surface: HTMLElement,
  appearance: AppearancePreferences
): { requested: Material; source: "default" | "explicit" | "chrome" } {
  if (surfaceIsChrome(surface)) {
    return { requested: appearance.chromeMaterial, source: "chrome" };
  }

  const raw = surface.dataset.surfaceMaterial;
  const bound = surface.dataset.surfaceMaterialBound;
  const source = surface.dataset.surfaceMaterialSource;

  // A value written by this binder is tracked separately from a value authored
  // in markup. If a caller changes the attribute after binding, it becomes an
  // explicit override on the next sync.
  if (source === "explicit" && isMaterial(raw)) {
    return { requested: raw, source: "explicit" };
  }
  if (source === "default" && raw === bound) {
    return { requested: appearance.material, source: "default" };
  }
  if (isMaterial(raw) && raw !== bound) {
    return { requested: raw, source: "explicit" };
  }
  return { requested: appearance.material, source: "default" };
}

/**
 * Resolve every material surface and the liquid-capable controls it contains.
 * This is the only place that translates appearance preferences into DOM
 * material datasets. Renderers consume the datasets and never rewrite them.
 */
export function syncMaterialSurfaces(
  root: HTMLElement,
  appearance: AppearancePreferences,
  resolved = resolveMaterial(appearance.material),
  chromeResolved = resolveMaterial(appearance.chromeMaterial, resolved.capabilities)
): MaterialSurfaceSync {
  const surfaces = queryWithin(root, ".material-surface");
  const bindings = new Map<HTMLElement, { requested: Material; resolved: ResolvedMaterial }>();

  surfaces.forEach((surface) => {
    const request = resolveSurfaceRequest(surface, appearance);
    const surfaceResolved = request.source === "default" && request.requested === appearance.material
      ? resolved
      : request.source === "chrome" && request.requested === appearance.chromeMaterial
        ? chromeResolved
        : resolveMaterial(request.requested, resolved.capabilities);
    const liquidEnabled = canRenderLiquid(request.requested, surfaceResolved);

    setData(surface, "surfaceMaterial", request.requested);
    setData(surface, "surfaceMaterialSource", request.source);
    setData(surface, "surfaceMaterialBound", request.requested);
    setData(surface, "surfaceResolvedMaterial", surfaceResolved.name);
    setData(surface, "surfaceReducedEffects", String(Boolean(surfaceResolved.reducedEffects)));
    setData(surface, "surfaceEnvironmentSource", surfaceResolved.capabilities.environmentSource);
    setData(surface, "surfaceDispersion", String(canRenderDispersion(request.requested, surfaceResolved)));
    setData(surface, "liquidEnhanced", String(liquidEnabled));
    bindings.set(surface, { requested: request.requested, resolved: surfaceResolved });
  });

  const controls = queryWithin(root, LIQUID_CONTROL_SELECTOR);
  controls.forEach((control) => {
    const nearestSurface = control.closest<HTMLElement>(".material-surface");
    const binding = nearestSurface ? bindings.get(nearestSurface) : undefined;
    const requested = binding?.requested ?? appearance.material;
    const controlResolved = binding?.resolved ?? resolved;
    setData(control, "liquidControl", String(canRenderLiquid(requested, controlResolved)));
    setData(control, "controlMaterial", requested);
    setData(control, "controlResolvedMaterial", controlResolved.name);
    setData(control, "controlDispersion", String(canRenderDispersion(requested, controlResolved)));
  });

  const contentLiquid = canRenderLiquid(appearance.material, resolved);
  setData(root, "liquidControls", String(contentLiquid));
  return { content: resolved, chrome: chromeResolved };
}

export function setAppearanceAttributes(root: HTMLElement, appearance: AppearancePreferences): ResolvedMaterial {
  const capabilities = detectMaterialCapabilities();
  const resolved = resolveMaterial(appearance.material, capabilities);
  const host = detectHostCapabilities();
  root.dataset.theme = appearance.theme;
  root.dataset.material = appearance.material;
  root.dataset.chromeMaterial = appearance.chromeMaterial;
  root.dataset.resolvedMaterial = resolved.name;
  root.dataset.environmentSource = resolved.capabilities.environmentSource;
  root.dataset.platform = host.platform ?? "unknown";
  root.dataset.titlebar = host.titlebar ?? "native";
  root.dataset.reducedEffects = String(Boolean(resolved.reducedEffects));
  root.dataset.reducedMotion = String(resolved.capabilities.reduceMotion);
  root.dataset.contrastGuard = String(
    resolved.capabilities.environmentContrastRisk ||
      resolved.capabilities.increaseContrast ||
      resolved.capabilities.forcedColors
  );
  root.dataset.dispersion = String(
    canRenderDispersion(appearance.material, resolved)
  );
  root.style.setProperty("--cz-opacity-frosted", String(appearance.opacity.frosted));
  root.style.setProperty("--cz-opacity-mica", String(appearance.opacity.mica));
  root.style.setProperty("--cz-opacity-liquid", String(appearance.opacity.liquid));

  const chromeResolved = resolveMaterial(appearance.chromeMaterial, capabilities);
  root.dataset.chromeResolvedMaterial = chromeResolved.name;
  root.dataset.chromeDispersion = String(
    canRenderDispersion(appearance.chromeMaterial, chromeResolved)
  );
  syncMaterialSurfaces(root, appearance, resolved, chromeResolved);
  return resolved;
}
