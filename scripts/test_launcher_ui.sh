#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

npm --prefix "$repo_root/launcher-ui" ci --ignore-scripts
npm --prefix "$repo_root/launcher-ui" run typecheck
npm --prefix "$repo_root/launcher-ui" test

cargo fmt --manifest-path "$repo_root/launcher-ui/Cargo.toml" -- --check
cargo test --locked --manifest-path "$repo_root/launcher-ui/Cargo.toml"

node - "$repo_root/launcher-ui/frontend/src/index.html" "$repo_root/launcher-ui/frontend/src" "$repo_root" <<'NODE'
const fs = require("node:fs");
const path = require("node:path");

const htmlPath = process.argv[2];
const sourceRoot = process.argv[3];
const repoRoot = process.argv[4];
const html = fs.readFileSync(htmlPath, "utf8");
const componentsCss = fs.readFileSync(path.join(sourceRoot, "design/components.css"), "utf8");
const requiredFiles = [
  "app.ts",
  "app/bootstrap.ts",
  "components/component-row.ts",
  "features/launcher-controller.ts",
  "features/window-controls.ts",
  "runtime/liquid-glass.ts",
  "runtime/material.ts",
  "services/ipc.ts",
  "services/window.ts",
  "state/store.ts",
  "views/overview-view.ts",
  "design/tokens.css",
  "design/materials.css",
  "design/components.css"
];

for (const file of requiredFiles) {
  if (!fs.existsSync(path.join(sourceRoot, file))) throw new Error(`launcher UI module is missing: ${file}`);
}
if (!html.includes('src="./app.js"')) throw new Error("launcher UI does not use the compiled app entrypoint");
if (!html.includes("./design/tokens.css")) throw new Error("semantic token layer is missing");
if (!html.includes("./design/materials.css")) throw new Error("material layer is missing");
if (!html.includes("./design/components.css")) throw new Error("component layer is missing");
if (html.includes("<script>")) throw new Error("launcher UI still contains an inline script");
if (!html.includes('data-window-action="close"')) throw new Error("custom titlebar controls are missing");
if (!html.includes('data-window-resize="SouthEast"')) throw new Error("window resize handles are missing");
if ((html.match(/data-surface-area="chrome"/g) ?? []).length < 2) throw new Error("titlebar and sidebar must share the chrome surface");
if (!html.includes('data-chrome-material-control="liquid"')) throw new Error("shared chrome material control is missing");
for (const material of ["frosted", "mica", "liquid"]) {
  if (!html.includes(`data-opacity-control="${material}"`)) throw new Error(`opacity control is missing: ${material}`);
}
if (!/html, body\s*\{[\s\S]*?overflow:\s*hidden/.test(componentsCss)) throw new Error("document scrolling must be locked");
if (!/\.app\s*\{[\s\S]*?overflow:\s*hidden/.test(componentsCss)) throw new Error("app shell must contain scrolling");
if (!/\.content\s*\{[\s\S]*?overflow-y:\s*auto/.test(componentsCss)) throw new Error("content must own vertical scrolling");
if (!/\.liquid-glass-canvas\s*\{[\s\S]*?position:\s*fixed/.test(componentsCss)) throw new Error("liquid glass canvas must be viewport-fixed");
if (!componentsCss.includes('data-liquid-renderer="webgl1"')) throw new Error("liquid renderer styling is missing");

const config = JSON.parse(fs.readFileSync(path.join(repoRoot, "launcher-ui/tauri.conf.json"), "utf8"));
const mainWindow = config.app?.windows?.find((window) => window.label === "main");
if (mainWindow?.create !== false) throw new Error("main window must be created by the Tauri setup hook");
if (mainWindow.transparent !== true || mainWindow.decorations !== false) {
  throw new Error("main window must stay transparent and undecorated");
}
console.log("launcher UI module contract passed");
NODE

echo "launcher UI contract passed"
