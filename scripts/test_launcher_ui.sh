#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cargo fmt --manifest-path "$repo_root/launcher-ui/Cargo.toml" -- --check
cargo test --locked --manifest-path "$repo_root/launcher-ui/Cargo.toml"

if ! command -v node >/dev/null 2>&1; then
  echo "launcher UI contract requires Node.js for the page interaction harness" >&2
  exit 1
fi

awk '/<script>/{inside=1; next} /<\/script>/{inside=0} inside' \
 "$repo_root/launcher-ui/src/ui.html" | node --check
node - "$repo_root/launcher-ui/src/ui.html" <<'NODE'
const fs = require("node:fs");
const vm = require("node:vm");

const htmlPath = process.argv[2];
const html = fs.readFileSync(htmlPath, "utf8");
const match = html.match(/<script>([\s\S]*)<\/script>/);
if (!match) throw new Error("launcher UI script is missing");
assert(html.includes("--cz-color-authored-black"), "semantic CHUZI token layer is missing");
assert(html.includes("--cz-material-transmission"), "material transmission token is missing");
assert(html.includes("--cz-material-solid-base"), "opaque solid base token is missing");
assert(html.includes("data-resolved-material"), "capability-aware material resolver is missing");
assert(html.includes("focus-within"), "optical highlight does not yield to keyboard focus");
assert(html.includes("data-window-inactive"), "optical highlight does not yield to inactive windows");
assert(html.includes("requestAnimationFrame"), "liquid pointer response is not frame-coalesced");
assert(html.includes("outline-color: Highlight"), "forced-colors focus fallback is missing");
for (const legacyColor of ["#087d6d", "#526fc6", "#c96754", "#edf3f0"]) {
  assert(!html.includes(legacyColor), `legacy authored palette remains: ${legacyColor}`);
}

const messages = [];
const elements = new Map();
const root = { dataset: {} };
const storage = new Map();
function element() {
  return {
    hidden: false,
    textContent: "",
    innerHTML: "",
    value: "",
    checked: false,
    classList: { add() {}, remove() {}, toggle() {} }
  };
}
const document = {
  documentElement: root,
  getElementById(id) {
    if (!elements.has(id)) elements.set(id, element());
    return elements.get(id);
  },
  querySelectorAll() { return []; },
  addEventListener() {}
};
const window = {
  ipc: { postMessage(value) { messages.push(JSON.parse(value)); } },
  localStorage: {
    getItem(key) { return storage.has(key) ? storage.get(key) : null; },
    setItem(key, value) { storage.set(key, String(value)); }
  },
  addEventListener() {}
};
const context = { console, document, window, JSON, Math, Date, Map, Number, String };
vm.runInNewContext(match[1], context, { filename: htmlPath });

assert(root.dataset.theme === "light", "appearance did not initialize to light theme");
assert(root.dataset.material === "solid", "appearance did not initialize to solid material");
assert(root.dataset.resolvedMaterial === "solid", "solid material did not resolve to solid");
context.applyAppearance("light", "frosted", false);
assert(root.dataset.material === "frosted", "requested frosted material was not retained");
assert(root.dataset.resolvedMaterial === "mica", "frosted fallback was not reported without backdrop support");
context.applyAppearance("dark", "liquid", true);
assert(root.dataset.theme === "dark" && root.dataset.material === "liquid", "appearance switch was not applied");
assert(root.dataset.resolvedMaterial === "mica", "liquid fallback was not reported without backdrop support");
assert(storage.has("chuzi.appearance"), "appearance selection was not persisted");
window.__CHUZI_CAPABILITIES__ = { backdropFilter: true, compositedOpacity: true, environmentSource: "host-backdrop", transparentWindow: true, dispersion: true };
context.applyAppearance("dark", "liquid", false);
assert(root.dataset.resolvedMaterial === "liquid-basic", "supported liquid material did not resolve to liquid-basic");
assert(root.dataset.environmentSource === "host-backdrop", "host backdrop capability was not retained");
assert(root.dataset.dispersion === "true", "liquid dispersion capability was not enabled for a real backdrop");
window.__CHUZI_CAPABILITIES__ = { backdropFilter: false, compositedOpacity: true, environmentSource: "page" };
context.applyAppearance("dark", "liquid", false);
assert(root.dataset.resolvedMaterial === "mica", "explicitly unavailable backdrop capability was ignored");
delete window.__CHUZI_CAPABILITIES__;
window.matchMedia = (query) => ({ matches: query === "(prefers-reduced-transparency: reduce)" });
context.applyAppearance("dark", "liquid", false);
assert(root.dataset.resolvedMaterial === "mica", "reduced transparency did not choose the stable Mica fallback");
assert(root.dataset.reducedEffects === "true", "reduced transparency did not disable optical effects");
assert(root.dataset.dispersion === "false", "reduced transparency left dispersion enabled");
window.matchMedia = (query) => ({ matches: query === "(prefers-contrast: more)" });
context.applyAppearance("dark", "frosted", false);
assert(root.dataset.resolvedMaterial === "solid", "increased contrast did not choose the opaque fallback");
assert(root.dataset.contrastGuard === "true", "increased contrast did not enable the contrast guard");
window.matchMedia = (query) => ({ matches: query === "(prefers-reduced-motion: reduce)" });
context.applyAppearance("dark", "liquid", false);
assert(root.dataset.reducedMotion === "true", "reduced motion preference was not retained");
assert(root.dataset.dispersion === "false", "reduced motion left dispersion enabled");
delete window.matchMedia;

function assert(condition, message) {
  if (!condition) throw new Error(message);
}
function take(action) {
  const message = messages.shift();
  assert(message && message.action === action, `expected ${action}, got ${message && message.action}`);
  return message;
}
function result(request, data, ok = true, error) {
  window.onLauncherEvent({ protocol: "chuzi.launcher-ui/v1", type: "result", id: request.id, ok, data, error });
}
function finishRefresh() {
  const initialize = take("initialize");
  result(initialize, { first_run: false, components: [] });
  const list = take("component-list");
  result(list, []);
  const settings = take("settings");
  result(settings, { auto_check_updates: false, auto_repair: false, update_channel: "nightly", launch_on_login: false, close_to_tray: false, check_interval: 0 });
  const plugins = take("plugin-list");
  result(plugins, []);
  assert(messages.length === 0, "refresh left unexpected messages");
}

context.refresh();
finishRefresh();

context.refresh();
const initWhileRefresh = take("initialize");
result(initWhileRefresh, { first_run: false, components: [] });
const listWhileRefresh = take("component-list");
result(listWhileRefresh, []);
const settingsWhileRefresh = take("settings");
result(settingsWhileRefresh, { auto_check_updates: false, auto_repair: false, update_channel: "nightly", launch_on_login: false, close_to_tray: false, check_interval: 0 });
assert(messages.length === 1 && messages[0].action === "plugin-list", "refresh completed before plugin state");
context.refresh();
assert(messages.length === 1 && messages[0].action === "plugin-list", "overlapping refresh was not suppressed");
const pluginsWhileRefresh = take("plugin-list");
result(pluginsWhileRefresh, []);
assert(messages.length === 0, "refresh left a pending plugin request");

context.refresh();
const autoInit = take("initialize");
result(autoInit, { first_run: false, components: [] });
const autoList = take("component-list");
result(autoList, []);
const autoSettings = take("settings");
result(autoSettings, { auto_check_updates: true, auto_repair: false, update_channel: "nightly", launch_on_login: false, close_to_tray: false, check_interval: 0 });
const autoPlugins = take("plugin-list");
result(autoPlugins, []);
const autoUpdate = take("check-update");
result(autoUpdate, { available: false, reason: "up_to_date" });
assert(elements.get("update-status").textContent === "Already up to date.", "automatic update check was not rendered");

context.checkUpdate();
context.checkUpdate();
assert(messages.length === 1, "duplicate update checks bypassed the request guard");
const update = take("check-update");
result(update, { available: true, reason: "update_available", manifest: { version: "nightly-200", commit: "abcdef0123456789" } });
assert(elements.get("update-status").textContent.includes("nightly-200"), "update result was not rendered");

context.startAction("plugin-list");
const demoList = take("plugin-list");
result(demoList, [{ descriptor: { id: "demo", version: "1", api: "chuzi.plugin/v1", capabilities: ["probe"], permissions: ["local.test"], signed_by: "test-key", installable: true }, installed: true, enabled: false, trusted: false, health: "untrusted" }]);
assert(elements.get("plugins").innerHTML.includes("plugin-trust"), "untrusted plugin did not expose trust action");

context.startAction("plugin-trust", "demo");
const pluginTrust = take("plugin-trust");
result(pluginTrust, { descriptor: { id: "demo", version: "1", api: "chuzi.plugin/v1", signed_by: "test-key" }, installed: true, enabled: false, trusted: true, health: "disabled" });
finishRefresh();

context.startAction("plugin-enable", "demo");
const pluginEnable = take("plugin-enable");
result(pluginEnable, { descriptor: { id: "demo", version: "1", api: "chuzi.plugin/v1", signed_by: "test-key" }, installed: true, enabled: true, trusted: true, health: "healthy" });
finishRefresh();

context.startAction("plugin-remove", "demo");
const pluginRemove = take("plugin-remove");
result(pluginRemove, null);
finishRefresh();

const first = context.startAction("component-install", "service");
const queued = context.startAction("component-enable", "service");
assert(messages.length === 1, "second operation bypassed the queue");
const install = take("component-install");
assert(install.id === first, "install request id changed");
result(install, { id: "service", installed: true, enabled: true, health: "healthy" });
const enable = take("component-enable");
assert(enable.id === queued, "queued operation was reordered");
result(enable, { id: "service", installed: true, enabled: false, health: "disabled" });
finishRefresh();

context.startAction("repair");
const repair = take("repair");
context.cancelOperation();
const cancel = take("cancel");
assert(cancel.target_id === repair.id, "cancel targeted the wrong operation");
result(cancel, { cancelled: true, target_id: repair.id });
assert(messages.length === 0, "cancel response incorrectly advanced the active operation");
result(repair, null, false, "operation_cancelled");
assert(messages.length === 0, "cancelled operation left queued work");

context.startAction("component-remove", "service");
const failed = take("component-remove");
result(failed, null, false, "launcher_operation_failed");
assert(messages.length === 0, "error response left the UI queue stuck or duplicated");

console.log("launcher UI interaction contract passed");
NODE

echo "launcher UI contract passed"
