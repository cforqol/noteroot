import {
  annotateCounts, formatTreeStatus, folderPaths, parentDir, baseName, joinPath,
  ancestors, isWithin, cleanRelPath, newNoteContent, resolveChild,
  displayName, folderChoices, renameTarget, moveTarget,
} from '../../lib/tree.js';

const sample = () => ({
  isDir: true,
  children: [
    { name: 'a', path: 'a', isDir: true, children: [
      { name: 'x.md', path: 'a/x.md' },
      { name: 'b', path: 'a/b', isDir: true, children: [{ name: 'y.md', path: 'a/b/y.md' }] },
    ] },
    { name: 'empty', path: 'empty', isDir: true },
    { name: 'top.md', path: 'top.md' },
  ],
});

test('annotateCounts totals and per-folder counts', () => {
  const t = sample();
  expect(annotateCounts(t)).toEqual({ notes: 3, folders: 3 });
  expect(t.children[0].noteCount).toBe(2);
  expect(t.children[0].folderCount).toBe(1);
  expect(t.children[1].noteCount).toBe(0);
  expect(annotateCounts({ isDir: true })).toEqual({ notes: 0, folders: 0 });
});

test('formatTreeStatus pluralises', () => {
  expect(formatTreeStatus({ notes: 1, folders: 1 })).toBe('1 note · 1 folder');
  expect(formatTreeStatus({ notes: 0, folders: 2 })).toBe('0 notes · 2 folders');
});

test('folderPaths is pre-order', () => {
  expect(folderPaths(sample())).toEqual(['a', 'a/b', 'empty']);
  expect(folderPaths({})).toEqual([]);
});

test('path helpers', () => {
  expect(parentDir('a/b/c.md')).toBe('a/b');
  expect(parentDir('c.md')).toBe('');
  expect(baseName('a/b/c.md')).toBe('c.md');
  expect(baseName('c.md')).toBe('c.md');
  expect(joinPath('a', 'b')).toBe('a/b');
  expect(joinPath('', 'b')).toBe('b');
  expect(ancestors('a/b/c.md')).toEqual(['a', 'a/b']);
  expect(ancestors('c.md')).toEqual([]);
  expect(isWithin('a/b', 'a')).toBe(true);
  expect(isWithin('a', 'a')).toBe(true);
  expect(isWithin('ab/c', 'a')).toBe(false);
});

describe('cleanRelPath', () => {
  test('normalises and appends .md for notes', () => {
    expect(cleanRelPath(' ideas\\todo ', { asNote: true })).toEqual({ ok: true, path: 'ideas/todo.md' });
    expect(cleanRelPath('/a/b/', {})).toEqual({ ok: true, path: 'a/b' });
    expect(cleanRelPath('x.MD', { asNote: true })).toEqual({ ok: true, path: 'x.MD' });
    expect(cleanRelPath('plain')).toEqual({ ok: true, path: 'plain' });
  });
  test('rejects empty, traversal, dotfiles and over-deep paths', () => {
    expect(cleanRelPath('').ok).toBe(false);
    expect(cleanRelPath(null).ok).toBe(false);
    expect(cleanRelPath('   ').ok).toBe(false);
    expect(cleanRelPath('a//b').ok).toBe(false);
    expect(cleanRelPath('../x').ok).toBe(false);
    expect(cleanRelPath('a/./b').ok).toBe(false);
    expect(cleanRelPath('.hidden').error).toMatch(/dot/);
    expect(cleanRelPath('a/.git/b').ok).toBe(false);
    expect(cleanRelPath('a/b/c/d/e/f').error).toMatch(/deep/);
    expect(cleanRelPath('a/b/c/d/e').ok).toBe(true);
  });
});

describe('resolveChild', () => {
  test('validates the typed name on its own, then the joined path', () => {
    expect(resolveChild('dir', 'n', { asNote: true })).toEqual({ ok: true, path: 'dir/n.md' });
    expect(resolveChild('', 'n')).toEqual({ ok: true, path: 'n' });
    expect(resolveChild('dir', '').ok).toBe(false);
    expect(resolveChild('dir', '../x').ok).toBe(false);
    expect(resolveChild('a/b/c/d', 'e/f', { asNote: true }).error).toMatch(/deep/);
  });
});

test('newNoteContent titles from the file name', () => {
  expect(newNoteContent('dir/My Note.md')).toBe('# My Note\n\n');
});

describe('displayName', () => {
  test('notes lose the .md suffix, folders keep their name', () => {
    expect(displayName('a/Note.md', false)).toBe('Note');
    expect(displayName('a/Note.MD', false)).toBe('Note');
    expect(displayName('a/folder', true)).toBe('folder');
    expect(displayName('top.md', false)).toBe('top');
  });
});

describe('folderChoices', () => {
  test('root first, then every folder in tree order with its depth', () => {
    expect(folderChoices(sample())).toEqual([
      { value: '', label: 'Notebook root', depth: 0 },
      { value: 'a', label: 'a', depth: 1 },
      { value: 'a/b', label: 'b', depth: 2 },
      { value: 'empty', label: 'empty', depth: 1 },
    ]);
    expect(folderChoices({ isDir: true })).toEqual([{ value: '', label: 'Notebook root', depth: 0 }]);
  });
});

describe('renameTarget', () => {
  test('changes the name in place, reporting an unchanged name as same', () => {
    expect(renameTarget('a/plan.md', 'plan2', false)).toEqual({ ok: true, path: 'a/plan2.md', same: false });
    expect(renameTarget('a/plan.md', 'plan', false)).toEqual({ ok: true, path: 'a/plan.md', same: true });
    expect(renameTarget('folder', 'other', true)).toEqual({ ok: true, path: 'other', same: false });
  });
  test('refuses slashes — moving is a separate action', () => {
    const r = renameTarget('a/plan.md', 'b/plan', false);
    expect(r.ok).toBe(false);
    expect(r.error).toMatch(/Move/);
    expect(renameTarget('a/plan.md', 'a\\b', false).ok).toBe(false);
  });
  test('reports invalid names from the child resolver', () => {
    expect(renameTarget('a/plan.md', '', false).ok).toBe(false);
    expect(renameTarget('a/plan.md', null, false).ok).toBe(false);
    expect(renameTarget('a/plan.md', '.hidden', false).ok).toBe(false);
  });
});

describe('moveTarget', () => {
  test('moves a note or folder to another folder, keeping the name', () => {
    expect(moveTarget('a/plan.md', false, 'b')).toEqual({ ok: true, path: 'b/plan.md', dir: 'b' });
    expect(moveTarget('a/child', true, 'b')).toEqual({ ok: true, path: 'b/child', dir: 'b' });
  });
  test('an optional new sub-folder is created under the destination', () => {
    expect(moveTarget('a/plan.md', false, 'b', 'archive')).toEqual({ ok: true, path: 'b/archive/plan.md', dir: 'b/archive' });
    expect(moveTarget('a/plan.md', false, '', 'top')).toEqual({ ok: true, path: 'top/plan.md', dir: 'top' });
  });
  test('a folder cannot be moved into itself', () => {
    expect(moveTarget('a', true, 'a').ok).toBe(false);
    expect(moveTarget('a', true, 'a/deep').error).toMatch(/itself/);
  });
  test('rejects an unchanged destination and an invalid new folder', () => {
    expect(moveTarget('a/plan.md', false, 'a').error).toMatch(/already/);
    expect(moveTarget('a/plan.md', false, 'b', '../x').ok).toBe(false);
    expect(moveTarget('a/plan.md', false, 'b', '').ok).toBe(true);
    expect(moveTarget('a/plan.md', false, 'b', null).ok).toBe(true); // null new folder
    expect(moveTarget('a/plan.md', false, 'a/b/c/d/e').ok).toBe(false); // too deep
  });
});
