/**
 * jsdom integration tests: tree rendering, decorations, counts,
 * collapse state, and the interaction callbacks.
 */
import { jest } from '@jest/globals';
import { renderTree } from '../../lib/tree-view.js';

const tree = () => ({
  isDir: true,
  children: [
    { name: 'work', path: 'work', isDir: true, children: [
      { name: 'plan.md', path: 'work/plan.md' },
      { name: 'deep', path: 'work/deep', isDir: true, children: [{ name: 'x.md', path: 'work/deep/x.md' }] },
    ] },
    { name: 'empty', path: 'empty', isDir: true },
    { name: 'top.md', path: 'top.md' },
  ],
});

let el;
beforeEach(() => {
  document.body.innerHTML = '<nav id="t"></nav>';
  el = document.getElementById('t');
});

test('renders folders, notes, counts in brackets and returns totals', () => {
  const totals = renderTree(el, tree());
  expect(totals).toEqual({ notes: 3, folders: 3 });
  const dirs = [...el.querySelectorAll('.node.dir')];
  expect(dirs.map((d) => d.querySelector('.label').textContent)).toEqual(['work', 'deep', 'empty']);
  expect(dirs.map((d) => d.querySelector('.count').textContent)).toEqual(['[2]', '[1]', '[0]']);
  const notes = [...el.querySelectorAll('.node:not(.dir)')];
  expect(notes.map((n) => n.textContent)).toEqual(['plan', 'x', 'top']);
  expect(notes[0].dataset.path).toBe('work/plan.md');
  expect(dirs[0].getAttribute('aria-expanded')).toBe('true');
  expect(dirs[0].querySelector('.caret').textContent).toBe('▾');
  expect(dirs[0].querySelector('.icon').textContent).toBe('📂');
});

test('indents by depth', () => {
  renderTree(el, tree());
  expect(el.querySelector('[data-path="work"]').style.paddingLeft).toBe('0px');
  expect(el.querySelector('[data-path="work/plan.md"]').style.paddingLeft).toBe('12px');
  expect(el.querySelector('[data-path="work/deep/x.md"]').style.paddingLeft).toBe('24px');
});

test('collapsed folders hide children and show closed icon', () => {
  renderTree(el, tree(), { collapsed: new Set(['work']) });
  expect(el.querySelector('[data-path="work/plan.md"]')).toBeNull();
  const work = el.querySelector('[data-path="work"]');
  expect(work.getAttribute('aria-expanded')).toBe('false');
  expect(work.querySelector('.caret').textContent).toBe('▸');
  expect(work.querySelector('.icon').textContent).toBe('📁');
  expect(work.querySelector('.count').textContent).toBe('[2]');
});

test('user decorations override icon and color; selected and active are marked', () => {
  renderTree(el, tree(), {
    current: 'top.md',
    selected: 'empty',
    decorations: { work: { emoji: '💼', color: '#ff0000' }, empty: { emoji: '', color: '' } },
  });
  const work = el.querySelector('[data-path="work"]');
  expect(work.querySelector('.icon').textContent).toBe('💼');
  expect(work.querySelector('.label').style.color).toBe('rgb(255, 0, 0)');
  expect(el.querySelector('[data-path="empty"]').classList.contains('selected')).toBe(true);
  expect(work.classList.contains('selected')).toBe(false);
  expect(el.querySelector('[data-path="top.md"]').classList.contains('active')).toBe(true);
  expect(el.querySelector('[data-path="work/plan.md"]').classList.contains('active')).toBe(false);
});

test('clicks call onOpen for notes and onToggle (inverted state) for folders', () => {
  const onOpen = jest.fn();
  const onToggle = jest.fn();
  renderTree(el, tree(), { collapsed: new Set(['empty']), onOpen, onToggle });
  el.querySelector('[data-path="top.md"]').click();
  expect(onOpen).toHaveBeenCalledWith('top.md', el.querySelector('[data-path="top.md"]'), expect.anything());
  el.querySelector('[data-path="work"]').click();
  expect(onToggle).toHaveBeenLastCalledWith('work', true);
  el.querySelector('[data-path="empty"]').click();
  expect(onToggle).toHaveBeenLastCalledWith('empty', false);
});

test('the ⋯ button and right-click both open the node menu', () => {
  const onMenu = jest.fn();
  const onToggle = jest.fn();
  renderTree(el, tree(), { onMenu, onToggle });
  const row = el.querySelector('[data-path="work"]').parentElement;
  const btn = row.querySelector('.row-menu');
  expect(btn.getAttribute('aria-label')).toBe('Actions for work');
  btn.click();
  expect(onMenu).toHaveBeenCalledTimes(1);
  expect(onMenu.mock.calls[0][0]).toMatchObject({ path: 'work', isDir: true, noteCount: 2 });
  expect(onToggle).not.toHaveBeenCalled(); // menu click must not toggle

  const ev = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 7, clientY: 9 });
  el.querySelector('[data-path="top.md"]').dispatchEvent(ev);
  expect(ev.defaultPrevented).toBe(true);
  expect(onMenu).toHaveBeenLastCalledWith(expect.objectContaining({ path: 'top.md' }), 7, 9);
});

test('an empty notebook shows a hint', () => {
  const totals = renderTree(el, { isDir: true, children: [] });
  expect(totals).toEqual({ notes: 0, folders: 0 });
  expect(el.querySelector('.tree-empty').textContent).toMatch(/No notes yet/);
  renderTree(el, { isDir: true });
  expect(el.querySelector('.tree-empty')).not.toBeNull();
});

test('re-rendering replaces previous content', () => {
  renderTree(el, tree());
  renderTree(el, { isDir: true, children: [{ name: 'only.md', path: 'only.md' }] });
  expect(el.querySelectorAll('.node')).toHaveLength(1);
});

test('interactions are safe without any callbacks', () => {
  renderTree(el, tree());
  expect(() => {
    el.querySelector('[data-path="top.md"]').click();
    el.querySelector('[data-path="work"]').click();
    el.querySelector('.row-menu').click();
    el.querySelector('.row').dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }));
  }).not.toThrow();
});

test('a locked folder is dimmed, shows a lock and hides its children', () => {
  const lockedTree = {
    isDir: true,
    children: [
      { name: 'secret', path: 'secret', isDir: true, locked: true, noteCount: 2 },
      { name: 'open.md', path: 'open.md' },
    ],
  };
  renderTree(el, lockedTree);
  const dir = el.querySelector('[data-path="secret"]');
  expect(dir.classList.contains('locked')).toBe(true);
  expect(dir.querySelector('.icon').textContent).toBe('🔒');
  expect(dir.querySelector('.count').textContent).toBe('');
  expect(el.querySelector('[data-path="secret/x.md"]')).toBeNull();
});

test('a locked note carries a lock glyph and the locked class', () => {
  renderTree(el, {
    isDir: true,
    children: [{ name: 'vault.md', path: 'vault.md', locked: true }],
  });
  const note = el.querySelector('[data-path="vault.md"]');
  expect(note.classList.contains('locked')).toBe(true);
  expect(note.querySelector('.lock-icon').textContent).toBe('🔒');
  expect(note.querySelector('.label').textContent).toBe('vault');
});

test('marks the split pane note and multi-selected notes', () => {
  renderTree(el, tree(), {
    current: 'top.md',
    splitPath: 'work/plan.md',
    multi: new Set(['work/plan.md', 'top.md']),
  });
  const top = el.querySelector('[data-path="top.md"]');
  expect(top.classList.contains('active')).toBe(true);
  expect(top.classList.contains('multi')).toBe(true);
  const plan = el.querySelector('[data-path="work/plan.md"]');
  expect(plan.classList.contains('split-open')).toBe(true);
  expect(plan.getAttribute('aria-selected')).toBe('true');
  const other = el.querySelector('[data-path="work/deep/x.md"]');
  expect(other.classList.contains('split-open')).toBe(false);
  expect(other.getAttribute('aria-selected')).toBe('false');
});
