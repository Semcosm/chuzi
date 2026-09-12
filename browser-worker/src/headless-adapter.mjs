import { createHash, randomBytes } from "node:crypto";
import { createConnection } from "node:net";
import { createServer } from "node:http";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, isAbsolute, normalize, resolve, sep } from "node:path";

const adapterProtocol = "chuzi.adapter/v1";
const adapterID = "chuzi.headless-cdp";
const adapterVersion = "0.1.0";
const lifecycleProtocol = "v1";
const operationName = "local.test_page_probe";
const localPageFile = resolve(dirname(fileURLToPath(import.meta.url)), "local-test-page.html");
const browserCommand = option("--browser-command", process.env.CHUZI_HEADLESS_BROWSER_COMMAND || "chromium");
const browserCommandArgs = options("--browser-command-arg");
const cdpTimeoutMs = boundedNumber(option("--cdp-timeout-ms", "10000"), 10000, 100, 120000);
const pollIntervalMs = boundedNumber(option("--poll-interval-ms", "100"), 100, 10, 2000);
const operationTimeoutMs = boundedNumber(option("--operation-timeout-ms", "10000"), 10000, 100, 120000);

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
    if (process.argv[index + 1] !== undefined) values.push(process.argv[index + 1]);
    index += 1;
  }
  return values;
}

function boundedNumber(value, fallback, minimum, maximum) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.min(maximum, Math.max(minimum, number)) : fallback;
}

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

function reply(request, type, payload = {}) {
  send({ protocol: adapterProtocol, id: request.id, type, payload });
}

function protocolError(request, code) {
  send({ protocol: adapterProtocol, id: request.id || "unknown", type: "error", error: code });
}

function value(request, key) {
  const candidate = request.payload && request.payload[key];
  return typeof candidate === "string" ? candidate : "";
}

function classified(className, code, retryable = false) {
  return { failure: { class: className, code, retryable } };
}

function cancelledError() {
  return Object.assign(new Error("operation cancelled"), classified("cancelled", "operation_cancelled"));
}

function validProfileDir(profileDir) {
  if (!isAbsolute(profileDir)) return false;
  const cleaned = normalize(profileDir);
  return cleaned !== sep && !cleaned.split(/[\\/]+/u).includes("..");
}

function validAccountID(accountID) {
  return /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/u.test(accountID);
}

function wait(milliseconds, signal) {
  return new Promise((resolveWait, rejectWait) => {
    if (signal && signal.aborted) {
      rejectWait(cancelledError());
      return;
    }
    const timer = setTimeout(resolveWait, milliseconds);
    if (!signal) return;
    signal.addEventListener("abort", () => {
      clearTimeout(timer);
      rejectWait(cancelledError());
    }, { once: true });
  });
}

function parseParameters(request) {
  const raw = value(request, "parameters");
  if (!raw || raw === "null") return {};
  let parameters;
  try {
    parameters = JSON.parse(raw);
  } catch {
    throw classified("configuration", "operation_parameters_invalid");
  }
  if (!parameters || Array.isArray(parameters) || typeof parameters !== "object") {
    throw classified("configuration", "operation_parameters_invalid");
  }
  for (const key of Object.keys(parameters)) {
    const normalized = key.toLowerCase().replaceAll("-", "_").replaceAll(".", "_");
    if (["credential", "credentials", "password", "secret", "token", "cookie",
      "authorization", "access_token", "refresh_token", "client_secret", "private_key"].includes(normalized)) {
      throw classified("configuration", "credential_parameter_rejected");
    }
  }
  return parameters;
}

function deadlineFor(request, parentSignal) {
  const requested = value(request, "deadline");
  const timestamp = requested ? Date.parse(requested) : Date.now() + operationTimeoutMs;
  if (!Number.isFinite(timestamp) || timestamp <= Date.now()) {
    throw classified("transient", "operation_deadline_exceeded", true);
  }
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), Math.min(operationTimeoutMs, timestamp - Date.now()));
  const relay = () => controller.abort();
  parentSignal.addEventListener("abort", relay, { once: true });
  return {
    signal: controller.signal,
    close() {
      clearTimeout(timer);
      parentSignal.removeEventListener("abort", relay);
    },
  };
}

class JsonLinePeer {
  constructor(child) {
    this.child = child;
    this.lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
    this.queue = [];
    this.waiters = [];
    this.closed = false;
    this.lines.on("line", (line) => {
      if (!line.trim()) return;
      let message;
      try {
        message = JSON.parse(line);
      } catch {
        this.fail(new Error("lifecycle_protocol_invalid"));
        return;
      }
      const waiter = this.waiters.shift();
      if (waiter) waiter.resolve(message);
      else this.queue.push(message);
    });
    child.once("error", () => this.fail(new Error("lifecycle_process_unavailable")));
    child.once("exit", () => this.fail(new Error("lifecycle_process_exited")));
  }

  fail(error) {
    if (this.closed) return;
    this.closed = true;
    for (const waiter of this.waiters.splice(0)) waiter.reject(error);
  }

  next() {
    if (this.queue.length) return Promise.resolve(this.queue.shift());
    if (this.closed) return Promise.reject(new Error("lifecycle_process_exited"));
    return new Promise((resolveNext, rejectNext) => this.waiters.push({ resolve: resolveNext, reject: rejectNext }));
  }

  nextWithSignal(signal) {
    if (!signal) return this.next();
    if (signal.aborted) return Promise.reject(cancelledError());
    return Promise.race([
      this.next(),
      new Promise((_, rejectNext) => signal.addEventListener("abort", () => rejectNext(cancelledError()), { once: true })),
    ]);
  }

  async request(message, expectedTypes, signal) {
    if (this.closed) throw new Error("lifecycle_process_exited");
    this.child.stdin.write(JSON.stringify(message) + "\n");
    while (true) {
      const response = await this.nextWithSignal(signal);
      if (response.protocol !== lifecycleProtocol) throw new Error("lifecycle_protocol_invalid");
      if (response.id !== message.id) continue;
      if (response.type === "error") throw new Error("lifecycle_request_rejected");
      if (expectedTypes.includes(response.type)) return response;
    }
  }

  close() {
    this.lines.close();
    this.fail(new Error("lifecycle_process_exited"));
  }
}

let lifecycleSequence = 0;

class HeadlessLifecycle {
  constructor(session, signal) {
    this.session = session;
    this.signal = signal;
    this.child = null;
    this.peer = null;
    this.port = "";
    this.websocketURL = null;
    this.closed = false;
  }

  nextID(prefix) {
    lifecycleSequence += 1;
    return prefix + "-" + lifecycleSequence;
  }

  args() {
    const args = [
      resolve(dirname(fileURLToPath(import.meta.url)), "headless.mjs"),
      "--stdio", "--browser-command", browserCommand,
    ];
    for (const argument of browserCommandArgs) args.push("--browser-command-arg", argument);
    args.push("--cdp-timeout-ms", String(cdpTimeoutMs), "--poll-interval-ms", String(pollIntervalMs));
    return args;
  }

  async start() {
    if (!validProfileDir(this.session.profileDir)) throw classified("configuration", "profile_path_invalid");
    if (!browserCommand.trim()) throw classified("configuration", "browser_command_missing");
    if (this.signal.aborted) throw cancelledError();
    try {
      this.child = spawn(process.execPath, this.args(), {
        stdio: ["pipe", "pipe", "ignore"],
        env: { ...process.env, NODE_OPTIONS: "" },
      });
    } catch {
      throw classified("configuration", "lifecycle_process_unavailable");
    }
    this.peer = new JsonLinePeer(this.child);
    try {
      await this.peer.request({
        protocol: lifecycleProtocol, id: this.nextID("hello"), type: "hello",
      }, ["hello_ack"], this.signal);
      const started = await this.peer.request({
        protocol: lifecycleProtocol,
        id: this.nextID("session"),
        type: "session_start",
        payload: {
          session_id: this.session.sessionID,
          account_id: this.session.accountID,
          request_id: this.session.requestID,
          profile_dir: this.session.profileDir,
          mode: "hold",
        },
      }, ["session_started"], this.signal);
      const port = started.payload && started.payload.cdp_port;
      if (started.payload?.runtime !== "headless-cdp" || !/^\d+$/u.test(port || "")) {
        throw classified("runtime", "lifecycle_metadata_invalid", true);
      }
      this.port = port;
      const version = await fetchVersion(port, this.signal);
      this.websocketURL = version.websocketURL;
      return { browserProduct: version.browserProduct, protocolVersion: version.protocolVersion };
    } catch (error) {
      await this.close();
      if (error?.failure) throw error.failure;
      throw classified("runtime", "lifecycle_failed", true);
    }
  }

  async cancel() {
    if (!this.peer || this.closed) return;
    try {
      await this.peer.request({
        protocol: lifecycleProtocol,
        id: this.nextID("cancel"),
        type: "session_cancel",
        payload: { session_id: this.session.sessionID },
      }, ["session_cancelled"]);
    } catch {
      // close below still reaps the lifecycle process
    }
  }

  async close() {
    if (this.closed) return;
    this.closed = true;
    if (this.peer && !this.peer.closed) {
      try {
        await this.peer.request({
          protocol: lifecycleProtocol, id: this.nextID("shutdown"), type: "shutdown",
        }, ["shutdown_ack"]);
      } catch {
        // the lifecycle process may already have exited
      }
    }
    if (this.child && this.child.exitCode === null && this.child.signalCode === null) this.child.kill();
    this.peer?.close();
  }
}

async function fetchVersion(port, signal) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), cdpTimeoutMs);
  const relay = () => controller.abort();
  signal.addEventListener("abort", relay, { once: true });
  try {
    const response = await fetch("http://127.0.0.1:" + port + "/json/version", { signal: controller.signal });
    if (!response.ok) throw new Error("endpoint unavailable");
    const body = await response.json();
    const websocketURL = typeof body.webSocketDebuggerUrl === "string" ? new URL(body.webSocketDebuggerUrl) : null;
    if (!websocketURL || websocketURL.protocol !== "ws:" || websocketURL.hostname !== "127.0.0.1" ||
        websocketURL.port !== String(port) || !websocketURL.pathname.startsWith("/devtools/browser/")) {
      throw classified("configuration", "cdp_endpoint_invalid");
    }
    return {
      websocketURL,
      browserProduct: typeof body.Browser === "string" ? body.Browser.split("/")[0] : "unknown",
      protocolVersion: typeof body["Protocol-Version"] === "string" ? body["Protocol-Version"] : "unknown",
    };
  } catch (error) {
    if (error?.failure) throw error;
    if (signal.aborted || error?.name === "AbortError") throw cancelledError();
    throw classified("transient", "cdp_endpoint_unavailable", true);
  } finally {
    clearTimeout(timer);
    signal.removeEventListener("abort", relay);
  }
}

class CdpSocket {
  constructor(url, signal) {
    this.url = url;
    this.signal = signal;
    this.socket = null;
    this.buffer = Buffer.alloc(0);
    this.handshakeBuffer = Buffer.alloc(0);
    this.handshaken = false;
    this.closed = false;
    this.fragments = [];
    this.pending = new Map();
    this.nextCommandID = 0;
  }

  async connect() {
    if (this.url.protocol !== "ws:" || this.url.hostname !== "127.0.0.1") {
      throw classified("configuration", "cdp_endpoint_invalid");
    }
    const key = randomBytes(16).toString("base64");
    const expectedAccept = createHash("sha1")
      .update(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest("base64");
    await new Promise((resolveConnect, rejectConnect) => {
      const socket = createConnection({ host: this.url.hostname, port: Number(this.url.port) });
      this.socket = socket;
      let settled = false;
      const settle = (error) => {
        if (settled) return;
        settled = true;
        if (error) rejectConnect(error);
        else resolveConnect();
      };
      socket.on("error", () => {
        this.failAll(new Error("cdp_connection_failed"));
        settle(classified("runtime", "cdp_connection_failed", true));
      });
      socket.on("close", () => {
        this.failAll(new Error("cdp_connection_closed"));
        if (!this.handshaken) settle(classified("runtime", "cdp_connection_failed", true));
      });
      socket.on("data", (chunk) => {
        if (!this.handshaken) {
          this.handshakeBuffer = Buffer.concat([this.handshakeBuffer, chunk]);
          const end = this.handshakeBuffer.indexOf("\r\n\r\n");
          if (end < 0) return;
          const header = this.handshakeBuffer.subarray(0, end).toString("ascii");
          const lines = header.split("\r\n");
          const acceptLine = lines.find((line) => /^sec-websocket-accept:/iu.test(line));
          const accept = acceptLine?.split(":", 2)[1]?.trim();
          if (!lines[0]?.includes(" 101 ") || accept !== expectedAccept) {
            settle(classified("runtime", "cdp_handshake_invalid", true));
            socket.destroy();
            return;
          }
          this.handshaken = true;
          this.buffer = this.handshakeBuffer.subarray(end + 4);
          this.handshakeBuffer = Buffer.alloc(0);
          this.consumeFrames();
          settle();
          return;
        }
        this.buffer = Buffer.concat([this.buffer, chunk]);
        this.consumeFrames();
      });
      socket.once("connect", () => {
        socket.write("GET " + this.url.pathname + this.url.search + " HTTP/1.1\r\n" +
          "Host: " + this.url.hostname + ":" + this.url.port + "\r\n" +
          "Upgrade: websocket\r\nConnection: Upgrade\r\n" +
          "Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n");
      });
      const abort = () => {
        socket.destroy();
        settle(cancelledError());
      };
      this.signal.addEventListener("abort", abort, { once: true });
    }).catch((error) => {
      if (error?.failure) throw error.failure;
      throw classified("runtime", "cdp_connection_failed", true);
    });
  }

  consumeFrames() {
    while (this.buffer.length >= 2) {
      const first = this.buffer[0];
      const second = this.buffer[1];
      const masked = (second & 0x80) !== 0;
      let length = second & 0x7f;
      let offset = 2;
      if (length === 126) {
        if (this.buffer.length < 4) return;
        length = this.buffer.readUInt16BE(2);
        offset = 4;
      } else if (length === 127) {
        if (this.buffer.length < 10) return;
        const high = this.buffer.readUInt32BE(2);
        const low = this.buffer.readUInt32BE(6);
        if (high > 0x1fffff) {
          this.failAll(new Error("cdp_frame_too_large"));
          return;
        }
        length = high * 0x100000000 + low;
        offset = 10;
      }
      const payloadOffset = masked ? offset + 4 : offset;
      const frameLength = payloadOffset + length;
      if (this.buffer.length < frameLength) return;
      let payload = this.buffer.subarray(payloadOffset, frameLength);
      if (masked) {
        const mask = this.buffer.subarray(offset, offset + 4);
        payload = Buffer.from(payload);
        for (let index = 0; index < payload.length; index += 1) payload[index] ^= mask[index % 4];
      }
      this.buffer = this.buffer.subarray(frameLength);
      const opcode = first & 0x0f;
      if (opcode === 0x8) {
        this.close();
        return;
      }
      if (opcode === 0x9) {
        this.writeFrame(0xA, payload);
        continue;
      }
      if (opcode === 0xA) continue;
      if (opcode === 0x0) {
        this.fragments.push(payload);
        if ((first & 0x80) !== 0) {
          this.handleText(Buffer.concat(this.fragments));
          this.fragments = [];
        }
      } else if (opcode === 0x1) {
        if ((first & 0x80) === 0) this.fragments = [payload];
        else this.handleText(payload);
      }
    }
  }

  handleText(payload) {
    let message;
    try {
      message = JSON.parse(payload.toString("utf8"));
    } catch {
      this.failAll(new Error("cdp_message_invalid"));
      return;
    }
    if (!Number.isInteger(message.id)) return;
    const pending = this.pending.get(message.id);
    if (!pending) return;
    this.pending.delete(message.id);
    clearTimeout(pending.timer);
    if (message.error) pending.reject(classified("runtime", "cdp_command_failed", true));
    else pending.resolve(message.result || {});
  }

  writeFrame(opcode, payload) {
    if (!this.socket || this.closed) return;
    const body = Buffer.isBuffer(payload) ? payload : Buffer.from(payload);
    const mask = randomBytes(4);
    let header;
    if (body.length < 126) {
      header = Buffer.from([0x80 | opcode, 0x80 | body.length]);
    } else if (body.length <= 0xffff) {
      header = Buffer.alloc(4);
      header[0] = 0x80 | opcode;
      header[1] = 0x80 | 126;
      header.writeUInt16BE(body.length, 2);
    } else {
      header = Buffer.alloc(10);
      header[0] = 0x80 | opcode;
      header[1] = 0x80 | 127;
      header.writeUInt32BE(Math.floor(body.length / 0x100000000), 2);
      header.writeUInt32BE(body.length >>> 0, 6);
    }
    const masked = Buffer.from(body);
    for (let index = 0; index < masked.length; index += 1) masked[index] ^= mask[index % 4];
    this.socket.write(Buffer.concat([header, mask, masked]));
  }

  command(method, params = {}, sessionID = "", signal = this.signal) {
    if (this.closed || !this.handshaken) return Promise.reject(classified("runtime", "cdp_not_connected", true));
    if (signal.aborted) return Promise.reject(cancelledError());
    const id = ++this.nextCommandID;
    const message = { id, method, params };
    if (sessionID) message.sessionId = sessionID;
    return new Promise((resolveCommand, rejectCommand) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        rejectCommand(classified("transient", "cdp_command_timeout", true));
      }, cdpTimeoutMs);
      this.pending.set(id, { resolve: resolveCommand, reject: rejectCommand, timer });
      this.writeFrame(0x1, JSON.stringify(message));
      signal.addEventListener("abort", () => {
        const pending = this.pending.get(id);
        if (!pending) return;
        this.pending.delete(id);
        clearTimeout(pending.timer);
        rejectCommand(cancelledError());
      }, { once: true });
    });
  }

  failAll(error) {
    for (const [id, pending] of this.pending) {
      clearTimeout(pending.timer);
      pending.reject(error);
      this.pending.delete(id);
    }
  }

  close() {
    if (this.closed) return;
    this.failAll(new Error("cdp_connection_closed"));
    try {
      this.writeFrame(0x8, Buffer.alloc(0));
    } catch {
      // the socket may already be closed
    }
    this.closed = true;
    this.socket?.destroy();
  }
}

async function createLocalPage(accountID) {
  const template = await readFile(localPageFile, "utf8");
  const escaped = accountID.replaceAll("&", "&amp;").replaceAll("\"", "&quot;")
    .replaceAll("<", "&lt;").replaceAll(">", "&gt;");
  const body = template.replaceAll("__CHUZI_ACCOUNT_ID__", escaped);
  const server = createServer((request, response) => {
    const pathname = new URL(request.url || "/", "http://127.0.0.1").pathname;
    if (pathname !== "/test-page") {
      response.writeHead(404).end();
      return;
    }
    response.writeHead(200, {
      "content-type": "text/html; charset=utf-8",
      "cache-control": "no-store",
      "content-length": Buffer.byteLength(body),
    });
    response.end(body);
  });
  await new Promise((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  const port = typeof address === "object" && address ? address.port : 0;
  if (!port) {
    server.close();
    throw classified("runtime", "local_test_page_unavailable", true);
  }
  return {
    url: "http://127.0.0.1:" + port + "/test-page",
    close: () => new Promise((resolveClose) => server.close(() => resolveClose())),
  };
}

async function executeLocalPage(session, operation, parameters, signal, lifecycle) {
  if (operation !== operationName) throw classified("configuration", "unsupported_operation");
  if (Object.keys(parameters).length !== 0) throw classified("configuration", "operation_parameters_unsupported");
  if (session.runtime && session.runtime !== "headless-cdp") throw classified("configuration", "runtime_mismatch");
  if (!validAccountID(session.accountID)) throw classified("configuration", "account_id_invalid");

  const page = await createLocalPage(session.accountID);
  const socket = new CdpSocket(lifecycle.websocketURL, signal);
  let targetID = "";
  try {
    await socket.connect();
    const target = await socket.command("Target.createTarget", { url: page.url }, "", signal);
    targetID = typeof target.targetId === "string" ? target.targetId : "";
    if (!targetID) throw classified("runtime", "cdp_target_missing", true);
    const attached = await socket.command("Target.attachToTarget", { targetId: targetID, flatten: true }, "", signal);
    const attachedSessionID = typeof attached.sessionId === "string" ? attached.sessionId : "";
    if (!attachedSessionID) throw classified("runtime", "cdp_session_missing", true);
    const deadline = Date.now() + operationTimeoutMs;
    let pageValue;
    while (Date.now() < deadline) {
      try {
        const evaluated = await socket.command("Runtime.evaluate", {
          expression: "(() => { const root = document.documentElement; return { ready: root.dataset.chuziReady === \"true\", accountId: root.dataset.accountId || \"\", title: document.title }; })()",
          returnByValue: true,
          awaitPromise: true,
        }, attachedSessionID, signal);
        pageValue = evaluated.result?.result?.value;
        if (pageValue?.ready === true && pageValue.accountId === session.accountID) break;
      } catch (error) {
        if (error?.failure?.failure?.class === "cancelled") throw error;
      }
      await wait(Math.min(pollIntervalMs, Math.max(1, deadline - Date.now())), signal);
    }
    if (!pageValue?.ready || pageValue.accountId !== session.accountID) {
      throw classified("business", "local_test_page_marker_missing");
    }
    return {
      succeeded: true,
      facts: { page: "local-test-page", marker: "ready", account_id: session.accountID },
    };
  } finally {
    if (targetID) {
      try {
        await socket.command("Target.closeTarget", { targetId: targetID }, "", signal);
      } catch {
        // the browser may already be closing
      }
    }
    socket.close();
    await page.close();
  }
}

function terminalPayload(result) {
  if (result.succeeded) return { facts: JSON.stringify(result.facts) };
  return {
    failure_class: result.failure.class,
    failure_code: result.failure.code,
    retryable: String(result.failure.retryable),
  };
}

const operations = new Map();

async function runOperation(request, task) {
  const session = {
    sessionID: value(request, "session_id"),
    accountID: value(request, "account_id"),
    requestID: value(request, "request_id"),
    profileDir: value(request, "profile_dir"),
    runtime: value(request, "runtime"),
  };
  let deadline;
  let lifecycle;
  try {
    if (!session.sessionID || !session.accountID || !session.requestID || !validProfileDir(session.profileDir)) {
      throw classified("configuration", "session_invalid");
    }
    if (!value(request, "operation_id") || !value(request, "operation")) {
      throw classified("configuration", "operation_invalid");
    }
    const parameters = parseParameters(request);
    deadline = deadlineFor(request, task.controller.signal);
    reply(request, "operation_started", { operation_id: value(request, "operation_id") });
    lifecycle = new HeadlessLifecycle(session, deadline.signal);
    await lifecycle.start();
    const result = await executeLocalPage(session, value(request, "operation"), parameters, deadline.signal, lifecycle);
    reply(request, "operation_succeeded", {
      operation_id: value(request, "operation_id"),
      ...terminalPayload(result),
    });
  } catch (error) {
    const classifiedFailure = error?.failure || error;
    const resultFailure = classifiedFailure?.class && classifiedFailure?.code
      ? classifiedFailure
      : { class: "runtime", code: "adapter_failed", retryable: true };
    if (resultFailure.class === "cancelled" || task.controller.signal.aborted) {
      reply(request, "operation_cancelled", { operation_id: value(request, "operation_id") });
    } else {
      reply(request, "operation_failed", {
        operation_id: value(request, "operation_id"),
        ...terminalPayload({ succeeded: false, failure: resultFailure }),
      });
    }
  } finally {
    deadline?.close();
    await lifecycle?.close();
    task.done = true;
    operations.delete(value(request, "operation_id"));
  }
}

function handleExecute(request) {
  const operationID = value(request, "operation_id");
  if (!operationID || operations.has(operationID)) {
    protocolError(request, operations.has(operationID) ? "operation_already_running" : "operation_id_required");
    return;
  }
  const task = { controller: new AbortController(), done: false };
  operations.set(operationID, task);
  void runOperation(request, task);
}

function handleCancel(request) {
  const operationID = value(request, "operation_id");
  if (!operationID) {
    protocolError(request, "operation_id_required");
    return;
  }
  const task = operations.get(operationID);
  if (task) task.controller.abort();
  reply(request, "operation_cancelled", { operation_id: operationID, already_stopped: task ? "false" : "true" });
}

async function shutdown(request) {
  for (const task of operations.values()) task.controller.abort();
  while ([...operations.values()].some((task) => !task.done)) await new Promise((resolveWait) => setTimeout(resolveWait, 5));
  reply(request, "shutdown_ack");
  setImmediate(() => process.exit(0));
}

const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
process.stdin.resume();
input.on("line", (line) => {
  if (!line.trim()) return;
  let request;
  try {
    request = JSON.parse(line);
  } catch {
    send({ protocol: adapterProtocol, id: "unknown", type: "error", error: "invalid_json" });
    return;
  }
  if (request.protocol !== adapterProtocol) {
    protocolError(request, "unsupported_protocol");
    return;
  }
  switch (request.type) {
    case "hello":
      reply(request, "hello_ack", {
        adapter_id: adapterID,
        version: adapterVersion,
        api: adapterProtocol,
        capabilities: "headless-cdp@1,local.test-page@1",
      });
      break;
    case "execute":
      handleExecute(request);
      break;
    case "cancel":
      handleCancel(request);
      break;
    case "shutdown":
      void shutdown(request);
      break;
    default:
      protocolError(request, "unsupported_message_type");
  }
});
