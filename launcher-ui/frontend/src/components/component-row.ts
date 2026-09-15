import type { ComponentState } from "../state/types.js";
import { actionButton, createElement } from "../utilities/dom.js";
import { statusBadge } from "./status.js";

function statusLabel(component: ComponentState): string {
  return component.installed ? (component.enabled ? component.health ?? "healthy" : "disabled") : "not installed";
}

export function componentRow(component: ComponentState): HTMLElement {
  const row = createElement("div", "component material-surface");
  row.dataset.surfaceRole = "secondary";
  row.dataset.optical = "interactive";
  const icon = createElement("div", "component-icon", component.id.slice(0, 2).toUpperCase());
  icon.setAttribute("aria-hidden", "true");
  const details = createElement("div");
  details.append(
    createElement("div", "component-name", component.id),
    createElement("div", "component-meta", `${component.version ?? "not installed"} | ${component.required ? "required" : "optional"}`)
  );
  const actions = createElement("div", "actions");
  if (!component.installed) actions.append(actionButton("Install", "component-install", component.id, "primary"));
  if (component.installed) actions.append(actionButton(component.enabled ? "Disable" : "Enable", `component-${component.enabled ? "disable" : "enable"}`, component.id));
  if (component.installed && component.health === "missing") actions.append(actionButton("Repair", "repair"));
  if (component.installed && !component.required) actions.append(actionButton("Remove", "component-remove", component.id, "danger"));
  row.append(icon, details, statusBadge(statusLabel(component), statusLabel(component)), actions);
  return row;
}
