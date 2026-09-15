import type { ComponentState } from "../state/types.js";
import { componentRow } from "../components/component-row.js";
import { createElement } from "../utilities/dom.js";

export function renderComponents(target: HTMLElement, components: ComponentState[]): void {
  if (components.length === 0) {
    target.replaceChildren(createElement("div", "empty", "No components are declared by this release."));
    return;
  }
  target.replaceChildren(...components.map(componentRow));
}
