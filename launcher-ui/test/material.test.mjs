import test from "node:test";
import assert from "node:assert/strict";
import { resolveMaterial } from "../frontend/dist/runtime/material.js";

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
