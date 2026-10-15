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

// --- Presigned flow (stage 7) ---

import {
  presignURL,
  confirmURL,
  presignReadURL,
  sha256Hex,
  presignUpload,
} from '../../lib/upload.js';

describe('presignURL/confirmURL/presignReadURL', () => {
  test('presign and confirm endpoints', () => {
    expect(presignURL('work')).toBe('/n/work/resources/presign');
    expect(confirmURL('work')).toBe('/n/work/resources/confirm');
  });
  test('encodes slug', () => {
    expect(presignURL('my nb')).toBe('/n/my%20nb/resources/presign');
  });
  test('presign-read uses resource URL base', () => {
    const sha = 'f'.repeat(64);
    expect(presignReadURL('work', sha)).toBe(`/n/work/resources/${sha}/presign-read`);
  });
});

describe('sha256Hex', () => {
  test('hashes an ArrayBuffer to hex', async () => {
    const buf = new TextEncoder().encode('hello').buffer;
    const hex = await sha256Hex(buf);
    expect(hex).toMatch(/^[a-f0-9]{64}$/);
    // known sha256("hello")
    expect(hex).toBe('2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824');
  });
  test('hashes a File-like object with arrayBuffer()', async () => {
    const fileLike = {
      arrayBuffer: async () => new TextEncoder().encode('hello').buffer,
    };
    const hex = await sha256Hex(fileLike);
    expect(hex).toBe('2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824');
  });
  test('hashes a TypedArray', async () => {
    const bytes = new TextEncoder().encode('hello');
    const hex = await sha256Hex(bytes);
    expect(hex).toBe('2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824');
  });
  test('rejects on invalid input', async () => {
    await expect(sha256Hex(undefined)).rejects.toThrow();
  });
});

function jsonRes(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  };
}

// File-like duck type: jsdom's Blob/File lack .arrayBuffer(), so tests
// use a plain object with name/size/type/arrayBuffer matching the File
// contract presignUpload relies on.
function fileNamed(name, type) {
  const bytes = new TextEncoder().encode('hello');
  return {
    name,
    size: 5,
    type: type === undefined ? 'text/plain' : type,
    arrayBuffer: async () => bytes.buffer.slice(0),
  };
}

describe('presignUpload', () => {
  test('direct mode when no WebCrypto', async () => {
    const orig = globalThis.crypto;
    // Simulate absence of subtle.
    Object.defineProperty(globalThis, 'crypto', { value: {}, configurable: true });
    try {
      const r = await presignUpload('work', fileNamed('a.txt'));
      expect(r.mode).toBe('direct');
    } finally {
      Object.defineProperty(globalThis, 'crypto', { value: orig, configurable: true });
    }
  });

  test('exists mode when blob already stored', async () => {
    const sha = 'a'.repeat(64);
    const fetchFn = async (url, init) => {
      expect(url).toBe('/n/work/resources/presign');
      return jsonRes(200, { exists: true, sha });
    };
    const r = await presignUpload('work', fileNamed('a.txt'), { fetchFn });
    expect(r).toEqual({ mode: 'exists', sha });
  });

  test('direct mode from server response', async () => {
    const fetchFn = async () => jsonRes(200, { direct: true });
    const r = await presignUpload('work', fileNamed('a.txt'), { fetchFn });
    expect(r.mode).toBe('direct');
  });

  test('s3 mode: PUT then confirm', async () => {
    const sha = 'b'.repeat(64);
    const calls = [];
    const fetchFn = async (url, init) => {
      calls.push(url);
      if (url.endsWith('/presign')) {
        const body = JSON.parse(init.body);
        expect(body.sha).toMatch(/^[a-f0-9]{64}$/);
        expect(body.filename).toBe('a.txt');
        expect(body.size).toBe(5);
        return jsonRes(200, {
          direct: false,
          uploadURL: 'https://s3.example.com/bucket/aa/sha?X-Amz-Signature=abc',
          sha: body.sha,
          entryID: 'e-123',
          expiresIn: 900,
        });
      }
      if (url.endsWith('/confirm')) {
        const body = JSON.parse(init.body);
        expect(body.entryID).toBe('e-123');
        expect(body.sha).toMatch(/^[a-f0-9]{64}$/);
        return jsonRes(200, { ok: true, sha: body.sha });
      }
      throw new Error('unexpected fetch: ' + url);
    };
    const putFn = async (url, init) => {
      expect(url).toContain('X-Amz-Signature=abc');
      expect(init.method).toBe('PUT');
      return { ok: true, status: 200 };
    };
    const r = await presignUpload('work', fileNamed('a.txt'), { fetchFn, putFn });
    expect(r.mode).toBe('s3');
    expect(r.sha).toMatch(/^[a-f0-9]{64}$/);
    expect(r.result.created).toBe(true);
    expect(r.result.filename).toBe('a.txt');
    expect(calls).toHaveLength(2);
  });

  test('error mode when presign returns policy rejection', async () => {
    const fetchFn = async () => jsonRes(415, { error: 'unsupported media type' });
    const r = await presignUpload('work', fileNamed('evil.exe'), { fetchFn });
    expect(r).toEqual({ mode: 'error', error: 'unsupported media type' });
  });

  test('throws when S3 PUT fails', async () => {
    const fetchFn = async (url) => {
      if (url.endsWith('/presign')) {
        return jsonRes(200, { direct: false, uploadURL: 'https://s3/x', sha: 'c'.repeat(64), entryID: 'e1', expiresIn: 900 });
      }
      return jsonRes(500, {});
    };
    const putFn = async () => ({ ok: false, status: 403 });
    await expect(presignUpload('work', fileNamed('a.txt'), { fetchFn, putFn }))
      .rejects.toThrow(/S3 upload failed \(403\)/);
  });

  test('throws when confirm fails', async () => {
    const sha = 'd'.repeat(64);
    const fetchFn = async (url) => {
      if (url.endsWith('/presign')) {
        return jsonRes(200, { direct: false, uploadURL: 'https://s3/x', sha, entryID: 'e1', expiresIn: 900 });
      }
      return jsonRes(409, { error: 'blob not found in store' });
    };
    const putFn = async () => ({ ok: true, status: 200 });
    await expect(presignUpload('work', fileNamed('a.txt'), { fetchFn, putFn }))
      .rejects.toThrow(/blob not found in store/);
  });
});

describe('presignUpload branch coverage (stage 7)', () => {
  test('presign response without error field keeps generic message', async () => {
    const fetchFn = async () => jsonRes(500, {});
    const r = await presignUpload('work', fileNamed('a.txt'), { fetchFn });
    expect(r).toEqual({ mode: 'error', error: 'presign failed (500)' });
  });

  test('confirm response without error field keeps generic message', async () => {
    const sha = '9'.repeat(64);
    const fetchFn = async (url) => {
      if (url.endsWith('/presign')) {
        return jsonRes(200, { direct: false, uploadURL: 'https://s3/x', sha, entryID: 'e1', expiresIn: 900 });
      }
      return jsonRes(500, {});
    };
    const putFn = async () => ({ ok: true, status: 200 });
    await expect(presignUpload('work', fileNamed('a.txt'), { fetchFn, putFn }))
      .rejects.toThrow('confirm failed (500)');
  });

  test('returns direct when hashing fails', async () => {
    const bad = {
      name: 'a.txt',
      size: 1,
      type: 'text/plain',
      arrayBuffer: async () => { throw new Error('read failed'); },
    };
    const fetchFn = async () => { throw new Error('must not fetch'); };
    const r = await presignUpload('work', bad, { fetchFn });
    expect(r.mode).toBe('direct');
  });

  test('uses global fetch when deps omitted, full s3 flow, empty MIME', async () => {
    const orig = globalThis.fetch;
    const urls = [];
    globalThis.fetch = async (url, init) => {
      urls.push(String(url));
      if (String(url).endsWith('/presign')) {
        const body = JSON.parse(init.body);
        expect(body.mime).toBe('application/octet-stream'); // file.type empty
        return jsonRes(200, {
          direct: false,
          uploadURL: 'https://s3.example.com/bucket/obj?X-Amz-Signature=z',
          sha: body.sha,
          entryID: 'e42',
          expiresIn: 900,
        });
      }
      if (String(url).startsWith('https://s3.example.com/')) {
        expect(init.method).toBe('PUT');
        expect(init.headers['Content-Type']).toBe('application/octet-stream');
        return { ok: true, status: 200 };
      }
      if (String(url).endsWith('/confirm')) {
        return jsonRes(200, { ok: true, sha: JSON.parse(init.body).sha });
      }
      throw new Error('unexpected url: ' + url);
    };
    try {
      const r = await presignUpload('work', fileNamed('noext', ''));
      expect(r.mode).toBe('s3');
      expect(r.result.mime).toBe('application/octet-stream');
      expect(urls.some((u) => u.endsWith('/presign'))).toBe(true);
      expect(urls.some((u) => u.endsWith('/confirm'))).toBe(true);
    } finally {
      if (orig === undefined) delete globalThis.fetch;
      else globalThis.fetch = orig;
    }
  });
});
