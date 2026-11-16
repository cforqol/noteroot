import { formatPipeTables } from '../../lib/tables.js';

test('pads cells, normalizes the delimiter, and adds leading/trailing pipes', () => {
  const src = [
    '|Name|Age|',
    '|-|-|',
    '|Alice|30|',
    '|Bob|7|',
  ].join('\n');
  const { content, changed } = formatPipeTables(src);
  expect(changed).toBe(true);
  expect(content).toBe([
    '| Name  | Age |',
    '| ----- | --- |',
    '| Alice | 30  |',
    '| Bob   | 7   |',
  ].join('\n'));
});

test('already-formatted tables are reported as unchanged', () => {
  const src = [
    '| Name  | Age |',
    '| ----- | --- |',
    '| Alice | 30  |',
  ].join('\n');
  const { content, changed } = formatPipeTables(src);
  expect(changed).toBe(false);
  expect(content).toBe(src);
});

test('honors left/right/center alignment markers, keeping total column width', () => {
  const src = [
    '| Item | Qty | Price |',
    '|:--|--:|:--:|',
    '| apple | 10 | 1.50 |',
  ].join('\n');
  const { content } = formatPipeTables(src);
  expect(content).toBe([
    '| Item  | Qty | Price |',
    '| :---- | --: | :---: |',
    '| apple |  10 | 1.50  |',
  ].join('\n'));
});

test('a row with fewer or more cells than the header is padded/truncated, matching CommonMark', () => {
  const src = [
    '| A | B | C |',
    '| - | - | - |',
    '| short |',
    '| too | many | cells | here |',
  ].join('\n');
  const { content } = formatPipeTables(src);
  const lines = content.split('\n');
  expect(lines[2]).toBe('| short |      |       |');
  expect(lines[3]).toBe('| too   | many | cells |');
});

test('text inside a fenced code block is left untouched, even if it contains pipes and dashes', () => {
  const src = [
    '```',
    '| not | a | table |',
    '| --- | - | ----- |',
    '```',
  ].join('\n');
  const { content, changed } = formatPipeTables(src);
  expect(changed).toBe(false);
  expect(content).toBe(src);
});

test('a table is still formatted correctly after a preceding fenced block', () => {
  const src = [
    '```js',
    'const x = 1;',
    '```',
    '',
    '|A|B|',
    '|-|-|',
    '|1|2|',
  ].join('\n');
  const { content } = formatPipeTables(src);
  expect(content.split('\n').slice(4)).toEqual(['| A   | B   |', '| --- | --- |', '| 1   | 2   |']);
});

test('a line with a pipe but no valid delimiter row after it is left alone', () => {
  const src = 'a | b | c\nnot a delimiter row\n';
  const { content, changed } = formatPipeTables(src);
  expect(changed).toBe(false);
  expect(content).toBe(src);
});

test('escaped pipes inside a cell are preserved as literal text, not column separators', () => {
  const src = [
    '| A | B |',
    '| - | - |',
    '| x\\|y | z |',
  ].join('\n');
  const { content } = formatPipeTables(src);
  expect(content.split('\n')[2]).toBe('| x\\|y | z   |');
});

test('preserves CRLF line endings when the source uses them', () => {
  const src = '|A|B|\r\n|-|-|\r\n|1|2|';
  const { content } = formatPipeTables(src);
  expect(content).toBe('| A   | B   |\r\n| --- | --- |\r\n| 1   | 2   |');
});

test('a note with no tables is reported unchanged', () => {
  const src = 'Just some prose.\n\nNo tables here.\n';
  const { content, changed } = formatPipeTables(src);
  expect(changed).toBe(false);
  expect(content).toBe(src);
});

test('a short column still pads to the minimum valid delimiter width of 3', () => {
  const src = [
    '|A|B|',
    '|-|-|',
    '|1|22|',
  ].join('\n');
  const { content } = formatPipeTables(src);
  expect(content).toBe(['| A   | B   |', '| --- | --- |', '| 1   | 22  |'].join('\n'));
});

test('multiple tables in one note are each formatted independently', () => {
  const src = [
    '|A|B|',
    '|-|-|',
    '|1|22|',
    '',
    'prose between',
    '',
    '|Long header|X|',
    '|-|-|',
    '|y|z|',
  ].join('\n');
  const { content } = formatPipeTables(src);
  const lines = content.split('\n');
  expect(lines[0]).toBe('| A   | B   |');
  expect(lines[2]).toBe('| 1   | 22  |');
  expect(lines[6]).toBe('| Long header | X   |');
  expect(lines[8]).toBe('| y           | z   |');
});
