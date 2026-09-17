import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import {
  createReader,
  readMessage,
  readType,
  sendMessage,
  waitForExit,
} from "./protocol-harness.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const adapter = resolve(root, "src", "headless-adapter.mjs");
const fakeBrowser = resolve(root, "test", "fixtures", "fake-cdp-browser.mjs");

function spawnAdapter(mode = "valid", account = "fake-account-1", extraEnv = {}) {
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
      ...extraEnv,
    },
  });
}

function cleanup(testContext, child, lines) {
  testContext.after(() => {
    lines.close();
    if (child.exitCode === null && child.signalCode === null) child.kill();
  });
}

async function stop(child, lines) {
  const exited = waitForExit(child, lines);
  sendMessage(child, "headless-adapter", { protocol: "chuzi.adapter/v1", id: "shutdown-1", type: "shutdown" });
  assert.equal((await readMessage(lines, "shutdown_ack")).type, "shutdown_ack");
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
  assert.match(hello.payload.capabilities, /genshin-cloudgame@1/u);

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

test("Genshin Cloud Game session probe reports only redacted page observations", async (t) => {
  const profile = await mkdtemp(resolve(root, "adapter-profile-cloudgame-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const targetURLFile = resolve(profile, "target-url.txt");
  const child = spawnAdapter("cloudgame-initial-blank", "fake-account-1", {
    FAKE_CDP_TARGET_URL_FILE: targetURLFile,
    FAKE_CDP_RESULT_SHAPE: "chromium",
  });
  const lines = createReader(child, "headless-adapter-cloudgame");
  cleanup(t, child, lines);

  execute(child, {
    session_id: "session-cloudgame",
    account_id: "authorized-account",
    request_id: "request-cloudgame",
    profile_dir: profile,
    runtime: "headless-cdp",
    operation_id: "operation-cloudgame",
    operation: "genshin.cloudgame.session_probe",
    parameters: "{}",
  });
  await readType(lines, "operation_started");
  const succeeded = await readType(lines, "operation_succeeded");
  const facts = JSON.parse(succeeded.payload.facts);
  assert.deepEqual(facts, {
    platform: "genshin-cloudgame",
    flow: "authorized-session-check",
    page: "recognized",
    shell: "present",
    session: "authenticated",
  });
  assert.equal(await readFile(targetURLFile, "utf8"), "https://ys.mihoyo.com/cloud/#/");
  assert.equal(Object.keys(facts).some((key) => /password|token|cookie|credential|account/u.test(key)), false);
  await stop(child, lines);
});

test("Genshin Cloud Game probe reports a non-authenticated page observation", async (t) => {
  const profile = await mkdtemp(resolve(root, "adapter-profile-cloudgame-auth-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const child = spawnAdapter("cloudgame-unauthenticated");
  const lines = createReader(child, "headless-adapter-cloudgame-auth");
  cleanup(t, child, lines);

  execute(child, {
    session_id: "session-cloudgame-auth",
    account_id: "authorized-account",
    request_id: "request-cloudgame-auth",
    profile_dir: profile,
    runtime: "headless-cdp",
    operation_id: "operation-cloudgame-auth",
    operation: "genshin.cloudgame.session_probe",
    parameters: "null",
  });
  await readType(lines, "operation_started");
  const succeeded = await readType(lines, "operation_succeeded");
  assert.deepEqual(JSON.parse(succeeded.payload.facts), {
    platform: "genshin-cloudgame",
    flow: "authorized-session-check",
    page: "recognized",
    shell: "present",
    session: "not_authenticated",
  });
  await stop(child, lines);
});

test("Genshin Cloud Game probe reports an unrecognized page observation", async (t) => {
  const profile = await mkdtemp(resolve(root, "adapter-profile-cloudgame-shell-"));
  t.after(() => rm(profile, { recursive: true, force: true }));
  const child = spawnAdapter("cloudgame-no-shell");
  const lines = createReader(child, "headless-adapter-cloudgame-shell");
  cleanup(t, child, lines);

  execute(child, {
    session_id: "session-cloudgame-shell",
    account_id: "authorized-account",
    request_id: "request-cloudgame-shell",
    profile_dir: profile,
    runtime: "headless-cdp",
    operation_id: "operation-cloudgame-shell",
    operation: "genshin.cloudgame.session_probe",
    parameters: "{}",
  });
  await readType(lines, "operation_started");
  const succeeded = await readType(lines, "operation_succeeded");
  assert.deepEqual(JSON.parse(succeeded.payload.facts), {
    platform: "genshin-cloudgame",
    flow: "authorized-session-check",
    page: "unrecognized",
    shell: "missing",
    session: "authenticated",
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
