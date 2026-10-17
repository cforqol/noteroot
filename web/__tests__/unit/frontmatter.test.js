import {
  ID_RE, isValidId, encodeULID, ulidChecksum, mintId, localDate, splitFrontmatter,
  readFrontmatter, deriveTitle, yamlString, normalizeNote, changedRange,
} from '../../lib/frontmatter.js';

const ID = '01ARZ3NDEKTSV4RRFFQ69G5FAV00';
const fixed = { mint: () => '01HF7YAT00041061050R3GG28ARH', today: '2026-10-17' };

describe('ids (vectors generated from internal/frontmatter encodeULID + ulidChecksum)', () => {
  test.each([
    [0, [0, 0, 0, 0, 0, 0, 0, 0, 0, 0], '00000000000000000000000000', 'E2'],
    [1700000000000, [1, 2, 3, 4, 5, 6, 7, 8, 9, 10], '01HF7YAT00041061050R3GG28A', 'RH'],
    [1760659200123, [255, 254, 253, 252, 251, 250, 249, 248, 247, 246], '01K7QQEP3VZZZFVZ7VZBWZHXZP', 'AX'],
    [281474976710655, [0xde, 0xad, 0xbe, 0xef, 0, 0x11, 0x22, 0x33, 0x44, 0x55], '7ZZZZZZZZZVTPVXVR024H36H2N', 'V1'],
  ])('ts=%d', (ts, rnd, ulid, sum) => {
    expect(encodeULID(ts, rnd)).toBe(ulid);
    expect(ulidChecksum(ulid)).toBe(sum);
    expect(mintId({ now: ts, random: () => Uint8Array.from(rnd) })).toBe(ulid + sum);
  });

  test('timestamps wrap to 48 bits like the server', () => {
    expect(encodeULID(2 ** 48 + 5, new Uint8Array(10))).toBe(encodeULID(5, new Uint8Array(10)));
  });

  test('default minting is random, valid and time-sortable', () => {
    const a = mintId();
    const b = mintId();
    expect(ID_RE.test(a) && isValidId(b)).toBe(true);
    expect(a).not.toBe(b);
    expect(mintId({ now: 1 }) < mintId({ now: 2 ** 40 })).toBe(true);
  });

  test('isValidId', () => {
    expect(isValidId(ID)).toBe(true);
    for (const bad of ['', 'nope', `${ID}0`, ID.replace('A', 'I'), ID.toLowerCase(), 42, null]) {
      expect(isValidId(bad)).toBe(false);
    }
  });
});

test('localDate pads month and day', () => {
  expect(localDate(new Date(2026, 0, 5))).toBe('2026-01-05');
  expect(localDate()).toMatch(/^\d{4}-\d{2}-\d{2}$/);
});

describe('splitFrontmatter (server rules)', () => {
  test('round-trips the pieces', () => {
    for (const src of ['---\nid: x\n---\nbody', '---\r\na: 1\r\n---\r\nb\r\n', '---\n---\n', '---\nk: v\n---']) {
      const sp = splitFrontmatter(src);
      expect(sp.ok).toBe(true);
      expect(sp.head + sp.block + sp.close + sp.body).toBe(src);
    }
    expect(splitFrontmatter('---\r\na: 1\r\n---\r\nb').eol).toBe('\r\n');
    expect(splitFrontmatter('---\nk: v\n---').body).toBe('');
  });

  test('no block: missing, unterminated, wrong opener, oversized', () => {
    for (const src of [null, '', 'body', '--- \nx\n---\n', '----\nx\n---\n', '---', '---\nunterminated\n', '---\nno newline', `---\n${'a'.repeat(70000)}\n---\n`]) {
      const sp = splitFrontmatter(src);
      expect(sp.ok).toBe(false);
      expect(sp.body).toBe(String(src ?? ''));
    }
    expect(splitFrontmatter('a\r\nb').eol).toBe('\r\n');
  });

  test('the 64 KiB limit counts UTF-8 bytes of the block', () => {
    const block = `${'é'.repeat(33000)}\n`; // 33001 code units, 66001 bytes
    expect(splitFrontmatter(`---\n${block}---\nx`).ok).toBe(false);
    expect(splitFrontmatter(`---\n${'é'.repeat(1000)}\n---\n${'big body '.repeat(10000)}`).ok).toBe(true);
  });
});

describe('readFrontmatter', () => {
  test('reads top-level keys, quotes and comments; ignores nesting', () => {
    const fm = readFrontmatter([
      '---',
      `id: "${ID}" # identity`,
      "title: 'Quoted # not a comment'",
      'created: 2024-01-02 # a comment',
      'meta:',
      '  updated: nested',
      '- stray',
      '# comment: line',
      'updated: #empty',
      '---',
      'body',
    ].join('\n'));
    expect(fm).toEqual({ ok: true, id: ID, title: 'Quoted # not a comment', created: '2024-01-02', updated: '' });
  });

  test('a key with an indented or sequence value is present; unterminated quotes tolerated', () => {
    expect(readFrontmatter('---\ntitle: |\n  long\n---\n').title).toBe('|');
    expect(readFrontmatter('---\ntitle: "open\n---\n').title).toBe('open');
    expect(readFrontmatter('---\nnokey\n---\n').ok).toBe(true);
    expect(readFrontmatter('plain').ok).toBe(false);
  });
});

describe('deriveTitle', () => {
  test('first H1 outside code fences, closing hashes trimmed', () => {
    expect(deriveTitle('intro\n```\n# not this\n```\n~~~\n# nor\n~~~\n  # Real title ##\n# second', 'a/b.md')).toBe('Real title');
    expect(deriveTitle('```\n~~~\n# inside\n```\n# after', '')).toBe('after');
    expect(deriveTitle('#nospace\n## h2 only\n#\n', 'dir/My note.md')).toBe('My note');
    expect(deriveTitle('', '')).toBe('Untitled');
    expect(deriveTitle(null, null)).toBe('Untitled');
    expect(deriveTitle(`# ${'x'.repeat(300)}`, '')).toHaveLength(200);
  });
});

test('yamlString quotes only when needed', () => {
  expect(yamlString('Plain title, with (parens) & stuff!')).toBe('Plain title, with (parens) & stuff!');
  expect(yamlString('Ünïcode 日本')).toBe('Ünïcode 日本');
  for (const v of ['Meeting: notes', 'a #tag', '2024', 'yes', 'No', '~', '- dash', 'trailing ', '"q"', '', '[x]', null]) {
    const out = yamlString(v);
    expect(out).toBe(JSON.stringify(String(v ?? '')));
  }
});

describe('normalizeNote', () => {
  test('adds a full block to a note without frontmatter', () => {
    const r = normalizeNote('# Hello world\n\nText', { path: 'x/y.md', ...fixed, created: '2024-05-06' });
    expect(r).toEqual({
      content: '---\nid: 01HF7YAT00041061050R3GG28ARH\ntitle: Hello world\ncreated: 2024-05-06\nupdated: 2024-05-06\n---\n\n# Hello world\n\nText',
      changed: true,
      added: ['id', 'title', 'created', 'updated'],
      id: '01HF7YAT00041061050R3GG28ARH',
    });
  });

  test('empty and blank-led notes, CRLF, preferred id, explicit updated', () => {
    expect(normalizeNote('', { path: 'n.md', ...fixed }).content).toContain('title: "n"\n'); // YAML 1.1 boolean
    expect(normalizeNote('', { path: 'note.md', ...fixed }).content)
      .toBe('---\nid: 01HF7YAT00041061050R3GG28ARH\ntitle: note\ncreated: 2026-10-17\nupdated: 2026-10-17\n---\n');
    expect(normalizeNote('\nbody', { id: ID, ...fixed }).content).toBe(`---\nid: ${ID}\ntitle: Untitled\ncreated: 2026-10-17\nupdated: 2026-10-17\n---\n\nbody`);
    expect(normalizeNote('a\r\nb', { id: ID, ...fixed, updated: '2025-01-01' }).content)
      .toBe(`---\r\nid: ${ID}\r\ntitle: Untitled\r\ncreated: 2026-10-17\r\nupdated: 2025-01-01\r\n---\r\n\r\na\r\nb`);
    expect(normalizeNote('\r\nx', { id: ID, ...fixed }).content.endsWith('---\r\n\r\nx')).toBe(true);
  });

  test('fills only the missing keys, keeps the rest byte-for-byte', () => {
    const src = `---\n# my notes\nid: ${ID}\ntags: [a, b]\ntitle:   Kept  \n---\nbody: not frontmatter\n`;
    const r = normalizeNote(src, { ...fixed, created: '2020-02-02' });
    expect(r.content).toBe(`---\n# my notes\nid: ${ID}\ntags: [a, b]\ntitle:   Kept  \ncreated: 2020-02-02\nupdated: 2020-02-02\n---\nbody: not frontmatter\n`);
    expect(r.added).toEqual(['created', 'updated']);
    expect(r.id).toBe(ID);
  });

  test('replaces an invalid id in place and inserts a missing one first', () => {
    expect(normalizeNote('---\ntitle: T\nid: bad\ncreated: 1\nupdated: 2\n---\n', fixed).content)
      .toBe('---\ntitle: T\nid: 01HF7YAT00041061050R3GG28ARH\ncreated: 1\nupdated: 2\n---\n');
    const r = normalizeNote('---\ntitle:\ncreated: 1\nupdated: 2\n---\n# Heading\n', { ...fixed, id: ID });
    expect(r.content).toBe(`---\nid: ${ID}\ntitle: Heading\ncreated: 1\nupdated: 2\n---\n# Heading\n`);
    expect(r.added).toEqual(['id', 'title']);
  });

  test('CRLF blocks keep CRLF on every touched line; empty block grows', () => {
    const r = normalizeNote(`---\r\nid: ${ID}\r\ntitle: \r\n---\r\n# B\r\n`, fixed);
    expect(r.content).toBe(`---\r\nid: ${ID}\r\ntitle: B\r\ncreated: 2026-10-17\r\nupdated: 2026-10-17\r\n---\r\n# B\r\n`);
    expect(normalizeNote('---\n---\n', { ...fixed, path: 'e.md' }).content)
      .toBe('---\nid: 01HF7YAT00041061050R3GG28ARH\ntitle: e\ncreated: 2026-10-17\nupdated: 2026-10-17\n---\n');
  });

  test('a correct note is untouched; touch updates only `updated`', () => {
    const ok = `---\nid: ${ID}\ntitle: T\ncreated: 2020-01-01\nupdated: 2020-01-02\n---\nx`;
    expect(normalizeNote(ok, fixed)).toEqual({ content: ok, changed: false, added: [], id: ID });
    const t = normalizeNote(ok, { ...fixed, touch: true });
    expect(t.content).toBe(ok.replace('updated: 2020-01-02', 'updated: 2026-10-17'));
    expect(normalizeNote(t.content, { ...fixed, touch: true }).changed).toBe(false);
    expect(normalizeNote(ok.replace('updated: 2020-01-02\n', ''), { ...fixed, touch: true }).content)
      .toBe(`---\nid: ${ID}\ntitle: T\ncreated: 2020-01-01\nupdated: 2026-10-17\n---\nx`);
  });

  test('is idempotent and never touches the body', () => {
    const body = '\n---\nid: fake\n---\n';
    const once = normalizeNote(`plain${body}`, fixed).content;
    expect(once.endsWith(`plain${body}`)).toBe(true);
    expect(normalizeNote(once, fixed).changed).toBe(false);
  });

  test('defaults: mints, dates today', () => {
    const r = normalizeNote('x');
    expect(isValidId(r.id)).toBe(true);
    expect(r.content).toContain(`created: ${localDate()}`);
    expect(normalizeNote(null).changed).toBe(true);
  });
});

test('changedRange finds the single replaced span', () => {
  expect(changedRange('abc', 'abc')).toBeNull();
  expect(changedRange('hello world', 'hello brave world')).toEqual({ start: 6, end: 6, text: 'brave ' });
  expect(changedRange('aXc', 'aYYc')).toEqual({ start: 1, end: 2, text: 'YY' });
  expect(changedRange('aaa', 'aa')).toEqual({ start: 2, end: 3, text: '' });
  expect(changedRange('', 'x')).toEqual({ start: 0, end: 0, text: 'x' });
});
