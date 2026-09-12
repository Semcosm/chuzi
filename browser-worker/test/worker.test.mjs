import assert from "node:assert/strict";
import { once } from "node:events";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { mkdtemp, rm } from "node:fs/promises";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const worker = resolve(root, "src", "worker.mjs");
const headlessWorker = resolve(root, "src", "headless.mjs");
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

function spawnHeadless(mode = "valid", timeoutMs = "1000") {
  return spawn(process.execPath, [
    headlessWorker,
    "--stdio",
    "--browser-command",
    process.execPath,
    "--browser-command-arg",
    fakeBrowser,
    "--cdp-timeout-ms",
    timeoutMs,
  ], {
    stdio: ["pipe", "pipe", "pipe"],
    env: { ...process.env, FAKE_CDP_MODE: mode, NODE_OPTIONS: "", CHUZI_FAKE_BROWSER_SCRIPT: fakeBrowser },
  });
}

function cleanupChild(testContext, child, lines) {
  testContext.after(() => {
    lines.close();
    if (child.exitCode === null && child.signalCode === null) child.kill();
  });
}

function waitForExit(child) {
  const exited = once(child, "exit");
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve();
  return exited;
}

async function stopChild(child, lines) {
  const exited = waitForExit(child);
  sendMessage(child, "worker", { protocol: "v1", id: "shutdown-1", type: "shutdown" });
  assert.equal((await readMessage(lines)).type, "shutdown_ack");
  await exited;
  lines.close();
}

test("worker performs a versioned handshake and shutdown", async () => {
  const child = spawn(process.execPath, [worker, "--stdio"], { stdio: ["pipe", "pipe", "pipe"] });
  const lines = createReader(child, "worker");

  sendMessage(child, "worker", { protocol: "v1", id: "hello-1", type: "hello" });
  const hello = await readMessage(lines);
  assert.equal(hello.protocol, "v1");
  assert.equal(hello.type, "hello_ack");

  sendMessage(child, "worker", { protocol: "v1", id: "shutdown-1", type: "shutdown" });
  const shutdown = await readMessage(lines);
  assert.equal(shutdown.type, "shutdown_ack");
  await waitForExit(child);
  lines.close();
});

test("worker exposes session start, cancellation, and shutdown lifecycle", async () => {
  const child = spawn(process.execPath, [worker, "--stdio"], { stdio: ["pipe", "pipe", "pipe"] });
  const lines = createReader(child, "worker");

  sendMessage(child, "worker", { protocol: "v1", id: "hello-1", type: "hello" });
  assert.equal((await readMessage(lines)).type, "hello_ack");

  sendMessage(child, "worker", {
    protocol: "v1",
    id: "session-1",
    type: "session_start",
    payload: {
      session_id: "session-1",
      account_id: "account-1",
      request_id: "request-1",
      profile_dir: "/service-generated/profile",
      mode: "hold",
    },
  });
  const started = await readMessage(lines);
  assert.equal(started.id, "session-1");
  assert.equal(started.type, "session_started");
  assert.equal(started.payload.session_id, "session-1");

  sendMessage(child, "worker", {
    protocol: "v1",
    id: "cancel-1",
    type: "session_cancel",
    payload: { session_id: "session-1" },
  });
  const cancelled = await readMessage(lines);
  assert.equal(cancelled.id, "session-1");
  assert.equal(cancelled.type, "session_cancelled");

  sendMessage(child, "worker", { protocol: "v1", id: "shutdown-1", type: "shutdown" });
  assert.equal((await readMessage(lines)).type, "shutdown_ack");
  await waitForExit(child);
  lines.close();
});

test("worker reports deferred browser runtime as a classified failure", async () => {
  const child = spawn(process.execPath, [worker, "--stdio"], { stdio: ["pipe", "pipe", "pipe"] });
  const lines = createReader(child, "worker");

  sendMessage(child, "worker", { protocol: "v1", id: "hello-1", type: "hello" });
  assert.equal((await readMessage(lines)).type, "hello_ack");
  sendMessage(child, "worker", {
    protocol: "v1",
    id: "session-1",
    type: "session_start",
    payload: {
      session_id: "session-1",
      account_id: "account-1",
      request_id: "request-1",
      profile_dir: "/service-generated/profile",
    },
  });
  assert.equal((await readMessage(lines)).type, "session_started");
  const failed = await readMessage(lines);
  assert.equal(failed.type, "session_failed");
  assert.equal(failed.payload.failure, "configuration");

  sendMessage(child, "worker", { protocol: "v1", id: "shutdown-1", type: "shutdown" });
  assert.equal((await readMessage(lines)).type, "shutdown_ack");
  await waitForExit(child);
  lines.close();
});

test("headless worker discovers a loopback CDP endpoint and exposes a session handle", async (t) => {
  const profile = await mkdtemp(resolve(root, "test-profile-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const child = spawnHeadless("valid");
  const lines = createReader(child, "headless-worker");
  cleanupChild(t, child, lines);
  sendMessage(child, "headless-worker", { protocol: "v1", id: "hello-1", type: "hello" });
  const hello = await readMessage(lines);
  assert.equal(hello.payload.browserRuntime, "headless-cdp");
  sendMessage(child, "headless-worker", { protocol: "v1", id: "session-1", type: "session_start", payload: {
    session_id: "session-1", account_id: "account-1", request_id: "request-1", profile_dir: profile, mode: "success",
  } });
  const started = await readMessage(lines);
  assert.equal(started.type, "session_started");
  assert.equal(started.payload.runtime, "headless-cdp");
  assert.equal(started.payload.session_handle, "session-1");
  assert.equal(started.payload.cdp_host, "127.0.0.1");
  assert.match(started.payload.cdp_port, /^\d+$/u);
  assert.equal(Object.hasOwn(started.payload, "cdp_url"), false);
  assert.equal((await readMessage(lines)).type, "session_succeeded");
  await stopChild(child, lines);
});

test("headless worker fails closed for invalid or unavailable CDP endpoints", async (t) => {
  for (const [mode, expectedReason] of [["invalid", "cdp_endpoint_invalid"], ["timeout", "cdp_endpoint_timeout"]]) {
    await t.test(mode, async () => {
      const profile = await mkdtemp(resolve(root, `test-profile-${mode}-`));
      t.after(() => rm(profile, { recursive: true, force: true }));
      const child = spawnHeadless(mode, "250");
      const lines = createReader(child, `headless-worker-${mode}`);
      cleanupChild(t, child, lines);
      sendMessage(child, `headless-worker-${mode}`, { protocol: "v1", id: "session-1", type: "session_start", payload: {
        session_id: "session-1", account_id: "account-1", request_id: "request-1", profile_dir: profile,
      } });
      const failed = await readMessage(lines);
      assert.equal(failed.type, "session_failed");
      assert.equal(failed.payload.failure, mode === "invalid" ? "configuration" : "transient");
      assert.equal(failed.payload.reason, expectedReason);
      await stopChild(child, lines);
    });
  }
});

test("headless worker cancellation terminates the external browser", async (t) => {
  const profile = await mkdtemp(resolve(root, "test-profile-hold-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const child = spawnHeadless("valid");
  const lines = createReader(child, "headless-worker");
  cleanupChild(t, child, lines);
  sendMessage(child, "headless-worker", { protocol: "v1", id: "session-1", type: "session_start", payload: {
    session_id: "session-1", account_id: "account-1", request_id: "request-1", profile_dir: profile, mode: "hold",
  } });
  assert.equal((await readMessage(lines)).type, "session_started");
  sendMessage(child, "headless-worker", { protocol: "v1", id: "cancel-1", type: "session_cancel", payload: { session_id: "session-1" } });
  const cancelled = await readMessage(lines);
  assert.equal(cancelled.type, "session_cancelled");
  await stopChild(child, lines);
});

test("headless worker rejects caller-provided relative Profile paths", async (t) => {
  const child = spawnHeadless("valid");
  const lines = createReader(child, "headless-worker");
  cleanupChild(t, child, lines);
  sendMessage(child, "headless-worker", { protocol: "v1", id: "session-1", type: "session_start", payload: {
    session_id: "session-1", account_id: "account-1", request_id: "request-1", profile_dir: "./not-allowed",
  } });
  const failed = await readMessage(lines);
  assert.equal(failed.type, "session_failed");
  assert.equal(failed.payload.failure, "configuration");
  assert.equal(failed.payload.reason, "profile_path_invalid");
  await stopChild(child, lines);
});
