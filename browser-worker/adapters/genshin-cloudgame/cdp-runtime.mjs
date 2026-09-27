import { createHash, randomBytes } from "node:crypto";
import { createConnection } from "node:net";

export function classified(className, code, retryable = false) {
  return { failure: { class: className, code, retryable } };
}

export function cancelledError() {
  return Object.assign(new Error("operation cancelled"), classified("cancelled", "operation_cancelled"));
}

export function wait(milliseconds, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(cancelledError()); return; }
    const timer = setTimeout(resolve, milliseconds);
    if (!signal) return;
    signal.addEventListener("abort", () => { clearTimeout(timer); reject(cancelledError()); }, { once: true });
  });
}

function validHandle(handle) {
  const match = /^(?:headless|headed)-cdp:\/\/127\.0\.0\.1:(\d+)$/u.exec(handle || "");
  const port = Number(match?.[1] || 0);
  return Boolean(match) && Number.isInteger(port) && port > 0 && port < 65536;
}

async function fetchVersion(port, signal, timeoutMs = 10000) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const relay = () => controller.abort();
  signal?.addEventListener("abort", relay, { once: true });
  try {
    const response = await fetch(`http://127.0.0.1:${port}/json/version`, { signal: controller.signal });
    if (!response.ok) throw new Error("endpoint unavailable");
    const body = await response.json();
    const websocketURL = typeof body.webSocketDebuggerUrl === "string" ? new URL(body.webSocketDebuggerUrl) : null;
    if (!websocketURL || websocketURL.protocol !== "ws:" || websocketURL.hostname !== "127.0.0.1" ||
        websocketURL.port !== String(port) || !websocketURL.pathname.startsWith("/devtools/browser/")) {
      throw classified("configuration", "cdp_endpoint_invalid");
    }
    return { websocketURL, browserProduct: typeof body.Browser === "string" ? body.Browser.split("/")[0] : "unknown", protocolVersion: typeof body["Protocol-Version"] === "string" ? body["Protocol-Version"] : "unknown" };
  } catch (error) {
    if (error?.failure) throw error;
    if (signal?.aborted || error?.name === "AbortError") throw cancelledError();
    throw classified("transient", "cdp_endpoint_unavailable", true);
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener("abort", relay);
  }
}

export class ExternalLifecycle {
  constructor(handle, signal, timeoutMs = 10000) {
    this.handle = handle;
    this.signal = signal;
    this.timeoutMs = timeoutMs;
    this.websocketURL = null;
  }

  async start() {
    if (!validHandle(this.handle)) throw classified("configuration", "cdp_handle_invalid");
    const port = this.handle.slice(this.handle.lastIndexOf(":") + 1);
    const version = await fetchVersion(port, this.signal, this.timeoutMs);
    this.websocketURL = version.websocketURL;
    return version;
  }

  async close() {}
}

export class CdpSocket {
  constructor(url, signal, timeoutMs = 10000) {
    this.url = url;
    this.signal = signal;
    this.timeoutMs = timeoutMs;
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
    if (this.url.protocol !== "ws:" || this.url.hostname !== "127.0.0.1") throw classified("configuration", "cdp_endpoint_invalid");
    const key = randomBytes(16).toString("base64");
    const expectedAccept = createHash("sha1").update(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest("base64");
    await new Promise((resolve, reject) => {
      const socket = createConnection({ host: this.url.hostname, port: Number(this.url.port) });
      this.socket = socket;
      let settled = false;
      const settle = (error) => { if (settled) return; settled = true; error ? reject(error) : resolve(); };
      socket.on("error", () => { this.failAll(new Error("cdp_connection_failed")); settle(classified("runtime", "cdp_connection_failed", true)); });
      socket.on("close", () => { this.failAll(new Error("cdp_connection_closed")); if (!this.handshaken) settle(classified("runtime", "cdp_connection_failed", true)); });
      socket.on("data", (chunk) => {
        if (!this.handshaken) {
          this.handshakeBuffer = Buffer.concat([this.handshakeBuffer, chunk]);
          const end = this.handshakeBuffer.indexOf("\r\n\r\n");
          if (end < 0) return;
          const header = this.handshakeBuffer.subarray(0, end).toString("ascii");
          const acceptLine = header.split("\r\n").find((line) => /^sec-websocket-accept:/iu.test(line));
          if (!header.split("\r\n")[0]?.includes(" 101 ") || acceptLine?.split(":", 2)[1]?.trim() !== expectedAccept) {
            settle(classified("runtime", "cdp_handshake_invalid", true)); socket.destroy(); return;
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
      socket.once("connect", () => socket.write(`GET ${this.url.pathname}${this.url.search} HTTP/1.1\r\nHost: ${this.url.hostname}:${this.url.port}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ${key}\r\nSec-WebSocket-Version: 13\r\n\r\n`));
      const abort = () => { socket.destroy(); settle(cancelledError()); };
      this.signal?.addEventListener("abort", abort, { once: true });
    });
  }

  consumeFrames() {
    while (this.buffer.length >= 2) {
      const first = this.buffer[0]; const second = this.buffer[1]; const masked = (second & 0x80) !== 0;
      let length = second & 0x7f; let offset = 2;
      if (length === 126) { if (this.buffer.length < 4) return; length = this.buffer.readUInt16BE(2); offset = 4; }
      else if (length === 127) { if (this.buffer.length < 10) return; const high = this.buffer.readUInt32BE(2); const low = this.buffer.readUInt32BE(6); if (high > 0x1fffff) { this.failAll(new Error("cdp_frame_too_large")); return; } length = high * 0x100000000 + low; offset = 10; }
      const payloadOffset = masked ? offset + 4 : offset; const frameLength = payloadOffset + length;
      if (this.buffer.length < frameLength) return;
      let payload = this.buffer.subarray(payloadOffset, frameLength);
      if (masked) { const mask = this.buffer.subarray(offset, offset + 4); payload = Buffer.from(payload); for (let i = 0; i < payload.length; i += 1) payload[i] ^= mask[i % 4]; }
      this.buffer = this.buffer.subarray(frameLength);
      const opcode = first & 0x0f;
      if (opcode === 0x8) { this.close(); return; }
      if (opcode === 0x9) { this.writeFrame(0xA, payload); continue; }
      if (opcode === 0xA) continue;
      if (opcode === 0x0) { this.fragments.push(payload); if ((first & 0x80) !== 0) { this.handleText(Buffer.concat(this.fragments)); this.fragments = []; } }
      else if (opcode === 0x1) { if ((first & 0x80) === 0) this.fragments = [payload]; else this.handleText(payload); }
    }
  }

  handleText(payload) {
    let message; try { message = JSON.parse(payload.toString("utf8")); } catch { this.failAll(new Error("cdp_message_invalid")); return; }
    if (!Number.isInteger(message.id)) return;
    const pending = this.pending.get(message.id); if (!pending) return;
    this.pending.delete(message.id); clearTimeout(pending.timer);
    if (message.error) pending.reject(classified("runtime", "cdp_command_failed", true)); else pending.resolve(message.result || {});
  }

  writeFrame(opcode, payload) {
    if (!this.socket || this.closed) return;
    const body = Buffer.isBuffer(payload) ? payload : Buffer.from(payload); const mask = randomBytes(4);
    let header;
    if (body.length < 126) header = Buffer.from([0x80 | opcode, 0x80 | body.length]);
    else if (body.length <= 0xffff) { header = Buffer.alloc(4); header[0] = 0x80 | opcode; header[1] = 0x80 | 126; header.writeUInt16BE(body.length, 2); }
    else { header = Buffer.alloc(10); header[0] = 0x80 | opcode; header[1] = 0x80 | 127; header.writeUInt32BE(Math.floor(body.length / 0x100000000), 2); header.writeUInt32BE(body.length >>> 0, 6); }
    const masked = Buffer.from(body); for (let i = 0; i < masked.length; i += 1) masked[i] ^= mask[i % 4];
    this.socket.write(Buffer.concat([header, mask, masked]));
  }

  command(method, params = {}, sessionID = "", signal = this.signal) {
    if (!this.handshaken || this.closed) return Promise.reject(classified("runtime", "cdp_connection_closed", true));
    const id = ++this.nextCommandID;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(classified("transient", "cdp_command_timeout", true)); }, this.timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
      this.writeFrame(0x1, JSON.stringify({ id, method, params, ...(sessionID ? { sessionId: sessionID } : {}) }));
      if (signal) signal.addEventListener("abort", () => { if (this.pending.delete(id)) { clearTimeout(timer); reject(cancelledError()); } }, { once: true });
    });
  }

  failAll(error) { for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(error); } this.pending.clear(); }
  close() { if (this.closed) return; this.closed = true; this.failAll(new Error("cdp_connection_closed")); try { this.socket?.destroy(); } catch {} }
}

export async function probePage(socket, url, evaluateExpression, signal) {
  let targetID = ""; let sessionID = "";
  try {
    const target = await socket.command("Target.createTarget", { url }, "", signal);
    targetID = typeof target.targetId === "string" ? target.targetId : "";
    if (!targetID) throw classified("runtime", "cdp_target_missing", true);
    const attached = await socket.command("Target.attachToTarget", { targetId: targetID, flatten: true }, "", signal);
    sessionID = typeof attached.sessionId === "string" ? attached.sessionId : "";
    if (!sessionID) throw classified("runtime", "cdp_session_missing", true);
    const evaluated = await socket.command("Runtime.evaluate", { expression: evaluateExpression, returnByValue: true, awaitPromise: true }, sessionID, signal);
    return evaluated.result?.value ?? evaluated.result?.result?.value ?? {};
  } finally {
    if (targetID) { try { await socket.command("Target.closeTarget", { targetId: targetID }, "", signal); } catch {} }
  }
}
