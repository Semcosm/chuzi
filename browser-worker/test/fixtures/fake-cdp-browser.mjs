#!/usr/bin/env node
import { createServer } from "node:http";
import { writeFileSync } from "node:fs";

const args = process.argv.slice(2);
const portArgument = args.find((arg) => arg.startsWith("--remote-debugging-port="));
const profileArgument = args.find((arg) => arg.startsWith("--user-data-dir="));
const port = Number(portArgument?.split("=", 2)[1]);
const mode = process.env.FAKE_CDP_MODE || "valid";
const profile = profileArgument?.slice("--user-data-dir=".length) || "";
if (!Number.isInteger(port) || port < 1 || !profile) process.exit(2);
if (process.env.FAKE_CDP_PID_FILE) writeFileSync(process.env.FAKE_CDP_PID_FILE, String(process.pid));

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

server.listen(port, "127.0.0.1");
process.on("SIGTERM", () => server.close(() => process.exit(0)));
process.on("SIGINT", () => server.close(() => process.exit(0)));
