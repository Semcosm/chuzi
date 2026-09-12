import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:net";
import { createInterface } from "node:readline";
import { isAbsolute, normalize, sep } from "node:path";

const protocolVersion = "v1";
const capabilities = ["protocol.v1", "browser-runtime.headless-cdp", "session.cdp"];
const defaults = {
  browserCommand: "chromium",
  cdpTimeoutMs: 10000,
  pollIntervalMs: 100,
};

function option(name, fallback) {
  const index = process.argv.indexOf(name);
  if (index === -1) return fallback;
  const value = process.argv[index + 1];
  return value === undefined ? fallback : value;
}

function options(name) {
  const values = [];
  for (let index = 0; index < process.argv.length; index += 1) {
    if (process.argv[index] !== name) continue;
    const value = process.argv[index + 1];
    if (value !== undefined) values.push(value);
    index += 1;
  }
  return values;
}

const browserCommand = option("--browser-command", process.env.CHUZI_HEADLESS_BROWSER_COMMAND || defaults.browserCommand);
const browserCommandArgs = options("--browser-command-arg");
const cdpTimeoutMs = boundedNumber(option("--cdp-timeout-ms", defaults.cdpTimeoutMs), defaults.cdpTimeoutMs, 100, 120000);
const pollIntervalMs = boundedNumber(option("--poll-interval-ms", defaults.pollIntervalMs), defaults.pollIntervalMs, 10, 2000);
const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
process.stdin.resume();
const sessions = new Map();

function terminateSessions() {
  for (const session of sessions.values()) {
    session.cancelled = true;
    session.abortController.abort();
    terminateBrowser(session.child);
  }
  sessions.clear();
}

process.once("SIGTERM", () => {
  terminateSessions();
  process.exit(0);
});
process.once("SIGINT", () => {
  terminateSessions();
  process.exit(0);
});

function boundedNumber(value, fallback, minimum, maximum) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.min(maximum, Math.max(minimum, number)) : fallback;
}

function send(message) {
  process.stdout.write(`${JSON.stringify(message)}\n`);
}

function reply(request, type, payload = {}) {
  send({ protocol: protocolVersion, id: request.id, type, payload });
}

function error(request, message) {
  send({ protocol: protocolVersion, id: request.id ?? "unknown", type: "error", error: message });
}

function value(request, key) {
  const candidate = request.payload?.[key];
  return typeof candidate === "string" ? candidate : "";
}

function validProfileDir(profileDir) {
  if (!isAbsolute(profileDir)) return false;
  const normalized = normalize(profileDir);
  return !normalized.split(/[\\/]+/u).includes("..") && normalized !== sep;
}

function loopbackPort() {
  return new Promise((resolvePort, reject) => {
    // Reserving a loopback port before starting the browser keeps the browser
    // command line deterministic without exposing a caller-selected port.
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      const port = typeof address === "object" && address ? address.port : 0;
      server.close((closeError) => closeError ? reject(closeError) : resolvePort(port));
    });
  });
}

function redactFailure(errorCode) {
  return { failure: errorCode.failure, reason: errorCode.reason };
}

function runtimeFailure(failure, reason) {
  return { failure, reason };
}

function terminateBrowser(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  if (process.platform === "win32") {
    if (child.pid) spawnSync("taskkill", ["/pid", String(child.pid), "/t", "/f"], { stdio: "ignore" });
    return;
  }
  if (child.pid) {
    try {
      process.kill(-child.pid, "SIGTERM");
      return;
    } catch {
      // A browser may not have created a process group yet.
    }
  }
  try {
    child.kill("SIGTERM");
  } catch {
    // The process already exited between the state check and kill.
  }
}

async function discoverCdp(session) {
  const deadline = Date.now() + cdpTimeoutMs;
  const endpoint = `http://127.0.0.1:${session.port}/json/version`;
  while (Date.now() < deadline) {
    if (session.cancelled) throw runtimeFailure("transient", "cancelled");
    if (session.startError) throw session.startError;
    if (session.child.exitCode !== null || session.child.signalCode !== null) {
      throw runtimeFailure("transient", "browser_crashed");
    }
    const requestController = new AbortController();
    const remainingMs = Math.max(1, deadline - Date.now());
    const abortRequest = () => requestController.abort();
    const timeout = setTimeout(abortRequest, remainingMs);
    const abortSession = () => requestController.abort();
    session.abortController.signal.addEventListener("abort", abortSession, { once: true });
    try {
      const response = await fetch(endpoint, { signal: requestController.signal });
      if (!response.ok) throw new Error("http status");
      const version = await response.json();
      const websocket = typeof version.webSocketDebuggerUrl === "string" ? new URL(version.webSocketDebuggerUrl) : null;
      if (!websocket || websocket.protocol !== "ws:" ||
          websocket.hostname !== "127.0.0.1" || websocket.port !== String(session.port) ||
          !websocket.pathname.startsWith("/devtools/browser/")) {
        throw runtimeFailure("configuration", "cdp_endpoint_invalid");
      }
      return {
        browserProduct: typeof version.Browser === "string" ? version.Browser.split("/")[0] : "unknown",
        protocolVersion: typeof version["Protocol-Version"] === "string" ? version["Protocol-Version"] : "unknown",
      };
    } catch (error) {
      if (session.cancelled) throw runtimeFailure("transient", "cancelled");
      if (error?.failure) throw error;
      if (Date.now() >= deadline) break;
      await new Promise((resolveDelay) => setTimeout(resolveDelay, pollIntervalMs));
    } finally {
      clearTimeout(timeout);
      session.abortController.signal.removeEventListener("abort", abortSession);
    }
  }
  throw runtimeFailure("transient", "cdp_endpoint_timeout");
}

async function startBrowser(session) {
  if (!browserCommand.trim()) throw runtimeFailure("configuration", "browser_command_missing");
  if (!validProfileDir(session.profileDir)) throw runtimeFailure("configuration", "profile_path_invalid");
  session.port = await loopbackPort();
  const args = [
    ...browserCommandArgs,
    "--headless=new",
    "--remote-debugging-address=127.0.0.1",
    `--remote-debugging-port=${session.port}`,
    `--user-data-dir=${session.profileDir}`,
    "--disable-gpu",
    "--no-first-run",
    "--no-default-browser-check",
    "about:blank",
  ];
  try {
    session.child = spawn(browserCommand, args, {
      detached: process.platform !== "win32",
      stdio: ["ignore", "ignore", "ignore"],
    });
  } catch {
    throw runtimeFailure("configuration", "browser_command_unavailable");
  }
  session.child.once("error", () => {
    session.startError = runtimeFailure("configuration", "browser_command_unavailable");
  });
  return discoverCdp(session);
}

async function finishSession(session, type, payload = {}) {
  if (session.finished) return;
  session.finished = true;
  sessions.delete(session.sessionID);
  session.abortController.abort();
  terminateBrowser(session.child);
  reply(session.request, type, { session_id: session.sessionID, ...payload });
}

async function runSession(session) {
  try {
    const runtime = await startBrowser(session);
    if (session.cancelled) return;
    reply(session.request, "session_started", {
      session_id: session.sessionID,
      runtime: "headless-cdp",
      session_handle: session.sessionID,
      cdp_host: "127.0.0.1",
      cdp_port: String(session.port),
      browser_product: runtime.browserProduct,
      protocol_version: runtime.protocolVersion,
    });
    const mode = value(session.request, "mode") || "probe";
    if (mode === "hold") return;
    if (mode === "crash") {
      terminateBrowser(session.child);
      await finishSession(session, "session_failed", runtimeFailure("transient", "browser_crashed"));
      return;
    }
    if (mode === "success") {
      await finishSession(session, "session_succeeded");
      return;
    }
    await finishSession(session, "session_failed", runtimeFailure("configuration", "automation_not_configured"));
  } catch (failure) {
    if (session.cancelled) {
      await finishSession(session, "session_cancelled");
      return;
    }
    const result = failure?.failure ? failure : runtimeFailure("transient", "browser_runtime_failed");
    await finishSession(session, "session_failed", redactFailure(result));
  }
}

function handleCancel(request) {
  const sessionID = value(request, "session_id");
  if (!sessionID) {
    error(request, "session_cancel requires session_id");
    return;
  }
  const session = sessions.get(sessionID);
  if (!session) {
    reply(request, "session_cancelled", { session_id: sessionID, already_stopped: "true" });
    return;
  }
  session.cancelled = true;
  session.abortController.abort();
  void finishSession(session, "session_cancelled");
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
        browserRuntime: "headless-cdp",
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
      const session = {
        sessionID,
        profileDir: value(request, "profile_dir"),
        request,
        child: null,
        port: 0,
        cancelled: false,
        finished: false,
        abortController: new AbortController(),
      };
      sessions.set(sessionID, session);
      void runSession(session);
      break;
    }
    case "session_cancel":
      handleCancel(request);
      break;
    case "shutdown":
      terminateSessions();
      reply(request, "shutdown_ack");
      setImmediate(() => process.exit(0));
      break;
    default:
      error(request, `unsupported message type: ${request.type ?? "missing"}`);
  }
});
