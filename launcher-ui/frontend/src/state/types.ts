export const PROTOCOL = "chuzi.launcher-ui/v1" as const;

export type Theme = "light" | "dark";
export type Material = "solid" | "frosted" | "mica" | "liquid";
export type MaterialOpacityKey = Exclude<Material, "solid">;

export interface MaterialOpacityPreferences {
  frosted: number;
  mica: number;
  liquid: number;
}

export interface AppearancePreferences {
  theme: Theme;
  /** The material used by content surfaces unless they declare another role. */
  material: Material;
  /** A shared material for the custom title bar and the sidebar. */
  chromeMaterial: Material;
  opacity: MaterialOpacityPreferences;
}

export interface ComponentState {
  id: string;
  version?: string;
  required?: boolean;
  installed: boolean;
  enabled: boolean;
  health?: string;
}

export interface PluginDescriptor {
  id?: string;
  version?: string;
  api?: string;
  capabilities?: string[];
  permissions?: string[];
  signed_by?: string;
  installable?: boolean;
}

export interface PluginState {
  descriptor: PluginDescriptor;
  installed: boolean;
  enabled: boolean;
  trusted: boolean;
  health?: string;
}

export interface LauncherSettings {
  auto_check_updates: boolean;
  auto_repair: boolean;
  update_channel: string;
  launch_on_login: boolean;
  close_to_tray: boolean;
  check_interval: number;
}

export interface InitializationStatus {
  first_run: boolean;
  required?: string[];
  optional?: string[];
  components?: ComponentState[];
  [key: string]: unknown;
}

export interface UpdateInfo {
  available?: boolean;
  reason?: string;
  manifest?: { version?: string; commit?: string };
  [key: string]: unknown;
}

export interface ProgressEvent {
  protocol: typeof PROTOCOL;
  type: "progress";
  id: string;
  operation: string;
  stage: string;
  item?: string;
  completed: number;
  total: number;
}

export interface ResultEvent<T = unknown> {
  protocol: typeof PROTOCOL;
  type: "result";
  id: string;
  ok: boolean;
  data?: T;
  error?: string;
}

export interface AppState {
  components: ComponentState[];
  plugins: PluginState[];
  settings: LauncherSettings | null;
  initialization: InitializationStatus | null;
  update: UpdateInfo | null;
  activeView: "overview" | "components" | "plugins" | "settings";
  refreshInFlight: boolean;
  refreshPending: Set<string>;
  autoCheckRequested: boolean;
  installQueue: string[];
  installing: string | null;
  currentOperation: string | null;
  progress: string;
  error: string;
}

export const initialState = (): AppState => ({
  components: [],
  plugins: [],
  settings: null,
  initialization: null,
  update: null,
  activeView: "overview",
  refreshInFlight: false,
  refreshPending: new Set(),
  autoCheckRequested: false,
  installQueue: [],
  installing: null,
  currentOperation: null,
  progress: "",
  error: ""
});
