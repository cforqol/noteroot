// UI E2E: note/folder CRUD, tree view, status bar, theme, tree placement
// and responsive layout, against the real binary in a real browser.
// Run with: npx playwright test --config web/playwright.config.js e2e/ui-notes.spec.js
// Requires `make build`.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, rmSync, utimesSync } from 'node:fs';
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
  // + and its caret are one control: the caret opens the folder/import menu.
  await page.click('#new-more');
  await page.locator('.ctx-menu .ctx-item', { hasText: 'New folder' }).click();
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
  await page.locator('.dialog .icon-choice[data-icon="🚀"]').click();
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
  // One folder header ("work") plus the nested note row.
  await expect(page.locator('#trash-list li')).toHaveCount(2);
  await expect(page.locator('#trash-list li.trash-folder .trash-path')).toContainText('work/');
  await page.locator('#trash-list li.nested', { hasText: 'work/plan.md' })
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
  await   row(page, 'essay.md').click();
  await expect(page.locator('#note')).toContainText('alpha');
  await expect(page.locator('#sidebar')).not.toBeInViewport();
  // No room for the note panel on a phone: it stays closed until asked for.
  await expect(page.locator('#readpane')).toBeHidden();
  await page.click('#readpanebtn');
  await expect(page.locator('#readpane')).toBeVisible();
  await page.click('#readpane-close');
  await expect(page.locator('#readpane')).toBeHidden();

  await page.click('#treebtn');
  await expect(page.locator('#sidebar')).toBeInViewport();
  await page.locator('.tree-scrim').click({ position: { x: 360, y: 400 } });
  await expect(page.locator('#sidebar')).not.toBeInViewport();

  await expect(page.locator('#sb-mode')).toBeVisible();
  await expect(page.locator('#sb-words')).toBeVisible();
  await expect(page.locator('.sb-extra').first()).toBeHidden();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(0);
  // The read gutter collapses on a phone: no column, no painted strip, no numbers.
  const gutter = await page.evaluate(() => {
    const art = document.getElementById('note');
    const block = art.querySelector('p, h1, h2, li, pre');
    return {
      pad: getComputedStyle(art).paddingLeft,
      bg: getComputedStyle(art).backgroundImage,
      num: block ? getComputedStyle(block, '::before').display : null,
    };
  });
  expect(gutter).toEqual({ pad: '16px', bg: 'none', num: 'none' });

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

test('homepage: adopt the open note, auto-open on load and after Esc, clear restores empty state', async ({ page }) => {
  await openApp(page);
  await row(page, 'work/plan.md').click();
  await expect(page.locator('#note')).toContainText('roadmap');

  // Adopt the open note via Settings → Startpage.
  await page.click('#settingsbtn');
  await page.locator('.settings-nav button[data-target="set-homepage"]').click();
  await expect(page.locator('#homepage-path')).toBeVisible();
  await page.locator('#homepage-use-current').click();
  await expect(page.locator('#homepage-path')).toHaveValue('work/plan.md');
  await page.click('#settings-close');

  // Esc while ON the homepage is a no-op.
  await page.keyboard.press('Escape');
  await expect(page.locator('#note')).toContainText('roadmap');

  // Reload: the homepage opens by itself once the notebook is selected.
  await page.reload();
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', SLUG);
  await expect(page.locator('#note')).toContainText('roadmap');

  // Esc from another note falls back to the homepage.
  await row(page, 'essay.md').click();
  await expect(page.locator('#note')).toContainText('alpha beta gamma');
  await page.keyboard.press('Escape');
  await expect(page.locator('#note')).toContainText('roadmap');

  // Clearing the homepage restores the plain empty state.
  await page.click('#settingsbtn');
  await page.locator('.settings-nav button[data-target="set-homepage"]').click();
  await page.locator('#homepage-clear').click();
  await expect(page.locator('#homepage-path')).toHaveValue('_blank');
  await page.click('#settings-close');
  await row(page, 'essay.md').click();
  await page.keyboard.press('Escape');
  await expect(page.locator('#note')).toContainText('Nothing open');
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

test('an old note can be saved, left and re-edited without a false conflict', async ({ page }) => {
  // Last touched ten days ago. Reads used to be heuristically cacheable
  // for ~10% of that age, so coming back after a save showed the old
  // text and the next save failed with "Changed elsewhere".
  await put('aged.md', '---\ntitle: Aged\ncreated: 2026-01-01\nupdated: 2026-01-01\n---\nOriginal text.\n');
  await put('aside.md', '# Aside\n\nElsewhere.\n');
  const past = new Date(Date.now() - 10 * 24 * 3600 * 1000);
  utimesSync(join(dataDir, 'notebooks', SLUG, 'vault', 'aged.md'), past, past);

  await openApp(page);
  await row(page, 'aged.md').click();
  await expect(page.locator('#note')).toContainText('Original text.');
  await page.click('#editbtn');
  await page.locator('#editbody').press('Control+End');
  await page.keyboard.type('First edit.\n');
  await page.click('#savebtn');
  await expect(page.locator('#savestate')).toHaveText('Saved.');
  await page.click('#closebtn');
  await expect(page.locator('#note')).toContainText('First edit.');

  await row(page, 'aside.md').click();
  await expect(page.locator('#note')).toContainText('Elsewhere.');
  await row(page, 'aged.md').click();
  await expect(page.locator('#note')).toContainText('First edit.');
  await page.click('#editbtn');
  await expect(page.locator('#editbody')).toHaveValue(/First edit\./);
  await page.locator('#editbody').press('Control+End');
  await page.keyboard.type('Second edit.\n');
  await page.click('#savebtn');
  await expect(page.locator('#savestate')).toHaveText('Saved.');
});

test('keybindings: rebind the new-note shortcut and run it', async ({ page }) => {
  await openApp(page);
  await page.click('#settingsbtn');
  await page.locator('#shortcuts-manage').click();
  await expect(page.locator('#shortcuts-modal')).toBeVisible();

  const newNote = page.locator('.keybinding-row[data-action="new-note"] .keybinding-capture');
  const reset = page.locator('.keybinding-row[data-action="new-note"] .keybinding-reset');
  await expect(newNote).toContainText('n');
  await expect(reset).toBeHidden(); // a default binding shows no reset button
  await newNote.click();
  await expect(newNote).toHaveClass(/capturing/);
  // Rebind New note from N to Ctrl+Shift+M.
  await page.keyboard.press('Control+Shift+M');
  await expect(newNote).not.toHaveClass(/capturing/);
  await expect(newNote).toContainText('Ctrl/⌘');
  await expect(newNote).toContainText('Shift');
  await expect(newNote).toContainText('m');
  await expect(reset).toBeVisible(); // a custom binding can be reset

  // Escape closes the shortcuts manager; the old key no longer opens the
  // dialog, the new one does.
  await page.keyboard.press('Escape');
  await expect(page.locator('#shortcuts-modal')).toBeHidden();
  await page.keyboard.press('n');
  await expect(page.locator('.dialog')).toHaveCount(0);
  await page.keyboard.press('Control+Shift+M');
  await expect(page.locator('.dialog')).toBeVisible();
  await page.keyboard.press('Escape');

  // Reset restores the default N binding.
  await page.click('#settingsbtn');
  await page.locator('#shortcuts-manage').click();
  await page.locator('#keybindings-reset').click();
  await expect(newNote).toContainText('n');
  await page.click('#shortcuts-close');
  await page.keyboard.press('n');
  await expect(page.locator('.dialog')).toBeVisible();
  await page.keyboard.press('Escape');
});

// --- read-mode line numbers & the folder icon picker ---------------------

/** Metrics for the read-mode line-number column painted by an article:
 *  where the text starts, where the pane starts, the strip, the numbers
 *  and (for a sample of block shapes) the box each number really occupies
 *  on screen, borders and all. */
async function readColumn(page, id) {
  return page.evaluate((elId) => {
    const art = document.getElementById(elId);
    const artBox = art.getBoundingClientRect();
    const padLeft = parseFloat(getComputedStyle(art).paddingLeft);
    const TAGS = ['H1', 'H2', 'H3', 'P', 'BLOCKQUOTE', 'UL', 'OL', 'PRE', 'HR', 'TABLE'];
    const blocks = [...art.children].filter((el) => TAGS.includes(el.tagName));
    const sample = ['H1', 'P', 'BLOCKQUOTE', 'UL', 'PRE']
      .map((t) => blocks.find((b) => b.tagName === t))
      .filter(Boolean);
    return {
      textLeft: artBox.left + padLeft,
      paneLeft: artBox.left,
      bg: getComputedStyle(art).backgroundImage,
      reset: getComputedStyle(art).counterReset,
      firstTop: blocks[0] ? blocks[0].getBoundingClientRect().top : artBox.top,
      contents: blocks.map((el) => getComputedStyle(el, '::before').content),
      nums: sample.map((el) => {
        const cs = getComputedStyle(el, '::before');
        const box = el.getBoundingClientRect();
        // ::before is placed against the block's padding box, so step over
        // the block's own left border before applying left and margin-left.
        const left = box.left + (parseFloat(getComputedStyle(el).borderLeftWidth) || 0)
          + (parseFloat(cs.left) || 0) + (parseFloat(cs.marginLeft) || 0);
        return {
          tag: el.tagName,
          content: cs.content,
          left,
          right: left + (parseFloat(cs.width) || 0),
        };
      }),
    };
  }, id);
}

test('read mode numbers its blocks in a gutter column, one per pane', async ({ page }) => {
  await put('columns.md',
    '# Alpha heading\n\nFirst paragraph.\n\n> quoted line\n\n- item one\n- item two\n\n```\ncode line\n```\n\nSecond paragraph.\n');
  await put('other.md', '# Other note\n\nBody of the other note.\n');

  await openApp(page);
  await row(page, 'columns.md').click();
  await expect(page.locator('#note')).toContainText('Second paragraph.');

  const main = await readColumn(page, 'note');
  expect(main.bg).toContain('linear-gradient'); // a strip, not a loose margin
  expect(main.contents).toEqual(Array(6).fill('counter(readln)'));
  expect(main.reset).toContain('readln'); // the pane owns the count
  // heading, paragraph, quote, list and code block share one right edge
  expect(new Set(main.nums.map((n) => Math.round(n.right * 10) / 10)).size).toBe(1);
  for (const n of main.nums) {
    expect(n.right).toBeLessThanOrEqual(main.textLeft + 0.5); // never in the text
    expect(n.left).toBeGreaterThanOrEqual(main.paneLeft - 0.5); // never off the pane
  }

  // A split note gets a column of its own, numbered from 1 again.
  await row(page, 'other.md').click({ button: 'right' });
  await page.locator('.ctx-menu .ctx-item', { hasText: 'Open in split pane' }).click();
  await expect(page.locator('#split-note')).toContainText('Body of the other note.');

  const split = await readColumn(page, 'split-note');
  expect(split.bg).toContain('linear-gradient');
  expect(split.contents).toEqual(Array(2).fill('counter(readln)'));
  expect(split.reset).toContain('readln');
  expect(new Set(split.nums.map((n) => Math.round(n.right * 10) / 10)).size).toBe(1);
  expect(split.nums[0].right).toBeLessThanOrEqual(split.textLeft + 0.5);
  expect(split.nums[0].left).toBeGreaterThanOrEqual(split.paneLeft - 0.5);

  // the main pane keeps its own column and its own numbering
  const mainAgain = await readColumn(page, 'note');
  expect(mainAgain.contents).toHaveLength(6);
  expect(mainAgain.textLeft).not.toBe(split.textLeft); // laid out per pane

  // The numbers really are painted inside those two boxes: hide them and
  // the pixels in each column change, while the pixels over the text do not.
  const clips = [main, split].map((c) => {
    const left = Math.min(...c.nums.map((n) => n.left));
    const right = Math.max(...c.nums.map((n) => n.right));
    return {
      column: { // the number boxes, in viewport coordinates
        x: Math.floor(left - 3), y: Math.floor(c.firstTop),
        width: Math.ceil(right - left + 6), height: 60,
      },
      text: { x: Math.ceil(c.textLeft), y: Math.floor(c.firstTop), width: 140, height: 60 },
    };
  });
  const shot = (clip) => page.screenshot({ clip });
  const before = await Promise.all(clips.flatMap((c) => [shot(c.column), shot(c.text)]));
  await page.addStyleTag({
    content: '#note > :is(p,h1,h2,h3,h4,h5,h6,pre,blockquote,ul,ol,table,hr)::before,'
      + '#split-note > :is(p,h1,h2,h3,h4,h5,h6,pre,blockquote,ul,ol,table,hr)::before { content: none !important; }',
  });
  const after = await Promise.all(clips.flatMap((c) => [shot(c.column), shot(c.text)]));
  expect(Buffer.compare(before[0], after[0])).not.toBe(0); // main column inked
  expect(Buffer.compare(before[2], after[2])).not.toBe(0); // split column inked
  expect(Buffer.compare(before[1], after[1])).toBe(0);     // text untouched
  expect(Buffer.compare(before[3], after[3])).toBe(0);
});

test('the folder icon picker scrolls inside its dialog instead of filling the screen', async ({ page }) => {
  await put('iconpicker/tmp.md', 'scratch\n');
  await openApp(page);
  await rowMenu(page, 'iconpicker', 'Icon & color');

  const grid = page.locator('.dialog .icon-grid');
  await expect(grid).toBeVisible();
  const vp = page.viewportSize();
  const box = await grid.boundingBox();
  expect(box.height).toBeLessThanOrEqual(361); // capped: min(46vh, 360px)
  expect(await grid.evaluate((el) => getComputedStyle(el).overflowY)).toBe('auto');
  expect(await grid.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);

  // the dialog as a whole still fits, Save button included
  const dlg = await page.locator('.dialog .modal-content').boundingBox();
  expect(dlg.y + dlg.height).toBeLessThanOrEqual(vp.height);
  await expect(page.locator('.dialog button[type=submit]')).toBeInViewport();

  // Pick an icon from below the fold, save, reopen: the frame scrolls the
  // folder's current icon into view instead of leaving it off screen.
  const last = page.locator('.dialog .icon-choice').last();
  const lastLabel = await last.getAttribute('data-icon');
  await last.click();
  await page.locator('.dialog button[type=submit]').click();
  await expect(row(page, 'iconpicker').locator('.icon')).toHaveText(lastLabel);

  await rowMenu(page, 'iconpicker', 'Icon & color');
  const sel = page.locator('.dialog .icon-choice[aria-selected="true"]');
  await expect(sel).toHaveAttribute('data-icon', lastLabel);
  const g = await grid.boundingBox();
  const s = await sel.boundingBox();
  expect(s.y).toBeGreaterThanOrEqual(g.y - 1);
  expect(s.y + s.height).toBeLessThanOrEqual(g.y + g.height + 1);

  await page.locator('.dialog .btn', { hasText: 'Cancel' }).click();
  await expect(page.locator('.dialog')).toHaveCount(0);
});

test('the note panel carries the contents, tags and links; the reading column carries only the note', async ({ page }) => {
  await put('other.md', '# Other note\n\nBack to [[panel]] for context.\n');
  await put('panel.md', [
    '---',
    'id: panelid',
    'title: Panel note',
    'tags: [alpha, beta]',
    '---',
    '# Panel heading',
    '',
    'First body paragraph pointing at [[other]].',
    '',
    ...Array.from({ length: 24 }, (_, i) => `Filler paragraph ${i + 1} to make the note taller than the pane.`),
    '',
    '## Second section',
    '',
    'Closing paragraph.',
    '',
  ].join('\n'));

  await openApp(page);
  await row(page, 'panel.md').click();
  await expect(page.locator('#note')).toContainText('Closing paragraph.');

  // The panel is on screen and holds what the note itself no longer does.
  const pane = page.locator('#readpane');
  await expect(pane).toBeVisible();
  await expect(page.locator('#content')).toHaveClass(/pane-on/);
  await expect(page.locator('#note .note-tags')).toHaveCount(0);
  await expect(page.locator('#note .links-panel')).toHaveCount(0);

  await expect(page.locator('#rp-toc a')).toHaveText(['Panel heading', 'Second section']);
  await expect(page.locator('#rp-tags .tag-chip')).toHaveText(['#alpha', '#beta']);
  await expect(page.locator('#rp-out')).toContainText('other');
  await expect(page.locator('#rp-back')).toContainText('other');

  // A TOC entry scrolls the note, not the page.
  const scrolled = await page.evaluate(async () => {
    const art = document.getElementById('note');
    art.scrollTop = 0;
    document.querySelectorAll('#rp-toc a')[1].click();
    await new Promise((r) => setTimeout(r, 50));
    return art.scrollTop;
  });
  expect(scrolled).toBeGreaterThan(0);

  // Hiding the panel gives the room back to the reading column.
  const wide = await page.locator('#note').boundingBox();
  await page.click('#readpanebtn');
  await expect(pane).toBeHidden();
  await expect(page.locator('#content')).not.toHaveClass(/pane-on/);
  const wider = await page.locator('#note').boundingBox();
  expect(wider.width).toBeGreaterThan(wide.width);

  await page.click('#readpanebtn');
  await expect(pane).toBeVisible();

  // Editing hides the panel: the editor gets the width, the panel has
  // nothing to describe while the source is on screen.
  await page.click('#editbtn');
  await expect(page.locator('#editor')).toBeVisible();
  await expect(pane).toBeHidden();
  const editBox = await page.locator('#editor').boundingBox();
  expect(editBox.width).toBeGreaterThan(wide.width);
  await page.click('#closebtn');
  await expect(pane).toBeVisible();
});

test('the Copy button puts the note on the clipboard without its frontmatter', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await put('copyme.md', '---\nid: c0ffee\ntitle: Copy me\n---\n# Copy me heading\n\nBody paragraph.\n');

  await openApp(page);
  await row(page, 'copyme.md').click();
  await expect(page.locator('#note')).toContainText('Copy me heading');

  await expect(page.locator('#copybtn')).toBeVisible();
  await page.click('#copybtn');
  await expect(page.locator('#toast')).toContainText('Note copied');

  const text = await page.evaluate(() => navigator.clipboard.readText());
  expect(text).toBe('# Copy me heading\n\nBody paragraph.\n');
  expect(text).not.toContain('---');
  expect(text).not.toContain('id: c0ffee');

  // Hidden while editing: there is nothing to copy but the source.
  await page.click('#editbtn');
  await expect(page.locator('#editor')).toBeVisible();
  await expect(page.locator('#copybtn')).toBeHidden();
});

test('task list checkboxes render in read mode', async ({ page }) => {
  // Seed both notes before the tree loads: a note written after the tree
  // was fetched never appears in it.
  await put('tasks.md', '# tasks\n\n- [ ] buy milk\n- [x] ship it\n');
  await put('rawinput.md', '<input type="text" name="pw">still here\n');

  await openApp(page);
  await row(page, 'tasks.md').click();
  await expect(page.locator('#note')).toBeVisible();
  await expect(page.locator('#note')).toContainText('buy milk');

  const boxes = page.locator('#note input[type="checkbox"]');
  await expect(boxes).toHaveCount(2);
  await expect(boxes.first()).not.toBeChecked();
  await expect(boxes.nth(1)).toBeChecked();

  // The checkbox stands in for the bullet rather than sitting next to it.
  const marker = await boxes.first().evaluate((el) =>
    getComputedStyle(el.closest('li')).getPropertyValue('list-style-type'));
  expect(marker.trim()).toBe('none');

  // Raw input HTML from a note never reaches the DOM.
  await row(page, 'rawinput.md').click();
  await expect(page.locator('#note')).toContainText('still here');
  await expect(page.locator('#note input')).toHaveCount(0);
});
