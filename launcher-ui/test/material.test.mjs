import test from "node:test";
import assert from "node:assert/strict";
import { resolveMaterial } from "../frontend/dist/runtime/material.js";

globalThis.document = { body: {} };
globalThis.CSS = { supports: () => false };
globalThis.window = {
  matchMedia: () => ({ matches: false })
};

test("material resolver preserves requested values while applying capability fallback", () => {
  assert.equal(resolveMaterial("solid").name, "solid");
  assert.equal(resolveMaterial("frosted").name, "mica");
  assert.equal(resolveMaterial("liquid").name, "mica");
});
