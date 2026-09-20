import { cp, mkdir, rm, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const dist = resolve(root, "dist");

await rm(dist, { recursive: true, force: true });
await mkdir(dist, { recursive: true });
await cp(resolve(root, "src"), resolve(dist, "src"), { recursive: true });
await writeFile(
  resolve(dist, "worker-manifest.json"),
  `${JSON.stringify({
    protocol: "v1",
    browserRuntime: "deferred",
    availableBackends: ["deferred", "headless-cdp", "headed-cdp"],
    adapters: [{ id: "chuzi.headless-cdp", api: "chuzi.adapter/v1", entry: "src/headless-adapter.mjs", capabilities: ["cdp@1", "headless-cdp@1", "headed-cdp@1", "browser-view@1", "local.test-page@1", "genshin-cloudgame@1"] }],
  }, null, 2)}\n`,
);
