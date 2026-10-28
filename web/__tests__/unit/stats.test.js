import {
  stripFrontmatter, countWords, textStats, cursorPosition, byteLength, humanSize, describeStatus,
} from '../../lib/stats.js';

describe('stripFrontmatter', () => {
  test('removes a leading YAML block only', () => {
    expect(stripFrontmatter('---\nid: x\n---\nbody')).toBe('body');
    expect(stripFrontmatter('---\r\nid: x\r\n---\r\nbody')).toBe('body');
    expect(stripFrontmatter('text\n---\nnot: fm\n---\n')).toBe('text\n---\nnot: fm\n---\n');
  });
  test('tolerates null/undefined', () => {
    expect(stripFrontmatter(null)).toBe('');
    expect(stripFrontmatter(undefined)).toBe('');
  });
});

describe('countWords', () => {
  test('counts whitespace-delimited tokens', () => {
    expect(countWords('one two\tthree\nfour')).toBe(4);
    expect(countWords('  ')).toBe(0);
    expect(countWords('')).toBe(0);
    expect(countWords(null)).toBe(0);
  });
});

describe('textStats', () => {
  test('words, chars, paragraphs, reading time', () => {
    const s = textStats('Hello world.\n\nSecond para here.\n\n\n\nThird.');
    expect(s.words).toBe(6);
    expect(s.chars).toBe('Helloworld.Secondparahere.Third.'.length);
    expect(s.paragraphs).toBe(3);
    expect(s.readingMinutes).toBe(1);
  });
  test('frontmatter is excluded', () => {
    expect(textStats('---\nid: abc\ntitle: t\n---\nonly these three').words).toBe(3);
  });
  test('empty body has zero reading time', () => {
    expect(textStats('')).toEqual({ words: 0, chars: 0, paragraphs: 0, readingMinutes: 0 });
  });
  test('reading time scales at 200 wpm', () => {
    expect(textStats('w '.repeat(1000)).readingMinutes).toBe(5);
  });
  test('a zero or non-numeric wpm falls back to the default', () => {
    expect(textStats('w '.repeat(1000), 0).readingMinutes).toBe(5);
    expect(textStats('w '.repeat(1000), 'nope').readingMinutes).toBe(5);
    expect(textStats('w '.repeat(100), 50).readingMinutes).toBe(2);
  });
  test('counts code points, not UTF-16 units', () => {
    expect(textStats('😀😀 a').chars).toBe(3);
  });
});

describe('cursorPosition', () => {
  test('1-based row and column', () => {
    expect(cursorPosition('ab\ncde', 0)).toEqual({ row: 1, col: 1 });
    expect(cursorPosition('ab\ncde', 2)).toEqual({ row: 1, col: 3 });
    expect(cursorPosition('ab\ncde', 3)).toEqual({ row: 2, col: 1 });
    expect(cursorPosition('ab\ncde', 6)).toEqual({ row: 2, col: 4 });
  });
  test('clamps and tolerates bad input', () => {
    expect(cursorPosition('ab', 99)).toEqual({ row: 1, col: 3 });
    expect(cursorPosition('ab', -4)).toEqual({ row: 1, col: 1 });
    expect(cursorPosition('ab', NaN)).toEqual({ row: 1, col: 1 });
    expect(cursorPosition(null, 3)).toEqual({ row: 1, col: 1 });
  });
});

describe('byteLength / humanSize', () => {
  test('UTF-8 byte length', () => {
    expect(byteLength('abc')).toBe(3);
    expect(byteLength('é')).toBe(2);
    expect(byteLength(null)).toBe(0);
  });
  test('humanSize scales units', () => {
    expect(humanSize(0)).toBe('0 B');
    expect(humanSize(1023)).toBe('1023 B');
    expect(humanSize(1536)).toBe('1.5 KB');
    expect(humanSize(20 * 1024)).toBe('20 KB');
    expect(humanSize(5 * 1024 * 1024)).toBe('5.0 MB');
    expect(humanSize(3 * 1024 ** 3)).toBe('3.0 GB');
    expect(humanSize(5000 * 1024 ** 3)).toBe('5000 GB');
  });
  test('invalid sizes render as a dash', () => {
    for (const v of [-1, NaN, Infinity, '12', null, undefined]) expect(humanSize(v)).toBe('—');
  });
});

describe('describeStatus', () => {
  test('unknown text gives dashes, size from bytes if given', () => {
    const d = describeStatus({ text: null });
    expect(d).toEqual({ words: '—', selected: '—', position: '—', chars: '—', paragraphs: '—', reading: '—', size: '—' });
    expect(describeStatus({ text: null, bytes: 2048 }).size).toBe('2.0 KB');
  });
  test('full stats with selection and cursor', () => {
    const text = 'alpha beta\ngamma delta';
    const d = describeStatus({ text, selection: { start: 0, end: 10 }, cursor: true });
    expect(d.words).toBe('4');
    expect(d.selected).toBe('2');
    expect(d.position).toBe('1:11');
    expect(d.size).toBe(`${text.length} B`);
    expect(d.reading).toBe('1 min');
  });
  test('reversed selection is normalised; collapsed selection counts zero', () => {
    const text = 'a b c';
    expect(describeStatus({ text, selection: { start: 5, end: 0 } }).selected).toBe('3');
    expect(describeStatus({ text, selection: { start: 2, end: 2 }, cursor: true })).toMatchObject({ selected: '0', position: '1:3' });
  });
  test('no cursor means no position; empty text has no reading time', () => {
    const d = describeStatus({ text: '' });
    expect(d.position).toBe('—');
    expect(d.reading).toBe('—');
    expect(d.selected).toBe('0');
  });
  test('cursor without a selection object reads position from the start', () => {
    expect(describeStatus({ text: 'x', cursor: true }).position).toBe('1:1');
  });
  test('explicit bytes override computed size', () => {
    expect(describeStatus({ text: 'abc', bytes: 1024 }).size).toBe('1.0 KB');
  });
});
