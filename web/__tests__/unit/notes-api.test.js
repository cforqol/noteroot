import { jest } from '@jest/globals';
import {
  getTree, createNote, deleteNote, moveNote, createFolder, moveFolder,
  deleteFolder, listTrash, restoreTrash, getNote, getSource, getRaw,
  deleteTrashEntry, setTrashRetention, purgeTrash,
  restoreTrashFolder, deleteTrashFolder,
  saveNote, exportNotebook, importNotebook, createBackup, resourceStats,
  createNotebook, renameNotebook, deleteNotebook,
  listResources, deleteResource, listTags,
  lockStatus, setLock, removeLock, unlock, relock,
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

describe('notebook CRUD', () => {
  test('createNotebook POSTs slug and name as JSON', async () => {
    const f = mockFetch([{ status: 201, body: { slug: 'new-nb', name: 'new-nb' } }]);
    expect(await createNotebook('new-nb', 'My Notebook', f)).toEqual({ slug: 'new-nb', name: 'new-nb' });
    expect(f.calls[0].url).toBe('/api/v1/notebooks');
    expect(f.calls[0].init.method).toBe('POST');
    expect(f.calls[0].init.headers['Content-Type']).toBe('application/json');
    expect(JSON.parse(f.calls[0].init.body)).toEqual({ slug: 'new-nb', name: 'My Notebook' });
  });

  test('renameNotebook PATCHes the new slug', async () => {
    const f = mockFetch([{ status: 200, body: { slug: 'renamed' } }]);
    expect(await renameNotebook('old', 'renamed', f)).toEqual({ slug: 'renamed' });
    expect(f.calls[0].url).toBe('/api/v1/notebooks/old');
    expect(f.calls[0].init.method).toBe('PATCH');
    expect(f.calls[0].init.headers['Content-Type']).toBe('application/json');
    expect(JSON.parse(f.calls[0].init.body)).toEqual({ slug: 'renamed' });
  });

  test('deleteNotebook DELETEs and reports the archive path', async () => {
    const f = mockFetch([{ status: 200, body: { deleted: 'gone', archived: '/d/notebooks/.archive/gone-1' } }]);
    expect(await deleteNotebook('gone', f)).toEqual({
      deleted: 'gone', archived: '/d/notebooks/.archive/gone-1',
    });
    expect(f.calls[0].url).toBe('/api/v1/notebooks/gone');
    expect(f.calls[0].init.method).toBe('DELETE');
    expect(f.calls[0].init.headers['Content-Type']).toBe('application/json');
    expect(f.calls[0].init.body).toBeUndefined();
  });

  test('notebook errors surface status and message', async () => {
    const f = mockFetch([{ status: 409, body: { error: 'already exists' } }]);
    await expect(createNotebook('dup', '', f)).rejects.toMatchObject({
      message: 'already exists', status: 409,
    });
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
    await createNotebook('nb2');
    await renameNotebook('nb2', 'nb3');
    await deleteNotebook('nb3');
    expect(calls.length).toBeGreaterThan(0);
  } finally {
    globalThis.fetch = real;
  }
});

describe('trash folder bulk actions', () => {
  test('restoreTrashFolder scopes to a folder, all, or the notebook', async () => {
    const f = mockFetch([
      { status: 200, body: { restored: 1 } },
      { status: 200, body: { restored: 2 } },
      { status: 200, body: { restored: 3 } },
    ]);
    expect(await restoreTrashFolder('nb', { path: 'work/deep' }, f)).toEqual({ restored: 1 });
    expect(await restoreTrashFolder('nb', { all: true }, f)).toEqual({ restored: 2 });
    expect(await restoreTrashFolder('nb', {}, f)).toEqual({ restored: 3 });
    expect(f.calls[0].url).toBe('/n/nb/trash/restore-folder?path=work%2Fdeep');
    expect(f.calls[1].url).toBe('/n/nb/trash/restore-folder?all=1');
    expect(f.calls[2].url).toBe('/n/nb/trash/restore-folder');
    for (const c of f.calls) {
      expect(c.init.method).toBe('POST');
      expect(c.init.headers['Content-Type']).toBe('application/json');
    }
  });

  test('deleteTrashFolder deletes one folder path for good', async () => {
    const f = mockFetch([{ status: 200, body: { purged: 4 } }]);
    expect(await deleteTrashFolder('nb', 'work/ideas', f)).toEqual({ purged: 4 });
    expect(f.calls[0].url).toBe('/n/nb/trash/folder?path=work%2Fideas');
    expect(f.calls[0].init.method).toBe('DELETE');
  });
});

describe('tags', () => {
  test('listTags GETs the notebook tag map', async () => {
    const f = mockFetch([{ status: 200, body: { tags: { a: ['x.md'] } } }]);
    expect(await listTags('my nb', f)).toEqual({ tags: { a: ['x.md'] } });
    expect(f.calls[0].url).toBe('/n/my%20nb/tags');
  });
});

describe('resource attachments', () => {
  test('listResources scopes the state and limit', async () => {
    const f = mockFetch([{ status: 200, body: { resources: [] } }]);
    expect(await listResources('my nb', undefined, f)).toEqual({ resources: [] });
    expect(f.calls[0].url).toBe('/n/my%20nb/resources?state=committed&limit=500');
    expect(f.calls[0].init.method).toBe('GET');
    await listResources('nb', 'reserved', f);
    expect(f.calls[1].url).toBe('/n/nb/resources?state=reserved&limit=500');
  });

  test('deleteResource DELETEs by SHA', async () => {
    const f = mockFetch([{ status: 204 }]);
    expect(await deleteResource('my nb', 'a/b', f)).toEqual({});
    expect(f.calls[0].url).toBe('/n/my%20nb/resources/a%2Fb');
    expect(f.calls[0].init.method).toBe('DELETE');
    expect(f.calls[0].init.headers.Accept).toBe('application/json');
  });

  test('deleteResource surfaces a failure', async () => {
    const f = mockFetch([{ status: 404, body: { error: 'no such resource' } }]);
    await expect(deleteResource('nb', 'x', f)).rejects.toThrow('no such resource');
  });
});

describe('content locks', () => {
  test('lockStatus omits path and kind when not given', async () => {
    const f = mockFetch([{ status: 200, body: { locked: false, unlocked: true } }]);
    expect(await lockStatus('my nb', '', '', f)).toEqual({ locked: false, unlocked: true });
    expect(f.calls[0].url).toBe('/n/my%20nb/contentlock');
    expect(f.calls[0].init.method).toBe('GET');
  });

  test('lockStatus passes path and kind as a query', async () => {
    const f = mockFetch([{ status: 200, body: {} }]);
    await lockStatus('nb', 'a/b.md', 'note', f);
    expect(f.calls[0].url).toBe('/n/nb/contentlock?path=a%2Fb.md&kind=note');
  });

  test('lockStatus encodes a notebook-level path', async () => {
    const f = mockFetch([{ status: 200, body: {} }]);
    await lockStatus('nb', '', 'notebook', f);
    expect(f.calls[0].url).toBe('/n/nb/contentlock?kind=notebook');
  });

  test('setLock PUTs a JSON body', async () => {
    const f = mockFetch([{ status: 200, body: { locked: true } }]);
    await setLock('nb', 'dir', 'folder', 'hunter2', f);
    const { url, init } = f.calls[0];
    expect(url).toBe('/n/nb/contentlock');
    expect(init.method).toBe('PUT');
    expect(init.headers['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body)).toEqual({ path: 'dir', kind: 'folder', password: 'hunter2' });
  });

  test('setLock surfaces 423 when the target is already locked', async () => {
    const f = mockFetch([{ status: 423, body: { error: 'locked' } }]);
    await expect(setLock('nb', 'a.md', 'note', 'pw', f)).rejects.toMatchObject({ status: 423 });
  });

  test('removeLock DELETEs with path and kind', async () => {
    const f = mockFetch([{ status: 200, body: { locked: false } }]);
    await removeLock('nb', 'a/b', 'folder', f);
    expect(f.calls[0].url).toBe('/n/nb/contentlock?path=a%2Fb&kind=folder');
    expect(f.calls[0].init.method).toBe('DELETE');
  });

  test('unlock POSTs the password', async () => {
    const f = mockFetch([{ status: 200, body: { unlocked: true } }]);
    await unlock('nb', 'a.md', 'secr3t', 'note', f);
    const { url, init } = f.calls[0];
    expect(url).toBe('/n/nb/contentlock/unlock');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ path: 'a.md', kind: 'note', password: 'secr3t' });
  });

  test('unlock surfaces 401 for a wrong password', async () => {
    const f = mockFetch([{ status: 401, body: { error: 'wrong password' } }]);
    await expect(unlock('nb', 'a.md', 'nope', 'note', f)).rejects.toMatchObject({
      status: 401, message: 'wrong password',
    });
  });

  test('relock POSTs path and kind', async () => {
    const f = mockFetch([{ status: 200, body: { locked: true } }]);
    await relock('nb', '', 'notebook', f);
    expect(f.calls[0].url).toBe('/n/nb/contentlock/lock');
    expect(f.calls[0].init.method).toBe('POST');
    expect(JSON.parse(f.calls[0].init.body)).toEqual({ path: '', kind: 'notebook' });
  });
});
