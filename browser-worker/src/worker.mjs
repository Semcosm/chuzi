import { createInterface } from "node:readline";

const protocolVersion = "v1";
const capabilities = ["protocol.v1", "browser-runtime.contract"];
const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
const sessions = new Map();

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

function error(request, message) {
  send({
    protocol: protocolVersion,
    id: request.id ?? "unknown",
    type: "error",
    error: message,
  });
}

function value(request, key) {
  const candidate = request.payload?.[key];
  return typeof candidate === "string" ? candidate : "";
}

function finishSession(sessionID, type, payload = {}) {
  const session = sessions.get(sessionID);
  if (!session) return;
  sessions.delete(sessionID);
  reply(session.request, type, { session_id: sessionID, ...payload });
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
    error(request, `unsupported protocol: ${request.protocol ?? "missing"}`);
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
    case "session_start": {
      const sessionID = value(request, "session_id");
      if (!sessionID || !value(request, "account_id") || !value(request, "request_id") || !value(request, "profile_dir")) {
        error(request, "session_start requires session_id, account_id, request_id, and profile_dir");
        break;
      }
      if (sessions.has(sessionID)) {
        error(request, "session is already running");
        break;
      }
      sessions.set(sessionID, { request });
      reply(request, "session_started", { session_id: sessionID });

      const mode = value(request, "mode") || "deferred";
      if (mode === "hold") break;
      if (mode === "crash") {
        setImmediate(() => process.exit(42));
        break;
      }
      setImmediate(() => {
        if (mode === "success") {
          finishSession(sessionID, "session_succeeded");
          return;
        }
        finishSession(sessionID, "session_failed", {
          failure: mode === "failure" ? "transient" : "configuration",
          reason: "browser runtime is deferred",
        });
      });
      break;
    }
    case "session_cancel": {
      const sessionID = value(request, "session_id");
      if (!sessionID) {
        error(request, "session_cancel requires session_id");
        break;
      }
      if (sessions.has(sessionID)) finishSession(sessionID, "session_cancelled");
      else reply(request, "session_cancelled", { session_id: sessionID, already_stopped: "true" });
      break;
    }
    case "shutdown":
      reply(request, "shutdown_ack");
      sessions.clear();
      setImmediate(() => process.exit(0));
      break;
    default:
      error(request, `unsupported message type: ${request.type ?? "missing"}`);
  }
});
