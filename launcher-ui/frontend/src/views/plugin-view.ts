import type { PluginState } from "../state/types.js";
import { pluginRow } from "../components/plugin-row.js";
import { createElement } from "../utilities/dom.js";

export function renderPlugins(target: HTMLElement, plugins: PluginState[]): void {
  if (plugins.length === 0) {
    target.replaceChildren(createElement("div", "empty", "No plugins are declared by this release."));
    return;
  }
  target.replaceChildren(...plugins.map(pluginRow));
}
