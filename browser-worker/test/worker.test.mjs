import assert from "node:assert/strict";
import { once } from "node:events";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const worker = resolve(root, "src", "worker.mjs");

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
