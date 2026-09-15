export function byId<T extends HTMLElement>(id: string): T {
  const element = document.getElementById(id);
  if (!element) throw new Error(`missing launcher element: ${id}`);
  return element as T;
}

export function createElement<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className?: string,
  text?: string
): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = text;
  return element;
}

export function setChildren(parent: Element, children: Node[]): void {
  const fragment = document.createDocumentFragment();
  children.forEach((child) => fragment.append(child));
  parent.replaceChildren(fragment);
}

export function actionButton(label: string, action: string, item?: string, className?: string): HTMLButtonElement {
  const button = createElement("button", className, label);
  button.type = "button";
  button.dataset.action = action;
  if (item) button.dataset.item = item;
  return button;
}
