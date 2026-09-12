import assert from "node:assert/strict";
import { once } from "node:events";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { mkdtemp, rm } from "node:fs/promises";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const adapter = resolve(root, "src", "headless-adapter.mjs");
const fakeBrowser = resolve(root, "test", "fixtures", "fake-cdp-browser.mjs");
const testDebug = process.env.CHUZI_TEST_DEBUG === "1";

function readMessage(lines) {
  return lines.nextMessage();
}

function createReader(child, label) {
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  lines.child = child;
  lines.label = label;
  const queue = [];
  const waiters = [];
  let closed = false;
  let closeError;
  const fail = (error) => {
    if (closed) return;
    closed = true;
    closeError = error;
    for (const waiter of waiters.splice(0)) waiter.reject(error);
  };
  lines.on("line", (line) => {
    if (!line.trim()) return;
    let message;
    try {
      message = JSON.parse(line);
    } catch (error) {
      fail(new Error(`${label} emitted invalid JSON: ${error.message}; line=${line}`));
      return;
    }
    if (testDebug) process.stderr.write(`[${label}] <- ${message.type || "unknown"} id=${message.id || "unknown"}\n`);
    const waiter = waiters.shift();
    if (waiter) waiter.resolve(message);
    else queue.push(message);
  });
  child.once("error", (error) => fail(new Error(`${label} child error: ${error.message}`)));
  child.once("exit", (code, signal) => fail(new Error(`${label} child exited before protocol message: code=${code ?? "null"} signal=${signal ?? "null"}`)));
  lines.nextMessage = () => {
    if (queue.length) return Promise.resolve(queue.shift());
    if (closed) return Promise.reject(closeError);
    return new Promise((resolveMessage, rejectMessage) => waiters.push({ resolve: resolveMessage, reject: rejectMessage }));
  };
  child.stderr?.setEncoding("utf8");
  if (testDebug) child.stderr?.on("data", (chunk) => process.stderr.write(`[${label}:stderr] ${chunk}`));
  else child.stderr?.resume();
  return lines;
}

function sendMessage(child, label, message) {
  if (testDebug) process.stderr.write(`[${label}] -> ${message.type || "unknown"} id=${message.id || "unknown"}\n`);
  child.stdin.write(JSON.stringify(message) + "\n");
}

async function readType(lines, type) {
  while (true) {
    const message = await readMessage(lines);
    if (message.type === type) return message;
  }
}

function spawnAdapter(mode = "valid", account = "fake-account-1") {
  return spawn(process.execPath, [
    adapter,
    "--stdio",
    "--browser-command",
    process.execPath,
    "--browser-command-arg",
    fakeBrowser,
    "--cdp-timeout-ms",
    "500",
    "--operation-timeout-ms",
    "1000",
  ], {
    stdio: ["pipe", "pipe", "pipe"],
    env: {
      ...process.env,
      FAKE_CDP_MODE: mode,
      FAKE_CDP_ACCOUNT_ID: account,
      NODE_OPTIONS: "",
    },
  });
}

function waitForExit(child) {
  const exited = once(child, "exit");
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve();
  return exited;
}

function cleanup(testContext, child, lines) {
  testContext.after(() => {
    lines.close();
    if (child.exitCode === null && child.signalCode === null) child.kill();
  });
}

async function stop(child, lines) {
  const exited = waitForExit(child);
  sendMessage(child, "headless-adapter", { protocol: "chuzi.adapter/v1", id: "shutdown-1", type: "shutdown" });
  assert.equal((await readMessage(lines)).type, "shutdown_ack");
  await exited;
  lines.close();
}

function execute(child, payload) {
  sendMessage(child, "headless-adapter", {
    protocol: "chuzi.adapter/v1",
    id: payload.operation_id + "-request",
    type: "execute",
    payload,
  });
}

test("headless-CDP adapter performs a local test-page operation with a fake account", async (t) => {
  const profile = await mkdtemp(resolve(root, "adapter-profile-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const child = spawnAdapter();
  const lines = createReader(child, "headless-adapter");
  cleanup(t, child, lines);

  sendMessage(child, "headless-adapter", { protocol: "chuzi.adapter/v1", id: "hello-1", type: "hello" });
  const hello = await readMessage(lines);
  assert.equal(hello.type, "hello_ack");
  assert.equal(hello.payload.api, "chuzi.adapter/v1");
  assert.match(hello.payload.capabilities, /local\.test-page@1/u);

  execute(child, {
    session_id: "session-1",
    account_id: "fake-account-1",
    request_id: "request-1",
    profile_dir: profile,
    runtime: "headless-cdp",
    operation_id: "operation-1",
    operation: "local.test_page_probe",
    parameters: "null",
  });
  assert.equal((await readType(lines, "operation_started")).payload.operation_id, "operation-1");
  const succeeded = await readType(lines, "operation_succeeded");
  assert.equal(succeeded.payload.operation_id, "operation-1");
  assert.deepEqual(JSON.parse(succeeded.payload.facts), {
    page: "local-test-page",
    marker: "ready",
    account_id: "fake-account-1",
  });
  await stop(child, lines);
});

test("CDP endpoint discovery alone is not a business success", async (t) => {
  const profile = await mkdtemp(resolve(root, "adapter-profile-unknown-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const child = spawnAdapter();
  const lines = createReader(child, "headless-adapter");
  cleanup(t, child, lines);

  execute(child, {
    session_id: "session-2",
    account_id: "fake-account-1",
    request_id: "request-2",
    profile_dir: profile,
    runtime: "headless-cdp",
    operation_id: "operation-2",
    operation: "probe",
    parameters: "{}",
  });
  await readType(lines, "operation_started");
  const failed = await readType(lines, "operation_failed");
  assert.equal(failed.payload.failure_class, "configuration");
  assert.equal(failed.payload.failure_code, "unsupported_operation");
  await stop(child, lines);
});

test("local page marker mismatch is a business failure and cancellation is classified", async (t) => {
  const profile = await mkdtemp(resolve(root, "adapter-profile-marker-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const child = spawnAdapter("marker-missing", "fake-account-1");
  const lines = createReader(child, "headless-adapter");
  cleanup(t, child, lines);

  execute(child, {
    session_id: "session-3",
    account_id: "fake-account-1",
    request_id: "request-3",
    profile_dir: profile,
    runtime: "headless-cdp",
    operation_id: "operation-3",
    operation: "local.test_page_probe",
    parameters: "{}",
  });
  await readType(lines, "operation_started");
  sendMessage(child, "headless-adapter", {
    protocol: "chuzi.adapter/v1",
    id: "cancel-3",
    type: "cancel",
    payload: { operation_id: "operation-3" },
  });
  const cancelled = await readMessage(lines);
  assert.equal(cancelled.id, "cancel-3");
  assert.equal(cancelled.type, "operation_cancelled");
  await readType(lines, "operation_cancelled");
  await stop(child, lines);
});
