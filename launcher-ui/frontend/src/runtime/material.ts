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

function canUseLiquid(requested: Material, resolved: ResolvedMaterial): boolean {
  return requested === "liquid" &&
    resolved.name === "liquid-basic" &&
    !resolved.reducedEffects &&
    !resolved.capabilities.environmentContrastRisk;
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

export function resolveMaterial(requested: Material): ResolvedMaterial {
  const capabilities = detectMaterialCapabilities();
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

export function setAppearanceAttributes(root: HTMLElement, appearance: AppearancePreferences): ResolvedMaterial {
  const resolved = resolveMaterial(appearance.material);
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
    canUseLiquid(appearance.material, resolved) &&
      (resolved.capabilities.dispersion || resolved.capabilities.webgl) &&
      !resolved.capabilities.reduceMotion &&
      !resolved.capabilities.reduceTransparency &&
      !resolved.capabilities.increaseContrast &&
      !resolved.capabilities.forcedColors &&
      !resolved.capabilities.environmentContrastRisk
  );
  root.style.setProperty("--cz-opacity-frosted", String(appearance.opacity.frosted));
  root.style.setProperty("--cz-opacity-mica", String(appearance.opacity.mica));
  root.style.setProperty("--cz-opacity-liquid", String(appearance.opacity.liquid));

  const chromeResolved = resolveMaterial(appearance.chromeMaterial);
  root.dataset.chromeResolvedMaterial = chromeResolved.name;
  root.dataset.chromeDispersion = String(
    canUseLiquid(appearance.chromeMaterial, chromeResolved) &&
      (chromeResolved.capabilities.dispersion || chromeResolved.capabilities.webgl) &&
      !chromeResolved.capabilities.reduceMotion &&
      !chromeResolved.capabilities.reduceTransparency &&
      !chromeResolved.capabilities.increaseContrast &&
      !chromeResolved.capabilities.forcedColors
  );
  document.querySelectorAll<HTMLElement>(".material-surface").forEach((surface) => {
    const isChrome = surface.dataset.surfaceArea === "chrome";
    const requested = isChrome ? appearance.chromeMaterial : appearance.material;
    const surfaceResolved = isChrome ? chromeResolved : resolved;
    const liquidEnabled = canUseLiquid(requested, surfaceResolved);
    surface.dataset.surfaceMaterial = requested;
    surface.dataset.surfaceResolvedMaterial = surfaceResolved.name;
    surface.dataset.surfaceReducedEffects = String(Boolean(surfaceResolved.reducedEffects));
    surface.dataset.surfaceEnvironmentSource = surfaceResolved.capabilities.environmentSource;
    surface.dataset.liquidEnhanced = String(liquidEnabled);
  });
  const controlLiquid = canUseLiquid(appearance.material, resolved);
  root.dataset.liquidControls = String(controlLiquid);
  document.querySelectorAll<HTMLElement>(
    "button, select, input[type='range'], .glass-toggle-track, .glass-tabs, .glass-progress, .glass-scroll-demo, .glass-lens-preview"
  ).forEach((control) => {
    const chromeAncestor = control.closest<HTMLElement>('[data-surface-area="chrome"]');
    const controlResolved = chromeAncestor ? chromeResolved : resolved;
    const controlRequested = chromeAncestor ? appearance.chromeMaterial : appearance.material;
    control.dataset.liquidControl = String(
      canUseLiquid(controlRequested, controlResolved)
    );
  });
  return resolved;
}
