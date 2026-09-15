export interface Store<S> {
  get(): S;
  update(updater: (state: S) => S): void;
  subscribe(listener: (state: S) => void): () => void;
}

export function createStore<S>(initial: S): Store<S> {
  let state = initial;
  const listeners = new Set<(next: S) => void>();
  return {
    get: () => state,
    update(updater) {
      state = updater(state);
      listeners.forEach((listener) => listener(state));
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    }
  };
}
