// E2E: the "+" split button, Esc in the plain editor, j/k reading,
// vertical split panes and the attachments manager, against the real
// binary in a real browser.
// Run with: npx playwright test --config web/playwright.config.js e2e/panes.spec.js
// Requires `make build`.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = 18620;
const BASE = `http://127.0.0.1:${PORT}`;
const SLUG = 'panesnb';
const ROOT = join(import.meta.dirname, '..', '..');
let serverProc = null;
let dataDir = null;

async function put(path, body) {
  const r = await fetch(`${BASE}/n/${SLUG}/note?path=${encodeURIComponent(path)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    body,
  });
  if (!r.ok) throw new Error(`seed ${path}: ${r.status}`);
}

async function source(path) {
  const r = await fetch(`${BASE}/n/${SLUG}/raw?path=${encodeURIComponent(path)}`);
  return r.text();
}

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'nr-e2e-panes-'));
  execSync(`NOTEROOT_DATA_DIR=${dataDir} ${join(ROOT, 'scripts/new-notebook.sh')} ${SLUG}`, { cwd: ROOT });
  // stdio is ignored: an unread pipe fills up and blocks the server.
  serverProc = spawn(join(ROOT, 'bin/noteroot'), [], {
    env: {
      ...process.env,
      NOTEROOT_DATA_DIR: dataDir,
      NOTEROOT_LISTEN: `127.0.0.1:${PORT}`,
      NOTEROOT_AUTH_MODE: 'none',
    },
    stdio: 'ignore',
  });
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('server start timeout')), 15000);
    const poll = setInterval(async () => {
      try {
        if ((await fetch(`${BASE}/healthz`)).ok) {
          clearInterval(poll);
          clearTimeout(timeout);
          resolve();
        }
      } catch { /* not ready */ }
    }, 200);
  });
  await put('a.md', '# Alpha\n\nfirst note\n');
  await put('b.md', '# Beta\n\nsecond note\n');
  await put('c.md', '# Gamma\n\nthird note\n');
});

test.afterAll(async () => {
  if (serverProc) serverProc.kill();
  if (dataDir) rmSync(dataDir, { recursive: true, force: true });
});

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('nr.editorPref', 'textarea');
    localStorage.setItem('nr.helpSeen', '1');
  });
});

const row = (page, path) => page.locator(`#tree [data-path="${path}"]`);

async function openApp(page) {
  await page.goto(BASE);
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', SLUG);
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
}

test.describe.configure({ mode: 'serial' });

test('the caret next to + opens New note / New folder / Import file', async ({ page }) => {
  await openApp(page);
  await expect(page.locator('#new-note')).toHaveText('+');
  await expect(page.locator('#new-folder')).toBeHidden();
  await page.click('#new-more');
  await expect(page.locator('#new-more')).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('.ctx-menu .ctx-item')).toHaveText(['New note', 'New note from template…', 'New folder', 'Import file…']);
  await page.keyboard.press('Escape');
  await expect(page.locator('.ctx-menu')).toHaveCount(0);
});

test('Esc in the plain-text editor saves and closes it', async ({ page }) => {
  await openApp(page);
  await row(page, 'a.md').click();
  await page.click('#editbtn');
  await page.locator('#editbody').press('Control+End');
  await page.locator('#editbody').pressSequentially('added by esc');
  await page.keyboard.press('Escape');
  await expect(page.locator('#editor')).toBeHidden();
  await expect(page.locator('#note')).toContainText('added by esc');
  expect(await source('a.md')).toContain('added by esc');
});

test('j and k step through notes while reading', async ({ page }) => {
  await openApp(page);
  await row(page, 'a.md').click();
  await expect(page.locator('#note')).toContainText('first note');
  await page.keyboard.press('j');
  await expect(page.locator('#note')).toContainText('second note');
  await page.keyboard.press('j');
  await expect(page.locator('#note')).toContainText('third note');
  await page.keyboard.press('k');
  await expect(page.locator('#note')).toContainText('second note');
  // Typing in the search box must not navigate.
  await page.locator('#q').focus();
  await page.keyboard.press('j');
  await expect(page.locator('#note')).toContainText('second note');
});

test('split view on an open note: editor on the left, live read view on the right', async ({ page }) => {
  await openApp(page);
  await row(page, 'a.md').click();
  await expect(page.locator('#note')).toContainText('first note');
  await expect(page.locator('#split-pane')).toBeHidden();

  await page.click('#splitbtn');
  await expect(page.locator('#split-pane')).toBeVisible();
  await expect(page.locator('#editbody')).toBeVisible();
  await expect(page.locator('#split-path')).toHaveText('a.md');
  await expect(page.locator('#split-note')).toContainText('first note');
  const ed = await page.locator('#editor').boundingBox();
  const rd = await page.locator('#split-pane').boundingBox();
  expect(rd.x).toBeGreaterThanOrEqual(ed.x + ed.width - 1);

  // Saving on the edit side refreshes the read side.
  await page.locator('#editbody').press('Control+End');
  await page.locator('#editbody').pressSequentially('\n\nlive preview text');
  await page.click('#savebtn');
  await expect(page.locator('#split-note')).toContainText('live preview text');

  await page.click('#split-close');
  await expect(page.locator('#split-pane')).toBeHidden();
});

test('the edit side is a setting: right edits, left reads', async ({ page }) => {
  await openApp(page);
  await page.evaluate(() => localStorage.setItem('nr.splitEditSide', 'right'));
  await row(page, 'a.md').click();
  await expect(page.locator('#note')).toContainText('first note');
  await page.click('#splitbtn');
  await expect(page.locator('#split-body')).toBeVisible();
  await expect(page.locator('#note')).toContainText('first note');
  await expect(page.locator('#editor')).toBeHidden();
});

test('split view shows two notes side by side and edits the second in place', async ({ page }) => {
  await openApp(page);
  await row(page, 'a.md').click();
  await expect(page.locator('#note')).toContainText('first note');
  await expect(page.locator('#split-pane')).toBeHidden();

  await row(page, 'c.md').click({ button: 'right' });
  await page.locator('.ctx-menu .ctx-item', { hasText: 'Open in split pane' }).click();
  await expect(page.locator('#splitbtn')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('#split-note')).toContainText('third note');
  await expect(page.locator('#split-path')).toHaveText('c.md');
  await expect(page.locator('#note')).toContainText('first note');

  const main = await page.locator('#note').boundingBox();
  const split = await page.locator('#split-pane').boundingBox();
  expect(split.x).toBeGreaterThanOrEqual(main.x + main.width - 1);
  const bar = await page.locator('#note-bar').boundingBox();
  expect(Math.abs(split.y - bar.y)).toBeLessThan(2);

  // Edit the second pane while the first stays in view; Esc saves and closes.
  await page.click('#split-edit');
  await expect(page.locator('#split-body')).toBeVisible();
  await page.locator('#split-body').press('Control+End');
  await page.locator('#split-body').pressSequentially('from the split');
  await page.keyboard.press('Escape');
  await expect(page.locator('#split-body')).toBeHidden();
  await expect(page.locator('#split-note')).toContainText('from the split');
  expect(await source('c.md')).toContain('from the split');
  await expect(page.locator('#note')).toContainText('first note');

  // Swap puts the second note in the main pane.
  await page.click('#split-swap');
  await expect(page.locator('#note')).toContainText('third note');
  await expect(page.locator('#split-note')).toContainText('first note');

  await page.click('#split-close');
  await expect(page.locator('#split-pane')).toBeHidden();
  await expect(page.locator('#splitbtn')).toHaveAttribute('aria-pressed', 'false');
});

test('the attachments manager lists, filters, uploads and closes on Esc', async ({ page }) => {
  await openApp(page);
  await page.click('#settingsbtn');
  await page.click('#settings-modal .settings-nav button[data-target="set-attachments"]');
  await page.click('#attachments-manage');
  await expect(page.locator('#attachments-modal')).toBeVisible();
  await expect(page.locator('#settings-modal')).toBeHidden();
  await expect(page.locator('#attachments-summary')).toHaveText('0 of 0 files');

  await page.locator('#attachments-upload-file').setInputFiles([
    { name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('hello attachments') },
    { name: 'pixel.png', mimeType: 'image/png', buffer: Buffer.from(
      'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64') },
  ]);
  await expect(page.locator('.attach-card')).toHaveCount(2);
  await expect(page.locator('#attachments-summary')).toHaveText('2 of 2 files');

  await page.fill('#attachments-search', 'notes');
  await expect(page.locator('.attach-card')).toHaveCount(1);
  await expect(page.locator('.attach-name')).toHaveText('notes.txt');
  await page.fill('#attachments-search', '');
  await page.selectOption('#attachments-filter', 'image');
  await expect(page.locator('.attach-name')).toHaveText('pixel.png');

  await page.locator('.attach-card .btn', { hasText: 'References' }).click();
  await expect(page.locator('.attach-refs')).toHaveText('Used in: none');

  await page.keyboard.press('Escape');
  await expect(page.locator('#attachments-modal')).toBeHidden();
});
