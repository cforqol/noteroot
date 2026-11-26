import {
  classifyAttachmentRef,
  attachmentRefFilename,
  extractAttachmentRefs,
  rewriteAttachmentRef,
} from '../../lib/import-attachments.js';

describe('classifyAttachmentRef', () => {
  test('http(s) URLs are fetchable', () => {
    expect(classifyAttachmentRef('http://example.com/a.png')).toBe('http');
    expect(classifyAttachmentRef('https://example.com/a.png')).toBe('http');
  });

  test('data/mailto/tel URIs are skipped', () => {
    expect(classifyAttachmentRef('data:image/png;base64,abc')).toBe('skip');
    expect(classifyAttachmentRef('mailto:a@b.com')).toBe('skip');
    expect(classifyAttachmentRef('tel:+123456')).toBe('skip');
  });

  test('bare anchors are skipped', () => {
    expect(classifyAttachmentRef('#section')).toBe('skip');
  });

  test('empty/missing target is skipped', () => {
    expect(classifyAttachmentRef('')).toBe('skip');
    expect(classifyAttachmentRef(undefined)).toBe('skip');
  });

  test('this notebook\'s own resource link is already resolved', () => {
    const sha = 'a'.repeat(64);
    expect(classifyAttachmentRef(`/n/work/resources/${sha}`, 'work')).toBe('resource');
  });

  test('own resource link with a query/fragment suffix is still resolved', () => {
    const sha = 'b'.repeat(64);
    expect(classifyAttachmentRef(`/n/work/resources/${sha}?x=1`, 'work')).toBe('resource');
  });

  test('a link to a different notebook is skipped, not treated as local', () => {
    const sha = 'c'.repeat(64);
    expect(classifyAttachmentRef(`/n/other/resources/${sha}`, 'work')).toBe('skip');
  });

  test('a /n/... link is skipped even without a slug to compare against', () => {
    const sha = 'd'.repeat(64);
    expect(classifyAttachmentRef(`/n/work/resources/${sha}`)).toBe('skip');
  });

  test('any other absolute site path is skipped', () => {
    expect(classifyAttachmentRef('/static/logo.png')).toBe('skip');
  });

  test('a relative path is local', () => {
    expect(classifyAttachmentRef('images/photo.png')).toBe('local');
    expect(classifyAttachmentRef('./photo.png')).toBe('local');
    expect(classifyAttachmentRef('../assets/photo.png')).toBe('local');
  });

  test('a bare filename is local', () => {
    expect(classifyAttachmentRef('photo.png')).toBe('local');
  });

  test('slug containing regex metacharacters does not break the resource check', () => {
    const sha = 'e'.repeat(64);
    expect(classifyAttachmentRef(`/n/a.b/resources/${sha}`, 'a.b')).toBe('resource');
    expect(classifyAttachmentRef(`/n/axb/resources/${sha}`, 'a.b')).toBe('skip');
  });
});

describe('attachmentRefFilename', () => {
  test('returns the last path segment', () => {
    expect(attachmentRefFilename('images/photo.png')).toBe('photo.png');
    expect(attachmentRefFilename('photo.png')).toBe('photo.png');
  });

  test('strips query strings and fragments', () => {
    expect(attachmentRefFilename('https://example.com/p/cat.png?w=200#frag')).toBe('cat.png');
  });

  test('decodes percent-encoded characters', () => {
    expect(attachmentRefFilename('https://example.com/My%20Photo.png')).toBe('My Photo.png');
  });

  test('falls back to the raw segment when decoding fails', () => {
    expect(attachmentRefFilename('weird%zzname.png')).toBe('weird%zzname.png');
  });
});

describe('extractAttachmentRefs', () => {
  test('extracts a markdown image reference', () => {
    const refs = extractAttachmentRefs('before ![alt text](images/photo.png) after');
    expect(refs).toEqual([{ target: 'images/photo.png', kind: 'local', filename: 'photo.png' }]);
  });

  test('extracts a markdown link reference', () => {
    const refs = extractAttachmentRefs('see [the doc](https://example.com/doc.pdf) for more');
    expect(refs).toEqual([{ target: 'https://example.com/doc.pdf', kind: 'http', filename: 'doc.pdf' }]);
  });

  test('extracts a raw <img src> reference', () => {
    const refs = extractAttachmentRefs('<img src="https://example.com/pic.jpg" alt="x">');
    expect(refs).toEqual([{ target: 'https://example.com/pic.jpg', kind: 'http', filename: 'pic.jpg' }]);
  });

  test('ignores a markdown link with an optional title', () => {
    const refs = extractAttachmentRefs('![alt](./local.png "a title")');
    expect(refs).toEqual([{ target: './local.png', kind: 'local', filename: 'local.png' }]);
  });

  test('skips already-resolved resource links and non-attachment links', () => {
    const sha = 'f'.repeat(64);
    const md = `![x](/n/work/resources/${sha}) and [anchor](#top) and [mail](mailto:a@b.com)`;
    expect(extractAttachmentRefs(md, 'work')).toEqual([]);
  });

  test('deduplicates repeated targets across multiple references', () => {
    const md = '![a](img.png) and again ![b](img.png)';
    const refs = extractAttachmentRefs(md);
    expect(refs).toHaveLength(1);
  });

  test('mixes multiple distinct kinds in one note', () => {
    const md = [
      '![local](assets/a.png)',
      '[remote](https://example.com/b.png)',
      '<img src="c.gif">',
    ].join('\n');
    const refs = extractAttachmentRefs(md);
    expect(refs.map((r) => r.target)).toEqual(['assets/a.png', 'https://example.com/b.png', 'c.gif']);
    expect(refs.map((r) => r.kind)).toEqual(['local', 'http', 'local']);
  });

  test('returns an empty array for markdown with no references', () => {
    expect(extractAttachmentRefs('# Just a heading\n\nSome text.')).toEqual([]);
  });

  test('handles missing/empty markdown', () => {
    expect(extractAttachmentRefs('')).toEqual([]);
    expect(extractAttachmentRefs(undefined)).toEqual([]);
  });
});

describe('rewriteAttachmentRef', () => {
  test('replaces every occurrence of the target with the resource URL', () => {
    const md = '![a](img.png) ... ![a again](img.png)';
    const out = rewriteAttachmentRef(md, 'img.png', '/n/work/resources/abc');
    expect(out).toBe('![a](/n/work/resources/abc) ... ![a again](/n/work/resources/abc)');
  });

  test('leaves the text unchanged when the target is not present', () => {
    const md = '![a](other.png)';
    expect(rewriteAttachmentRef(md, 'img.png', '/n/work/resources/abc')).toBe(md);
  });

  test('returns the input unchanged for an empty target', () => {
    const md = '![a](img.png)';
    expect(rewriteAttachmentRef(md, '', '/n/work/resources/abc')).toBe(md);
  });
});
