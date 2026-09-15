import type { LauncherSettings } from "../state/types.js";
import { byId } from "../utilities/dom.js";

export function renderSettings(settings: LauncherSettings | null): void {
  if (!settings) return;
  byId<HTMLInputElement>("auto-check").checked = settings.auto_check_updates;
  byId<HTMLInputElement>("auto-repair").checked = settings.auto_repair;
  byId<HTMLSelectElement>("channel").value = settings.update_channel || "nightly";
  byId<HTMLInputElement>("login").checked = settings.launch_on_login;
  byId<HTMLInputElement>("tray").checked = settings.close_to_tray;
  byId<HTMLInputElement>("interval").value = String(Math.round(Number(settings.check_interval || 0) / 60000000000));
}
