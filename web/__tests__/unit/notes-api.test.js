import { jest } from '@jest/globals';
import {
  getTree, createNote, deleteNote, moveNote, createFolder, moveFolder,
  deleteFolder, listTrash, restoreTrash, getNote, getSource, getRaw,
  deleteTrashEntry, setTrashRetention, purgeTrash,
  saveNote, exportNotebook, importNotebook, createBackup, resourceStats,
} from '../../lib/notes-api.js';

function mockFetch(responses) {
  const calls = [];
  const f = jest.fn(async (url, init) => {
    calls.push({ url, init });
    const next = responses.shift() ?? { status: 200, body: {} };
    return {
      ok: next.status >= 200 && next.status < 300,
      status: next.status,
      statusText: next.statusText || '',
      json: async () => { if (next.body === undefined) throw new Error('no body'); return next.body; },
    };
  });
  f.calls = calls;
  return f;
}

test('getTree', async () => {
  const f = mockFetch([{ status: 200, body: { children: [] } }]);
  expect(await getTree('my nb', f)).toEqual({ children: [] });
  expect(f.calls[0].url).toBe('/n/my%20nb/tree');
  expect(f.calls[0].init.method).toBe('GET');
  expect(f.calls[0].init.body).toBeUndefined();
});

test('createNote is create-only markdown', async () => {
  const f = mockFetch([{ status: 200, body: { sha: 's' } }]);
  await createNote('nb', 'a b/c.md', '# c', f);
  const { url, init } = f.calls[0];
  expect(url).toBe('/n/nb/note?path=a%20b%2Fc.md');
  expect(init.method).toBe('PUT');
  expect(init.body).toBe('# c');
  expect(init.headers['If-None-Match']).toBe('*');
  expect(init.headers['Content-Type']).toMatch(/markdown/);
});

test('createNote surfaces 412 as an error with status', async () => {
  const f = mockFetch([{ status: 412, body: { error: 'precondition failed', currentSha: 'abc' } }]);
  await expect(createNote('nb', 'a.md', 'x', f)).rejects.toMatchObject({
    message: 'precondition failed', status: 412, currentSha: 'abc',
  });
});

test('deleteNote fetches the sha then sends If-Match', async () => {
  const f = mockFetch([{ status: 200, body: { sha: 'deadbeef' } }, { status: 200, body: { trashed: ['t'] } }]);
  expect(await deleteNote('nb', 'a.md', f)).toEqual({ trashed: ['t'] });
  expect(f.calls[0].url).toBe('/n/nb/note?path=a.md');
  expect(f.calls[1].init.method).toBe('DELETE');
  expect(f.calls[1].init.headers['If-Match']).toBe('"deadbeef"');
  expect(f.calls[1].init.headers['Content-Type']).toBe('application/json');
});

test('deleteNote stops when the lookup fails', async () => {
  const f = mockFetch([{ status: 404, body: { error: 'not found' } }]);
  await expect(deleteNote('nb', 'ghost.md', f)).rejects.toMatchObject({ status: 404 });
  expect(f).toHaveBeenCalledTimes(1);
});

test('moveNote / moveFolder / createFolder send JSON bodies', async () => {
  const f = mockFetch([{ status: 200, body: {} }, { status: 200, body: {} }, { status: 201, body: {} }]);
  await moveNote('nb', 'a.md', 'b.md', f);
  await moveFolder('nb', 'a', 'b', f);
  await createFolder('nb', 'x/y', f);
  expect(f.calls.map((c) => [c.init.method, c.url])).toEqual([
    ['PATCH', '/n/nb/note'], ['PATCH', '/n/nb/folder'], ['POST', '/n/nb/folder'],
  ]);
  expect(JSON.parse(f.calls[0].init.body)).toEqual({ from: 'a.md', to: 'b.md' });
  expect(JSON.parse(f.calls[1].init.body)).toEqual({ from: 'a', to: 'b' });
  expect(JSON.parse(f.calls[2].init.body)).toEqual({ path: 'x/y' });
  for (const c of f.calls) expect(c.init.headers['Content-Type']).toBe('application/json');
});

test('deleteFolder uses the path query', async () => {
  const f = mockFetch([{ status: 200, body: { trashed: [] } }]);
  await deleteFolder('nb', 'a b', f);
  expect(f.calls[0].url).toBe('/n/nb/folder?path=a%20b');
  expect(f.calls[0].init.method).toBe('DELETE');
});

test('listTrash returns entries with the notebook retention', async () => {
  const f = mockFetch([
    { status: 200, body: { entries: [{ name: 'n' }], retentionDays: 30 } },
    { status: 200, body: {} },
    { status: 200, body: { entries: [], retentionDays: 'nonsense' } },
  ]);
  expect(await listTrash('nb', f)).toEqual({ entries: [{ name: 'n' }], retentionDays: 30 });
  // A server that says nothing means "never purge", not "purge today".
  expect(await listTrash('nb', f)).toEqual({ entries: [], retentionDays: 0 });
  expect(await listTrash('nb', f)).toEqual({ entries: [], retentionDays: 0 });
});

test('deleteTrashEntry removes one entry by name', async () => {
  const f = mockFetch([{ status: 200, body: { deleted: 'a.md' } }]);
  expect(await deleteTrashEntry('nb', 'a b.md', f)).toEqual({ deleted: 'a.md' });
  expect(f.calls[0].url).toBe('/n/nb/trash?name=a%20b.md');
  expect(f.calls[0].init.method).toBe('DELETE');
  expect(f.calls[0].init.headers['Content-Type']).toBe('application/json');
});

test('setTrashRetention PUTs the day count as JSON', async () => {
  const f = mockFetch([
    { status: 200, body: { retentionDays: 7, purged: 2 } },
    { status: 200, body: { retentionDays: 0, purged: 0 } },
  ]);
  expect(await setTrashRetention('nb', 7, f)).toEqual({ retentionDays: 7, purged: 2 });
  await setTrashRetention('nb', '0', f); // a select hands over strings
  expect(f.calls[0].url).toBe('/n/nb/trash/settings');
  expect(f.calls[0].init.method).toBe('PUT');
  expect(JSON.parse(f.calls[0].init.body)).toEqual({ retentionDays: 7 });
  expect(JSON.parse(f.calls[1].init.body)).toEqual({ retentionDays: 0 });
});

test('purgeTrash sweeps by retention, or empties with all', async () => {
  const f = mockFetch([
    { status: 200, body: { purged: 1 } },
    { status: 200, body: { purged: 9 } },
    { status: 200, body: { purged: 0 } },
  ]);
  expect(await purgeTrash('nb', {}, f)).toEqual({ purged: 1 });
  await purgeTrash('nb', { all: true }, f);
  await purgeTrash('nb', undefined, f);
  expect(f.calls.map((c) => c.url)).toEqual([
    '/n/nb/trash/purge', '/n/nb/trash/purge?all=1', '/n/nb/trash/purge',
  ]);
  expect(f.calls[0].init.method).toBe('POST');
});

test('restoreTrash posts the entry name', async () => {
  const f = mockFetch([{ status: 200, body: { path: 'a.md' } }]);
  expect(await restoreTrash('nb', 'x y', f)).toEqual({ path: 'a.md' });
  expect(f.calls[0].url).toBe('/n/nb/trash/restore?name=x%20y');
  expect(f.calls[0].init.method).toBe('POST');
});

test('error falls back to statusText, then a generic message; non-JSON bodies are fine', async () => {
  const f = mockFetch([
    { status: 500, statusText: 'Server Error', body: undefined },
    { status: 502, statusText: '', body: undefined },
  ]);
  await expect(createFolder('nb', 'a', f)).rejects.toThrow('Server Error');
  await expect(createFolder('nb', 'a', f)).rejects.toThrow('request failed (502)');
});

test('success with an empty body returns {}', async () => {
  const f = mockFetch([{ status: 204, body: undefined }]);
  expect(await moveNote('nb', 'a', 'b', f)).toEqual({});
});

test('uses the global fetch by default', async () => {
  const real = globalThis.fetch;
  globalThis.fetch = mockFetch([{ status: 200, body: { children: [] } }]);
  try {
    expect(await getTree('nb')).toEqual({ children: [] });
  } finally {
    globalThis.fetch = real;
  }
});

test('getNote fetches the rendered view', async () => {
  const f = mockFetch([{ status: 200, body: { html: '<p>', sha: 's', title: 't' } }]);
  expect(await getNote('nb', 'a b.md', f)).toEqual({ html: '<p>', sha: 's', title: 't' });
  expect(f.calls[0].url).toBe('/n/nb/note?path=a%20b.md');
  expect(f.calls[0].init.method).toBe('GET');
});

describe('getSource / getRaw', () => {
  const raw = (over = {}) => jest.fn(async () => ({
    ok: true,
    status: 200,
    headers: { get: (n) => ({ ETag: 'W/"abc123"', 'Last-Modified': 'Wed, 21 Oct 2026 07:28:00 GMT' })[n] },
    text: async () => 'body text',
    ...over,
  }));

  test('reads text, ETag sha and Last-Modified from one response', async () => {
    const f = raw();
    expect(await getSource('nb', 'a b.md', f)).toEqual({
      text: 'body text',
      sha: 'abc123',
      modified: Date.parse('Wed, 21 Oct 2026 07:28:00 GMT'),
    });
    expect(f.mock.calls[0][0]).toBe('/n/nb/raw?path=a%20b.md');
  });

  test('missing or unusable headers yield nulls, not errors', async () => {
    const f = jest.fn(async () => ({ ok: true, status: 200, text: async () => 'x' }));
    expect(await getSource('nb', 'a.md', f)).toEqual({ text: 'x', sha: null, modified: null });
    const g = jest.fn(async () => ({ ok: true, status: 200, headers: { get: () => '' }, text: async () => 'y' }));
    expect(await getSource('nb', 'a.md', g)).toEqual({ text: 'y', sha: null, modified: null });
  });

  test('a non-ok response throws with status and message', async () => {
    const f = jest.fn(async () => ({ ok: false, status: 404, statusText: 'Not Found', json: async () => ({ error: 'missing' }) }));
    await expect(getSource('nb', 'x.md', f)).rejects.toMatchObject({ status: 404, message: 'missing' });
  });

  test('getRaw returns just the text', async () => {
    const f = raw();
    expect(await getRaw('nb', 'a.md', f)).toBe('body text');
  });
});

describe('saveNote guards', () => {
  test('baseSha sends If-Match and no force', async () => {
    const f = mockFetch([{ status: 200, body: { sha: 'next' } }]);
    await saveNote('nb', 'a.md', 'x', { baseSha: 'old' }, f);
    expect(f.calls[0].url).toBe('/n/nb/note?path=a.md');
    expect(f.calls[0].init.headers['If-Match']).toBe('"old"');
    expect(f.calls[0].init.headers['If-None-Match']).toBeUndefined();
  });
  test('force appends force=1; createOnly sets If-None-Match', async () => {
    const f = mockFetch([{ status: 200, body: {} }]);
    await saveNote('nb', 'a.md', 'x', { force: true, createOnly: true }, f);
    expect(f.calls[0].url).toBe('/n/nb/note?path=a.md&force=1');
    expect(f.calls[0].init.headers['If-None-Match']).toBe('*');
  });
});

describe('notebook transfer', () => {
  test('exportNotebook returns the blob and reports failures', async () => {
    const blob = new Blob(['x']);
    const f = jest.fn(async () => ({ ok: true, status: 200, blob: async () => blob }));
    expect(await exportNotebook('nb', f)).toBe(blob);
    expect(f.mock.calls[0][0]).toBe('/n/nb/export');

    const g = jest.fn(async () => ({ ok: false, status: 500, statusText: '', json: async () => ({ error: 'boom' }) }));
    await expect(exportNotebook('nb', g)).rejects.toMatchObject({ message: 'boom', status: 500 });
  });

  test('importNotebook posts the file multipart, with an optional dry run', async () => {
    const f = mockFetch([{ status: 200, body: { entries: [] } }, { status: 200, body: { dryRun: true } }]);
    const file = new File(['x'], 'notes.tar.gz', { type: 'application/gzip' });
    await importNotebook('nb', file, { dryRun: true }, f);
    await importNotebook('nb', file, {}, f);
    expect(f.calls[0].url).toBe('/n/nb/import?dry_run=true');
    expect(f.calls[1].url).toBe('/n/nb/import');
    expect(f.calls[0].init.body).toBeInstanceOf(FormData);
    expect(f.calls[0].init.body.get('file').name).toBe('notes.tar.gz');
  });

  test('importNotebook names a file-less upload', async () => {
    const f = mockFetch([{ status: 200, body: {} }]);
    const headless = new File(['x'], '');
    await importNotebook('nb', headless, {}, f);
    expect(f.calls[0].init.body.get('file').name).toBe('import.tar.gz');
  });

  test('createBackup and resourceStats hit the right endpoints', async () => {
    const f = mockFetch([{ status: 200, body: { createdAt: 'x' } }, { status: 200, body: { usedBytes: 1 } }]);
    expect(await createBackup('nb', f)).toEqual({ createdAt: 'x' });
    expect(await resourceStats('nb', f)).toEqual({ usedBytes: 1 });
    expect(f.calls[0].url).toBe('/n/nb/backup');
    expect(f.calls[0].init.method).toBe('POST');
    expect(f.calls[0].init.headers['Content-Type']).toBe('application/json');
    expect(f.calls[1].url).toBe('/n/nb/resources/stats');
    expect(f.calls[1].init.method).toBe('GET');
  });
});

test('every client falls back to the global fetch and default options', async () => {
  const real = globalThis.fetch;
  const calls = [];
  globalThis.fetch = jest.fn(async (url, init) => {
    calls.push({ url, init });
    return {
      ok: true,
      status: 200,
      statusText: '',
      json: async () => ({}),
      text: async () => '',
      blob: async () => new Blob([]),
      headers: { get: () => null },
    };
  });
  try {
    await getTree('nb');
    await getNote('nb', 'a.md');
    await getRaw('nb', 'a.md');
    await getSource('nb', 'a.md');
    await saveNote('nb', 'a.md', 'x');
    await createNote('nb', 'a.md', 'x');
    await deleteNote('nb', 'a.md');
    await moveNote('nb', 'a.md', 'b.md');
    await createFolder('nb', 'f');
    await moveFolder('nb', 'f', 'g');
    await deleteFolder('nb', 'f');
    await listTrash('nb');
    await deleteTrashEntry('nb', 'x.md');
    await setTrashRetention('nb', 7);
    await purgeTrash('nb');
    await restoreTrash('nb', 't');
    await exportNotebook('nb');
    await importNotebook('nb', new File(['x'], 'n.tar.gz'));
    await createBackup('nb');
    await resourceStats('nb');
    expect(calls.length).toBeGreaterThan(0);
  } finally {
    globalThis.fetch = real;
  }
});
