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
