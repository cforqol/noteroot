import {
  THEMES, PLACEMENTS, getTheme, setTheme, nextTheme, applyTheme,
  getPlacement, setPlacement, togglePlacement, applyPlacement,
  getDecorations, setDecoration, moveFolderPrefs, removeFolderPrefs,
  getCollapsed, setCollapsed, replaceCollapsed,
  SETTINGS, getSetting, setSetting, getSettings,
  STATUS_FIELDS, getStatusFields, setStatusField,
  isHelpSeen, markHelpSeen, resetPreferences, listDrafts, clearDrafts,
} from '../../lib/prefs.js';

const broken = {
  getItem() { throw new Error('denied'); },
  setItem() { throw new Error('quota'); },
};

describe('theme', () => {
  test('defaults to auto, persists, rejects unknown', () => {
    expect(getTheme()).toBe('auto');
    expect(setTheme('dark')).toBe('dark');
    expect(getTheme()).toBe('dark');
    expect(setTheme('neon')).toBe('auto');
    expect(getTheme()).toBe('auto');
    localStorage.setItem('nr.theme', 'garbage');
    expect(getTheme()).toBe('auto');
  });
  test('nextTheme cycles auto > light > dark', () => {
    expect(THEMES.map((t) => nextTheme(t))).toEqual(['light', 'dark', 'auto']);
    expect(nextTheme('bogus')).toBe('auto');
  });
  test('applyTheme sets/removes data-theme', () => {
    applyTheme('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(document.documentElement.dataset.userTheme).toBe('dark');
    applyTheme('light');
    expect(document.documentElement.dataset.theme).toBe('light');
    applyTheme('auto');
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(document.documentElement.dataset.userTheme).toBe('auto');
  });
  test('storage failures degrade to defaults', () => {
    expect(getTheme(broken)).toBe('auto');
    expect(setTheme('dark', broken)).toBe('dark');
    expect(getTheme(null)).toBe('auto');
  });
});

describe('tree placement', () => {
  test('defaults to left and persists', () => {
    expect(getPlacement()).toBe('left');
    expect(setPlacement('right')).toBe('right');
    expect(getPlacement()).toBe('right');
    expect(setPlacement('sideways')).toBe('left');
    expect(PLACEMENTS).toContain('hidden');
  });
  test('toggle hides, then restores the previous side', () => {
    setPlacement('right');
    expect(togglePlacement()).toBe('hidden');
    expect(getPlacement()).toBe('hidden');
    expect(togglePlacement()).toBe('right');
  });
  test('toggle from hidden with no memory goes left', () => {
    setPlacement('hidden');
    expect(togglePlacement()).toBe('left');
  });
  test('applyPlacement writes body[data-tree], defaulting bad values', () => {
    applyPlacement('hidden');
    expect(document.body.dataset.tree).toBe('hidden');
    applyPlacement('???');
    expect(document.body.dataset.tree).toBe('left');
  });
});

describe('folder decorations', () => {
  test('set / get / sanitise / clear', () => {
    expect(getDecorations('nb')).toEqual({});
    expect(setDecoration('nb', 'work', { emoji: ' 💼 ', color: '#AABBCC' })).toEqual({ emoji: '💼', color: '#aabbcc' });
    expect(getDecorations('nb')).toEqual({ work: { emoji: '💼', color: '#aabbcc' } });
    expect(setDecoration('nb', 'work', { emoji: 'x'.repeat(40), color: 'red; background:url(x)' }).emoji).toHaveLength(16);
    expect(getDecorations('nb').work.color).toBe('');
    setDecoration('nb', 'work', {});
    expect(getDecorations('nb')).toEqual({});
    setDecoration('nb', 'other', undefined);
    expect(getDecorations('nb')).toEqual({});
  });
  test('notebooks are independent; corrupt storage is tolerated', () => {
    setDecoration('a', 'f', { emoji: '1' });
    expect(getDecorations('b')).toEqual({});
    localStorage.setItem('nr.folderDeco', '{not json');
    expect(getDecorations('a')).toEqual({});
    localStorage.setItem('nr.folderDeco', '[1,2]');
    expect(getDecorations('a')).toEqual({});
    localStorage.setItem('nr.folderDeco', JSON.stringify({ a: 'oops' }));
    expect(getDecorations('a')).toEqual({});
  });
  test('moveFolderPrefs carries a folder and its children', () => {
    setDecoration('nb', 'old', { emoji: 'O' });
    setDecoration('nb', 'old/sub', { emoji: 'S' });
    setDecoration('nb', 'older', { emoji: 'X' });
    setCollapsed('nb', 'old', true);
    setCollapsed('nb', 'old/sub', true);
    setCollapsed('nb', 'keep', true);
    moveFolderPrefs('nb', 'old', 'new/place');
    expect(getDecorations('nb')).toEqual({
      'new/place': { emoji: 'O', color: '' },
      'new/place/sub': { emoji: 'S', color: '' },
      older: { emoji: 'X', color: '' },
    });
    expect([...getCollapsed('nb')].sort()).toEqual(['keep', 'new/place', 'new/place/sub']);
  });
  test('moveFolderPrefs / removeFolderPrefs on a notebook with no data', () => {
    expect(() => moveFolderPrefs('none', 'a', 'b')).not.toThrow();
    expect(() => removeFolderPrefs('none', 'a')).not.toThrow();
  });
  test('removeFolderPrefs drops a folder subtree only', () => {
    setDecoration('nb', 'gone', { emoji: 'G' });
    setDecoration('nb', 'gone/child', { emoji: 'C' });
    setDecoration('nb', 'gone2', { emoji: 'K' });
    setCollapsed('nb', 'gone', true);
    setCollapsed('nb', 'gone/child', true);
    setCollapsed('nb', 'gone2', true);
    removeFolderPrefs('nb', 'gone');
    expect(Object.keys(getDecorations('nb'))).toEqual(['gone2']);
    expect([...getCollapsed('nb')]).toEqual(['gone2']);
  });
});

describe('collapsed folders', () => {
  test('default expanded; set / unset / replace', () => {
    expect(getCollapsed('nb').size).toBe(0);
    expect([...setCollapsed('nb', 'a', true)]).toEqual(['a']);
    setCollapsed('nb', 'b', true);
    expect([...getCollapsed('nb')].sort()).toEqual(['a', 'b']);
    expect([...setCollapsed('nb', 'a', false)]).toEqual(['b']);
    expect([...replaceCollapsed('nb', ['x', 'y'])]).toEqual(['x', 'y']);
    expect(replaceCollapsed('nb', []).size).toBe(0);
  });
  test('ignores non-string and non-array junk', () => {
    localStorage.setItem('nr.treeCollapsed', JSON.stringify({ nb: ['ok', 3, null] }));
    expect([...getCollapsed('nb')]).toEqual(['ok']);
    localStorage.setItem('nr.treeCollapsed', JSON.stringify({ nb: 'text' }));
    expect(getCollapsed('nb').size).toBe(0);
    expect([...setCollapsed('nb', 'z', true)]).toEqual(['z']);
  });
});

test('inaccessible localStorage falls back to defaults without throwing', () => {
  const saved = globalThis.localStorage;
  try {
    Object.defineProperty(globalThis, 'localStorage', {
      get() { throw new Error('SecurityError'); },
      configurable: true,
    });
    expect(getTheme()).toBe('auto');
    expect(getPlacement()).toBe('left');
    expect(getDecorations('nb')).toEqual({});
    expect(getCollapsed('nb').size).toBe(0);
    expect(() => setTheme('dark')).not.toThrow();
  } finally {
    Object.defineProperty(globalThis, 'localStorage', { value: saved, writable: true, configurable: true });
  }
});

describe('settings registry', () => {
  test('every setting falls back to its default when unset or invalid', () => {
    for (const [name, s] of Object.entries(SETTINGS)) {
      expect(getSetting(name)).toBe(s.fallback);
    }
    setSetting('fontSize', 'large');
    expect(getSetting('fontSize')).toBe('large');
    localStorage.setItem('fontSize', 'huge'); // wrong key; ignored
    expect(getSetting('fontSize')).toBe('large');
    setSetting('fontSize', 'huge');
    expect(getSetting('fontSize')).toBe(SETTINGS.fontSize.fallback);
    setSetting('autosave', '2000');
    expect(getSetting('autosave')).toBe('2000');
    setSetting('autosave', 5000);
    expect(getSetting('autosave')).toBe('5000');
    setSetting('autosave', 'nope');
    expect(getSetting('autosave')).toBe(SETTINGS.autosave.fallback);
  });

  test('getSettings returns one value per registered setting', () => {
    const all = getSettings();
    expect(Object.keys(all).sort()).toEqual(Object.keys(SETTINGS).sort());
    expect(all.fontSize).toBe(getSetting('fontSize'));
  });

  test('a throwing storage degrades to defaults', () => {
    expect(getSetting('fontSize', broken)).toBe('medium');
    expect(setSetting('fontSize', 'large', broken)).toBe('large');
    expect(getSettings(broken)).toEqual(getSettings(null));
  });
});

describe('status-bar fields', () => {
  test('all shown by default; toggles persist and ignore unknown fields', () => {
    const all = getStatusFields();
    for (const f of STATUS_FIELDS) expect(all[f]).toBe(true);
    setStatusField('words', false);
    expect(getStatusFields().words).toBe(false);
    expect(getStatusFields().selected).toBe(true);
    setStatusField('nope', false);
    expect(getStatusFields().nope).toBeUndefined();
    setStatusField('words', true);
    expect(getStatusFields().words).toBe(true);
  });
  test('saved false values and corrupt storage are tolerated', () => {
    localStorage.setItem('nr.statusFields', JSON.stringify({ chars: false }));
    expect(getStatusFields().chars).toBe(false);
    localStorage.setItem('nr.statusFields', 'not json');
    expect(getStatusFields().chars).toBe(true);
  });
});

describe('first-run help flag', () => {
  test('unset until marked, then true; unknown values read as unset', () => {
    localStorage.removeItem('nr.helpSeen');
    expect(isHelpSeen()).toBe(false);
    markHelpSeen();
    expect(isHelpSeen()).toBe(true);
    localStorage.setItem('nr.helpSeen', '0');
    expect(isHelpSeen()).toBe(false);
  });
});

describe('resetPreferences', () => {
  test('drops appearance/editor/status prefs but keeps decorations, collapsed, drafts and help', () => {
    setTheme('dark');
    setPlacement('right');
    setStatusField('words', false);
    setSetting('fontSize', 'large');
    setCollapsed('nb', 'a', true);
    setDecoration('nb', 'f', { emoji: 'E' });
    markHelpSeen();
    localStorage.setItem('nr.pad.s.nb.a.md', JSON.stringify({ savedAt: 1, content: 'x' }));

    resetPreferences();

    expect(getTheme()).toBe('auto');
    expect(getPlacement()).toBe('left');
    expect(getStatusFields().words).toBe(true);
    expect(getSetting('fontSize')).toBe('medium');
    expect(isHelpSeen()).toBe(true);
    expect(getDecorations('nb').f).toEqual({ emoji: 'E', color: '' });
    expect([...getCollapsed('nb')]).toEqual(['a']);
    expect(listDrafts()).toHaveLength(1);
  });
});

describe('drafts', () => {
  test('lists key, slug, path and savedAt, splitting on the first dot', () => {
    localStorage.setItem('nr.pad.nb.note.md', JSON.stringify({ savedAt: 1700000000, content: 'hi' }));
    localStorage.setItem('nr.pad.nb.with.dot.md', JSON.stringify({ savedAt: 7, content: 'z' }));
    localStorage.setItem('nr.pad.orphan', JSON.stringify({ savedAt: 3, content: 'o' }));
    localStorage.setItem('nr.other', 'nope');
    const drafts = listDrafts();
    expect(drafts).toHaveLength(3);
    const byKey = Object.fromEntries(drafts.map((d) => [d.key, d]));
    expect(byKey['nr.pad.nb.note.md']).toEqual({ key: 'nr.pad.nb.note.md', slug: 'nb', path: 'note.md', savedAt: 1700000000 });
    expect(byKey['nr.pad.nb.with.dot.md'].path).toBe('with.dot.md');
    expect(byKey['nr.pad.orphan']).toEqual({ key: 'nr.pad.orphan', slug: 'orphan', path: '', savedAt: 3 });
    expect(drafts.every((d) => !d.key.startsWith('nr.other'))).toBe(true);
  });

  test('a corrupt or valueless draft still lists with savedAt 0', () => {
    localStorage.setItem('nr.pad.nb.broken.md', '{not json');
    localStorage.setItem('nr.pad.nb.nodate.md', JSON.stringify({ content: 'x' }));
    const byKey = Object.fromEntries(listDrafts().map((d) => [d.key, d]));
    expect(byKey['nr.pad.nb.broken.md'].savedAt).toBe(0);
    expect(byKey['nr.pad.nb.nodate.md'].savedAt).toBe(0);
  });

  test('clearDrafts removes every pad and reports how many there were', () => {
    localStorage.setItem('nr.pad.nb.a.md', JSON.stringify({ savedAt: 1 }));
    localStorage.setItem('nr.pad.nb.b.md', JSON.stringify({ savedAt: 2 }));
    localStorage.setItem('nr.theme', 'dark');
    expect(clearDrafts()).toBe(2);
    expect(listDrafts()).toHaveLength(0);
    expect(localStorage.getItem('nr.theme')).toBe('dark');
    expect(clearDrafts()).toBe(0);
  });

  test('a throwing storage degrades safely', () => {
    const noLength = {
      getItem() { return null; },
      setItem() {},
      removeItem() {},
      get length() { throw new Error('denied'); },
    };
    expect(listDrafts(noLength)).toEqual([]);
    expect(clearDrafts(noLength)).toBe(0);
    expect(listDrafts(broken)).toEqual([]);
    expect(clearDrafts(broken)).toBe(0);
  });
});

