import test from "node:test";
import assert from "node:assert/strict";
import { readAppearance } from "../frontend/dist/runtime/appearance.js";

const storage = {
  value: null,
  getItem() {
    return this.value;
  },
  setItem(_key, value) {
    this.value = value;
  }
};

globalThis.window = {
  localStorage: storage,
  matchMedia: () => ({ matches: false })
};

test("appearance defaults to a shared mica chrome recipe", () => {
  storage.value = null;
  assert.deepEqual(readAppearance(), {
    theme: "light",
    material: "solid",
    chromeMaterial: "mica",
    opacity: { frosted: 0.86, mica: 0.92, liquid: 0.78 }
  });
});

test("appearance migrates the legacy material to title bar and sidebar together", () => {
  storage.value = JSON.stringify({ theme: "dark", material: "liquid" });
  assert.equal(readAppearance().chromeMaterial, "liquid");
});

test("appearance clamps each material opacity to its safe range", () => {
  storage.value = JSON.stringify({
    material: "frosted",
    chromeMaterial: "mica",
    opacity: { frosted: 2, mica: 0, liquid: 0.7 }
  });
  assert.deepEqual(readAppearance().opacity, { frosted: 0.96, mica: 0.75, liquid: 0.7 });
});
