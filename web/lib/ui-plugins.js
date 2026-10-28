/**
 * Built-in UI plugin system.
 *
 * Each plugin is a plain object:
 *   { id, name, description, init(slug), destroy() }
 *
 * Two kinds of browser-local state:
 *   - enabled state, per notebook (`noteroot_uiplugins_<slug>`)
 *   - plugin preferences, per user (`noteroot_uipluginprefs`), so a
 *     preference such as the Pomodoro phase lengths follows you across
 *     notebooks and survives a restart
 *
 * Plugins are initialised/destroyed by the host (app.js) when a
 * notebook opens or settings change.
 */

const STORAGE_PREFIX = "noteroot_uiplugins_";
const PREFS_KEY = "noteroot_uipluginprefs";

export function getUiPluginEnabled(slug, id) {
  try {
    return JSON.parse(localStorage.getItem(`${STORAGE_PREFIX}${slug}`) || "{}")[id] === true;
  } catch {
    return false;
  }
}

export function setUiPluginEnabled(slug, id, enabled) {
  try {
    const key = `${STORAGE_PREFIX}${slug}`;
    const state = JSON.parse(localStorage.getItem(key) || "{}");
    state[id] = Boolean(enabled);
    localStorage.setItem(key, JSON.stringify(state));
  } catch { /* storage unavailable */ }
}

/**
 * Preferences for one plugin, as a plain object. Missing or corrupt
 * storage yields {} so callers can spread defaults over the result.
 */
export function getUiPluginPrefs(id) {
  try {
    const prefs = JSON.parse(localStorage.getItem(PREFS_KEY) || "{}")[id];
    return prefs && typeof prefs === "object" && !Array.isArray(prefs) ? prefs : {};
  } catch {
    return {};
  }
}

/** Replace the stored preferences of one plugin (other plugins untouched). */
export function setUiPluginPrefs(id, prefs) {
  try {
    const all = JSON.parse(localStorage.getItem(PREFS_KEY) || "{}");
    all[id] = prefs && typeof prefs === "object" && !Array.isArray(prefs) ? prefs : {};
    localStorage.setItem(PREFS_KEY, JSON.stringify(all));
  } catch { /* storage unavailable */ }
}

const _active = new Map(); // id → { destroy }

/**
 * Apply the current enabled-set: destroy plugins that should be off,
 * init plugins that should be on.
 */
export async function syncUiPlugins(slug, registry) {
  for (const plugin of registry) {
    const want = getUiPluginEnabled(slug, plugin.id);
    const running = _active.has(plugin.id);
    if (want && !running) {
      try {
        const handle = await plugin.init(slug);
        _active.set(plugin.id, handle || {});
      } catch (e) {
        console.error(`[ui-plugin] ${plugin.id} init failed`, e);
      }
    } else if (!want && running) {
      try {
        const handle = _active.get(plugin.id);
        if (typeof handle?.destroy === "function") handle.destroy();
      } catch (e) {
        console.error(`[ui-plugin] ${plugin.id} destroy failed`, e);
      }
      _active.delete(plugin.id);
    }
  }
}

/** Destroy all active plugins (e.g. on notebook switch). */
export function destroyAllUiPlugins() {
  for (const [id, handle] of _active) {
    try {
      if (typeof handle?.destroy === "function") handle.destroy();
    } catch (e) {
      console.error(`[ui-plugin] ${id} destroy failed`, e);
    }
  }
  _active.clear();
}
