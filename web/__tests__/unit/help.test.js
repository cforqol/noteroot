import {
  SHORTCUTS, HELP_SECTIONS, inline, shortcutFor, isTypingTarget, renderHelp, shortcutsTable,
} from '../../lib/help.js';

const doc = () => document.implementation.createHTMLDocument('help');

describe('default document / options', () => {
  test('helpers fall back to the global document and empty options', () => {
    expect(inline('**x**').textContent).toBe('x');
    expect(shortcutsTable().querySelectorAll('tr')).toHaveLength(SHORTCUTS.length);
    const c = document.createElement('div');
    renderHelp(c);
    expect(c.querySelector('.help-grid')).toBeTruthy();
    expect(shortcutFor({ key: '?', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false })).toBe('help');
    expect(shortcutFor({ key: 'e' })).toBe('edit'); // typing defaults to false
    expect(shortcutFor({ key: '' })).toBeNull(); // empty key
    expect(shortcutFor({})).toBeNull(); // missing key
  });
});

describe('inline', () => {
  test('turns **bold**, `code` and {keys} into elements, never HTML', () => {
    const frag = inline('a **b** c `d` {E} <x>', doc());
    const parts = [...frag.childNodes];
    expect(parts[0].nodeType).toBe(3); // "a "
    expect(parts[1].tagName).toBe('STRONG');
    expect(parts[1].textContent).toBe('b');
    expect(parts[2].textContent).toBe(' c ');
    expect(parts[3].tagName).toBe('CODE');
    expect(parts[3].textContent).toBe('d');
    expect(parts[4].textContent).toBe(' ');
    expect(parts[5].tagName).toBe('KBD');
    expect(parts[5].textContent).toBe('E');
    expect(parts[6].textContent).toBe(' <x>'); // raw HTML is text
    expect(frag.textContent).toBe('a b c d E <x>');
  });

  test('empty and plain strings behave', () => {
    expect(inline('', doc()).childNodes).toHaveLength(0);
    expect(inline('plain', doc()).textContent).toBe('plain');
    expect(inline(null, doc()).textContent).toBe('null');
  });
});

describe('shortcutsTable', () => {
  test('renders one row per shortcut with "+" between key caps', () => {
    const table = shortcutsTable(doc());
    expect(table.className).toBe('shortcuts');
    const rows = table.querySelectorAll('tr');
    expect(rows).toHaveLength(SHORTCUTS.length);
    const first = rows[0];
    expect([...first.querySelectorAll('kbd')].map((k) => k.textContent)).toEqual(['Ctrl/⌘', 'S']);
    expect(first.children[1].textContent).toBe(SHORTCUTS[0].action);
    expect(first.children[0].textContent).toBe('Ctrl/⌘ + S');
  });
});

describe('renderHelp', () => {
  test('welcome variant: intro + quick start + shortcuts, no topic grid', () => {
    const c = doc().createElement('div');
    renderHelp(c, { welcome: true, doc: doc() });
    expect(c.querySelector('.help-intro')).toBeTruthy();
    expect(c.querySelector('.help-start')).toBeTruthy();
    expect(c.querySelector('.help-grid')).toBeNull();
    expect(c.querySelector('.help-keys table.shortcuts')).toBeTruthy();
  });

  test('full guide: every section is rendered with an anchor id', () => {
    const d = doc();
    const c = d.createElement('div');
    renderHelp(c, { doc: d });
    expect(c.querySelector('.help-intro')).toBeNull();
    const grid = c.querySelector('.help-grid');
    expect(grid.querySelectorAll('.help-section')).toHaveLength(HELP_SECTIONS.length);
    for (const s of HELP_SECTIONS) expect(grid.querySelector(`#help-${s.id}`)).toBeTruthy();
    expect(c.querySelector('.help-keys')).toBeTruthy();
  });
});

describe('isTypingTarget', () => {
  test('true for form fields and contentEditable, false otherwise', () => {
    const d = doc();
    expect(isTypingTarget(d.createElement('input'))).toBe(true);
    expect(isTypingTarget(d.createElement('textarea'))).toBe(true);
    expect(isTypingTarget(d.createElement('select'))).toBe(true);
    const editable = d.createElement('div');
    editable.isContentEditable = true;
    expect(isTypingTarget(editable)).toBe(true);
    expect(isTypingTarget(d.createElement('div'))).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
    expect(isTypingTarget(undefined)).toBe(false);
  });
});

describe('shortcutFor', () => {
  const e = (over = {}) => ({ key: '', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...over });

  test('mod+S and mod+K win regardless of typing', () => {
    expect(shortcutFor(e({ key: 's', ctrlKey: true }), { typing: true })).toBe('save');
    expect(shortcutFor(e({ key: 'S', metaKey: true }))).toBe('save');
    expect(shortcutFor(e({ key: 'k', metaKey: true }), { typing: true })).toBe('search');
  });

  test('mod with shift or alt is not a shortcut', () => {
    expect(shortcutFor(e({ key: 's', ctrlKey: true, shiftKey: true }))).toBeNull();
    expect(shortcutFor(e({ key: 'k', ctrlKey: true, altKey: true }))).toBeNull();
  });

  test('single-key shortcuts fire only when not typing, and never with a modifier', () => {
    expect(shortcutFor(e({ key: '?' }))).toBe('help');
    expect(shortcutFor(e({ key: '/' }))).toBe('search');
    expect(shortcutFor(e({ key: 'E' }))).toBe('edit');
    expect(shortcutFor(e({ key: 'n' }))).toBe('new-note');
    expect(shortcutFor(e({ key: 'x' }))).toBeNull();
    expect(shortcutFor(e({ key: 'e' }), { typing: true })).toBeNull();
    expect(shortcutFor(e({ key: 'n' }), { typing: true })).toBeNull();
    expect(shortcutFor(e({ key: 'e', ctrlKey: true }))).toBeNull();
    expect(shortcutFor(e({ key: 'n', altKey: true }))).toBeNull();
  });

  test('explicit bindings override the built-in map', () => {
    const bindings = {
      save: { key: 'd', ctrl: true },
      search: { key: 'g' },
      edit: { key: 'x' },
      help: null,
    };
    expect(shortcutFor(e({ key: 'd', ctrlKey: true }), { bindings })).toBe('save');
    expect(shortcutFor(e({ key: 's', ctrlKey: true }), { bindings })).toBeNull();
    expect(shortcutFor(e({ key: 'g' }), { bindings })).toBe('search');
    expect(shortcutFor(e({ key: 'x' }), { bindings })).toBe('edit');
    expect(shortcutFor(e({ key: '?' }), { bindings })).toBeNull();
    expect(shortcutFor(e({ key: 'x' }), { bindings, typing: true })).toBeNull();
  });
});
