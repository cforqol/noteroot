import { jest } from '@jest/globals';
import {
  getUiPluginEnabled,
  setUiPluginEnabled,
  getUiPluginPrefs,
  setUiPluginPrefs,
  syncUiPlugins,
  destroyAllUiPlugins,
} from '../../lib/ui-plugins.js';

afterEach(() => {
  destroyAllUiPlugins();
  jest.restoreAllMocks();
});

describe('enabled state (per notebook)', () => {
  test('defaults to off, round-trips, isolates notebooks', () => {
    expect(getUiPluginEnabled('notes', 'pomodoro')).toBe(false);
    setUiPluginEnabled('notes', 'pomodoro', true);
    expect(getUiPluginEnabled('notes', 'pomodoro')).toBe(true);
    expect(getUiPluginEnabled('other', 'pomodoro')).toBe(false);
    setUiPluginEnabled('notes', 'pomodoro', false);
    expect(getUiPluginEnabled('notes', 'pomodoro')).toBe(false);
  });

  test('only the requested plugin id is written', () => {
    setUiPluginEnabled('notes', 'calendar', true);
    setUiPluginEnabled('notes', 'pomodoro', true);
    expect(JSON.parse(localStorage.getItem('noteroot_uiplugins_notes')))
      .toEqual({ calendar: true, pomodoro: true });
  });

  test('corrupt storage degrades to off', () => {
    localStorage.setItem('noteroot_uiplugins_notes', 'not json');
    expect(getUiPluginEnabled('notes', 'pomodoro')).toBe(false);
  });

  test('storage failures never throw', () => {
    jest.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied'); });
    jest.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota'); });
    expect(getUiPluginEnabled('notes', 'pomodoro')).toBe(false);
    expect(() => setUiPluginEnabled('notes', 'pomodoro', true)).not.toThrow();
  });
});

describe('plugin preferences (per user)', () => {
  test('missing preferences are an empty object', () => {
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
  });

  test('round-trips and leaves other plugins alone', () => {
    setUiPluginPrefs('pomodoro', { work: 50, short: 5, long: 15 });
    setUiPluginPrefs('calendar', { firstDay: 1 });
    expect(getUiPluginPrefs('pomodoro')).toEqual({ work: 50, short: 5, long: 15 });
    expect(getUiPluginPrefs('calendar')).toEqual({ firstDay: 1 });
    setUiPluginPrefs('pomodoro', { work: 25 });
    expect(getUiPluginPrefs('pomodoro')).toEqual({ work: 25 });
    expect(getUiPluginPrefs('calendar')).toEqual({ firstDay: 1 });
  });

  test('is stored user-level, not per notebook', () => {
    setUiPluginPrefs('pomodoro', { work: 50 });
    expect(localStorage.getItem('noteroot_uipluginprefs')).toBe('{"pomodoro":{"work":50}}');
    expect(localStorage.getItem('noteroot_uiplugins_notes')).toBeNull();
  });

  test('corrupt or non-object storage reads as {}', () => {
    localStorage.setItem('noteroot_uipluginprefs', 'nope');
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
    localStorage.setItem('noteroot_uipluginprefs', '{"pomodoro":[1,2]}');
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
    localStorage.setItem('noteroot_uipluginprefs', '{"pomodoro":"50"}');
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
  });

  test('non-object writes are stored as {}', () => {
    setUiPluginPrefs('pomodoro', '50');
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
    setUiPluginPrefs('pomodoro', [25]);
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
  });

  test('storage failures never throw', () => {
    jest.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied'); });
    jest.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota'); });
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
    expect(() => setUiPluginPrefs('pomodoro', { work: 50 })).not.toThrow();
  });
});

describe('syncUiPlugins', () => {
  function fakePlugin(id, hooks = {}) {
    return {
      id,
      name: id,
      description: id,
      init: jest.fn(async () => hooks.handle || {}),
      ...hooks,
    };
  }

  test('inits an enabled plugin once and destroys it when disabled', async () => {
    const destroy = jest.fn();
    const plugin = fakePlugin('a', { handle: { destroy } });
    const registry = [plugin];
    setUiPluginEnabled('notes', 'a', true);

    await syncUiPlugins('notes', registry);
    expect(plugin.init).toHaveBeenCalledTimes(1);
    await syncUiPlugins('notes', registry);
    expect(plugin.init).toHaveBeenCalledTimes(1); // already running

    setUiPluginEnabled('notes', 'a', false);
    await syncUiPlugins('notes', registry);
    expect(destroy).toHaveBeenCalledTimes(1);

    setUiPluginEnabled('notes', 'a', true);
    await syncUiPlugins('notes', registry);
    expect(plugin.init).toHaveBeenCalledTimes(2);
  });

  test('a failing init is logged and leaves the plugin inactive', async () => {
    const errSpy = jest.spyOn(console, 'error').mockImplementation(() => {});
    const plugin = fakePlugin('bad', {
      init: jest.fn(async () => { throw new Error('boom'); }),
    });
    setUiPluginEnabled('notes', 'bad', true);

    await syncUiPlugins('notes', [plugin]);
    expect(errSpy).toHaveBeenCalled();

    // Not marked running, so a later sync retries rather than skipping.
    await syncUiPlugins('notes', [plugin]);
    expect(plugin.init).toHaveBeenCalledTimes(2);
  });

  test('a throwing destroy is logged and the plugin is forgotten', async () => {
    const errSpy = jest.spyOn(console, 'error').mockImplementation(() => {});
    const plugin = fakePlugin('bad', {
      handle: { destroy: () => { throw new Error('boom'); } },
    });
    setUiPluginEnabled('notes', 'bad', true);
    await syncUiPlugins('notes', [plugin]);

    setUiPluginEnabled('notes', 'bad', false);
    await syncUiPlugins('notes', [plugin]);
    expect(errSpy).toHaveBeenCalled();

    // Gone from the active set: enabling again re-inits it.
    setUiPluginEnabled('notes', 'bad', true);
    await syncUiPlugins('notes', [plugin]);
    expect(plugin.init).toHaveBeenCalledTimes(2);
  });

  test('destroyAllUiPlugins stops everything', async () => {
    const destroy = jest.fn();
    const plugin = fakePlugin('a', { handle: { destroy } });
    setUiPluginEnabled('notes', 'a', true);
    await syncUiPlugins('notes', [plugin]);

    destroyAllUiPlugins();
    expect(destroy).toHaveBeenCalledTimes(1);
    destroyAllUiPlugins();
    expect(destroy).toHaveBeenCalledTimes(1);
  });
});
