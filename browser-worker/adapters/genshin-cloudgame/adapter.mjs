import { createInterface } from "node:readline";
import { readFile } from "node:fs/promises";
import { isAbsolute, normalize, sep } from "node:path";
import { CdpSocket, ExternalLifecycle, classified, cancelledError, wait } from "./cdp-runtime.mjs";

const protocol = "chuzi.adapter/v1";
const adapterID = "genshin-cloudgame";
const adapterVersion = await loadAdapterVersion();
const operationName = "genshin.cloudgame.session_probe";
const cloudGameURL = "https://ys.mihoyo.com/cloud/#/";
const pollIntervalMs = 100;
const operationTimeoutMs = 10000;
const operations = new Map();

function send(message) { process.stdout.write(`${JSON.stringify(message)}\n`); }
function reply(request, type, payload = {}) { send({ protocol, id: request.id, type, payload }); }
function protocolError(request, code) { send({ protocol, id: request.id || "unknown", type: "error", error: code }); }
function value(request, key) { const candidate = request.payload?.[key]; return typeof candidate === "string" ? candidate : ""; }
function validProfileDir(value) {
  if (typeof value !== "string" || !isAbsolute(value) || value.split(/[\\/]+/u).includes("..")) return false;
  const normalized = normalize(value);
  return normalized !== sep && normalized !== "";
}
async function loadAdapterVersion() {
  let manifest;
  try {
    manifest = JSON.parse(await readFile(new URL("./adapter-manifest.json", import.meta.url), "utf8"));
  } catch {
    throw new Error("adapter_manifest_unavailable");
  }
  if (!manifest || manifest.format !== "chuzi-adapter/v1" || manifest.api !== protocol || manifest.id !== adapterID || typeof manifest.version !== "string" || !manifest.version.trim()) {
    throw new Error("adapter_manifest_invalid");
  }
  return manifest.version;
}
function validAccountID(value) { return /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/u.test(value); }
function isCdpRuntime(value) { return value === "headless-cdp" || value === "headed-cdp"; }
function parseParameters(request) {
  const raw = value(request, "parameters");
  if (!raw || raw === "null") return {};
  let parsed;
  try { parsed = JSON.parse(raw); } catch { throw classified("configuration", "operation_parameters_invalid"); }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object" || Object.keys(parsed).length !== 0) throw classified("configuration", "operation_parameters_unsupported");
  return parsed;
}
function deadlineFor(request, parentSignal) {
  const requested = value(request, "deadline");
  const timestamp = requested ? Date.parse(requested) : Date.now() + operationTimeoutMs;
  if (!Number.isFinite(timestamp) || timestamp <= Date.now()) throw classified("transient", "operation_deadline_exceeded", true);
  const controller = new AbortController();
  let expired = false;
  const timer = setTimeout(() => { expired = true; controller.abort(); }, Math.min(operationTimeoutMs, timestamp - Date.now()));
  const relay = () => controller.abort();
  parentSignal.addEventListener("abort", relay, { once: true });
  return { signal: controller.signal, get expired() { return expired; }, close() { clearTimeout(timer); parentSignal.removeEventListener("abort", relay); } };
}

async function executeGenshin(session, parameters, signal, lifecycle) {
  if (Object.keys(parameters).length !== 0) throw classified("configuration", "operation_parameters_unsupported");
  if (!isCdpRuntime(session.runtime)) throw classified("configuration", "runtime_mismatch");
  if (!validAccountID(session.accountID)) throw classified("configuration", "account_id_invalid");
  const socket = new CdpSocket(lifecycle.websocketURL, signal);
  let targetID = "";
  try {
    await socket.connect();
    const target = await socket.command("Target.createTarget", { url: cloudGameURL }, "", signal);
    targetID = typeof target.targetId === "string" ? target.targetId : "";
    if (!targetID) throw classified("runtime", "cdp_target_missing", true);
    const attached = await socket.command("Target.attachToTarget", { targetId: targetID, flatten: true }, "", signal);
    const attachedSessionID = typeof attached.sessionId === "string" ? attached.sessionId : "";
    if (!attachedSessionID) throw classified("runtime", "cdp_session_missing", true);
    const deadline = Date.now() + operationTimeoutMs;
    let pageValue;
    while (Date.now() < deadline) {
      const evaluated = await socket.command("Runtime.evaluate", {
        expression: "(() => { const title = document.title || ''; const root = document.querySelector('#app'); const text = document.body?.innerText || ''; const ready = document.readyState === 'complete'; const loggedOut = /(^|\n)登录(\n|$)/u.test(text); const loggedIn = /退出登录/u.test(text); return { ready, loaded: ready && !!root && title !== '', title, shell: !!root, loggedIn, loggedOut }; })()",
        returnByValue: true, awaitPromise: true,
      }, attachedSessionID, signal);
      pageValue = evaluated.result?.value ?? evaluated.result?.result?.value;
      if (pageValue?.loaded === true || (pageValue?.ready === true && String(pageValue?.title || "") !== "")) break;
      await wait(Math.min(pollIntervalMs, Math.max(1, deadline - Date.now())), signal);
    }
    const loaded = pageValue?.loaded === true || (pageValue?.ready === true && String(pageValue?.title || "") !== "");
    if (!loaded) throw classified("transient", "platform_page_load_timeout", true);
    const title = String(pageValue.title || "");
    const recognized = /云[·.・]?原神|Genshin\s+Impact\s*[·.]?\s*Cloud/iu.test(title);
    return { succeeded: true, facts: {
      platform: recognized ? "genshin-cloudgame" : "unknown", flow: "authorized-session-check",
      page: recognized && pageValue.shell === true ? "recognized" : "unrecognized",
      shell: pageValue.shell === true ? "present" : "missing",
      session: pageValue.loggedIn === true && pageValue.loggedOut !== true ? "authenticated" : pageValue.loggedOut === true && pageValue.loggedIn !== true ? "not_authenticated" : "unknown",
    } };
  } finally {
    if (targetID) { try { await socket.command("Target.closeTarget", { targetId: targetID }, "", signal); } catch {} }
    socket.close();
  }
}

async function runOperation(request, task) {
  const session = { sessionID: value(request, "session_id"), accountID: value(request, "account_id"), requestID: value(request, "request_id"), profileDir: value(request, "profile_dir"), runtime: value(request, "runtime"), handle: value(request, "session_handle") };
  let deadline; let lifecycle;
  try {
    if (!session.sessionID || !session.accountID || !session.requestID || !validProfileDir(session.profileDir) || !session.handle) throw classified("configuration", "session_invalid");
    if (value(request, "operation") !== operationName || !value(request, "operation_id")) throw classified("configuration", "unsupported_operation");
    deadline = deadlineFor(request, task.controller.signal);
    reply(request, "operation_started", { operation_id: value(request, "operation_id") });
    lifecycle = new ExternalLifecycle(session.handle, deadline.signal);
    await lifecycle.start();
    const result = await executeGenshin(session, parseParameters(request), deadline.signal, lifecycle);
    reply(request, "operation_succeeded", { operation_id: value(request, "operation_id"), facts: JSON.stringify(result.facts) });
  } catch (error) {
    const failure = error?.failure?.class && error?.failure?.code ? error.failure : { class: "runtime", code: "adapter_failed", retryable: true };
    if (deadline?.expired && failure.class === "cancelled") reply(request, "operation_failed", { operation_id: value(request, "operation_id"), failure_class: "transient", failure_code: "operation_deadline_exceeded", retryable: "true" });
    else if (failure.class === "cancelled" || task.controller.signal.aborted) reply(request, "operation_cancelled", { operation_id: value(request, "operation_id") });
    else reply(request, "operation_failed", { operation_id: value(request, "operation_id"), failure_class: failure.class, failure_code: failure.code, retryable: String(failure.retryable) });
  } finally { deadline?.close(); await lifecycle?.close(); task.done = true; operations.delete(value(request, "operation_id")); }
}
function handleExecute(request) {
  const operationID = value(request, "operation_id");
  if (!operationID || operations.has(operationID)) { protocolError(request, operations.has(operationID) ? "operation_already_running" : "operation_id_required"); return; }
  const task = { controller: new AbortController(), done: false }; operations.set(operationID, task); void runOperation(request, task);
}
function handleCancel(request) { const operationID = value(request, "operation_id"); if (!operationID) { protocolError(request, "operation_id_required"); return; } const task = operations.get(operationID); task?.controller.abort(); reply(request, "operation_cancelled", { operation_id: operationID, already_stopped: task ? "false" : "true" }); }
async function shutdown(request) { for (const task of operations.values()) task.controller.abort(); while ([...operations.values()].some((task) => !task.done)) await new Promise((resolve) => setTimeout(resolve, 5)); reply(request, "shutdown_ack"); setImmediate(() => process.exit(0)); }

const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
process.stdin.resume();
input.on("line", (line) => {
  if (!line.trim()) return;
  let request; try { request = JSON.parse(line); } catch { send({ protocol, id: "unknown", type: "error", error: "invalid_json" }); return; }
  if (request.protocol !== protocol) { protocolError(request, "unsupported_protocol"); return; }
  switch (request.type) {
    case "hello": reply(request, "hello_ack", { adapter_id: adapterID, version: adapterVersion, api: protocol, capabilities: "genshin-cloudgame@1" }); break;
    case "execute": handleExecute(request); break;
    case "cancel": handleCancel(request); break;
    case "shutdown": void shutdown(request); break;
    default: protocolError(request, "unsupported_message_type");
  }
});
