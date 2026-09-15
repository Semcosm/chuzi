import test from "node:test";
import assert from "node:assert/strict";
import { LauncherClient } from "../frontend/dist/services/ipc.js";
import { LauncherController } from "../frontend/dist/features/launcher-controller.js";

const PROTOCOL = "chuzi.launcher-ui/v1";

class FakeTransport {
  requests = [];
  pending = new Map();

  request(request) {
    this.requests.push(request);
    return new Promise((resolve) => this.pending.set(request.id, resolve));
  }

  onProgress() {
    return Promise.resolve(() => {});
  }

  resolveNext(action, data, ok = true, error) {
    const request = this.requests.find((candidate) => candidate.action === action && this.pending.has(candidate.id));
    assert.ok(request, `request ${action} was not sent`);
    const resolve = this.pending.get(request.id);
    this.pending.delete(request.id);
    resolve({ protocol: PROTOCOL, type: "result", id: request.id, ok, data, error });
  }

  async waitFor(action) {
    for (let attempt = 0; attempt < 100; attempt += 1) {
      const request = this.requests.find((candidate) => candidate.action === action && this.pending.has(candidate.id));
      if (request) return request;
      await new Promise((resolve) => setImmediate(resolve));
    }
    assert.fail(`request ${action} was not sent`);
  }
}

function controllerFor(transport) {
  const client = new LauncherClient(transport);
  return { client, controller: new LauncherController(client, () => {}) };
}

test("controller refreshes state in order and schedules one automatic update check", async () => {
  const transport = new FakeTransport();
  const { client, controller } = controllerFor(transport);
  await client.start();
  controller.refresh();

  transport.resolveNext("initialize", { first_run: false, components: [] });
  await transport.waitFor("component-list");
  transport.resolveNext("component-list", []);
  await transport.waitFor("settings");
  transport.resolveNext("settings", {
    auto_check_updates: true,
    auto_repair: false,
    update_channel: "nightly",
    launch_on_login: false,
    close_to_tray: false,
    check_interval: 0
  });
  await transport.waitFor("plugin-list");
  transport.resolveNext("plugin-list", []);
  const updateRequest = await transport.waitFor("check-update");
  assert.equal(controller.state.get().autoCheckRequested, true);
  transport.resolveNext("check-update", { available: false, reason: "up_to_date" });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(controller.state.get().update.reason, "up_to_date");
  assert.equal(updateRequest.action, "check-update");
  assert.equal(transport.requests.filter((request) => request.action === "check-update").length, 1);
  assert.equal(controller.state.get().refreshInFlight, false);
});

test("controller clears failed mutations without leaving the operation active", async () => {
  const transport = new FakeTransport();
  const { client, controller } = controllerFor(transport);
  await client.start();

  controller.startAction("component-remove", "service");
  const request = await transport.waitFor("component-remove");
  transport.resolveNext("component-remove", null, false, "launcher_operation_failed");
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(request.item, "service");
  assert.equal(controller.state.get().currentOperation, null);
  assert.equal(controller.state.get().error, "launcher_operation_failed");
  assert.equal(transport.requests.length, 1);
});
