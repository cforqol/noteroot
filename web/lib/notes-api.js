/**
 * Browser-side client for the note, folder, trash and notebook-transfer
 * endpoints.
 *
 * Every mutation sends a JSON, markdown or multipart content type (the
 * server's CSRF layer rejects form-urlencoded) and surfaces failures as
 * Error objects carrying the HTTP `status` and, for 412 responses,
 * `currentSha`.
 */

const enc = encodeURIComponent;

async function failure(r) {
  let data = {};
  try {
    data = await r.json();
  } catch {
    /* empty or non-JSON body */
  }
  const err = new Error(data.error || r.statusText || `request failed (${r.status})`);
  err.status = r.status;
  err.currentSha = data.currentSha;
  return err;
}

async function request(method, url, { body, headers = {} } = {}, fetchImpl = globalThis.fetch) {
  const init = { method, headers: { Accept: 'application/json', ...headers } };
  if (body !== undefined) init.body = body;
  const r = await fetchImpl(url, init);
  if (!r.ok) throw await failure(r);
  try {
    return await r.json();
  } catch {
    return {};
  }
}

const JSON_TYPE = { 'Content-Type': 'application/json' };
const MARKDOWN_TYPE = { 'Content-Type': 'text/markdown; charset=utf-8' };

/** GET the folder/note tree. */
export function getTree(slug, f) {
  return request('GET', `/n/${enc(slug)}/tree`, {}, f);
}

/** Frontmatter tags: {tags: {tag: [note paths]}}. */
export function listTags(slug, f) {
  return request('GET', `/n/${enc(slug)}/tags`, {}, f);
}

/** Create a notebook: {slug, name, profile}. Slug is lowercase alnum+dash. */
export function createNotebook(slug, name = '', f) {
  return request('POST', '/api/v1/notebooks', {
    headers: JSON_TYPE,
    body: JSON.stringify({ slug, name }),
  }, f);
}

/** Rename a notebook's slug: {slug, name, profile}. */
export function renameNotebook(slug, newSlug, f) {
  return request('PATCH', `/api/v1/notebooks/${enc(slug)}`, {
    headers: JSON_TYPE,
    body: JSON.stringify({ slug: newSlug }),
  }, f);
}

/** Archive a notebook: {deleted, archived}. Leaves the list; recoverable on the server. */
export function deleteNotebook(slug, f) {
  return request('DELETE', `/api/v1/notebooks/${enc(slug)}`, { headers: JSON_TYPE }, f);
}

/** A note's rendered view: {path, id, title, sha, html, outgoing, backlinks, ...}. */
export function getNote(slug, path, f) {
  return request('GET', `/n/${enc(slug)}/note?path=${enc(path)}`, {}, f);
}

/** A note's exact source text. */
export async function getRaw(slug, path, f = globalThis.fetch) {
  return (await getSource(slug, path, f)).text;
}

/**
 * A note's source with the version it came from, read from one response
 * so the two always match: {text, sha (ETag, null if absent), modified
 * (Last-Modified in ms, null if absent)}. Guard writes derived from
 * `text` with this `sha`, never with one read separately.
 */
export async function getSource(slug, path, f = globalThis.fetch) {
  const r = await f(`/n/${enc(slug)}/raw?path=${enc(path)}`);
  if (!r.ok) throw await failure(r);
  const header = (name) => r.headers?.get?.(name) || '';
  const sha = header('ETag').replace(/^W\//, '').replace(/"/g, '') || null;
  const modified = Date.parse(header('Last-Modified')) || null;
  return { text: await r.text(), sha, modified };
}

/**
 * Save a note's source: {path, id, sha, mtime}. `baseSha` refuses to
 * overwrite a change made elsewhere (412), `createOnly` refuses an
 * existing path (412), `force` overwrites regardless.
 */
export function saveNote(slug, path, content, { baseSha = null, createOnly = false, force = false } = {}, f) {
  const headers = { ...MARKDOWN_TYPE };
  if (baseSha) headers['If-Match'] = `"${baseSha}"`;
  else if (createOnly) headers['If-None-Match'] = '*';
  const q = force ? '&force=1' : '';
  return request('PUT', `/n/${enc(slug)}/note?path=${enc(path)}${q}`, { body: content, headers }, f);
}

/** Create a note; fails with 412 when the path already exists. */
export function createNote(slug, path, content, f) {
  return saveNote(slug, path, content, { createOnly: true }, f);
}

/** Soft-delete a note into the trash, guarded by its current sha. */
export async function deleteNote(slug, path, f) {
  const note = await getNote(slug, path, f);
  return request('DELETE', `/n/${enc(slug)}/note?path=${enc(path)}`, {
    headers: { ...JSON_TYPE, 'If-Match': `"${note.sha}"` },
  }, f);
}

export function moveNote(slug, from, to, f) {
  return request('PATCH', `/n/${enc(slug)}/note`, { body: JSON.stringify({ from, to }), headers: JSON_TYPE }, f);
}

export function createFolder(slug, path, f) {
  return request('POST', `/n/${enc(slug)}/folder`, { body: JSON.stringify({ path }), headers: JSON_TYPE }, f);
}

export function moveFolder(slug, from, to, f) {
  return request('PATCH', `/n/${enc(slug)}/folder`, { body: JSON.stringify({ from, to }), headers: JSON_TYPE }, f);
}

/** Trash every note in a folder and remove the folder. */
export function deleteFolder(slug, path, f) {
  return request('DELETE', `/n/${enc(slug)}/folder?path=${enc(path)}`, { headers: JSON_TYPE }, f);
}

/**
 * {folderPath: {emoji, color}} for every folder with a user-assigned
 * icon/color — the notebook's own server-side state, so it follows
 * the user to any browser or device.
 */
export async function getFolderPrefs(slug, f) {
  const data = await request('GET', `/n/${enc(slug)}/folder-prefs`, {}, f);
  return data.prefs || {};
}

/** Set (or, when both parts are empty, clear) a folder's icon and color. */
export function setFolderPref(slug, path, { emoji = '', color = '' } = {}, f) {
  return request('PUT', `/n/${enc(slug)}/folder-prefs`, {
    headers: JSON_TYPE,
    body: JSON.stringify({ path, emoji, color }),
  }, f);
}

/**
 * The trash: `{entries, retentionDays}`. `retentionDays` is 0 when the
 * notebook never purges automatically; each entry then has no
 * `expiresAt`.
 */
export async function listTrash(slug, f) {
  const data = await request('GET', `/n/${enc(slug)}/trash`, {}, f);
  return { entries: data.entries || [], retentionDays: Number(data.retentionDays) || 0 };
}

export function restoreTrash(slug, name, f) {
  return request('POST', `/n/${enc(slug)}/trash/restore?name=${enc(name)}`, { headers: JSON_TYPE }, f);
}

/** Permanently delete one trash entry by its server-side name. */
export function deleteTrashEntry(slug, name, f) {
  return request('DELETE', `/n/${enc(slug)}/trash?name=${enc(name)}`, { headers: JSON_TYPE }, f);
}

/** Restore every trashed note from a folder (`path`) or all (`all`). */
export function restoreTrashFolder(slug, { path = null, all = false } = {}, f) {
  let q = '';
  if (all) q = '?all=1';
  else if (path) q = `?path=${enc(path)}`;
  return request('POST', `/n/${enc(slug)}/trash/restore-folder${q}`, { headers: JSON_TYPE }, f);
}

/** Delete every trashed note from a folder for good. */
export function deleteTrashFolder(slug, path, f) {
  return request('DELETE', `/n/${enc(slug)}/trash/folder?path=${enc(path)}`, { headers: JSON_TYPE }, f);
}


/**
 * Set how long the notebook keeps trashed notes. `days` of 0 means
 * never purge. Returns `{retentionDays, purged}` — shortening retention
 * sweeps straight away, and `purged` says how many went.
 */
export function setTrashRetention(slug, days, f) {
  return request('PUT', `/n/${enc(slug)}/trash/settings`, {
    headers: JSON_TYPE,
    body: JSON.stringify({ retentionDays: Number(days) }),
  }, f);
}

/** Run the retention sweep now, or empty the trash outright. */
export function purgeTrash(slug, { all = false } = {}, f) {
  const q = all ? '?all=1' : '';
  return request('POST', `/n/${enc(slug)}/trash/purge${q}`, { headers: JSON_TYPE }, f);
}

/** The whole notebook (notes + notebook.toml) as a .tar.gz Blob. */
export async function exportNotebook(slug, f = globalThis.fetch) {
  const r = await f(`/n/${enc(slug)}/export`);
  if (!r.ok) throw await failure(r);
  return r.blob();
}

/**
 * A sidebar multi-selection (notes and/or whole folders) as a .zip Blob.
 * `paths` may mix note and folder paths; the server expands folders to
 * every note beneath them. `attachments` adds every referenced resource
 * blob, deduplicated across the selection.
 */
export async function exportSelection(slug, paths, { attachments = false } = {}, f = globalThis.fetch) {
  const q = new URLSearchParams();
  for (const p of paths) q.append('path', p);
  if (attachments) q.set('attachments', '1');
  const r = await f(`/n/${enc(slug)}/export/selection?${q.toString()}`);
  if (!r.ok) throw await failure(r);
  return r.blob();
}

/**
 * Import a .tar.gz of notes: {dryRun, entries: [{path, size, sha, status,
 * message}]} with status created | updated | skipped | error. A dry run
 * only reports what would happen.
 */
export function importNotebook(slug, file, { dryRun = false } = {}, f) {
  const form = new FormData();
  form.append('file', file, file.name || 'import.tar.gz');
  return request('POST', `/n/${enc(slug)}/import${dryRun ? '?dry_run=true' : ''}`, { body: form }, f);
}

// --- resumable notebook import/export (large notebooks) ---
//
// Export builds the archive server-side and resolves once it is ready
// to download; the download itself is left to the browser (a multi-GB
// archive has no business living in a JS Blob), whose own download
// manager already resumes a dropped connection over Range, which the
// download endpoint supports. Import uploads the archive in
// offset-fenced chunks, so a dropped connection or a page left idle
// mid-upload costs at most the in-flight chunk, never the bytes already
// accepted — the same shape as the server's job model in transfer.go.

/** Start a server-side export build: {id, status: "building"}. */
export function createExportJob(slug, f) {
  return request('POST', `/n/${enc(slug)}/export/jobs`, { headers: JSON_TYPE }, f);
}

/** One export job's state: {status, size?, error?}. */
export function exportJobStatus(slug, id, f) {
  return request('GET', `/n/${enc(slug)}/export/jobs/${enc(id)}`, {}, f);
}

/** The browser-downloadable URL for a finished export job. */
export function exportJobDownloadUrl(slug, id) {
  return `/n/${enc(slug)}/export/jobs/${enc(id)}/download`;
}

/** Cancel a build in progress, or discard a finished job's archive. */
export function cancelExportJob(slug, id, f) {
  return request('DELETE', `/n/${enc(slug)}/export/jobs/${enc(id)}`, { headers: JSON_TYPE }, f);
}

async function pollExportJob(slug, id, f, intervalMs) {
  for (;;) {
    const info = await exportJobStatus(slug, id, f);
    if (info.status === 'done') return info;
    if (info.status === 'error') throw new Error(info.error || 'export failed');
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}

/**
 * Build a notebook export and resolve once it is ready: {id, url, size}.
 * `url` is a direct link the caller can hand to the browser (anchor
 * click, `window.open`, drop into a download manager) to stream the
 * finished archive without ever holding it in page memory.
 */
export async function exportNotebookReady(slug, f = globalThis.fetch, { intervalMs = 300 } = {}) {
  const created = await createExportJob(slug, f);
  const info = await pollExportJob(slug, created.id, f, intervalMs);
  return { id: created.id, url: exportJobDownloadUrl(slug, created.id), size: info.size };
}

/** Start a resumable import upload session: {id, offset: 0}. */
export function createImportJob(slug, f) {
  return request('POST', `/n/${enc(slug)}/import/jobs`, { headers: JSON_TYPE }, f);
}

/** The offset a resuming upload should continue from: {id, offset}. */
export function importJobStatus(slug, id, f) {
  return request('GET', `/n/${enc(slug)}/import/jobs/${enc(id)}`, {}, f);
}

/** Cancel an upload in progress and discard its staged bytes. */
export function cancelImportJob(slug, id, f) {
  return request('DELETE', `/n/${enc(slug)}/import/jobs/${enc(id)}`, { headers: JSON_TYPE }, f);
}

/**
 * Upload one chunk at `offset`. A 409 (the server disagrees with the
 * caller's offset) resolves as `{conflict: true, offset}` instead of
 * throwing, so importNotebookResumable can resync and retry without
 * treating a stale offset as a hard failure.
 */
async function appendImportChunk(slug, id, offset, chunk, f) {
  const r = await f(`/n/${enc(slug)}/import/jobs/${enc(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/octet-stream', 'X-Noteroot-Offset': String(offset) },
    body: chunk,
  });
  if (r.status === 409) {
    let data = {};
    try { data = await r.json(); } catch { /* empty or non-JSON body */ }
    return { conflict: true, offset: Number(data.offset) || 0 };
  }
  if (!r.ok) throw await failure(r);
  const data = await r.json();
  return { conflict: false, offset: Number(data.offset) };
}

/**
 * Upload `file` as a notebook import archive in chunks of `chunkSize`
 * bytes, surviving transient failures by resyncing to whatever offset
 * the server reports — via the chunk response on a 409, or a status
 * fetch after any other failure — rather than assuming the client's own
 * count is still correct. Up to `maxRetries` consecutive failures are
 * tolerated per chunk before giving up. Resolves to the job id, not yet
 * applied or even validated — call completeImportJob next.
 */
export async function uploadImportArchive(
  slug, file, { chunkSize = 8 << 20, maxRetries = 5 } = {}, f = globalThis.fetch,
) {
  const { id } = await createImportJob(slug, f);
  let offset = 0;
  let retries = 0;
  while (offset < file.size) {
    const end = Math.min(offset + chunkSize, file.size);
    try {
      const result = await appendImportChunk(slug, id, offset, file.slice(offset, end), f);
      offset = result.offset;
      retries = 0;
    } catch (err) {
      retries += 1;
      if (retries > maxRetries) throw err;
      const status = await importJobStatus(slug, id, f);
      offset = status.offset;
    }
  }
  return id;
}

/**
 * Validate (dry run) or apply an uploaded import job: {dryRun, entries},
 * the same shape as importNotebook. A dry run leaves the job in place —
 * the bytes are already on the server, so confirming afterward calls
 * this again with dryRun: false on the same id, never re-uploading.
 */
export function completeImportJob(slug, id, { dryRun = false } = {}, f) {
  const q = dryRun ? '?dry_run=true' : '';
  return request('POST', `/n/${enc(slug)}/import/jobs/${enc(id)}/complete${q}`, { headers: JSON_TYPE }, f);
}

/**
 * Upload `file` and apply (or dry-run) it in one call: the shape most
 * callers want when there is no separate review step between the
 * upload and the decision to apply it. Resolves to the same {dryRun,
 * entries} shape as importNotebook.
 */
export async function importNotebookResumable(
  slug, file, { dryRun = false, chunkSize = 8 << 20, maxRetries = 5 } = {}, f = globalThis.fetch,
) {
  const id = await uploadImportArchive(slug, file, { chunkSize, maxRetries }, f);
  return completeImportJob(slug, id, { dryRun }, f);
}

/** Server-side backup (git bundle + state snapshot): the backup manifest. */
export function createBackup(slug, f) {
  return request('POST', `/n/${enc(slug)}/backup`, { headers: JSON_TYPE }, f);
}

/** Attachment storage: {backend, usedBytes, reservedBytes, quotaBytes, journal}. */
export function resourceStats(slug, f) {
  return request('GET', `/n/${enc(slug)}/resources/stats`, {}, f);
}

/** List uploaded resources. state defaults to "committed". */
export function listResources(slug, state = 'committed', f) {
  return request('GET', `/n/${enc(slug)}/resources?state=${enc(state)}&limit=500`, {}, f);
}

/** List every Karakeep-archived URL in the notebook and the notes that mention it. */
export function listKarakeepBookmarks(slug, f) {
  return request('GET', `/n/${enc(slug)}/integrations/karakeep/bookmarks`, {}, f);
}

/** Delete an uploaded resource by SHA. */
export function deleteResource(slug, sha, f) {
  return request('DELETE', `/n/${enc(slug)}/resources/${enc(sha)}`, { headers: { Accept: 'application/json' } }, f);
}

/**
 * Re-hash a stored blob and compare it to its sha (the content key):
 * {sha, status: "ok" | "missing" | "corrupted", size}.
 */
export function verifyResource(slug, sha, f) {
  return request('GET', `/n/${enc(slug)}/resources/${enc(sha)}/verify`, {}, f);
}

/** Vault-relative paths of every note that links to a resource. */
export function listResourceRefs(slug, sha, f) {
  return request('GET', `/n/${enc(slug)}/resources/${enc(sha)}/refs`, {}, f);
}

/**
 * Content-lock status for a path: {path, locked, unlocked, locks, exact?,
 * kind?, params}. `locked` is true when any lock covers the path;
 * `unlocked` is true when the current session has opened them all.
 * `kind` is present only when this exact path carries a lock.
 */
export function lockStatus(slug, path = '', kind = '', f) {
  const q = new URLSearchParams();
  if (path) q.set('path', path);
  if (kind) q.set('kind', kind);
  const qs = q.toString();
  return request('GET', `/n/${enc(slug)}/contentlock${qs ? `?${qs}` : ''}`, {}, f);
}

/** Set or change the password on a notebook, folder or note. */
export function setLock(slug, path, kind, password, f) {
  return request('PUT', `/n/${enc(slug)}/contentlock`, {
    headers: JSON_TYPE,
    body: JSON.stringify({ path, kind, password }),
  }, f);
}

/** Remove a lock. Requires the path to be unlocked first. */
export function removeLock(slug, path, kind, f) {
  const q = new URLSearchParams({ path, kind });
  return request('DELETE', `/n/${enc(slug)}/contentlock?${q.toString()}`, { headers: JSON_TYPE }, f);
}

/** Enter a password to open a locked path. Sets the unlock cookie. */
export function unlock(slug, path, password, kind = '', f) {
  return request('POST', `/n/${enc(slug)}/contentlock/unlock`, {
    headers: JSON_TYPE,
    body: JSON.stringify({ path, kind, password }),
  }, f);
}

/** Re-lock a path, dropping the session that opened it. */
export function relock(slug, path, kind = '', f) {
  return request('POST', `/n/${enc(slug)}/contentlock/lock`, {
    headers: JSON_TYPE,
    body: JSON.stringify({ path, kind }),
  }, f);
}

/** Get note history (git log for a file). */
export function getHistory(slug, path, { limit = 50, before = '' } = {}, f) {
  const q = new URLSearchParams();
  q.set('path', path);
  if (limit !== 50) q.set('limit', String(limit));
  if (before) q.set('before', before);
  return request('GET', `/n/${enc(slug)}/note/history?${q}`, {}, f);
}

/** Get diff between two commits for a note. */
export function getDiff(slug, path, from, to, f) {
  const q = new URLSearchParams();
  q.set('path', path);
  q.set('from', from);
  q.set('to', to);
  return request('GET', `/n/${enc(slug)}/note/diff?${q}`, {}, f);
}
