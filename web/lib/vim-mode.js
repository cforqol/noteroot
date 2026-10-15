/**
 * Vim mode utilities: circuit breaker and mode detection.
 *
 * Pure logic with injectable dependencies so unit tests can exercise
 * the real functions instead of reimplementations. DOM-only concerns
 * (localStorage, window) are injected; defaults fall back to the real
 * globals so app.js can call these directly.
 */

const DAY_MS = 86400000; // 24 hours
const FAILURE_THRESHOLD = 3;
const DESKTOP_MIN_WIDTH = 768;

const DEFAULT_VIM_STATE = { enabled: true, failures: 0, lastFailure: 0 };

function defaultStorage() {
  try {
    return globalThis.localStorage;
  } catch {
    return null;
  }
}

/**
 * Read the persisted circuit-breaker state.
 * Returns the default (enabled) state on missing or corrupt storage.
 */
export function getVimModeState(storage = defaultStorage()) {
  try {
    const raw = storage?.getItem('nr.vimMode');
    return raw ? JSON.parse(raw) : { ...DEFAULT_VIM_STATE };
  } catch {
    return { ...DEFAULT_VIM_STATE };
  }
}

/**
 * Persist the circuit-breaker state. Storage errors (quota, privacy mode)
 * are swallowed: the editor must keep working without persistence.
 */
export function setVimModeState(state, storage = defaultStorage()) {
  try {
    storage?.setItem('nr.vimMode', JSON.stringify(state));
  } catch {
    /* ignore */
  }
}

/**
 * Record a vim-mode failure. Disables vim once the failure threshold is
 * reached. Returns the new state.
 */
export function recordVimFailure(now = Date.now(), storage = defaultStorage()) {
  const state = getVimModeState(storage);
  state.failures += 1;
  state.lastFailure = now;

  if (state.failures >= FAILURE_THRESHOLD) {
    state.enabled = false;
  }

  setVimModeState(state, storage);
  return state;
}

/**
 * Clear the circuit breaker (manual reset from settings).
 */
export function resetVimCircuitBreaker(storage = defaultStorage()) {
  const state = { enabled: true, failures: 0, lastFailure: 0 };
  setVimModeState(state, storage);
  return state;
}

/**
 * Read the user's editor preference: 'auto' | 'vim' | 'textarea'.
 */
export function getEditorPreference(storage = defaultStorage()) {
  try {
    return storage?.getItem('nr.editorPref') || 'auto';
  } catch {
    return 'auto';
  }
}

export function setEditorPreference(pref, storage = defaultStorage()) {
  try {
    storage?.setItem('nr.editorPref', pref);
  } catch {
    /* ignore */
  }
}

/**
 * Auto-reset the failure counter once the reset window has elapsed.
 * Mutates and persists the state. Returns the refreshed state.
 */
function maybeAutoReset(state, now, storage) {
  if (state.failures > 0 && now - state.lastFailure > DAY_MS) {
    state.failures = 0;
    setVimModeState(state, storage);
  }
  return state;
}

/**
 * Decide whether the vim editor should be used.
 *
 * Order of checks:
 *  1. User preference ('textarea' => never)
 *  2. Browser compatibility (SharedArrayBuffer + Atomics)
 *  3. Viewport width (skipped when preference is explicitly 'vim')
 *  4. Circuit breaker state (+ 24h auto-reset of the failure counter)
 *
 * @param {object} [opts]
 * @param {function} [opts.checkCompatibility] returns error string or undefined
 * @param {number} [opts.viewportWidth]
 * @param {number} [opts.now]
 * @param {object} [opts.storage]
 */
export function shouldUseVim(opts = {}) {
  const storage = opts.storage ?? defaultStorage();
  const checkCompatibility =
    opts.checkCompatibility ?? (() => undefined);
  const viewportWidth =
    opts.viewportWidth ??
    (typeof window !== 'undefined' ? window.innerWidth : DESKTOP_MIN_WIDTH);
  const now = opts.now ?? Date.now();

  const pref = getEditorPreference(storage);
  if (pref === 'textarea') {
    return false;
  }

  const compatError = checkCompatibility();
  if (compatError) {
    return false;
  }

  // Explicit vim preference skips the viewport gate (still subject to
  // browser compatibility and the circuit breaker).
  if (pref !== 'vim' && viewportWidth < DESKTOP_MIN_WIDTH) {
    return false;
  }

  const state = getVimModeState(storage);
  if (!state.enabled) {
    return false;
  }

  maybeAutoReset(state, now, storage);
  return true;
}

export { DAY_MS, FAILURE_THRESHOLD, DESKTOP_MIN_WIDTH, DEFAULT_VIM_STATE };
