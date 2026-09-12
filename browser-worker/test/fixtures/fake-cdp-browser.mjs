#!/usr/bin/env node
import { createServer } from "node:http";
import { writeFileSync } from "node:fs";
import { createHash } from "node:crypto";

const args = process.argv.slice(2);
const portArgument = args.find((arg) => arg.startsWith("--remote-debugging-port="));
const profileArgument = args.find((arg) => arg.startsWith("--user-data-dir="));
const port = Number(portArgument?.split("=", 2)[1]);
const mode = process.env.FAKE_CDP_MODE || "valid";
const profile = profileArgument?.slice("--user-data-dir=".length) || "";
if (!Number.isInteger(port) || port < 1 || !profile) process.exit(2);
if (process.env.FAKE_CDP_PID_FILE) writeFileSync(process.env.FAKE_CDP_PID_FILE, String(process.pid));

function frame(payload) {
  const body = Buffer.from(JSON.stringify(payload));
  if (body.length < 126) return Buffer.concat([Buffer.from([0x81, body.length]), body]);
  const header = Buffer.alloc(4);
  header[0] = 0x81;
  header[1] = 126;
  header.writeUInt16BE(body.length, 2);
  return Buffer.concat([header, body]);
}

function unframe(buffer) {
  if (buffer.length < 2) return null;
  const second = buffer[1];
  let length = second & 0x7f;
  let offset = 2;
  if (length === 126) {
    if (buffer.length < 4) return null;
    length = buffer.readUInt16BE(2);
    offset = 4;
  }
  const maskOffset = offset + 4;
  if (buffer.length < maskOffset + length) return null;
  const mask = buffer.subarray(offset, maskOffset);
  const payload = Buffer.from(buffer.subarray(maskOffset, maskOffset + length));
  for (let index = 0; index < payload.length; index += 1) payload[index] ^= mask[index % 4];
  return { payload, rest: buffer.subarray(maskOffset + length) };
}

function cdpResult(message) {
  switch (message.method) {
    case "Target.createTarget":
      return { targetId: "fake-target-1" };
    case "Target.attachToTarget":
      return { sessionId: "fake-session-1" };
    case "Runtime.evaluate":
      return {
        result: {
          result: {
            type: "object",
            value: {
              ready: mode !== "marker-missing",
              accountId: process.env.FAKE_CDP_ACCOUNT_ID || "fake-account-1",
              title: "chuzi local test page",
            },
          },
        },
      };
    case "Target.closeTarget":
      return { success: true };
    case "Page.enable":
      return {};
    default:
      return {};
  }
}

const server = createServer((request, response) => {
  if (request.url !== "/json/version") {
    response.writeHead(404).end();
    return;
  }
  if (mode === "timeout") {
    response.writeHead(503).end();
    return;
  }
  const websocket = mode === "invalid"
    ? `ws://127.0.0.1:${port + 1}/devtools/browser/fake`
    : `ws://127.0.0.1:${port}/devtools/browser/fake`;
  response.setHeader("content-type", "application/json");
  response.end(JSON.stringify({ Browser: "FakeChromium/1.0", "Protocol-Version": "1.3", webSocketDebuggerUrl: websocket }));
});

server.on("upgrade", (request, socket) => {
  const key = request.headers["sec-websocket-key"];
  if (request.url !== "/devtools/browser/fake" || typeof key !== "string") {
    socket.destroy();
    return;
  }
  const accept = createHash("sha1")
    .update(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest("base64");
  socket.write("HTTP/1.1 101 Switching Protocols\r\n" +
    "Upgrade: websocket\r\nConnection: Upgrade\r\n" +
    "Sec-WebSocket-Accept: " + accept + "\r\n\r\n");
  let buffer = Buffer.alloc(0);
  socket.on("data", (chunk) => {
    buffer = Buffer.concat([buffer, chunk]);
    while (true) {
      const message = unframe(buffer);
      if (!message) return;
      buffer = message.rest;
      const requestMessage = JSON.parse(message.payload.toString("utf8"));
      socket.write(frame({ id: requestMessage.id, result: cdpResult(requestMessage) }));
    }
  });
  socket.on("error", () => socket.destroy());
});

server.listen(port, "127.0.0.1");
process.on("SIGTERM", () => server.close(() => process.exit(0)));
process.on("SIGINT", () => server.close(() => process.exit(0)));
