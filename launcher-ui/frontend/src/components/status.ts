import { createElement } from "../utilities/dom.js";

export function statusBadge(label: string, modifier: string): HTMLSpanElement {
  const status = createElement("span", `status ${modifier.replaceAll(" ", "_")}`, label);
  return status;
}
