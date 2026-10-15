import { uploadURL, resourceURL, markdownFor, isImage } from '../../lib/upload.js';

describe('uploadURL', () => {
  test('builds URL with encoded filename', () => {
    expect(uploadURL('work', 'photo.jpg')).toBe('/n/work/resources?filename=photo.jpg');
  });

  test('encodes special characters in filename', () => {
    expect(uploadURL('work', 'a b&c.txt')).toBe('/n/work/resources?filename=a%20b%26c.txt');
  });

  test('encodes unicode filename', () => {
    expect(uploadURL('work', 'héllo.png')).toBe('/n/work/resources?filename=h%C3%A9llo.png');
  });

  test('encodes notebook slug', () => {
    expect(uploadURL('my nb', 'a.txt')).toBe('/n/my%20nb/resources?filename=a.txt');
  });
});

describe('resourceURL', () => {
  test('builds serve URL', () => {
    const sha = 'a'.repeat(64);
    expect(resourceURL('work', sha)).toBe(`/n/work/resources/${sha}`);
  });

  test('encodes slug', () => {
    expect(resourceURL('my nb', 'abc')).toBe('/n/my%20nb/resources/abc');
  });
});

describe('markdownFor', () => {
  const sha = 'b'.repeat(64);

  test('image mime produces image embed', () => {
    const md = markdownFor('work', { sha, filename: 'photo.png', mime: 'image/png' });
    expect(md).toBe(`![photo.png](/n/work/resources/${sha})`);
  });

  test('non-image mime produces link', () => {
    const md = markdownFor('work', { sha, filename: 'doc.pdf', mime: 'application/pdf' });
    expect(md).toBe(`[doc.pdf](/n/work/resources/${sha})`);
  });

  test('missing mime falls back to link', () => {
    const md = markdownFor('work', { sha, filename: 'file.bin', mime: '' });
    expect(md).toBe(`[file.bin](/n/work/resources/${sha})`);
  });

  test('encodes filename in alt text', () => {
    const md = markdownFor('work', { sha, filename: 'a]b.png', mime: 'image/png' });
    expect(md).toContain('a]b.png');
  });
});

describe('isImage', () => {
  test('detects by mime', () => {
    expect(isImage('image/png', '')).toBe(true);
    expect(isImage('image/jpeg', '')).toBe(true);
    expect(isImage('application/pdf', '')).toBe(false);
  });

  test('detects by extension when mime missing', () => {
    expect(isImage('', 'photo.jpg')).toBe(true);
    expect(isImage('', 'photo.PNG')).toBe(true);
    expect(isImage('', 'clip.webp')).toBe(true);
    expect(isImage('', 'doc.pdf')).toBe(false);
  });

  test('false for empty inputs', () => {
    expect(isImage('', '')).toBe(false);
    expect(isImage(undefined, undefined)).toBe(false);
  });
});

import { thumbURL, posterURL, rewriteImageThumbs, findVideoResources } from '../../lib/upload.js';

describe('thumbURL', () => {
  const sha = 'c'.repeat(64);

  test('default dimensions', () => {
    expect(thumbURL('work', sha)).toBe(`/n/work/resources/${sha}/thumb?w=400&h=400`);
  });

  test('custom dimensions', () => {
    expect(thumbURL('work', sha, 200, 150)).toBe(`/n/work/resources/${sha}/thumb?w=200&h=150`);
  });

  test('encodes slug', () => {
    expect(thumbURL('my nb', sha)).toBe(`/n/my%20nb/resources/${sha}/thumb?w=400&h=400`);
  });
});

describe('posterURL', () => {
  test('builds poster URL', () => {
    const sha = 'd'.repeat(64);
    expect(posterURL('work', sha)).toBe(`/n/work/resources/${sha}/poster`);
  });
});

describe('rewriteImageThumbs', () => {
  const sha = 'e'.repeat(64);

  test('rewrites resource img to thumb', () => {
    const html = `<img src="/n/work/resources/${sha}" alt="photo">`;
    const out = rewriteImageThumbs(html, 'work', { maxWidth: 640, maxHeight: 480 });
    expect(out).toBe(`<img src="/n/work/resources/${sha}/thumb?w=640&h=480" alt="photo">`);
  });

  test('leaves already-thumb URLs alone', () => {
    const html = `<img src="/n/work/resources/${sha}/thumb?w=100&h=100">`;
    const out = rewriteImageThumbs(html, 'work');
    expect(out).toBe(html);
  });

  test('leaves external images alone', () => {
    const html = `<img src="https://example.com/pic.png">`;
    expect(rewriteImageThumbs(html, 'work')).toBe(html);
  });

  test('leaves non-resource local images alone', () => {
    const html = `<img src="/static/logo.png">`;
    expect(rewriteImageThumbs(html, 'work')).toBe(html);
  });

  test('rewrites multiple images', () => {
    const s1 = 'a'.repeat(64);
    const s2 = 'b'.repeat(64);
    const html = `<p><img src="/n/work/resources/${s1}"><img src="/n/work/resources/${s2}"></p>`;
    const out = rewriteImageThumbs(html, 'work', { maxWidth: 100, maxHeight: 100 });
    expect(out).toContain(`/n/work/resources/${s1}/thumb?w=100&h=100`);
    expect(out).toContain(`/n/work/resources/${s2}/thumb?w=100&h=100`);
  });

  test('handles slug with regex metacharacters', () => {
    const html = `<img src="/n/a.b/resources/${sha}">`;
    // "a.b" should not match "axb"
    const out = rewriteImageThumbs(html, 'a.b');
    expect(out).toContain('/thumb?w=800&h=800');
    expect(out).not.toContain('/n/axb/');
  });
});

describe('findVideoResources', () => {
  test('extracts sha list from HTML', () => {
    const s1 = 'a'.repeat(64);
    const s2 = 'b'.repeat(64);
    const html = `<a href="/n/work/resources/${s1}">x</a><img src="/n/work/resources/${s2}">`;
    const shas = findVideoResources(html, 'work');
    expect(shas).toHaveLength(2);
    expect(shas).toContain(s1);
    expect(shas).toContain(s2);
  });

  test('deduplicates', () => {
    const s = 'a'.repeat(64);
    const html = `<a href="/n/work/resources/${s}">x</a><a href="/n/work/resources/${s}">y</a>`;
    expect(findVideoResources(html, 'work')).toHaveLength(1);
  });

  test('empty when no matches', () => {
    expect(findVideoResources('<p>hello</p>', 'work')).toHaveLength(0);
  });
});
