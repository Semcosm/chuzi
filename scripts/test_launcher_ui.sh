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

const messages = [];
const elements = new Map();
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
  getElementById(id) {
    if (!elements.has(id)) elements.set(id, element());
    return elements.get(id);
  },
  querySelectorAll() { return []; },
  addEventListener() {}
};
const window = {
  ipc: { postMessage(value) { messages.push(JSON.parse(value)); } },
  addEventListener() {}
};
const context = { console, document, window, JSON, Math, Date, Map, Number, String };
vm.runInNewContext(match[1], context, { filename: htmlPath });

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
