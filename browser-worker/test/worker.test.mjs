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

function readMessage(lines) {
  return new Promise((resolveMessage, reject) => {
    lines.once("line", (line) => {
      try {
        resolveMessage(JSON.parse(line));
      } catch (error) {
        reject(error);
      }
    });
  });
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

async function stopChild(child, lines) {
  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "shutdown-1", type: "shutdown" })}\n`);
  assert.equal((await readMessage(lines)).type, "shutdown_ack");
  await once(child, "exit");
  lines.close();
}

test("worker performs a versioned handshake and shutdown", async () => {
  const child = spawn(process.execPath, [worker, "--stdio"], { stdio: ["pipe", "pipe", "pipe"] });
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });

  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "hello-1", type: "hello" })}\n`);
  const hello = await readMessage(lines);
  assert.equal(hello.protocol, "v1");
  assert.equal(hello.type, "hello_ack");

  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "shutdown-1", type: "shutdown" })}\n`);
  const shutdown = await readMessage(lines);
  assert.equal(shutdown.type, "shutdown_ack");
  await once(child, "exit");
  lines.close();
});

test("worker exposes session start, cancellation, and shutdown lifecycle", async () => {
  const child = spawn(process.execPath, [worker, "--stdio"], { stdio: ["pipe", "pipe", "pipe"] });
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });

  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "hello-1", type: "hello" })}\n`);
  assert.equal((await readMessage(lines)).type, "hello_ack");

  child.stdin.write(`${JSON.stringify({
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
  })}\n`);
  const started = await readMessage(lines);
  assert.equal(started.id, "session-1");
  assert.equal(started.type, "session_started");
  assert.equal(started.payload.session_id, "session-1");

  child.stdin.write(`${JSON.stringify({
    protocol: "v1",
    id: "cancel-1",
    type: "session_cancel",
    payload: { session_id: "session-1" },
  })}\n`);
  const cancelled = await readMessage(lines);
  assert.equal(cancelled.id, "session-1");
  assert.equal(cancelled.type, "session_cancelled");

  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "shutdown-1", type: "shutdown" })}\n`);
  assert.equal((await readMessage(lines)).type, "shutdown_ack");
  await once(child, "exit");
  lines.close();
});

test("worker reports deferred browser runtime as a classified failure", async () => {
  const child = spawn(process.execPath, [worker, "--stdio"], { stdio: ["pipe", "pipe", "pipe"] });
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });

  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "hello-1", type: "hello" })}\n`);
  assert.equal((await readMessage(lines)).type, "hello_ack");
  child.stdin.write(`${JSON.stringify({
    protocol: "v1",
    id: "session-1",
    type: "session_start",
    payload: {
      session_id: "session-1",
      account_id: "account-1",
      request_id: "request-1",
      profile_dir: "/service-generated/profile",
    },
  })}\n`);
  assert.equal((await readMessage(lines)).type, "session_started");
  const failed = await readMessage(lines);
  assert.equal(failed.type, "session_failed");
  assert.equal(failed.payload.failure, "configuration");

  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "shutdown-1", type: "shutdown" })}\n`);
  assert.equal((await readMessage(lines)).type, "shutdown_ack");
  await once(child, "exit");
  lines.close();
});

test("headless worker discovers a loopback CDP endpoint and exposes a session handle", async (t) => {
  const profile = await mkdtemp(resolve(root, "test-profile-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const child = spawnHeadless("valid");
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  cleanupChild(t, child, lines);
  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "hello-1", type: "hello" })}\n`);
  const hello = await readMessage(lines);
  assert.equal(hello.payload.browserRuntime, "headless-cdp");
  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "session-1", type: "session_start", payload: {
    session_id: "session-1", account_id: "account-1", request_id: "request-1", profile_dir: profile, mode: "success",
  } })}\n`);
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
      const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
      cleanupChild(t, child, lines);
      child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "session-1", type: "session_start", payload: {
        session_id: "session-1", account_id: "account-1", request_id: "request-1", profile_dir: profile,
      } })}\n`);
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
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  cleanupChild(t, child, lines);
  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "session-1", type: "session_start", payload: {
    session_id: "session-1", account_id: "account-1", request_id: "request-1", profile_dir: profile, mode: "hold",
  } })}\n`);
  assert.equal((await readMessage(lines)).type, "session_started");
  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "cancel-1", type: "session_cancel", payload: { session_id: "session-1" } })}\n`);
  const cancelled = await readMessage(lines);
  assert.equal(cancelled.type, "session_cancelled");
  await stopChild(child, lines);
});

test("headless worker rejects caller-provided relative Profile paths", async (t) => {
  const child = spawnHeadless("valid");
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  cleanupChild(t, child, lines);
  child.stdin.write(`${JSON.stringify({ protocol: "v1", id: "session-1", type: "session_start", payload: {
    session_id: "session-1", account_id: "account-1", request_id: "request-1", profile_dir: "./not-allowed",
  } })}\n`);
  const failed = await readMessage(lines);
  assert.equal(failed.type, "session_failed");
  assert.equal(failed.payload.failure, "configuration");
  assert.equal(failed.payload.reason, "profile_path_invalid");
  await stopChild(child, lines);
});
