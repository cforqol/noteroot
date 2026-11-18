import { trimTrailingSpaces, splitLineForWhitespace, softTabBackspaceRange } from '../../lib/whitespace.js';

describe('trimTrailingSpaces', () => {
  test('strips trailing spaces and tabs, reports whether anything changed', () => {
    const r = trimTrailingSpaces('one \ntwo\t\nthree');
    expect(r.content).toBe('one\ntwo\nthree');
    expect(r.changed).toBe(true);
  });
  test('leaves clean text alone', () => {
    const r = trimTrailingSpaces('one\ntwo');
    expect(r.content).toBe('one\ntwo');
    expect(r.changed).toBe(false);
  });
  test('preserves CRLF line endings', () => {
    const r = trimTrailingSpaces('one \r\ntwo \r\n');
    expect(r.content).toBe('one\r\ntwo\r\n');
  });
  test('coerces non-string input', () => {
    expect(trimTrailingSpaces(undefined).content).toBe('');
  });
});

describe('splitLineForWhitespace', () => {
  test('merges consecutive spaces and non-spaces into runs', () => {
    expect(splitLineForWhitespace('ab  cd')).toEqual([
      { text: 'ab', space: false },
      { text: '  ', space: true },
      { text: 'cd', space: false },
    ]);
  });
  test('a line that is only spaces is one run', () => {
    expect(splitLineForWhitespace('   ')).toEqual([{ text: '   ', space: true }]);
  });
  test('empty line yields no runs', () => {
    expect(splitLineForWhitespace('')).toEqual([]);
    expect(splitLineForWhitespace(undefined)).toEqual([]);
  });
  test('leading and trailing spaces are their own runs', () => {
    expect(splitLineForWhitespace(' ab ')).toEqual([
      { text: ' ', space: true },
      { text: 'ab', space: false },
      { text: ' ', space: true },
    ]);
  });
});

describe('softTabBackspaceRange', () => {
  test('deletes a full soft-tab run at an aligned column', () => {
    expect(softTabBackspaceRange('    x', 4, 4)).toEqual({ start: 0, end: 4 });
    expect(softTabBackspaceRange('  x', 2, 2)).toEqual({ start: 0, end: 2 });
  });
  test('indent past the first tab stop', () => {
    expect(softTabBackspaceRange('        x', 8, 4)).toEqual({ start: 4, end: 8 });
  });
  test('null when the column is not a tab-stop multiple', () => {
    expect(softTabBackspaceRange('   x', 3, 4)).toBeNull();
  });
  test('null when the run is not all spaces', () => {
    expect(softTabBackspaceRange('a   ', 4, 4)).toBeNull();
    expect(softTabBackspaceRange('a  b', 4, 4)).toBeNull();
  });
  test('null near the start of a line or buffer', () => {
    expect(softTabBackspaceRange('  ', 2, 4)).toBeNull();
    expect(softTabBackspaceRange('x', 1, 4)).toBeNull();
    // caret is far enough into the buffer, but right at the start of its own line
    expect(softTabBackspaceRange('xxxxx\ny', 6, 4)).toBeNull();
  });
  test('null for an invalid width', () => {
    expect(softTabBackspaceRange('    ', 4, 0)).toBeNull();
    expect(softTabBackspaceRange('    ', 4, 1.5)).toBeNull();
  });
  test('a tab stop at the start of a later line uses that line, not the buffer offset', () => {
    expect(softTabBackspaceRange('xx\n    y', 7, 4)).toEqual({ start: 3, end: 7 });
  });
});
