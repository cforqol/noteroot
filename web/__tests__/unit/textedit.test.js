import { indentEdit } from '../../lib/textedit.js';

// Apply an indentEdit result back to the text the way the editor does.
const apply = (value, e) => value.slice(0, e.start) + e.text + value.slice(e.end);

describe('indentEdit — Tab without a selection', () => {
  test('inserts the unit at a collapsed caret and moves it', () => {
    const e = indentEdit('ab', 1, 1, '  ');
    expect(e).toEqual({ start: 1, end: 1, text: '  ', selStart: 3, selEnd: 3 });
    expect(apply('ab', e)).toBe('a  b');
  });

  test('inserts at the very start and end', () => {
    expect(indentEdit('ab', 0, 0, '\t').text).toBe('\t');
    const e = indentEdit('ab', 2, 2, '\t');
    expect(apply('ab', e)).toBe('ab\t');
    expect(e.selStart).toBe(3);
  });
});

describe('indentEdit — indent a selection spanning lines', () => {
  test('indents every touched line and keeps the selection over the same text', () => {
    const value = 'one\ntwo\nthree';
    const e = indentEdit(value, 0, 7, '  '); // "one\ntwo"
    expect(apply(value, e)).toBe('  one\n  two\nthree');
    expect(e.start).toBe(0);
    expect(e.selStart).toBe(2); // shifted by the first line's added unit
    expect(e.selEnd).toBe(11); // two added units
  });

  test('the first-line delta shifts the caret when the selection starts mid-line', () => {
    const value = 'one\ntwo\nthree';
    const e = indentEdit(value, 5, 9, '  '); // "wo\nt"
    expect(apply(value, e)).toBe('one\n  two\n  three');
    expect(e.selStart).toBe(7); // +2 from the first line's new indent
    expect(e.selEnd).toBe(13);
  });

  test('a selection ending just after a newline does not indent the next line', () => {
    const value = 'one\ntwo\nthree';
    const e = indentEdit(value, 0, 4, '  '); // "one\n"
    expect(apply(value, e)).toBe('  one\ntwo\nthree');
    expect(e.selEnd).toBe(6);
  });

  test('a multi-line selection over the final line has no trailing newline', () => {
    const value = 'one\ntwo';
    const e = indentEdit(value, 0, 7, '  ');
    expect(apply(value, e)).toBe('  one\n  two');
  });
});

describe('indentEdit — Shift+Tab (outdent)', () => {
  test('removes one unit equivalent of leading whitespace, spaces or a tab', () => {
    const value = '    one\ntwo\n  three';
    const e = indentEdit(value, 0, value.length, '  ', true);
    expect(apply(value, e)).toBe('  one\ntwo\nthree');
    expect(e.selStart).toBe(0);
    expect(e.selEnd).toBe(value.length - 4);
  });

  test('a tab unit strips a full tab but only up to four spaces', () => {
    expect(apply('  x', indentEdit('  x', 0, 3, '\t', true))).toBe('x');
    expect(apply('\tx', indentEdit('\tx', 0, 2, '\t', true))).toBe('x');
    expect(apply('      x', indentEdit('      x', 0, 7, '\t', true))).toBe('  x');
  });

  test('an outdent with nothing to strip leaves the text unchanged, caret collapses', () => {
    const value = 'one\ntwo';
    const e = indentEdit(value, 0, 3, '  ', true);
    expect(apply(value, e)).toBe(value);
    expect(e.selStart).toBe(0);
  });

  test('outdent with a collapsed caret collapses to the line start delta', () => {
    const value = '  one';
    const e = indentEdit(value, 4, 4, '  ', true);
    expect(apply(value, e)).toBe('one');
    expect(e.selStart).toBe(2);
    expect(e.selEnd).toBe(2);
  });
});
