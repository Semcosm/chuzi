import type { ComponentState, InitializationStatus, UpdateInfo } from "../state/types.js";
import { actionButton, createElement } from "../utilities/dom.js";
import { renderComponents } from "./component-view.js";

export function renderUpdate(target: HTMLElement, info: UpdateInfo | null): void {
  if (!info) {
    target.className = "empty";
    target.textContent = "No update check has run.";
    return;
  }
  if (info.available) {
    const version = info.manifest?.version ?? "new release";
    const commit = info.manifest?.commit ? ` (${info.manifest.commit.slice(0, 12)})` : "";
    target.className = "status update";
    target.textContent = `Update available: ${version}${commit}`;
    return;
  }
  target.className = "empty";
  target.textContent = info.reason === "up_to_date" ? "Already up to date." : "No update is available.";
}

export function renderWorkspaceStatus(target: HTMLElement, label: string): void {
  target.replaceChildren(document.createTextNode("Workspace status "));
  const strong = createElement("strong", undefined, label);
  target.append(strong);
}

export function renderOnboarding(target: HTMLElement, info: InitializationStatus | null, components: ComponentState[]): void {
  if (!info?.first_run) {
    target.hidden = true;
    return;
  }
  const choices = [...(info.required ?? []), ...(info.optional ?? [])];
  const choiceList = createElement("div");
  choices.forEach((id) => {
    const component = components.find((item) => item.id === id) ?? {
      id,
      installed: false,
      enabled: false,
      required: (info.required ?? []).includes(id)
    };
    const label = createElement("label", "choice");
    const checkbox = createElement("input") as HTMLInputElement;
    checkbox.type = "checkbox";
    checkbox.dataset.choice = id;
    checkbox.checked = Boolean(component.required || !component.installed);
    checkbox.disabled = Boolean(component.required);
    const copy = createElement("span");
    copy.append(
      createElement("strong", undefined, id),
      createElement("small", undefined, component.required ? "Required" : "Optional component")
    );
    label.append(checkbox, copy);
    choiceList.append(label);
  });
  const head = createElement("div", "panel-head");
  head.append(createElement("h3", undefined, "First launch"));
  const body = createElement("div", "panel-body");
  if (choices.length > 0) {
    body.append(choiceList);
  } else {
    body.append(createElement("div", "empty", "The required launcher is ready."));
  }
  const actions = createElement("div", "actions onboarding-actions");
  actions.append(actionButton("Install selected and continue", "finish-onboarding", undefined, "primary"));
  body.append(actions);
  target.replaceChildren(head, body);
  target.hidden = false;
}

export function renderOverviewComponents(target: HTMLElement, components: ComponentState[]): void {
  renderComponents(target, components);
}
