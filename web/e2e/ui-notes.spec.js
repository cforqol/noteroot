// UI E2E: note/folder CRUD, tree view, status bar, theme, tree placement
// and responsive layout, against the real binary in a real browser.
// Run with: npx playwright test --config web/playwright.config.js e2e/ui-notes.spec.js
// Requires `make build`.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = 18400;
const BASE = `http://127.0.0.1:${PORT}`;
const SLUG = 'uinotes';
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
  dataDir = mkdtempSync(join(tmpdir(), 'nr-ui-'));
  const root = join(import.meta.dirname, '..', '..');
  execSync(`NOTEROOT_DATA_DIR=${dataDir} ${join(root, 'scripts/new-notebook.sh')} ${SLUG}`, { cwd: root });
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
        if ((await fetch(`${BASE}/healthz`)).ok) {
          clearInterval(poll);
          clearTimeout(timeout);
          resolve();
        }
      } catch { /* not ready */ }
    }, 200);
  });
});

test.afterAll(async () => {
  if (serverProc) serverProc.kill();
  if (dataDir) rmSync(dataDir, { recursive: true, force: true });
});

// Fresh browser state per test; plain-text editor unless a test opts in
// to Vim (the wasm editor is exercised by vim-mode.spec.js).
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    if (!sessionStorage.getItem('nr.test-init')) {
      sessionStorage.setItem('nr.test-init', '1');
      localStorage.clear();
      localStorage.setItem('nr.editorPref', 'textarea');
      localStorage.setItem('nr.helpSeen', '1');
    }
  });
});

async function openApp(page) {
  await page.goto(BASE);
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', SLUG);
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
}

const row = (page, path) => page.locator(`#tree [data-path="${path}"]`);
async function rowMenu(page, path, item) {
  await row(page, path).hover();
  await page.locator(`#tree [data-path="${path}"]`).locator('xpath=..').locator('.row-menu').click();
  await page.locator('.ctx-menu .ctx-item', { hasText: item }).click();
}
async function fillDialog(page, values) {
  const dlg = page.locator('.dialog');
  await expect(dlg).toBeVisible();
  for (const [name, v] of Object.entries(values)) await dlg.locator(`[name="${name}"]`).fill(v);
  await dlg.locator('button[type=submit]').click();
}

test.describe.configure({ mode: 'serial' });

test('empty notebook shows a hint and zero counts', async ({ page }) => {
  await openApp(page);
  await expect(page.locator('#tree-status')).toHaveText('0 notes · 0 folders');
  await expect(page.locator('.tree-empty')).toBeVisible();
  await expect(page.locator('#sb-mode')).toHaveText('Read');
});

test('create a folder and a note inside it; tree shows icon, count and status', async ({ page }) => {
  await openApp(page);
  await page.click('#new-folder');
  await fillDialog(page, { name: 'projects' });
  await expect(row(page, 'projects')).toBeVisible();
  await expect(row(page, 'projects').locator('.count')).toHaveText('[0]');
  await expect(row(page, 'projects').locator('.icon')).toHaveText('📂');
  await expect(page.locator('#tree-status')).toHaveText('0 notes · 1 folder');

  // The new folder is selected, so "+ Note" lands inside it.
  await page.click('#new-note');
  await expect(page.locator('.dialog-message')).toContainText('projects');
  await fillDialog(page, { name: 'roadmap' });
  await expect(page.locator('#editor')).toBeVisible();
  await expect(page.locator('#editbody')).toHaveValue(/# roadmap\n\n$/); // server adds a frontmatter id
  await page.click('#closebtn');
  await expect(row(page, 'projects/roadmap.md')).toBeVisible();
  await expect(row(page, 'projects').locator('.count')).toHaveText('[1]');
  await expect(page.locator('#tree-status')).toHaveText('1 note · 1 folder');
});

test('new note validation and duplicate protection', async ({ page }) => {
  await openApp(page);
  await page.click('#new-note');
  await fillDialog(page, { name: '../evil' });
  await expect(page.locator('.dialog-error')).toBeVisible();
  await page.locator('.dialog button', { hasText: 'Cancel' }).click();
  await expect(page.locator('.dialog')).toHaveCount(0);

  await row(page, 'projects').click(); // select projects
  await row(page, 'projects').click();
  await page.click('#new-note');
  await fillDialog(page, { name: 'roadmap' }); // already exists
  await expect(page.locator('#toast')).toContainText('already exists');
  await expect(page.locator('#editor')).toBeHidden();
});

test('status bar: words, selection, row:col, size and mode while typing', async ({ page }) => {
  await put('essay.md', '---\nid: 01ARZ3NDEKTSV4RRFFQ69G5FAV\n---\nplaceholder');
  await openApp(page);
  await row(page, 'essay.md').click();
  await expect(page.locator('#sb-mode')).toHaveText('Read');
  await expect(page.locator('#sb-words')).toHaveText('1');
  await page.click('#editbtn');
  await expect(page.locator('#sb-mode')).toHaveText('Plain');
  await expect(page.locator('#editormode')).toHaveText('plain text');

  const ta = page.locator('#editbody');
  await ta.fill('alpha beta gamma\n\ndelta epsilon');
  await expect(page.locator('#sb-words')).toHaveText('5');
  await expect(page.locator('#sb-paras')).toHaveText('2');
  await expect(page.locator('#sb-chars')).toHaveText(String('alphabetagammadeltaepsilon'.length));
  await expect(page.locator('#sb-reading')).toHaveText('1 min');
  await expect(page.locator('#sb-pos')).toHaveText('3:14');
  await expect(page.locator('#sb-size')).toHaveText('31 B');
  await expect(page.locator('#sb-selected')).toHaveText('0');

  await page.keyboard.press('Control+a');
  await expect(page.locator('#sb-selected')).toHaveText('5');
  await page.keyboard.press('Control+Home');
  await expect(page.locator('#sb-pos')).toHaveText('1:1');
  await page.keyboard.press('Shift+End');
  await expect(page.locator('#sb-selected')).toHaveText('3');

  await page.click('#savebtn');
  await expect(page.locator('#savestate')).toHaveText('Saved.');
  await page.click('#closebtn');
  await expect(page.locator('#sb-mode')).toHaveText('Read');
  await expect(page.locator('#sb-words')).toHaveText('5');
});

test('status bar shows Vim mode when the Vim editor runs', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('nr.editorPref', 'vim'));
  await openApp(page);
  await row(page, 'essay.md').click();
  await page.click('#editbtn');
  await expect(page.locator('#editormode')).toHaveText('vim', { timeout: 15000 });
  await expect(page.locator('#sb-mode')).toHaveText('Vim');
  await expect(page.locator('#sb-pos')).toHaveText('—'); // Vim exposes no cursor
  await expect(page.locator('#sb-words')).not.toHaveText('—');
});

test('folder collapse state, expand/collapse all, and persistence across reload', async ({ page }) => {
  await openApp(page);
  await expect(row(page, 'projects/roadmap.md')).toBeVisible();
  await row(page, 'projects').click();
  await expect(row(page, 'projects/roadmap.md')).toHaveCount(0);
  await expect(row(page, 'projects').locator('.icon')).toHaveText('📁');
  await expect(row(page, 'projects').locator('.count')).toHaveText('[1]'); // count survives collapse
  await page.reload();
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
  await expect(row(page, 'projects/roadmap.md')).toHaveCount(0);
  await page.click('#tree-expand');
  await expect(row(page, 'projects/roadmap.md')).toBeVisible();
  await page.click('#tree-collapse');
  await expect(row(page, 'projects/roadmap.md')).toHaveCount(0);
  await page.click('#tree-expand');
});

test('user-assigned folder icon and color persist and follow a rename', async ({ page }) => {
  await openApp(page);
  await rowMenu(page, 'projects', 'Icon & color');
  await page.locator('.dialog [name=emoji]').fill('🚀');
  await page.locator('.dialog [name=useColor]').check();
  await page.locator('.dialog [name=color]').fill('#ff0000');
  await page.locator('.dialog button[type=submit]').click();
  await expect(row(page, 'projects').locator('.icon')).toHaveText('🚀');
  await expect(row(page, 'projects').locator('.label')).toHaveCSS('color', 'rgb(255, 0, 0)');
  await page.reload();
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
  await expect(row(page, 'projects').locator('.icon')).toHaveText('🚀');

  await rowMenu(page, 'projects', 'Rename…');
  await fillDialog(page, { name: 'active' });
  await expect(row(page, 'active')).toBeVisible();
  await expect(row(page, 'active').locator('.icon')).toHaveText('🚀');
  await expect(row(page, 'active/roadmap.md')).toBeVisible();
  await expect(row(page, 'projects')).toHaveCount(0);

  // Move the renamed folder into a new top-level "work" folder.
  await rowMenu(page, 'active', 'Move…');
  await page.locator('.dialog [name="dest"]').selectOption('');
  await page.locator('.dialog [name="newFolder"]').fill('work');
  await page.locator('.dialog button[type=submit]').click();
  await expect(row(page, 'work/active')).toBeVisible();
  await expect(row(page, 'work/active').locator('.icon')).toHaveText('🚀');
  await expect(row(page, 'work/active/roadmap.md')).toBeVisible();
  await expect(row(page, 'work').locator('.count')).toHaveText('[1]');
});

test('rename a note, then delete it, then restore it from the trash panel', async ({ page }) => {
  await openApp(page);
  await rowMenu(page, 'work/active/roadmap.md', 'Rename…');
  await fillDialog(page, { name: 'plan' });
  await expect(row(page, 'work/active/plan.md')).toBeVisible();
  await expect(row(page, 'work/active/roadmap.md')).toHaveCount(0);

  // Move it up into "work" so the folder-delete test below stays simple.
  await rowMenu(page, 'work/active/plan.md', 'Move…');
  await page.locator('.dialog [name="dest"]').selectOption('work');
  await page.locator('.dialog button[type=submit]').click();
  await expect(row(page, 'work/plan.md')).toBeVisible();

  await row(page, 'work/plan.md').click();
  await expect(page.locator('#note')).toContainText('roadmap'); // content is unchanged by the rename
  await rowMenu(page, 'work/plan.md', 'Delete note');
  await expect(page.locator('.dialog-message')).toContainText('work/plan.md');
  await page.locator('.dialog button[type=submit]').click();
  await expect(row(page, 'work/plan.md')).toHaveCount(0);
  await expect(page.locator('#editbtn')).toBeHidden();

  await page.click('#trash-btn');
  await expect(page.locator('#trash-list .trash-path', { hasText: 'work/plan.md' })).toBeVisible();
  await page.locator('#trash-list li', { hasText: 'work/plan.md' })
    .getByRole('button', { name: 'Restore' }).click();
  await expect(row(page, 'work/plan.md')).toBeVisible();
  await page.click('#trash-close');
  await expect(page.locator('#trash-modal')).toBeHidden();
});

test('deleting a folder trashes its notes; they restore into a recreated folder', async ({ page }) => {
  await openApp(page);
  await rowMenu(page, 'work', 'Delete folder');
  await expect(page.locator('.dialog-message')).toContainText('Move 1 note ');
  await page.locator('.dialog button[type=submit]').click();
  await expect(row(page, 'work')).toHaveCount(0);
  await expect(page.locator('#tree-status')).toContainText('folder');

  await page.click('#trash-btn');
  await expect(page.locator('#trash-list li')).toHaveCount(1);
  await page.locator('#trash-list li', { hasText: 'work/plan.md' })
    .getByRole('button', { name: 'Restore' }).click();
  await expect(row(page, 'work/plan.md')).toBeVisible();
  await page.click('#trash-close');
});

test('the editor blocks moving a note that is open for editing', async ({ page }) => {
  await openApp(page);
  await row(page, 'work/plan.md').click();
  await page.click('#editbtn');
  await rowMenu(page, 'work/plan.md', 'Rename…');
  await expect(page.locator('#toast')).toContainText('Close the editor');
  await expect(page.locator('.dialog')).toHaveCount(0);
  await page.click('#closebtn');
});

test('theme: header button cycles and persists; dark really darkens the page', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' });
  await openApp(page);
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  expect(await bg()).toBe('rgb(255, 255, 255)');
  await page.click('#themebtn'); // light
  await page.click('#themebtn'); // dark
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  expect(await bg()).toBe('rgb(22, 24, 29)');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  expect(await bg()).toBe('rgb(22, 24, 29)');
  await page.click('#themebtn'); // auto
  await expect(page.locator('html')).not.toHaveAttribute('data-theme', /.+/);
  expect(await bg()).toBe('rgb(255, 255, 255)');
});

test('theme: explicit light beats a dark OS preference; settings radio works', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await openApp(page);
  await page.click('#settingsbtn');
  await page.locator('input[name="theme"][value="light"]').check();
  await page.click('#settings-close');
  expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe('rgb(255, 255, 255)');
  await page.click('#settingsbtn');
  await page.locator('input[name="theme"][value="auto"]').check();
  await page.click('#settings-close');
  expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe('rgb(22, 24, 29)');
});

test('tree placement: left, right, hidden; persists; ☰ toggles', async ({ page }) => {
  await openApp(page);
  const x = (sel) => page.locator(sel).evaluate((e) => e.getBoundingClientRect().x);
  expect(await x('#sidebar')).toBeLessThan(await x('#content'));

  await page.click('#settingsbtn');
  await page.locator('input[name="tree-placement"][value="right"]').check();
  await page.click('#settings-close');
  expect(await x('#sidebar')).toBeGreaterThan(await x('#content'));
  await page.reload();
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
  expect(await x('#sidebar')).toBeGreaterThan(await x('#content'));

  await page.click('#treebtn');
  await expect(page.locator('#sidebar')).toBeHidden();
  await expect(page.locator('body')).toHaveAttribute('data-tree', 'hidden');
  await page.click('#treebtn');
  await expect(page.locator('#sidebar')).toBeVisible();
  await expect(page.locator('body')).toHaveAttribute('data-tree', 'right'); // returns to its last side

  await page.click('#settingsbtn');
  await page.locator('input[name="tree-placement"][value="hidden"]').check();
  await page.click('#settings-close');
  await expect(page.locator('#sidebar')).toBeHidden();
  await expect(page.locator('#content')).toBeVisible();
});

test('phone viewport: tree is a drawer, status bar compacts, no horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await openApp(page);
  // Lands on the note list.
  await expect(page.locator('#sidebar')).toBeInViewport();
  await expect(page.locator('.tree-scrim')).toBeVisible();
  await row(page, 'essay.md').click();
  await expect(page.locator('#note')).toContainText('alpha');
  await expect(page.locator('#sidebar')).not.toBeInViewport();

  await page.click('#treebtn');
  await expect(page.locator('#sidebar')).toBeInViewport();
  await page.locator('.tree-scrim').click({ position: { x: 360, y: 400 } });
  await expect(page.locator('#sidebar')).not.toBeInViewport();

  await expect(page.locator('#sb-mode')).toBeVisible();
  await expect(page.locator('#sb-words')).toBeVisible();
  await expect(page.locator('.sb-extra').first()).toBeHidden();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(0);

  // Dialogs fit the screen.
  await page.click('#treebtn');
  await page.click('#new-note');
  const box = await page.locator('.dialog .modal-content').boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(375);
  await page.locator('.dialog button', { hasText: 'Cancel' }).click();
});

test('phone viewport: right-placed drawer slides in from the right', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('nr.treePlacement', 'right'));
  await page.setViewportSize({ width: 375, height: 812 });
  await openApp(page);
  const sidebar = page.locator('#sidebar');
  // Open on landing: flush with the right edge once the slide-in settles.
  await expect.poll(async () => (await sidebar.boundingBox()).x + (await sidebar.boundingBox()).width).toBeCloseTo(375, 0);
  expect((await sidebar.boundingBox()).x).toBeGreaterThan(0);
  // Closed: parked fully off-canvas to the right.
  await page.locator('.tree-scrim').click({ position: { x: 20, y: 400 } });
  await expect.poll(async () => (await sidebar.boundingBox()).x).toBeGreaterThanOrEqual(375);
});

test('tablet/desktop breakpoints keep the tree docked', async ({ page }) => {
  for (const w of [1280, 900, 760]) {
    await page.setViewportSize({ width: w, height: 800 });
    await openApp(page);
    await expect(page.locator('#sidebar')).toBeVisible();
    await expect(page.locator('.tree-scrim')).toBeHidden();
    const sidebar = await page.locator('#sidebar').boundingBox();
    const content = await page.locator('#content').boundingBox();
    expect(sidebar.x + sidebar.width).toBeLessThanOrEqual(content.x + 1);
  }
});

test('folder API: validation and safety over real HTTP', async ({ request }) => {
  const base = `${BASE}/n/${SLUG}/folder`;
  const h = { 'Content-Type': 'application/json' };
  expect((await request.post(base, { headers: h, data: { path: 'api/one' } })).status()).toBe(201);
  expect((await request.post(base, { headers: h, data: { path: 'api/one' } })).status()).toBe(409);
  expect((await request.post(base, { headers: h, data: { path: '.hidden' } })).status()).toBe(400);
  expect((await request.post(base, { headers: h, data: { path: '../escape' } })).status()).toBeGreaterThanOrEqual(400);
  expect((await request.patch(base, { headers: h, data: { from: 'api', to: 'api/inner' } })).status()).toBe(409);
  expect((await request.patch(base, { headers: h, data: { from: 'api/one', to: 'api/two' } })).status()).toBe(200);
  // DELETE on a note path must never trash the note through the folder route.
  expect((await request.delete(`${base}?path=essay.md`, { headers: h })).status()).toBe(400);
  expect((await request.get(`${BASE}/n/${SLUG}/note?path=essay.md`)).status()).toBe(200);
  expect((await request.delete(`${base}?path=api`, { headers: h })).status()).toBe(200);
  expect((await request.delete(`${base}?path=api`, { headers: h })).status()).toBe(404);
  // Cross-origin mutation is refused by the CSRF layer.
  const evil = await request.post(base, { headers: { ...h, Origin: 'https://evil.example' }, data: { path: 'x' } });
  expect(evil.status()).toBe(403);
});
