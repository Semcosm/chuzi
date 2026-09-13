import { createInterface } from "node:readline";

const testDebug = process.env.CHUZI_TEST_DEBUG === "1";
const waitReportIntervalMs = 5000;
const maxTailBytes = 16 * 1024;
const maxTraceEntries = 48;
const readers = new WeakMap();

function appendTail(current, chunk) {
  const next = current + chunk;
  if (Buffer.byteLength(next, "utf8") <= maxTailBytes) return next;
  return Buffer.from(next, "utf8").subarray(-maxTailBytes).toString("utf8");
}

function formatMessage(message) {
  return `${message.type || "unknown"} id=${message.id || "unknown"}`;
}

export function createReader(child, label) {
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  const queue = [];
  const waiters = [];
  const trace = [];
  let stderrTail = "";
  let closed = false;
  let closeError;
  let exitState;
  let resolveExit;

  lines.child = child;
  lines.label = label;
  readers.set(child, lines);
  lines.exit = new Promise((resolveExitState) => {
    resolveExit = resolveExitState;
  });

  const record = (entry) => {
    trace.push(entry);
    if (trace.length > maxTraceEntries) trace.shift();
  };

  const diagnostics = (waitingFor = "none") => {
    const status = exitState
      ? `exited code=${exitState.code ?? "null"} signal=${exitState.signal ?? "null"}`
      : `running pid=${child.pid ?? "unknown"} exitCode=${child.exitCode ?? "null"} signal=${child.signalCode ?? "null"}`;
    const recentTrace = trace.length ? trace.join(" | ") : "<none>";
    const stderr = stderrTail.trimEnd() || "<empty>";
    return [
      `[${label}] waiting_for=${waitingFor}`,
      `[${label}] child=${status} queued=${queue.length} waiters=${waiters.length}`,
      `[${label}] protocol_trace=${recentTrace}`,
      `[${label}] stderr_tail=${stderr}`,
    ].join("\n");
  };

  const fail = (error) => {
    if (closed) return;
    closed = true;
    closeError = new Error(`${error.message}\n${diagnostics()}`, { cause: error });
    for (const waiter of waiters.splice(0)) waiter.reject(closeError);
  };

  lines.on("line", (line) => {
    if (!line.trim()) return;
    let message;
    try {
      message = JSON.parse(line);
    } catch (error) {
      fail(new Error(`${label} emitted invalid JSON: ${error.message}; line=${line}`));
      return;
    }
    const formatted = formatMessage(message);
    record(`<- ${formatted}`);
    if (testDebug) process.stderr.write(`[${label}] ${formatted}\n`);
    const waiter = waiters.shift();
    if (waiter) waiter.resolve(message);
    else queue.push(message);
  });

  child.once("error", (error) => {
    record(`!! child error ${error.message}`);
    fail(new Error(`${label} child error: ${error.message}`));
  });
  const onExit = (code, signal) => {
    if (exitState) return;
    exitState = { code, signal };
    resolveExit(exitState);
    record(`!! child exit code=${code ?? "null"} signal=${signal ?? "null"}`);
  };
  const onClose = (code, signal) => {
    onExit(code, signal);
    fail(new Error(`${label} child exited before protocol message: code=${code ?? "null"} signal=${signal ?? "null"}`));
  };
  child.once("exit", onExit);
  child.once("close", onClose);
  // A very short-lived child can finish between spawn() and listener setup.
  // Check the cached status so a subsequent read never waits forever.
  if (child.exitCode !== null || child.signalCode !== null) {
    onClose(child.exitCode, child.signalCode);
  }

  child.stderr?.setEncoding("utf8");
  child.stderr?.on("data", (chunk) => {
    stderrTail = appendTail(stderrTail, chunk);
    if (testDebug) process.stderr.write(`[${label}:stderr] ${chunk}`);
  });
  // A piped stderr stream must be consumed even when diagnostics are disabled,
  // otherwise a noisy child can block on a full pipe and look like a protocol
  // deadlock.
  child.stderr?.resume();

  lines.nextMessage = (waitingFor = "next protocol message") => {
    if (queue.length) return Promise.resolve(queue.shift());
    if (closed) return Promise.reject(closeError);
    if (testDebug) process.stderr.write(`${diagnostics(waitingFor)}\n`);
    return new Promise((resolveMessage, rejectMessage) => {
      let reportTimer;
      const settle = (settleMessage) => {
        if (reportTimer) clearInterval(reportTimer);
        settleMessage();
      };
      if (testDebug) {
        reportTimer = setInterval(() => {
          process.stderr.write(`${diagnostics(waitingFor)}\n`);
        }, waitReportIntervalMs);
        reportTimer.unref?.();
      }
      waiters.push({
        resolve: (message) => settle(() => resolveMessage(message)),
        reject: (error) => settle(() => rejectMessage(error)),
      });
    });
  };
  lines.diagnostics = diagnostics;
  lines.record = record;
  lines.fail = fail;
  return lines;
}

export function sendMessage(child, label, message, lines = null) {
  const formatted = formatMessage(message);
  const reader = lines?.label === label ? lines : readers.get(child);
  reader?.record?.(`-> ${formatted}`);
  if (testDebug) process.stderr.write(`[${label}] -> ${formatted}\n`);
  if (child.exitCode !== null || child.signalCode !== null || child.stdin?.destroyed) {
    const detail = reader?.diagnostics?.(`send ${formatted}`) || `child exited code=${child.exitCode ?? "null"} signal=${child.signalCode ?? "null"}`;
    throw new Error(`${label} cannot send ${formatted}\n${detail}`);
  }
  try {
    child.stdin.write(JSON.stringify(message) + "\n");
  } catch (error) {
    const detail = reader?.diagnostics?.(`send ${formatted}`) || error.message;
    throw new Error(`${label} failed to send ${formatted}: ${error.message}\n${detail}`, { cause: error });
  }
}

export function readMessage(lines, waitingFor = "next protocol message") {
  return lines.nextMessage(waitingFor);
}

export async function readType(lines, type) {
  while (true) {
    const message = await readMessage(lines, `message type=${type}`);
    if (message.type === type) return message;
  }
}

export function waitForExit(child, lines) {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve();
  if (lines?.exit) return lines.exit;
  return new Promise((resolveExit) => child.once("exit", resolveExit));
}
