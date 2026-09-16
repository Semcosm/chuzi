import test from "node:test";
import assert from "node:assert/strict";
import {
  canRenderLiquid,
  resolveMaterial,
  syncMaterialSurfaces
} from "../frontend/dist/runtime/material.js";

globalThis.document = { body: {} };
globalThis.CSS = { supports: () => false };
globalThis.window = {
  matchMedia: () => ({ matches: false }),
  __CHUZI_CAPABILITIES__: undefined
};

test("material resolver preserves requested values while applying capability fallback", () => {
  assert.equal(resolveMaterial("solid").name, "solid");
  assert.equal(resolveMaterial("frosted").name, "mica");
  assert.equal(resolveMaterial("liquid").name, "mica");
});

test("material resolver accepts desktop compositor only with a transparent host", () => {
  globalThis.CSS = { supports: () => true };
  globalThis.window.__CHUZI_CAPABILITIES__ = {
    environmentSource: "desktop-compositor",
    transparentWindow: true,
    backdropFilter: true,
    compositedOpacity: true
  };

  const composited = resolveMaterial("liquid");
  assert.equal(composited.name, "liquid-basic");
  assert.equal(composited.capabilities.environmentSource, "desktop-compositor");
  assert.equal(composited.capabilities.transparentWindow, true);

  globalThis.window.__CHUZI_CAPABILITIES__ = {
    environmentSource: "desktop-compositor",
    transparentWindow: false,
    backdropFilter: true,
    compositedOpacity: true
  };
  const unsafe = resolveMaterial("liquid");
  assert.equal(unsafe.capabilities.environmentSource, "none");
  assert.equal(unsafe.name, "liquid-basic");
});

function capabilities(overrides = {}) {
  return {
    backdropFilter: true,
    compositedOpacity: true,
    webgl: true,
    environmentSource: "page",
    transparentWindow: false,
    refraction: true,
    dispersion: true,
    environmentContrastRisk: false,
    reduceMotion: false,
    reduceTransparency: false,
    increaseContrast: false,
    forcedColors: false,
    ...overrides
  };
}

function surface(dataset = {}) {
  const item = { dataset: { ...dataset } };
  item.closest = (selector) => selector === ".material-surface" ? item : null;
  return item;
}

function childControl(parent) {
  const item = { dataset: {} };
  item.closest = (selector) => selector === ".material-surface" ? parent : null;
  return item;
}

test("surface sync preserves explicit content recipes while global material changes", () => {
  const previousDocument = globalThis.document;
  const root = { dataset: {} };
  const defaultSurface = surface();
  const explicitSurface = surface({ surfaceMaterial: "frosted" });
  const chromeSurface = surface({ surfaceArea: "chrome" });
  const controls = [childControl(explicitSurface)];
  const surfaces = [defaultSurface, explicitSurface, chromeSurface];
  globalThis.document = {
    querySelectorAll(selector) {
      return selector === ".material-surface" ? surfaces : controls;
    }
  };

  try {
    const caps = capabilities();
    const solid = resolveMaterial("solid", caps);
    const mica = resolveMaterial("mica", caps);
    syncMaterialSurfaces(root, {
      theme: "light",
      material: "solid",
      chromeMaterial: "mica",
      opacity: { frosted: 0.86, mica: 0.92, liquid: 0.78 }
    }, solid, mica);

    assert.equal(defaultSurface.dataset.surfaceMaterial, "solid");
    assert.equal(explicitSurface.dataset.surfaceMaterial, "frosted");
    assert.equal(chromeSurface.dataset.surfaceMaterial, "mica");
    assert.equal(controls[0].dataset.controlMaterial, "frosted");

    const liquid = resolveMaterial("liquid", caps);
    syncMaterialSurfaces(root, {
      theme: "light",
      material: "liquid",
      chromeMaterial: "solid",
      opacity: { frosted: 0.86, mica: 0.92, liquid: 0.78 }
    }, liquid, solid);

    assert.equal(defaultSurface.dataset.surfaceMaterial, "liquid");
    assert.equal(defaultSurface.dataset.surfaceMaterialSource, "default");
    assert.equal(explicitSurface.dataset.surfaceMaterial, "frosted");
    assert.equal(explicitSurface.dataset.surfaceMaterialSource, "explicit");
    assert.equal(chromeSurface.dataset.surfaceMaterial, "solid");
    assert.equal(controls[0].dataset.controlMaterial, "frosted");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("new surfaces bind through the same resolver as initial surfaces", () => {
  const previousDocument = globalThis.document;
  const root = { dataset: {} };
  const surfaces = [];
  globalThis.document = {
    querySelectorAll(selector) {
      return selector === ".material-surface" ? surfaces : [];
    }
  };

  try {
    const caps = capabilities();
    const appearance = {
      theme: "dark",
      material: "mica",
      chromeMaterial: "frosted",
      opacity: { frosted: 0.86, mica: 0.92, liquid: 0.78 }
    };
    const mica = resolveMaterial("mica", caps);
    const frosted = resolveMaterial("frosted", caps);
    const inserted = surface();
    surfaces.push(inserted);
    syncMaterialSurfaces(root, appearance, mica, frosted);

    assert.equal(inserted.dataset.surfaceMaterial, "mica");
    assert.equal(inserted.dataset.surfaceResolvedMaterial, "mica");
    assert.equal(inserted.dataset.surfaceMaterialSource, "default");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("liquid enhancement stays disabled when no environment source exists", () => {
  const caps = capabilities({ environmentSource: "none" });
  const liquid = resolveMaterial("liquid", caps);
  assert.equal(liquid.name, "liquid-basic");
  assert.equal(canRenderLiquid("liquid", liquid), false);
});
