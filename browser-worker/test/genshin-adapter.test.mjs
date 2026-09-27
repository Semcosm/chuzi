import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { createReader, readMessage, readType, sendMessage, waitForExit } from "./protocol-harness.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const adapter = resolve(root, "adapters", "genshin-cloudgame", "adapter.mjs");
const manifestPath = resolve(root, "adapters", "genshin-cloudgame", "adapter-manifest.json");
const fakeBrowser = resolve(root, "test", "fixtures", "fake-cdp-browser.mjs");

async function reservePort() {
  const server = createServer();
  await new Promise((resolvePromise, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolvePromise);
  });
  const port = server.address().port;
  await new Promise((resolvePromise) => server.close(resolvePromise));
  return port;
}

async function waitForEndpoint(port) {
  const deadline = Date.now() + 1500;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`);
      if (response.ok) return;
    } catch {
      // The local fixture may still be starting.
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 10));
  }
  throw new Error("fake CDP endpoint did not start");
}

function spawnGenshinAdapter() {
  return spawn(process.execPath, [adapter], {
    stdio: ["pipe", "pipe", "pipe"],
    env: { ...process.env, NODE_OPTIONS: "" },
  });
}

function cleanup(testContext, child, lines, browser) {
  testContext.after(() => {
    lines.close();
    if (child.exitCode === null && child.signalCode === null) child.kill();
    if (browser.exitCode === null && browser.signalCode === null) browser.kill();
  });
}

async function stop(child, lines) {
  const exited = waitForExit(child, lines);
  sendMessage(child, lines.label, { protocol: "chuzi.adapter/v1", id: "shutdown", type: "shutdown" }, lines);
  assert.equal((await readMessage(lines, "shutdown_ack")).type, "shutdown_ack");
  await exited;
  lines.close();
}

test("standalone Genshin adapter exposes its package contract", async (t) => {
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  const child = spawnGenshinAdapter();
  const lines = createReader(child, "genshin-package-hello");
  t.after(() => {
    lines.close();
    if (child.exitCode === null && child.signalCode === null) child.kill();
  });
  sendMessage(child, lines.label, { protocol: "chuzi.adapter/v1", id: "hello", type: "hello" }, lines);
  const hello = await readMessage(lines);
  assert.equal(hello.type, "hello_ack");
  assert.equal(hello.payload.adapter_id, manifest.id);
  assert.equal(hello.payload.api, manifest.api);
  assert.equal(hello.payload.version, manifest.version);
  assert.equal(hello.payload.capabilities, "genshin-cloudgame@1");
  await stop(child, lines);
});

test("standalone Genshin adapter runs session_probe through fake CDP", async (t) => {
  const profile = await mkdtemp(resolve(root, "test", "genshin-package-profile-"));
  const port = await reservePort();
  const targetURLFile = resolve(profile, "target-url.txt");
  const browser = spawn(process.execPath, [fakeBrowser, `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`], {
    stdio: ["ignore", "ignore", "ignore"],
    env: { ...process.env, FAKE_CDP_MODE: "cloudgame-initial-blank", FAKE_CDP_TARGET_URL_FILE: targetURLFile, FAKE_CDP_RESULT_SHAPE: "chromium" },
  });
  await waitForEndpoint(port);
  const child = spawnGenshinAdapter();
  const lines = createReader(child, "genshin-package-operation");
  cleanup(t, child, lines, browser);
  sendMessage(child, lines.label, {
    protocol: "chuzi.adapter/v1", id: "execute", type: "execute",
    payload: {
      session_id: "session-package", account_id: "synthetic-account", request_id: "request-package",
      profile_dir: profile, runtime: "headed-cdp", session_handle: `headed-cdp://127.0.0.1:${port}`,
      operation_id: "operation-package", operation: "genshin.cloudgame.session_probe", parameters: "{}",
    },
  }, lines);
  await readType(lines, "operation_started");
  const succeeded = await readType(lines, "operation_succeeded");
  const facts = JSON.parse(succeeded.payload.facts);
  assert.deepEqual(facts, { platform: "genshin-cloudgame", flow: "authorized-session-check", page: "recognized", shell: "present", session: "authenticated" });
  assert.equal(await readFile(targetURLFile, "utf8"), "https://ys.mihoyo.com/cloud/#/");
  const output = JSON.stringify(succeeded);
  assert.doesNotMatch(output, /cookie|credential|password|token|secret|synthetic-account|genshin-package-profile/u);
  await stop(child, lines);
  await rm(profile, { recursive: true, force: true });
});
