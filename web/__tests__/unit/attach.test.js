/**
 * Attach-menu helpers: link markdown, URL safety, note resolution.
 */
import {
  isValidLinkUrl, buildLinkMarkdown, buildNoteLinkMarkdown,
  collectNotePaths, resolveNotePath, filterNotePaths, adjacentNote, bodyInsert,
} from '../../lib/attach.js';

describe('isValidLinkUrl', () => {
  test('accepts http, https, mailto and relative paths', () => {
    for (const ok of [
      'https://example.com',
      'http://example.com/a?b=c#d',
      'HTTPS://EXAMPLE.COM',
      'mailto:someone@example.com',
      '/n/work/note?path=a.md',
      '../sibling.md',
      'a/b/c.md',
      '#anchor',
      '  https://example.com  ',
    ]) {
      expect(isValidLinkUrl(ok)).toBe(true);
    }
  });

  test('rejects script-ish schemes and empties', () => {
    for (const bad of [
      'javascript:alert(1)',
      'JAVASCRIPT:alert(1)',
      '  javascript:alert(1)  ',
      'data:text/html,<script>x</script>',
      'vbscript:msgbox',
      'file:///etc/passwd',
      'ftp://x',
      '',
      '   ',
    ]) {
      expect(isValidLinkUrl(bad)).toBe(false);
    }
  });

  test('treats null and non-strings safely', () => {
    expect(isValidLinkUrl(null)).toBe(false);
    expect(isValidLinkUrl(undefined)).toBe(false);
    expect(isValidLinkUrl(42)).toBe(true); // "42" is a relative path
  });
});

describe('buildLinkMarkdown', () => {
  test('uses the URL as label when none given', () => {
    expect(buildLinkMarkdown('https://x.test', '')).toBe('[https://x.test](https://x.test)');
    expect(buildLinkMarkdown('https://x.test')).toBe('[https://x.test](https://x.test)');
  });

  test('percent-encodes spaces and parens in the target', () => {
    expect(buildLinkMarkdown('/a b(c).md', 'label')).toBe('[label](/a%20b%28c%29.md)');
  });

  test('escapes brackets in the label', () => {
    expect(buildLinkMarkdown('https://x.test', 'a [b] c')).toBe('[a \\[b\\] c](https://x.test)');
    expect(buildLinkMarkdown('https://x.test', 'back\\slash')).toBe('[back\\\\slash](https://x.test)');
  });

  test('trims both parts', () => {
    expect(buildLinkMarkdown('  https://x.test  ', '  go  ')).toBe('[go](https://x.test)');
  });

  test('tolerates a missing url or label', () => {
    expect(buildLinkMarkdown(null, 'x')).toBe('[x]()');
    expect(buildLinkMarkdown('', 'x')).toBe('[x]()');
  });
});

describe('buildNoteLinkMarkdown', () => {
  test('bare path when no label', () => {
    expect(buildNoteLinkMarkdown('notes/todo.md')).toBe('[[notes/todo.md]]');
    expect(buildNoteLinkMarkdown('notes/todo.md', '')).toBe('[[notes/todo.md]]');
    expect(buildNoteLinkMarkdown('')).toBeNull();
    expect(buildNoteLinkMarkdown(null)).toBeNull();
  });

  test('label equal to the file name collapses to a bare link', () => {
    expect(buildNoteLinkMarkdown('notes/todo.md', 'todo')).toBe('[[notes/todo.md]]');
    expect(buildNoteLinkMarkdown('notes/todo.md', 'notes/todo.md')).toBe('[[notes/todo.md]]');
  });

  test('other labels become path|label', () => {
    expect(buildNoteLinkMarkdown('notes/todo.md', 'My tasks')).toBe('[[notes/todo.md|My tasks]]');
    expect(buildNoteLinkMarkdown('todo.md', 'My tasks')).toBe('[[todo.md|My tasks]]');
  });

  test('rejects paths and labels the wikilink parser cannot take', () => {
    expect(buildNoteLinkMarkdown('we[i]rd.md')).toBeNull();
    expect(buildNoteLinkMarkdown('notes/todo.md', 'a]b')).toBeNull();
    expect(buildNoteLinkMarkdown('notes/todo.md', 'a|b')).toBeNull();
    expect(buildNoteLinkMarkdown('notes/todo.md', 'line\nbreak')).toBeNull();
    expect(buildNoteLinkMarkdown('notes/todo.md', 'a[b')).toBeNull();
  });

  test('a path without .md keeps its extension-less form', () => {
    expect(buildNoteLinkMarkdown('notes/todo')).toBe('[[notes/todo]]');
    expect(buildNoteLinkMarkdown('notes/todo', 'todo')).toBe('[[notes/todo]]'); // file name match
    expect(buildNoteLinkMarkdown('todo', 'My tasks')).toBe('[[todo|My tasks]]');
  });
});

describe('collectNotePaths', () => {
  const tree = {
    isDir: true, path: '', children: [
      { path: 'welcome.md' },
      { isDir: true, path: 'work', children: [
        { path: 'work/plan.md' },
        { isDir: true, path: 'work/deep', children: [{ path: 'work/deep/x.md' }] },
      ] },
      { path: 'essay.md' },
    ],
  };

  test('flattens and sorts every non-directory node', () => {
    expect(collectNotePaths(tree)).toEqual(['essay.md', 'welcome.md', 'work/deep/x.md', 'work/plan.md']);
  });

  test('tolerates empty and malformed roots', () => {
    expect(collectNotePaths(null)).toEqual([]);
    expect(collectNotePaths({})).toEqual([]);
    expect(collectNotePaths({ children: [{ isDir: true, path: 'empty' }] })).toEqual([]);
  });
});

describe('resolveNotePath', () => {
  const paths = ['a/todo.md', 'b/todo.md', 'notes/plan.md', 'index.md'];

  test('exact path wins', () => {
    expect(resolveNotePath(paths, 'notes/plan.md')).toBe('notes/plan.md');
    expect(resolveNotePath(paths, 'index.md')).toBe('index.md');
  });

  test('adds .md when the query omits it', () => {
    expect(resolveNotePath(paths, 'notes/plan')).toBe('notes/plan.md');
    expect(resolveNotePath(paths, '  notes/plan  ')).toBe('notes/plan.md');
  });

  test('unique stem resolves, ambiguous name does not', () => {
    expect(resolveNotePath(paths, 'plan')).toBe('notes/plan.md'); // unique file name
    expect(resolveNotePath(paths, 'todo')).toBeNull(); // a/todo and b/todo
    expect(resolveNotePath(paths, 'a/todo')).toBe('a/todo.md');
    expect(resolveNotePath(['only.md'], 'only')).toBe('only.md');
    expect(resolveNotePath(['x/dup.md', 'y/dup.md'], 'dup')).toBeNull();
  });

  test('no match and empty queries return null', () => {
    expect(resolveNotePath(paths, 'missing')).toBeNull();
    expect(resolveNotePath(paths, 'a/missing')).toBeNull(); // slashes never fall back to a name search
    expect(resolveNotePath(paths, '')).toBeNull();
    expect(resolveNotePath(paths, null)).toBeNull();
    expect(resolveNotePath([], 'x.md')).toBeNull();
    expect(resolveNotePath(['sub/fresh.md'], 'other.md')).toBeNull();
  });

  test('a bare .md query matches a unique nested file name', () => {
    expect(resolveNotePath(['sub/fresh.md'], 'fresh.md')).toBe('sub/fresh.md');
    expect(resolveNotePath(['a/x.md', 'b/x.md'], 'x.md')).toBeNull(); // ambiguous
  });
});

describe('filterNotePaths', () => {
  const paths = ['Welcome.md', 'work/Plan.md', 'work/deep/plan2.md'];

  test('empty query shows everything', () => {
    expect(filterNotePaths(paths, '')).toBe(paths);
    expect(filterNotePaths(paths, '   ')).toBe(paths);
  });

  test('case-insensitive substring match', () => {
    expect(filterNotePaths(paths, 'WELCOME')).toEqual(['Welcome.md']);
    expect(filterNotePaths(paths, 'plan')).toEqual(['work/Plan.md', 'work/deep/plan2.md']);
    expect(filterNotePaths(paths, 'nope')).toEqual([]);
  });
});

describe('adjacentNote', () => {
  const paths = ['a.md', 'work/b.md', 'work/c.md', 'z.md'];

  test('steps forward and backward in pre-order', () => {
    expect(adjacentNote(paths, 'a.md', 1)).toBe('work/b.md');
    expect(adjacentNote(paths, 'work/b.md', 1)).toBe('work/c.md');
    expect(adjacentNote(paths, 'z.md', -1)).toBe('work/c.md');
    expect(adjacentNote(paths, 'work/b.md', -1)).toBe('a.md');
  });

  test('returns null at the ends, off-list, or with no step', () => {
    expect(adjacentNote(paths, 'z.md', 1)).toBeNull();
    expect(adjacentNote(paths, 'a.md', -1)).toBeNull();
    expect(adjacentNote(paths, 'nope.md', 1)).toBeNull();
    expect(adjacentNote(paths, '', 1)).toBeNull();
    expect(adjacentNote(paths, 'a.md', 0)).toBeNull();
    expect(adjacentNote(paths, 'a.md', NaN)).toBeNull();
    expect(adjacentNote([], 'a.md', 1)).toBeNull();
  });

  test('jumps more than one step and clamps to the list', () => {
    expect(adjacentNote(paths, 'a.md', 2)).toBe('work/c.md');
    expect(adjacentNote(paths, 'a.md', 10)).toBeNull();
    expect(adjacentNote(paths, 'z.md', -10)).toBeNull();
  });
});

describe('bodyInsert', () => {
  const note = '---\ntitle: x\n---\n\nhello\n';
  const bodyStart = 17; // after "---\ntitle: x\n---\n"

  test('a cursor in the body inserts in place', () => {
    expect(bodyInsert(note, 20, 20, 'X')).toEqual({ start: 20, end: 20, text: 'X' });
  });

  test('an insert inside the frontmatter moves to the body start', () => {
    expect(bodyInsert(note, 5, 5, 'md')).toEqual({ start: bodyStart, end: bodyStart, text: 'md\n' });
    expect(bodyInsert(note, 5, 8, 'md\n')).toEqual({ start: bodyStart, end: bodyStart, text: 'md\n' });
    // A selection that starts in the block but ends in the body still
    // replaces from the body start to the selection end.
    expect(bodyInsert(note, 5, 22, 'md')).toEqual({ start: bodyStart, end: 22, text: 'md\n' });
  });

  test('an unclosed block ends the file without a line break', () => {
    const bare = '---\ntitle: x\n---';
    expect(bodyInsert(bare, 0, 0, 'md')).toEqual({ start: bare.length, end: bare.length, text: '\nmd' });
  });

  test('a note without frontmatter inserts in place', () => {
    expect(bodyInsert('hello', 2, 2, 'X')).toEqual({ start: 2, end: 2, text: 'X' });
    expect(bodyInsert('', 0, 0, 'X')).toEqual({ start: 0, end: 0, text: 'X' });
  });
});
