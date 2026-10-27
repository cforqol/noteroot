/**
 * Built-in UI plugin system.
 *
 * Each plugin is a plain object:
 *   { id, name, description, init(slug), destroy() }
 *
 * Enabled state is stored per-notebook in localStorage.
 * Plugins are initialised/destroyed by the host (app.js) when a
 * notebook opens or settings change.
 */

const STORAGE_PREFIX = "noteroot_uiplugins_";

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
