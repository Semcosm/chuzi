import type { PluginState } from "../state/types.js";
import { actionButton, createElement } from "../utilities/dom.js";
import { statusBadge } from "./status.js";

function statusLabel(plugin: PluginState): string {
  if (!plugin.installed) return "not installed";
  if (!plugin.trusted) return "untrusted";
  return plugin.enabled ? plugin.health ?? "healthy" : "disabled";
}

export function pluginRow(plugin: PluginState): HTMLElement {
  const descriptor = plugin.descriptor;
  const id = descriptor.id ?? "unknown plugin";
  const row = createElement("div", "component material-surface");
  row.dataset.surfaceRole = "secondary";
  row.dataset.optical = "interactive";
  const icon = createElement("div", "component-icon", id.slice(0, 2).toUpperCase());
  icon.setAttribute("aria-hidden", "true");
  const details = createElement("div");
  const capabilities = descriptor.capabilities?.join(", ") || "none";
  const permissions = descriptor.permissions?.join(", ") || "none";
  details.append(
    createElement("div", "component-name", id),
    createElement("div", "component-meta", [
      descriptor.version ?? "unknown",
      descriptor.api ?? "unknown API",
      `capabilities: ${capabilities}`,
      `permissions: ${permissions}`,
      `signer: ${descriptor.signed_by ?? "unsigned"}`
    ].join(" | "))
  );
  const actions = createElement("div", "actions");
  if (!plugin.installed && descriptor.installable) actions.append(actionButton("Install", "plugin-install", id, "primary"));
  if (plugin.installed) actions.append(actionButton(plugin.trusted ? "Untrust" : "Trust", `plugin-${plugin.trusted ? "untrust" : "trust"}`, id));
  if (plugin.installed && plugin.trusted) actions.append(actionButton(plugin.enabled ? "Disable" : "Enable", `plugin-${plugin.enabled ? "disable" : "enable"}`, id));
  if (plugin.installed) actions.append(actionButton("Remove", "plugin-remove", id, "danger"));
  row.append(icon, details, statusBadge(statusLabel(plugin), statusLabel(plugin)), actions);
  return row;
}
