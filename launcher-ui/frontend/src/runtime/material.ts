import type { Material, Theme } from "../state/types.js";

export interface MaterialCapabilities {
  backdropFilter: boolean;
  compositedOpacity: boolean;
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

export interface ResolvedMaterial {
  name: "solid" | "mica" | "frosted" | "liquid-basic";
  capabilities: MaterialCapabilities;
  reducedEffects?: boolean;
}

function mediaMatches(query: string): boolean {
  try {
    return Boolean(window.matchMedia?.(query).matches);
  } catch {
    return false;
  }
}

export function detectMaterialCapabilities(): MaterialCapabilities {
  const supports = typeof CSS !== "undefined" && typeof CSS.supports === "function";
  const host = (window as Window & { __CHUZI_CAPABILITIES__?: Partial<MaterialCapabilities> })
    .__CHUZI_CAPABILITIES__ ?? {};
  const browserBackdropFilter = Boolean(
    supports &&
      (CSS.supports("backdrop-filter", "blur(1px)") || CSS.supports("-webkit-backdrop-filter", "blur(1px)"))
  );
  const backdropFilter = typeof host.backdropFilter === "boolean" ? host.backdropFilter : browserBackdropFilter;
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
      name: capabilities.backdropFilter && capabilities.compositedOpacity ? "liquid-basic" : "mica",
      capabilities
    };
  }
  return { name: "solid", capabilities };
}

export function setAppearanceAttributes(root: HTMLElement, theme: Theme, material: Material): ResolvedMaterial {
  const resolved = resolveMaterial(material);
  root.dataset.theme = theme;
  root.dataset.material = material;
  root.dataset.resolvedMaterial = resolved.name;
  root.dataset.environmentSource = resolved.capabilities.environmentSource;
  root.dataset.reducedEffects = String(Boolean(resolved.reducedEffects));
  root.dataset.reducedMotion = String(resolved.capabilities.reduceMotion);
  root.dataset.contrastGuard = String(
    resolved.capabilities.environmentContrastRisk ||
      resolved.capabilities.increaseContrast ||
      resolved.capabilities.forcedColors
  );
  root.dataset.dispersion = String(
    material === "liquid" &&
      resolved.name === "liquid-basic" &&
      resolved.capabilities.dispersion &&
      resolved.capabilities.environmentSource !== "none" &&
      !resolved.capabilities.reduceMotion &&
      !resolved.capabilities.reduceTransparency &&
      !resolved.capabilities.increaseContrast &&
      !resolved.capabilities.forcedColors &&
      !resolved.capabilities.environmentContrastRisk
  );
  return resolved;
}
