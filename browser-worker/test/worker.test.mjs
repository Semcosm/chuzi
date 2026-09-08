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
