import test from "node:test";
import assert from "node:assert/strict";
import { LauncherClient } from "../frontend/dist/services/ipc.js";

class FakeTransport {
  requests = [];
  listeners = [];

  request(request) {
    this.requests.push(request);
    return new Promise((resolve) => {
      this.listeners.push(() => resolve({ protocol: "chuzi.launcher-ui/v1", type: "result", id: request.id, ok: true, data: null }));
    });
  }

  onProgress() {
    return Promise.resolve(() => {});
  }
}

test("launcher client serializes requests and keeps cancellation out of the queue", async () => {
  const transport = new FakeTransport();
  const client = new LauncherClient(transport);
  const results = [];
  client.onResult((event) => results.push(event));
  await client.start();
  const first = client.send("component-list");
  const second = client.send("settings");
  const cancel = client.send("cancel", { target_id: first });
  assert.equal(transport.requests.length, 2);
  assert.equal(transport.requests[0].id, first);
  assert.equal(transport.requests[1].id, cancel);
  transport.listeners[1]();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(results[0].id, cancel);
  transport.listeners[0]();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(transport.requests[2].id, second);
  transport.listeners[2]();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(results.map((event) => event.id), [cancel, first, second]);
});
