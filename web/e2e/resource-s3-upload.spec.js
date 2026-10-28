// E2E tests for stage 7: direct-to-S3 presigned upload from the browser.
// Run with: npx playwright test --config web/playwright.config.js
//
// Spawns an in-process fake S3 (with CORS, since the browser PUTs to it
// cross-origin) and the app server with NOTEROOT_S3_* configured. Asserts
// the file bytes land in object storage without passing through the app
// server, the journal confirms, notes render, and reads/thumbs stream
// back from the remote store.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const APP_PORT = 18201;
const S3_PORT = 18210;
const BASE = `http://127.0.0.1:${APP_PORT}`;
const S3_BASE = `http://127.0.0.1:${S3_PORT}`;
const BUCKET = 'testbucket';

let serverProc = null;
let dataDir = null;
let s3 = null; // { server, objects: Map, puts: count }

const PNG_1X1 = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

// startFakeS3 runs a minimal S3-compatible server with CORS enabled.
async function startFakeS3() {
  const objects = new Map();
  const state = { objects, puts: 0, gets: 0 };

  const server = createServer((req, res) => {
    const cors = {
      'Access-Control-Allow-Origin': '*',
      'Access-Control-Allow-Methods': 'PUT, GET, HEAD, DELETE, OPTIONS',
      'Access-Control-Allow-Headers': '*',
      'Access-Control-Expose-Headers': 'Content-Length',
    };
    if (req.method === 'OPTIONS') {
      res.writeHead(204, cors);
      res.end();
      return;
    }
    const u = new URL(req.url, S3_BASE);
    const parts = u.pathname.split('/').filter(Boolean); // [bucket, key...]
    const key = parts.slice(1).map(decodeURIComponent).join('/');
    if (parts[0] !== BUCKET || !key) {
      res.writeHead(404, cors);
      res.end('bad path');
      return;
    }

    if (req.method === 'PUT') {
      const chunks = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        objects.set(key, Buffer.concat(chunks));
        state.puts++;
        res.writeHead(200, cors);
        res.end();
      });
      return;
    }
    if (req.method === 'GET') {
      const obj = objects.get(key);
      if (!obj) {
        res.writeHead(404, cors);
        res.end('missing');
        return;
      }
      state.gets++;
      res.writeHead(200, { ...cors, 'Content-Type': 'application/octet-stream', 'Content-Length': obj.length });
      res.end(obj);
      return;
    }
    if (req.method === 'HEAD') {
      const obj = objects.get(key);
      if (!obj) {
        res.writeHead(404, cors);
        res.end();
        return;
      }
      res.writeHead(200, { ...cors, 'Content-Length': obj.length });
      res.end();
      return;
    }
    if (req.method === 'DELETE') {
      objects.delete(key);
      res.writeHead(204, cors);
      res.end();
      return;
    }
    res.writeHead(405, cors);
    res.end();
  });

  await new Promise((resolve) => server.listen(S3_PORT, '127.0.0.1', resolve));
  state.close = () => new Promise((resolve) => server.close(resolve));
  return state;
}

test.beforeAll(async () => {
  s3 = await startFakeS3();
  dataDir = mkdtempSync(join(tmpdir(), 'nr-e2e-s3-'));
  const root = join(import.meta.dirname, '..', '..');
  execSync(`NOTEROOT_DATA_DIR=${dataDir} ${join(root, 'scripts/new-notebook.sh')} s3note`, { cwd: root });

  serverProc = spawn(join(root, 'bin/noteroot'), [], {
    env: {
      ...process.env,
      NOTEROOT_DATA_DIR: dataDir,
      NOTEROOT_LISTEN: `127.0.0.1:${APP_PORT}`,
      NOTEROOT_AUTH_MODE: 'none',
      NOTEROOT_S3_ENDPOINT: S3_BASE,
      NOTEROOT_S3_BUCKET: BUCKET,
      NOTEROOT_S3_REGION: 'us-east-1',
      NOTEROOT_S3_ACCESS_KEY: 'AKIDEXAMPLE',
      NOTEROOT_S3_SECRET_KEY: 'SECRETKEY',
      NOTEROOT_S3_PATH_STYLE: 'true',
    },
    stdio: 'ignore',
  });

  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('server start timeout')), 15000);
    const poll = setInterval(async () => {
      try {
        const res = await fetch(`${BASE}/`);
        if (res.ok) {
          clearInterval(poll);
          clearTimeout(timeout);
          resolve();
        }
      } catch {
        /* not ready */
      }
    }, 200);
  });

  await fetch(`${BASE}/n/s3note/note?path=test.md`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    body: '---\ntitle: S3 Note\n---\n\nDirect-to-S3 uploads.\n',
  });
});

test.afterAll(async () => {
  if (serverProc) {
    serverProc.kill();
    serverProc = null;
  }
  if (s3) {
    await s3.close();
    s3 = null;
  }
  if (dataDir) {
    rmSync(dataDir, { recursive: true, force: true });
    dataDir = null;
  }
});

const sha256hex = (buf) => createHash('sha256').update(buf).digest('hex');

async function openEditor(page) {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto(BASE);
  await page.waitForFunction(() => {
    const sel = document.getElementById('notebooks');
    return sel && sel.options.length > 0;
  }, { timeout: 10000 });
  await page.selectOption('#notebooks', 's3note');
  await page.waitForSelector('.node', { timeout: 10000 });
  await page.click('.node');
  await expect(page.locator('#note')).toContainText('Direct-to-S3 uploads', { timeout: 10000 });
  await page.click('#editbtn');
  await expect(page.locator('#editbody')).toBeVisible({ timeout: 10000 });
  await expect(page.locator('#editormode')).toHaveText('plain text');
}

async function dropFile(page, name, content, mimeType) {
  await page.evaluate(({ name, content, mimeType, isBinary }) => {
    let file;
    if (isBinary) {
      const bytes = Uint8Array.from(atob(content), (c) => c.charCodeAt(0));
      file = new File([bytes], name, { type: mimeType });
    } else {
      file = new File([content], name, { type: mimeType });
    }
    const dt = new DataTransfer();
    dt.items.add(file);
    const editor = document.getElementById('editor');
    const ev = new Event('drop', { bubbles: true, cancelable: true });
    ev.dataTransfer = dt;
    editor.dispatchEvent(ev);
  }, {
    name,
    content: mimeType.startsWith('image/') ? PNG_1X1.toString('base64') : content,
    mimeType,
    isBinary: mimeType.startsWith('image/'),
  });
}

// Skip the first-run welcome modal so it never covers the page under test.
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('nr.helpSeen', '1'));
});

test.describe('direct-to-S3 browser upload', () => {
  test('bytes land in object storage, journal confirms, note renders', async ({ page }) => {
    const content = 'browser bytes go straight to object storage';
    const sha = sha256hex(Buffer.from(content));

    await openEditor(page);
    await dropFile(page, 'remote.txt', content, 'text/plain');

    // Markdown inserted + upload reported.
    await expect(page.locator('#editbody')).toHaveValue(
      new RegExp(`\\[remote\\.txt\\]\\(/n/s3note/resources/${sha}\\)`),
      { timeout: 15000 },
    );
    await expect(page.locator('#savestate')).toContainText('Uploaded remote.txt', { timeout: 15000 });

    // The bytes are in the fake S3 — keyed by content hash.
    expect(s3.objects.has(sha)).toBe(true);
    expect(s3.objects.get(sha).toString()).toBe(content);
    expect(s3.puts).toBe(1);

    // Autosave persists the note.
    await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });

    // Serve streams the blob back through the app server from S3.
    const res = await page.request.get(`${BASE}/n/s3note/resources/${sha}`);
    expect(res.status()).toBe(200);
    expect(await res.text()).toBe(content);

    // Renders as a link after reload.
    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 's3note');
    await page.waitForSelector('.node', { timeout: 10000 });
    await page.click('.node');
    const link = page.locator('#note a', { hasText: 'remote.txt' });
    await expect(link).toBeVisible({ timeout: 10000 });
    await expect(link).toHaveAttribute('href', `/n/s3note/resources/${sha}`);

    // Journal committed exactly one entry for the sha.
    const list = await (await page.request.get(`${BASE}/n/s3note/resources`)).json();
    const entries = (list.resources || []).filter((r) => r.SHA === sha);
    expect(entries.length).toBe(1);
    expect(entries[0].State).toBe('committed');
  });

  test('duplicate upload short-circuits: no second PUT to S3', async ({ page }) => {
    const content = 'only uploaded once to s3';
    const sha = sha256hex(Buffer.from(content));
    const putsBefore = s3.puts;

    await openEditor(page);
    await dropFile(page, 'once.txt', content, 'text/plain');
    await expect(page.locator('#savestate')).toContainText('Uploaded once.txt', { timeout: 15000 });
    expect(s3.puts).toBe(putsBefore + 1);

    // Same bytes again → exists short-circuit, no new PUT.
    await dropFile(page, 'once.txt', content, 'text/plain');
    await expect(page.locator('#savestate')).toContainText('already uploaded (dedup)', { timeout: 15000 });
    expect(s3.puts).toBe(putsBefore + 1);

    // Still exactly one journal entry for that sha.
    const list = await (await page.request.get(`${BASE}/n/s3note/resources`)).json();
    const entries = (list.resources || []).filter((r) => r.SHA === sha);
    expect(entries.length).toBe(1);
  });

  test('image uploads render as thumbnails served from S3', async ({ page }) => {
    const sha = sha256hex(PNG_1X1);
    const putsBefore = s3.puts;

    await openEditor(page);
    await dropFile(page, 's3pic.png', '', 'image/png');
    await expect(page.locator('#editbody')).toHaveValue(
      new RegExp(`!\\[s3pic\\.png\\]\\(/n/s3note/resources/${sha}\\)`),
      { timeout: 15000 },
    );
    await expect(page.locator('#savestate')).toContainText('Uploaded s3pic.png', { timeout: 15000 });
    expect(s3.puts).toBe(putsBefore + 1);
    expect(s3.objects.has(sha)).toBe(true);
    await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });

    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 's3note');
    await page.waitForSelector('.node', { timeout: 10000 });
    await page.click('.node');

    const img = page.locator('#note img[src*="/thumb"]');
    await expect(img).toBeVisible({ timeout: 10000 });
    const loaded = await img.evaluate((el) => el.complete && el.naturalWidth > 0);
    expect(loaded).toBe(true);
  });
});

test.describe('presign API (S3 mode)', () => {
  test('returns a direct upload URL instead of direct:true', async ({ page }) => {
    await page.goto(BASE);
    const result = await page.evaluate(async () => {
      const sha = 'c'.repeat(64);
      const r = await fetch('/n/s3note/resources/presign', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sha, size: 10, filename: 'api.txt', mime: 'text/plain' }),
      });
      return { status: r.status, body: await r.json() };
    });
    expect(result.status).toBe(200);
    expect(result.body.direct).toBe(false);
    expect(result.body.uploadURL).toContain(`${S3_BASE}/${BUCKET}/`);
    expect(result.body.uploadURL).toContain('X-Amz-Signature=');
    expect(result.body.entryID).toBeTruthy();
    expect(result.body.expiresIn).toBe(900);
  });

  test('confirm without upload returns 409', async ({ page }) => {
    await page.goto(BASE);
    const result = await page.evaluate(async () => {
      const sha = 'd'.repeat(64);
      const pre = await (await fetch('/n/s3note/resources/presign', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sha, size: 5, filename: 'ghost.txt', mime: 'text/plain' }),
      })).json();
      const conf = await fetch('/n/s3note/resources/confirm', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ entryID: pre.entryID, sha }),
      });
      return { status: conf.status, body: await conf.json() };
    });
    expect(result.status).toBe(409);
    expect(result.body.error).toBeTruthy();
  });

  test('presign-read returns a signed URL that fetches from S3', async ({ page }) => {
    const content = 'remote readable blob';
    const sha = sha256hex(Buffer.from(content));

    await page.goto(BASE);
    // Seed via the classic server-side upload (writes through S3Store).
    const seeded = await page.evaluate(async ({ content }) => {
      const r = await fetch('/n/s3note/resources?filename=reads3.txt', {
        method: 'POST',
        headers: { 'Content-Type': 'text/plain' },
        body: content,
      });
      return r.json();
    }, { content });
    expect(seeded.sha).toBe(sha);
    expect(s3.objects.has(sha)).toBe(true);

    const read = await page.evaluate(async ({ sha }) => {
      const r = await fetch(`/n/s3note/resources/${sha}/presign-read`);
      return { status: r.status, body: await r.json() };
    }, { sha });
    expect(read.status).toBe(200);
    expect(read.body.direct).toBe(false);
    expect(read.body.url).toContain('X-Amz-Signature=');

    // Fetch the signed URL from the Playwright side (no CORS needed).
    const res = await page.request.get(read.body.url);
    expect(res.status()).toBe(200);
    expect(await res.text()).toBe(content);
  });

  test('stats report the s3 backend', async ({ page }) => {
    await page.goto(BASE);
    const stats = await page.evaluate(async () => {
      const r = await fetch('/n/s3note/resources/stats');
      return r.json();
    });
    expect(stats.backend).toBe('s3');
    expect(stats.journal).toBeDefined();
  });

  test('policy rejection still applies when S3 is configured', async ({ page }) => {
    await page.goto(BASE);
    const result = await page.evaluate(async () => {
      const r = await fetch('/n/s3note/resources/presign', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sha: 'e'.repeat(64),
          size: 60 * 1024 * 1024,
          filename: 'huge.bin',
          mime: 'application/octet-stream',
        }),
      });
      return { status: r.status, body: await r.json() };
    });
    expect(result.status).toBe(413);
    expect(result.body.error).toBeTruthy();
  });
});
