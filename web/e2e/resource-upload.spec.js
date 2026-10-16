// E2E tests for stage 7 resource upload (local storage mode).
// Run with: npx playwright test --config web/playwright.config.js
//
// Covers: drag-and-drop upload → markdown insertion → autosave →
// rendered note, image thumbnail rewriting, and the presign API's
// {direct:true} response when object storage is not configured.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = 18200;
const BASE = `http://127.0.0.1:${PORT}`;
let serverProc = null;
let dataDir = null;

// 1x1 red PNG.
const PNG_1X1 = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'nr-e2e-up-'));
  const root = join(import.meta.dirname, '..', '..');
  execSync(`NOTEROOT_DATA_DIR=${dataDir} ${join(root, 'scripts/new-notebook.sh')} upnote`, { cwd: root });

  serverProc = spawn(join(root, 'bin/noteroot'), [], {
    env: {
      ...process.env,
      NOTEROOT_DATA_DIR: dataDir,
      NOTEROOT_LISTEN: `127.0.0.1:${PORT}`,
      NOTEROOT_AUTH_MODE: 'none',
    },
    stdio: 'pipe',
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

  await fetch(`${BASE}/n/upnote/note?path=test.md`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    body: '---\ntitle: Upload Note\n---\n\nDrop files here.\n',
  });
});

test.afterAll(() => {
  if (serverProc) {
    serverProc.kill();
    serverProc = null;
  }
  if (dataDir) {
    rmSync(dataDir, { recursive: true, force: true });
    dataDir = null;
  }
});

// openEditor navigates to the note and opens the plain-text editor
// (mobile viewport forces textarea mode, avoiding the vim iframe).
async function openEditor(page) {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto(BASE);
  await page.waitForFunction(() => {
    const sel = document.getElementById('notebooks');
    return sel && sel.options.length > 0;
  }, { timeout: 10000 });
  await page.selectOption('#notebooks', 'upnote');
  await page.waitForSelector('.node', { timeout: 10000 });
  await page.click('.node');
  await expect(page.locator('#note')).toContainText('Drop files here', { timeout: 10000 });
  await page.click('#editbtn');
  await expect(page.locator('#editbody')).toBeVisible({ timeout: 10000 });
  await expect(page.locator('#editormode')).toHaveText('plain text');
}

// dropFile dispatches a synthetic drop with a File on the editor.
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

test.describe('drag-and-drop upload (local mode)', () => {
  test('text upload inserts markdown, autosaves, and renders on reload', async ({ page }) => {
    await openEditor(page);

    await dropFile(page, 'e2e.txt', 'hello from e2e', 'text/plain');

    // Markdown link inserted at the cursor.
    await expect(page.locator('#editbody')).toHaveValue(
      /\[e2e\.txt\]\(\/n\/upnote\/resources\/[a-f0-9]{64}\)/,
      { timeout: 15000 },
    );
    await expect(page.locator('#savestate')).toContainText('Uploaded e2e.txt', { timeout: 15000 });

    // Autosave (2s debounce) persists the note.
    await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });

    // Reload in read mode: link renders with the resource href.
    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'upnote');
    await page.waitForSelector('.node', { timeout: 10000 });
    await page.click('.node');
    const link = page.locator('#note a', { hasText: 'e2e.txt' });
    await expect(link).toBeVisible({ timeout: 10000 });
    await expect(link).toHaveAttribute('href', /\/n\/upnote\/resources\/[a-f0-9]{64}/);

    // The blob itself is servable.
    const href = await link.getAttribute('href');
    const res = await page.request.get(`${BASE}${href}`);
    expect(res.status()).toBe(200);
    expect(await res.text()).toBe('hello from e2e');
  });

  test('duplicate upload reports dedup, store unchanged', async ({ page }) => {
    await openEditor(page);

    await dropFile(page, 'dup.txt', 'duplicate payload', 'text/plain');
    await expect(page.locator('#savestate')).toContainText('Uploaded dup.txt', { timeout: 15000 });

    // Same content again → presign exists short-circuit.
    await dropFile(page, 'dup.txt', 'duplicate payload', 'text/plain');
    await expect(page.locator('#savestate')).toContainText('already uploaded (dedup)', { timeout: 15000 });

    // Only one resource entry for that sha in the list. (Journal entries
    // serialize with capitalised field names: SHA, Filename, State, …)
    const list = await page.request.get(`${BASE}/n/upnote/resources`);
    const body = await list.json();
    const entries = body.resources || [];
    const shas = entries.map((r) => r.SHA);
    expect(shas.length).toBeGreaterThan(0);
    expect(new Set(shas).size).toBe(shas.length);
    // Both uploads committed exactly once each.
    const committed = entries.filter((r) => r.State === 'committed');
    expect(committed.length).toBe(shas.length);
  });

  test('image upload renders as a rewritten thumbnail after reload', async ({ page }) => {
    await openEditor(page);

    await dropFile(page, 'pic.png', '', 'image/png');
    await expect(page.locator('#editbody')).toHaveValue(
      /\[pic\.png\]\(\/n\/upnote\/resources\/[a-f0-9]{64}\)|!\[pic\.png\]\(\/n\/upnote\/resources\/[a-f0-9]{64}\)/,
      { timeout: 15000 },
    );
    await expect(page.locator('#savestate')).toContainText('Uploaded pic.png', { timeout: 15000 });
    await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });

    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'upnote');
    await page.waitForSelector('.node', { timeout: 10000 });
    await page.click('.node');

    // Rendered note uses the thumb URL (stage 6 rewrite) and loads.
    const img = page.locator('#note img[src*="/thumb"]');
    await expect(img).toBeVisible({ timeout: 10000 });
    const loaded = await img.evaluate((el) => el.complete && el.naturalWidth > 0);
    expect(loaded).toBe(true);
  });
});

test.describe('presign API (local mode)', () => {
  test('reports direct:true when object storage is not configured', async ({ page }) => {
    await page.goto(BASE);
    const result = await page.evaluate(async () => {
      const sha = 'a'.repeat(64);
      const r = await fetch('/n/upnote/resources/presign', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sha, size: 10, filename: 'x.txt', mime: 'text/plain' }),
      });
      return { status: r.status, body: await r.json() };
    });
    expect(result.status).toBe(200);
    expect(result.body.direct).toBe(true);
    expect(result.body.uploadURL).toBeUndefined();
  });

  test('stats report the local backend', async ({ page }) => {
    await page.goto(BASE);
    const stats = await page.evaluate(async () => {
      const r = await fetch('/n/upnote/resources/stats');
      return r.json();
    });
    expect(stats.backend).toBe('local');
    expect(stats.journal).toBeDefined();
  });

  test('presign-read falls back to the server URL in local mode', async ({ page }) => {
    await page.goto(BASE);
    const read = await page.evaluate(async () => {
      // Seed our own blob so the test is order-independent.
      const up = await fetch('/n/upnote/resources?filename=readseed.txt', {
        method: 'POST',
        headers: { 'Content-Type': 'text/plain' },
        body: 'read seed',
      });
      const created = await up.json();
      if (!created.sha) return { error: 'upload failed' };
      const r = await fetch(`/n/upnote/resources/${created.sha}/presign-read`);
      return { status: r.status, body: await r.json(), sha: created.sha };
    });
    expect(read.status).toBe(200);
    expect(read.body.direct).toBe(true);
    expect(read.body.url).toBe(`/n/upnote/resources/${read.sha}`);
  });

  test('policy rejection surfaces on presign from the browser', async ({ page }) => {
    await page.goto(BASE);
    const result = await page.evaluate(async () => {
      const r = await fetch('/n/upnote/resources/presign', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sha: 'b'.repeat(64),
          size: 60 * 1024 * 1024, // > 50 MiB
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
