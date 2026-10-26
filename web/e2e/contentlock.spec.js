// E2E: the content lock (advisory password gate) over the real binary in a
// real browser — set a lock from the sidebar menu, see the locked placeholder,
// unlock with the password, reject a wrong one, re-lock, and lock the whole
// notebook from Settings.
// Run with: npx playwright test --config web/playwright.config.js e2e/contentlock.spec.js
// Requires `make build`.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = 18700;
const BASE = `http://127.0.0.1:${PORT}`;
const SLUG = 'locknb';
const ROOT = join(import.meta.dirname, '..', '..');
const BIN = join(ROOT, 'bin/noteroot');
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

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'nr-e2e-lock-'));
  execSync(`NOTEROOT_DATA_DIR=${dataDir} ${join(ROOT, 'scripts/new-notebook.sh')} ${SLUG}`, { cwd: ROOT });
  serverProc = spawn(BIN, [], {
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
  await put('open.md', '# Open\n\nNothing secret here.\n');
  await put('secret.md', '# Secret\n\nthe treasure is buried under the oak.\n');
  await put('vault/hidden.md', '# Hidden\n\nfolder secret\n');
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

async function openApp(page) {
  await page.goto(BASE);
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', SLUG);
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
}

async function openNodeMenu(page, path) {
  const row = page.locator(`#tree [data-path="${path}"]`).locator('..');
  await row.locator('.row-menu').click();
}

test.describe.configure({ mode: 'serial' });

test('lock a note, get the placeholder, unlock with the password', async ({ page }) => {
  await openApp(page);
  await page.locator('#tree [data-path="secret.md"]').click();
  await expect(page.locator('#note')).toContainText('buried under the oak');

  await openNodeMenu(page, 'secret.md');
  await page.locator('.ctx-menu .ctx-item', { hasText: 'Lock with password…' }).click();
  const dlg = page.locator('.modal.dialog');
  await dlg.locator('input[name="password"]').fill('hunter2');
  await dlg.locator('input[name="confirm"]').fill('hunter2');
  await dlg.locator('button[type="submit"]').click();
  await expect(page.locator('#toast')).toContainText('Locked');

  // Tree marks it, and re-opening shows the locked placeholder, not the body.
  await expect(page.locator('#tree [data-path="secret.md"] .lock-icon')).toBeVisible();
  await page.locator('#tree [data-path="open.md"]').click();
  await expect(page.locator('#note')).toContainText('Nothing secret');
  await page.locator('#tree [data-path="secret.md"]').click();
  await expect(page.locator('#note')).toContainText('This note is locked');
  await expect(page.locator('#note')).not.toContainText('buried under the oak');

  // Unlock through the placeholder button.
  await page.locator('#note button', { hasText: 'Unlock…' }).click();
  await page.locator('.modal.dialog input[name="password"]').fill('wrong-pw');
  await page.locator('.modal.dialog button[type="submit"]').click();
  // A wrong password keeps the dialog open with the reason.
  await expect(page.locator('.modal.dialog')).toContainText('Wrong password');
  await page.locator('.modal.dialog input[name="password"]').fill('hunter2');
  await page.locator('.modal.dialog button[type="submit"]').click();
  await expect(page.locator('#note')).toContainText('buried under the oak');
});

test('relock hides the note again but the session can unlock it', async ({ page }) => {
  await openApp(page);
  await openNodeMenu(page, 'secret.md');
  await page.locator('.ctx-menu .ctx-item', { hasText: 'Unlock…' }).click();
  await page.locator('.modal.dialog input[name="password"]').fill('hunter2');
  await page.locator('.modal.dialog button[type="submit"]').click();
  await expect(page.locator('#toast')).toContainText('Unlocked');

  // Lock again: the note leaves the current view and shows the placeholder.
  await openNodeMenu(page, 'secret.md');
  await page.locator('.ctx-menu .ctx-item', { hasText: 'Lock with password…' }).click();
  await page.locator('.modal.dialog input[name="password"]').fill('hunter2');
  await page.locator('.modal.dialog input[name="confirm"]').fill('hunter2');
  await page.locator('.modal.dialog button[type="submit"]').click();
  await expect(page.locator('#toast')).toContainText('Locked');

  await page.locator('#tree [data-path="secret.md"]').click();
  await expect(page.locator('#note')).toContainText('This note is locked');
});

test('a wrong password leaves the note locked, the right one opens it', async ({ page }) => {
  await openApp(page);
  await page.locator('#tree [data-path="secret.md"]').click();
  // Clear any unlock cookie so the gate applies.
  await page.context().clearCookies();
  await page.reload();
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', SLUG);
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
  await page.locator('#tree [data-path="secret.md"]').click();
  await expect(page.locator('#note')).toContainText('This note is locked');
  await expect(page.locator('#tree [data-path="secret.md"] .lock-icon')).toBeVisible();
});

test('locked folder hides its children until unlocked', async ({ page }) => {
  await openApp(page);
  // First remove the note lock so the notebook tree is otherwise open.
  await openNodeMenu(page, 'vault');
  await page.locator('.ctx-menu .ctx-item', { hasText: 'Lock with password…' }).click();
  await page.locator('.modal.dialog input[name="password"]').fill('folderpw');
  await page.locator('.modal.dialog input[name="confirm"]').fill('folderpw');
  await page.locator('.modal.dialog button[type="submit"]').click();
  await expect(page.locator('#toast')).toContainText('Locked');
  // The folder shows a lock and its child is gone from the tree.
  await expect(page.locator('#tree [data-path="vault"] .icon')).toHaveText('🔒');
  await expect(page.locator('#tree [data-path="vault/hidden.md"]')).toHaveCount(0);

  await openNodeMenu(page, 'vault');
  await page.locator('.ctx-menu .ctx-item', { hasText: 'Unlock…' }).click();
  await page.locator('.modal.dialog input[name="password"]').fill('folderpw');
  await page.locator('.modal.dialog button[type="submit"]').click();
  await expect(page.locator('#toast')).toContainText('Unlocked');
  await expect(page.locator('#tree [data-path="vault/hidden.md"]')).toHaveCount(1);
});

test('notebook-level lock from Settings covers the whole notebook', async ({ page }) => {
  await openApp(page);
  await page.click('#settingsbtn');
  await page.click('#settings-modal .settings-nav button[data-target="set-lock"]');
  await page.locator('#lock-notebook').click();
  const dlg = page.locator('.modal.dialog');
  await dlg.locator('input[name="password"]').fill('notepw');
  await dlg.locator('input[name="confirm"]').fill('notepw');
  await dlg.locator('button[type="submit"]').click();
  await expect(page.locator('#toast')).toContainText('Locked');
  await expect(page.locator('#lock-notebook-info')).toContainText(/Locked/);

  // A fresh browser (new session) sees the whole tree hidden behind the notebook lock.
  await page.context().clearCookies();
  await page.reload();
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', SLUG);
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
  await expect(page.locator('#tree .tree-empty')).toBeVisible();
});
