// TEMPORARY diagnostic probe — delete me.
import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = 18555;
const BASE = `http://127.0.0.1:${PORT}`;
let serverProc = null;
let dataDir = null;

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'nr-probe-'));
  const root = join(import.meta.dirname, '..', '..');
  execSync(`NOTEROOT_DATA_DIR=${dataDir} ${join(root, 'scripts/new-notebook.sh')} probe`, { cwd: root });
  serverProc = spawn(join(root, 'bin/noteroot'), [], {
    env: { ...process.env, NOTEROOT_DATA_DIR: dataDir, NOTEROOT_LISTEN: `127.0.0.1:${PORT}`, NOTEROOT_AUTH_MODE: 'none' },
    stdio: 'pipe',
  });
  await new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('timeout')), 15000);
    const p = setInterval(async () => {
      try { const r = await fetch(`${BASE}/`); if (r.ok) { clearInterval(p); clearTimeout(t); resolve(); } } catch { /* wait */ }
    }, 200);
  });
  await fetch(`${BASE}/n/probe/note?path=test.md`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    body: '---\ntitle: T\n---\n\n' + Array.from({ length: 80 }, (_, i) => `line ${i + 1}`).join('\n') + '\n',
  });
});

test.afterAll(async () => {
  if (serverProc) serverProc.kill();
  if (dataDir) rmSync(dataDir, { recursive: true, force: true });
});

test('probe vim canvas geometry', async ({ page }) => {
  page.on('console', (m) => console.log('[browser]', m.type(), m.text()));
  await page.addInitScript(() => {
    localStorage.setItem('nr.helpSeen', '1');
    localStorage.setItem('nr.editorPref', 'vim');
  });
  await page.goto(BASE);
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', 'probe');
  await page.waitForSelector('.node');
  await page.click('.node');
  await expect(page.locator('#editbtn')).toBeVisible();

  const before = await page.evaluate(() => {
    const c = document.getElementById('vim-container');
    const ed = document.getElementById('editor');
    const content = document.getElementById('content');
    return {
      containerRect: c.getBoundingClientRect().toJSON(),
      editorRect: ed.getBoundingClientRect().toJSON(),
      contentRect: content.getBoundingClientRect().toJSON(),
    };
  });
  console.log('BEFORE EDIT', JSON.stringify(before, null, 1));

  await page.click('#editbtn');
  await expect(page.locator('#editormode')).toHaveText('vim', { timeout: 20000 });
  await expect(page.locator('#vim-container')).toBeVisible();
  // let vim boot and draw
  await page.waitForTimeout(6000);

  const after = await page.evaluate(() => {
    const cv = document.getElementById('vim-canvas');
    const c = document.getElementById('vim-container');
    const ed = document.getElementById('editor');
    const content = document.getElementById('content');
    const r = cv.getBoundingClientRect();
    return {
      canvasAttr: { w: cv.width, h: cv.height },
      canvasRect: { w: r.width, h: r.height, top: r.top, bottom: r.bottom },
      dpr: window.devicePixelRatio,
      containerRect: c.getBoundingClientRect().toJSON(),
      editorRect: ed.getBoundingClientRect().toJSON(),
      contentRect: content.getBoundingClientRect().toJSON(),
      editorScrollH: ed.scrollHeight,
      contentScrollH: content.scrollHeight,
      bannerH: document.getElementById('banner').getBoundingClientRect().height,
      toolbarH: document.querySelector('#editor .toolbar').getBoundingClientRect().height,
      loadingHidden: document.getElementById('vim-loading').hidden,
    };
  });
  console.log('AFTER EDIT', JSON.stringify(after, null, 1));
  await page.screenshot({ path: '/tmp/probe-vim.png', fullPage: false });

  const geom = () => page.evaluate(() => {
    const cv = document.getElementById('vim-canvas');
    const r = cv.getBoundingClientRect();
    return { attrW: cv.width, attrH: cv.height, cssW: Math.round(r.width), cssH: Math.round(r.height) };
  });

  // 1. Toggle the tree sidebar: container widens, no window resize fires.
  await page.click('#treebtn');
  await page.waitForTimeout(1200);
  console.log('AFTER TREE TOGGLE', JSON.stringify(await geom()));
  await page.screenshot({ path: '/tmp/probe-vim-tree.png' });

  // 2. Show a banner: container shrinks.
  await page.evaluate(() => { document.getElementById('banner').innerHTML = '<div class="warn">hello banner</div>'; });
  await page.waitForTimeout(1200);
  console.log('AFTER BANNER', JSON.stringify(await geom()));
  await page.screenshot({ path: '/tmp/probe-vim-banner.png' });

  // 3. Toggle the status bar off: content grows.
  await page.evaluate(() => { document.getElementById('statusbar').hidden = true; });
  await page.waitForTimeout(1200);
  console.log('AFTER STATUSBAR', JSON.stringify(await geom()));
});
