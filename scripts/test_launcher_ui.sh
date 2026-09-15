#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

npm --prefix "$repo_root/launcher-ui" ci --ignore-scripts
npm --prefix "$repo_root/launcher-ui" run typecheck
npm --prefix "$repo_root/launcher-ui" test

cargo fmt --manifest-path "$repo_root/launcher-ui/Cargo.toml" -- --check
cargo test --locked --manifest-path "$repo_root/launcher-ui/Cargo.toml"

node - "$repo_root/launcher-ui/frontend/src/index.html" "$repo_root/launcher-ui/frontend/src" <<'NODE'
const fs = require("node:fs");
const path = require("node:path");

const htmlPath = process.argv[2];
const sourceRoot = process.argv[3];
const html = fs.readFileSync(htmlPath, "utf8");
const requiredFiles = [
  "app.ts",
  "app/bootstrap.ts",
  "components/component-row.ts",
  "features/launcher-controller.ts",
  "runtime/material.ts",
  "services/ipc.ts",
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
console.log("launcher UI module contract passed");
NODE

echo "launcher UI contract passed"
