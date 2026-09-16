import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const testDirectory = path.dirname(fileURLToPath(import.meta.url));
const bundlePath = path.resolve(testDirectory, "../frontend/dist/app.js");

test("launcher entry bundle includes its Tauri runtime dependencies", () => {
  const bundle = fs.readFileSync(bundlePath, "utf8");

  assert.ok(bundle.length > 1000, "launcher entry bundle is unexpectedly small");
  assert.match(bundle, /window\.__TAURI_INTERNALS__\.invoke/);
  assert.doesNotMatch(bundle, /^\s*import\s.*@tauri-apps\/api/m);
  assert.doesNotMatch(bundle, /^\s*import\s/m);
});
