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

/** Remove one trashed note for good. */
export function deleteTrashEntry(slug, name, f) {
  return request('DELETE', `/n/${enc(slug)}/trash?name=${enc(name)}`, { headers: JSON_TYPE }, f);
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
 * Import a .tar.gz of notes: {dryRun, entries: [{path, size, sha, status,
 * message}]} with status created | updated | skipped | error. A dry run
 * only reports what would happen.
 */
export function importNotebook(slug, file, { dryRun = false } = {}, f) {
  const form = new FormData();
  form.append('file', file, file.name || 'import.tar.gz');
  return request('POST', `/n/${enc(slug)}/import${dryRun ? '?dry_run=true' : ''}`, { body: form }, f);
}

/** Server-side backup (git bundle + state snapshot): the backup manifest. */
export function createBackup(slug, f) {
  return request('POST', `/n/${enc(slug)}/backup`, { headers: JSON_TYPE }, f);
}

/** Attachment storage: {backend, usedBytes, reservedBytes, quotaBytes, journal}. */
export function resourceStats(slug, f) {
  return request('GET', `/n/${enc(slug)}/resources/stats`, {}, f);
}
