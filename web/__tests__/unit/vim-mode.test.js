/**
 * Unit tests for lib/vim-mode.js - circuit breaker and mode detection.
 * These tests exercise the real exported functions from the shared module.
 */
import {
  getVimModeState,
  setVimModeState,
  recordVimFailure,
  resetVimCircuitBreaker,
  getEditorPreference,
  setEditorPreference,
  shouldUseVim,
  FAILURE_THRESHOLD,
  DAY_MS,
} from '../../lib/vim-mode.js';

// Simple in-memory storage stub
function makeStorage(initial = {}) {
  const store = { ...initial };
  return {
    getItem: (k) => (k in store ? store[k] : null),
    setItem: (k, v) => { store[k] = String(v); },
    removeItem: (k) => { delete store[k]; },
    _store: store,
  };
}

describe('getVimModeState', () => {
  test('returns default state when storage is empty', () => {
    const s = makeStorage();
    expect(getVimModeState(s)).toEqual({ enabled: true, failures: 0, lastFailure: 0 });
  });

  test('returns stored state', () => {
    const s = makeStorage({ 'nr.vimMode': JSON.stringify({ enabled: false, failures: 3, lastFailure: 123 }) });
    expect(getVimModeState(s)).toEqual({ enabled: false, failures: 3, lastFailure: 123 });
  });

  test('returns default state on corrupt JSON', () => {
    const s = makeStorage({ 'nr.vimMode': 'not-json{' });
    expect(getVimModeState(s)).toEqual({ enabled: true, failures: 0, lastFailure: 0 });
  });

  test('returns default state when storage.getItem throws', () => {
    const s = { getItem: () => { throw new Error('denied'); }, setItem: () => {} };
    expect(getVimModeState(s)).toEqual({ enabled: true, failures: 0, lastFailure: 0 });
  });

  test('returns default state when storage is null', () => {
    expect(getVimModeState(null)).toEqual({ enabled: true, failures: 0, lastFailure: 0 });
  });

  test('defaultStorage falls back to null when localStorage throws', () => {
    // Test the internal defaultStorage() path by calling without storage arg
    // in an environment where globalThis.localStorage is inaccessible.
    // Simulate by deleting globalThis.localStorage temporarily.
    const saved = globalThis.localStorage;
    try {
      Object.defineProperty(globalThis, 'localStorage', {
        get() { throw new Error('SecurityError: blocked'); },
        configurable: true,
      });
      // With no explicit storage arg, defaultStorage() returns null,
      // so getVimModeState falls back to the default state.
      expect(getVimModeState()).toEqual({ enabled: true, failures: 0, lastFailure: 0 });
    } finally {
      Object.defineProperty(globalThis, 'localStorage', {
        value: saved,
        writable: true,
        configurable: true,
      });
    }
  });
});

describe('setVimModeState', () => {
  test('persists state as JSON', () => {
    const s = makeStorage();
    setVimModeState({ enabled: false, failures: 2, lastFailure: 999 }, s);
    expect(JSON.parse(s.getItem('nr.vimMode'))).toEqual({ enabled: false, failures: 2, lastFailure: 999 });
  });

  test('swallows storage errors (quota, privacy mode)', () => {
    const s = { getItem: () => null, setItem: () => { throw new Error('QuotaExceeded'); } };
    expect(() => setVimModeState({ enabled: true, failures: 0, lastFailure: 0 }, s)).not.toThrow();
  });

  test('handles null storage gracefully', () => {
    expect(() => setVimModeState({ enabled: true, failures: 0, lastFailure: 0 }, null)).not.toThrow();
  });

  test('setVimModeState uses default storage when no storage arg', () => {
    // jsdom provides globalThis.localStorage, so defaultStorage() returns it
    setVimModeState({ enabled: true, failures: 1, lastFailure: 42 });
    expect(JSON.parse(localStorage.getItem('nr.vimMode')).failures).toBe(1);
  });

  test('setEditorPreference uses default storage when no storage arg', () => {
    setEditorPreference('vim');
    expect(localStorage.getItem('nr.editorPref')).toBe('vim');
  });

  test('getEditorPreference uses default storage when no storage arg', () => {
    localStorage.setItem('nr.editorPref', 'auto');
    expect(getEditorPreference()).toBe('auto');
  });

  test('resetVimCircuitBreaker uses default storage when no storage arg', () => {
    localStorage.setItem('nr.vimMode', JSON.stringify({ enabled: false, failures: 3, lastFailure: 1 }));
    const state = resetVimCircuitBreaker();
    expect(state).toEqual({ enabled: true, failures: 0, lastFailure: 0 });
  });

  test('recordVimFailure uses default now and storage when omitted', () => {
    // First clear any leftover state
    localStorage.removeItem('nr.vimMode');
    const state = recordVimFailure(); // no args: uses Date.now() and defaultStorage()
    expect(state.failures).toBe(1);
    expect(state.lastFailure).toBeGreaterThan(0);
  });

  test('shouldUseVim with no opts uses defaults', () => {
    // No storage, no checkCompatibility, no viewportWidth -> uses window
    localStorage.removeItem('nr.editorPref');
    localStorage.removeItem('nr.vimMode');
    Object.defineProperty(window, 'innerWidth', { value: 1024, configurable: true });
    expect(shouldUseVim()).toBe(true);
  });
});

describe('recordVimFailure', () => {
  test('increments failures and sets lastFailure', () => {
    const s = makeStorage();
    const now = 1700000000000;
    const state = recordVimFailure(now, s);
    expect(state.failures).toBe(1);
    expect(state.lastFailure).toBe(now);
    expect(state.enabled).toBe(true);
  });

  test('disables after reaching failure threshold', () => {
    const s = makeStorage();
    recordVimFailure(1000, s);
    recordVimFailure(2000, s);
    expect(getVimModeState(s).enabled).toBe(true);

    const state = recordVimFailure(3000, s);
    expect(state.failures).toBe(FAILURE_THRESHOLD);
    expect(state.enabled).toBe(false);
    expect(getVimModeState(s).enabled).toBe(false);
  });

  test('stays disabled after exceeding threshold', () => {
    const s = makeStorage();
    for (let i = 0; i < 5; i++) recordVimFailure(1000 + i, s);
    const state = getVimModeState(s);
    expect(state.enabled).toBe(false);
    expect(state.failures).toBe(5);
  });

  test('persists state after each failure', () => {
    const s = makeStorage();
    recordVimFailure(1000, s);
    expect(s.getItem('nr.vimMode')).not.toBeNull();
  });
});

describe('resetVimCircuitBreaker', () => {
  test('clears failures and re-enables vim', () => {
    const s = makeStorage({ 'nr.vimMode': JSON.stringify({ enabled: false, failures: 5, lastFailure: 999 }) });
    const state = resetVimCircuitBreaker(s);
    expect(state).toEqual({ enabled: true, failures: 0, lastFailure: 0 });
    expect(getVimModeState(s)).toEqual({ enabled: true, failures: 0, lastFailure: 0 });
  });
});

describe('getEditorPreference', () => {
  test('returns stored preference', () => {
    const s = makeStorage({ 'nr.editorPref': 'vim' });
    expect(getEditorPreference(s)).toBe('vim');
  });

  test('defaults to auto when unset', () => {
    const s = makeStorage();
    expect(getEditorPreference(s)).toBe('auto');
  });

  test('defaults to auto when storage throws', () => {
    const s = { getItem: () => { throw new Error('nope'); } };
    expect(getEditorPreference(s)).toBe('auto');
  });
});

describe('setEditorPreference', () => {
  test('persists preference', () => {
    const s = makeStorage();
    setEditorPreference('textarea', s);
    expect(s.getItem('nr.editorPref')).toBe('textarea');
  });

  test('swallows storage errors', () => {
    const s = { getItem: () => null, setItem: () => { throw new Error('full'); } };
    expect(() => setEditorPreference('vim', s)).not.toThrow();
  });
});

describe('shouldUseVim', () => {
  const okCompat = () => undefined;
  const badCompat = () => 'SharedArrayBuffer unsupported';

  test('returns true on desktop with compatible browser and no preference', () => {
    const s = makeStorage();
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 1024 })).toBe(true);
  });

  test('returns false when preference is textarea', () => {
    const s = makeStorage({ 'nr.editorPref': 'textarea' });
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 1024 })).toBe(false);
  });

  test('falls back to the desktop width when window is unavailable', () => {
    const desc = Object.getOwnPropertyDescriptor(globalThis, 'window');
    Object.defineProperty(globalThis, 'window', { value: undefined, configurable: true, writable: true });
    try {
      const s = makeStorage();
      expect(shouldUseVim({ storage: s, checkCompatibility: okCompat })).toBe(true);
      expect(shouldUseVim({ storage: makeStorage({ 'nr.editorPref': 'textarea' }), checkCompatibility: okCompat })).toBe(false);
    } finally {
      if (desc) Object.defineProperty(globalThis, 'window', desc);
      else delete globalThis.window;
    }
  });

  test('returns false on narrow viewport with auto preference', () => {
    const s = makeStorage();
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 500 })).toBe(false);
  });

  test('returns true on narrow viewport when preference is vim', () => {
    const s = makeStorage({ 'nr.editorPref': 'vim' });
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 500 })).toBe(true);
  });

  test('returns false when browser is incompatible', () => {
    const s = makeStorage();
    expect(shouldUseVim({ storage: s, checkCompatibility: badCompat, viewportWidth: 1024 })).toBe(false);
  });

  test('returns false when circuit breaker is open (disabled)', () => {
    const s = makeStorage({ 'nr.vimMode': JSON.stringify({ enabled: false, failures: 3, lastFailure: Date.now() }) });
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 1024 })).toBe(false);
  });

  test('auto-resets failure counter after DAY_MS', () => {
    const now = 1700000000000;
    const old = now - DAY_MS - 1;
    const s = makeStorage({ 'nr.vimMode': JSON.stringify({ enabled: true, failures: 3, lastFailure: old }) });
    
    const result = shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 1024, now });
    
    expect(result).toBe(true);
    expect(getVimModeState(s).failures).toBe(0);
  });

  test('does not auto-reset before DAY_MS elapses', () => {
    const now = 1700000000000;
    const recent = now - 3600000; // 1 hour ago
    const s = makeStorage({ 'nr.vimMode': JSON.stringify({ enabled: true, failures: 2, lastFailure: recent }) });
    
    const result = shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 1024, now });
    
    expect(result).toBe(true); // still enabled (only 2 failures)
    expect(getVimModeState(s).failures).toBe(2);
  });

  test('viewport boundary: exactly 768px uses vim', () => {
    const s = makeStorage();
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 768 })).toBe(true);
  });

  test('viewport boundary: 767px uses textarea', () => {
    const s = makeStorage();
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 767 })).toBe(false);
  });

  test('incompatible browser overrides explicit vim preference', () => {
    const s = makeStorage({ 'nr.editorPref': 'vim' });
    expect(shouldUseVim({ storage: s, checkCompatibility: badCompat, viewportWidth: 1024 })).toBe(false);
  });

  test('disabled circuit breaker overrides explicit vim preference', () => {
    const s = makeStorage({
      'nr.editorPref': 'vim',
      'nr.vimMode': JSON.stringify({ enabled: false, failures: 3, lastFailure: Date.now() }),
    });
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat, viewportWidth: 1024 })).toBe(false);
  });

  test('shouldUseVim uses default storage when none provided', () => {
    // In jsdom, globalThis.localStorage exists, so this exercises the
    // defaultStorage() happy path inside shouldUseVim.
    localStorage.setItem('nr.editorPref', 'textarea');
    expect(shouldUseVim({ checkCompatibility: okCompat, viewportWidth: 1024 })).toBe(false);
    localStorage.removeItem('nr.editorPref');
  });

  test('shouldUseVim handles missing checkCompatibility option', () => {
    // No checkCompatibility provided: defaults to () => undefined (pass)
    const s = makeStorage();
    expect(shouldUseVim({ storage: s, viewportWidth: 1024 })).toBe(true);
  });

  test('shouldUseVim uses window.innerWidth when viewportWidth not provided', () => {
    const s = makeStorage();
    Object.defineProperty(window, 'innerWidth', { value: 1024, configurable: true });
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat })).toBe(true);
    Object.defineProperty(window, 'innerWidth', { value: 500, configurable: true });
    expect(shouldUseVim({ storage: s, checkCompatibility: okCompat })).toBe(false);
  });
});
