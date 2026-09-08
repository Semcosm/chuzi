import { createInterface } from "node:readline";

const protocolVersion = "v1";
const capabilities = ["protocol.v1", "browser-runtime.contract"];
const input = createInterface({ input: process.stdin, crlfDelay: Infinity });

function send(message) {
  process.stdout.write(`${JSON.stringify(message)}\n`);
}

function reply(request, type, payload = {}) {
  send({
    protocol: protocolVersion,
    id: request.id,
    type,
    payload,
  });
}

input.on("line", (line) => {
  if (!line.trim()) return;

  let request;
  try {
    request = JSON.parse(line);
  } catch {
    send({ protocol: protocolVersion, id: "unknown", type: "error", error: "invalid JSON" });
    return;
  }

  if (request.protocol !== protocolVersion) {
    send({
      protocol: protocolVersion,
      id: request.id ?? "unknown",
      type: "error",
      error: `unsupported protocol: ${request.protocol ?? "missing"}`,
    });
    return;
  }

  switch (request.type) {
    case "hello":
      reply(request, "hello_ack", {
        service: "chuzi-browser-worker",
        browserRuntime: "deferred",
        capabilities: capabilities.join(","),
      });
      break;
    case "ping":
      reply(request, "pong");
      break;
    case "shutdown":
      reply(request, "shutdown_ack");
      setImmediate(() => process.exit(0));
      break;
    default:
      send({
        protocol: protocolVersion,
        id: request.id ?? "unknown",
        type: "error",
        error: `unsupported message type: ${request.type ?? "missing"}`,
      });
  }
});
