// E2E tests for stage 8: rename/move, quota, GC, backup, import/export.
// Run with: npx playwright test --config web/playwright.config.js
//
// All tests are API-level against a real bin/noteroot spawned with a temp
// data dir and a 10 KB quota, exercising the stage-8 endpoints end-to-end.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, rmSync, existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { gzipSync, gunzipSync } from 'node:zlib';

const PORT = 18300;
const BASE = `http://127.0.0.1:${PORT}`;
const SLUG = 'upnote';
const QUOTA_BYTES = 10000;
let serverProc = null;
let dataDir = null;

// --- minimal ustar tar builder -----------------------------------------

function tarHeader(name, size) {
  const buf = Buffer.alloc(512);
  buf.write(name, 0, 100, 'utf8');
  buf.write('000644 \0', 100, 8, 'utf8');        // mode
  buf.write('0000000\0', 108, 8, 'utf8');         // uid
  buf.write('0000000\0', 116, 8, 'utf8');         // gid
  buf.write(size.toString(8).padStart(11, '0') + ' ', 124, 12, 'utf8'); // size
  buf.write('00000000000 ', 136, 12, 'utf8');     // mtime
  buf.write('        ', 148, 8, 'utf8');          // checksum placeholder (spaces)
  buf.write('0', 156, 1, 'utf8');                 // typeflag: regular file
  buf.write('ustar\0', 257, 6, 'utf8');
  buf.write('00', 263, 2, 'utf8');
  let sum = 0;
  for (const b of buf) sum += b;
  buf.write(sum.toString(8).padStart(6, '0') + '\0 ', 148, 8, 'utf8');
  return buf;
}

function buildTarGz(files) {
  const chunks = [];
  for (const [name, content] of Object.entries(files)) {
    const data = Buffer.from(content, 'utf8');
    chunks.push(tarHeader(name, data.length));
    chunks.push(data);
    const pad = (512 - (data.length % 512)) % 512;
    if (pad > 0) chunks.push(Buffer.alloc(pad));
  }
  chunks.push(Buffer.alloc(1024)); // end-of-archive
  return gzipSync(Buffer.concat(chunks));
}

// parseTar extracts a flat {name: content} map from an uncompressed tar buffer.
function parseTar(buf) {
  const out = {};
  let off = 0;
  while (off + 512 <= buf.length) {
    const hdr = buf.subarray(off, off + 512);
    if (hdr.every((b) => b === 0)) break;
    const name = hdr.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
    const size = parseInt(hdr.subarray(124, 136).toString('utf8').replace(/[\0 ]/g, ''), 8) || 0;
    const data = buf.subarray(off + 512, off + 512 + size);
    out[name] = data.toString('utf8');
    off += 512 + Math.ceil(size / 512) * 512;
  }
  return out;
}

// --- server lifecycle ---------------------------------------------------

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'nr-e2e-stage8-'));
  const root = join(import.meta.dirname, '..', '..');
  execSync(`NOTEROOT_DATA_DIR=${dataDir} ${join(root, 'scripts/new-notebook.sh')} ${SLUG}`, { cwd: root });

  // Seed the vault with a file and a git commit so git bundle has refs
  // (the API note is written after the server starts).
  const vaultDir = join(dataDir, 'notebooks', SLUG, 'vault');
  writeFileSync(join(vaultDir, 'seed.md'), '# Seed\n');
  execSync('git add . && git commit -m "seed"', {
    cwd: vaultDir,
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: 'e2e', GIT_AUTHOR_EMAIL: 'e2e@test',
      GIT_COMMITTER_NAME: 'e2e', GIT_COMMITTER_EMAIL: 'e2e@test',
    },
    stdio: 'ignore',
  });
  serverProc = spawn(join(root, 'bin/noteroot'), [], {
    env: {
      ...process.env,
      NOTEROOT_DATA_DIR: dataDir,
      NOTEROOT_LISTEN: `127.0.0.1:${PORT}`,
      NOTEROOT_AUTH_MODE: 'none',
      NOTEROOT_QUOTA_BYTES: String(QUOTA_BYTES),
      NOTEROOT_GC_INTERVAL: '999h',
    },
    stdio: 'ignore',
  });

  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('server start timeout')), 15000);
    const poll = setInterval(async () => {
      try {
        const res = await fetch(`${BASE}/`);
        if (res.ok) { clearInterval(poll); clearTimeout(timeout); resolve(); }
      } catch { /* not ready */ }
    }, 200);
  });

  // Seed note used by most tests.
  const res = await fetch(`${BASE}/n/${SLUG}/note?path=test.md`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    body: '---\ntitle: Stage 8\n---\n\nHello stage 8.\n',
  });
  expect(res.status).toBeLessThan(300);
});

test.afterAll(() => {
  if (serverProc) { serverProc.kill(); serverProc = null; }
  if (dataDir) { rmSync(dataDir, { recursive: true, force: true }); dataDir = null; }
});

// --- rename / move -------------------------------------------------------

test('PATCH note moves a file and the uid travels', async () => {
  const create = await fetch(`${BASE}/n/${SLUG}/note?path=src.md`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    body: '---\ntitle: Source\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV00\n---\n\nmove me\n',
  });
  expect(create.status).toBe(201);

  const res = await fetch(`${BASE}/n/${SLUG}/note`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ from: 'src.md', to: 'archive/moved.md' }),
  });
  expect(res.status).toBe(200);
  const body = await res.json();
  expect(body.from).toBe('src.md');
  expect(body.to).toBe('archive/moved.md');

  // Old path gone, new path readable with the same uid.
  const old = await fetch(`${BASE}/n/${SLUG}/note?path=src.md`);
  expect(old.status).toBe(404);
  const next = await fetch(`${BASE}/n/${SLUG}/note?path=archive/moved.md`);
  expect(next.status).toBe(200);
  const text = await next.text();
  expect(text).toContain('move me');
  expect(text).toContain('01ARZ3NDEKTSV4RRFFQ69G5FAV00');
});

test('PATCH note to existing destination returns 409', async () => {
  const res = await fetch(`${BASE}/n/${SLUG}/note`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ from: 'test.md', to: 'archive/moved.md' }),
  });
  expect(res.status).toBe(409);
});

test('PATCH note missing fields returns 400', async () => {
  const res = await fetch(`${BASE}/n/${SLUG}/note`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ from: 'test.md' }),
  });
  expect(res.status).toBe(400);
});

// --- quota ---------------------------------------------------------------

test('stats report quota fields', async () => {
  const res = await fetch(`${BASE}/n/${SLUG}/resources/stats`);
  expect(res.status).toBe(200);
  const stats = await res.json();
  expect(stats.quotaBytes).toBe(QUOTA_BYTES);
  expect(typeof stats.usedBytes).toBe('number');
});

test('upload beyond quota returns 507', async () => {
  // Fill close to the quota first.
  const filler = 'x'.repeat(9000);
  const first = await fetch(`${BASE}/n/${SLUG}/resources?filename=filler.txt`, {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: filler,
  });
  expect(first.status).toBe(201);

  // 2000 more bytes pushes 9000+2000 > 10000.
  const res = await fetch(`${BASE}/n/${SLUG}/resources?filename=too-big.txt`, {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: 'y'.repeat(2000),
  });
  expect(res.status).toBe(507);
  const body = await res.json();
  expect(body.error).toContain('quota');
});

// --- GC ------------------------------------------------------------------

test('POST gc returns removal counts', async () => {
  const res = await fetch(`${BASE}/n/${SLUG}/resources/gc`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
  });
  expect(res.status).toBe(200);
  const body = await res.json();
  expect(body).toHaveProperty('journalEntriesRemoved');
  expect(body).toHaveProperty('blobsRemoved');
});

// --- backup --------------------------------------------------------------

test('POST backup writes bundle and state snapshot', async () => {
  const res = await fetch(`${BASE}/n/${SLUG}/backup`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
  });
  expect(res.status).toBe(200);
  const m = await res.json();
  expect(m.bundlePath).toBeTruthy();
  expect(m.statePath).toBeTruthy();
  expect(m.bundleSHA).toMatch(/^[0-9a-f]{64}$/);
  expect(m.stateSHA).toMatch(/^[0-9a-f]{64}$/);
  expect(m.errors ?? []).toHaveLength(0);

  // Files exist on disk and are non-empty.
  const backupsDir = join(dataDir, 'notebooks', SLUG, '.notebook', 'backups');
  const bundle = join(backupsDir, m.bundlePath);
  const state = join(backupsDir, m.statePath);
  expect(existsSync(bundle)).toBe(true);
  expect(existsSync(state)).toBe(true);
  expect(readFileSync(bundle).length).toBeGreaterThan(0);

  // Exactly the expected artifacts plus no stray files.
  const listed = readdirSync(backupsDir);
  expect(listed).toContain(m.bundlePath);
  expect(listed).toContain(m.statePath);
});

// --- export --------------------------------------------------------------

test('GET export streams a tar.gz of vault notes', async () => {
  const res = await fetch(`${BASE}/n/${SLUG}/export`);
  expect(res.status).toBe(200);
  expect(res.headers.get('content-type')).toContain('application/gzip');
  expect(res.headers.get('content-disposition')).toContain('attachment');

  const gz = Buffer.from(await res.arrayBuffer());
  const tar = parseTar(gunzipSync(gz));
  expect(Object.keys(tar)).toContain('test.md');
  expect(Object.keys(tar)).toContain('archive/moved.md');
  expect(tar['test.md']).toContain('Hello stage 8');
});

// --- import --------------------------------------------------------------

test('POST import dry-run classifies without writing', async () => {
  const archive = buildTarGz({
    'dry/new-note.md': '# Dry new\n',
    'test.md': '# DIFFERENT CONTENT\n',
  });
  const res = await fetch(`${BASE}/n/${SLUG}/import?dry_run=true`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/gzip' },
    body: archive,
  });
  expect(res.status).toBe(200);
  const body = await res.json();
  expect(body.dryRun).toBe(true);
  const byPath = Object.fromEntries(body.entries.map((e) => [e.path, e.status]));
  expect(byPath['dry/new-note.md']).toBe('created');
  expect(byPath['test.md']).toBe('updated');

  // Nothing was written.
  const check = await fetch(`${BASE}/n/${SLUG}/note?path=dry/new-note.md`);
  expect(check.status).toBe(404);
});

test('POST import creates, then re-import skips (idempotent)', async () => {
  const content = '# Imported note\n';
  const archive = buildTarGz({
    'imported/note.md': content,
    '../escape.md': 'evil',
    'photo.png': 'not a note',
  });

  const res = await fetch(`${BASE}/n/${SLUG}/import`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/gzip' },
    body: archive,
  });
  expect(res.status).toBe(200);
  const body = await res.json();
  const byPath = Object.fromEntries(body.entries.map((e) => [e.path, e.status]));
  expect(byPath['imported/note.md']).toBe('created');
  expect(byPath['../escape.md']).toBe('error');
  expect(byPath['photo.png']).toBe('error');

  // The note is readable with the exact content.
  const got = await fetch(`${BASE}/n/${SLUG}/note?path=imported/note.md`);
  expect(got.status).toBe(200);
  expect(await got.text()).toContain('Imported note');

  // Re-import identical archive → skipped, still 200.
  const again = await fetch(`${BASE}/n/${SLUG}/import`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/gzip' },
    body: archive,
  });
  expect(again.status).toBe(200);
  const againBody = await again.json();
  const againByPath = Object.fromEntries(againBody.entries.map((e) => [e.path, e.status]));
  expect(againByPath['imported/note.md']).toBe('skipped');
});

test('POST import rejects non-gzip body with 400', async () => {
  const res = await fetch(`${BASE}/n/${SLUG}/import`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/gzip' },
    body: Buffer.from('this is not gzip'),
  });
  expect(res.status).toBe(400);
});
