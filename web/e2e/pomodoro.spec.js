// Pomodoro widget E2E: layout (no-overlap), toggle visibility, phase buttons,
// running indicator.  Plugin is pre-enabled via localStorage seed so it mounts
// as soon as the notebook opens.
// Run with: npx playwright test --config web/playwright.config.js e2e/pomodoro.spec.js
// Requires `make build`.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = 18800;
const BASE = `http://127.0.0.1:${PORT}`;
const SLUG = 'pomotest';
let serverProc = null;
let dataDir = null;

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'nr-pomo-'));
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
          clearInterval(poll); clearTimeout(timeout); resolve();
        }
      } catch { /* not ready */ }
    }, 200);
  });
});

test.afterAll(() => {
  if (serverProc) serverProc.kill();
  if (dataDir) rmSync(dataDir, { recursive: true, force: true });
});

test.beforeEach(async ({ page }) => {
  await page.addInitScript((slug) => {
    localStorage.clear();
    localStorage.setItem('nr.helpSeen', '1');
    localStorage.setItem('nr.editorPref', 'textarea');
    // enable the pomodoro plugin for this notebook
    localStorage.setItem(`noteroot_uiplugins_${slug}`, JSON.stringify({ pomodoro: true }));
  }, SLUG);
});

async function openApp(page) {
  await page.goto(BASE);
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', SLUG);
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
}

test.describe.configure({ mode: 'serial' });

test('🍅 toggle button appears in the top bar after plugin enable', async ({ page }) => {
  await openApp(page);
  await expect(page.locator('#pomo-toggle')).toBeVisible();
  await expect(page.locator('#pomo-toggle')).toHaveText('🍅');
});

test('clicking the toggle shows and hides the widget', async ({ page }) => {
  await openApp(page);
  const toggle = page.locator('#pomo-toggle');
  const widget = page.locator('#pomo-widget');

  await expect(widget).toBeVisible();
  await toggle.click();
  await expect(widget).toBeHidden();
  await toggle.click();
  await expect(widget).toBeVisible();
});

test('phase row, countdown, and controls do not overlap each other', async ({ page }) => {
  await openApp(page);
  const phases  = page.locator('.pomo-phases');
  const display = page.locator('.pomo-display');
  const ctrls   = page.locator('.pomo-controls');

  await expect(phases).toBeVisible();
  await expect(display).toBeVisible();
  await expect(ctrls).toBeVisible();

  const [pb, db, cb] = await Promise.all([
    phases.boundingBox(),
    display.boundingBox(),
    ctrls.boundingBox(),
  ]);

  // phases row ends before the countdown starts (with 2 px slack)
  expect(pb.y + pb.height).toBeLessThan(db.y + 2);
  // countdown ends before the controls start (with 2 px slack)
  expect(db.y + db.height).toBeLessThan(cb.y + 2);
});

test('phase buttons carry the short labels Work / Short / Long', async ({ page }) => {
  await openApp(page);
  const btns = page.locator('.pomo-phase-btn');
  await expect(btns).toHaveCount(3);
  await expect(btns.nth(0)).toHaveText('Work');
  await expect(btns.nth(1)).toHaveText('Short');
  await expect(btns.nth(2)).toHaveText('Long');
});

test('Start marks the toggle as running; Reset clears it', async ({ page }) => {
  await openApp(page);
  const toggle = page.locator('#pomo-toggle');

  await expect(toggle).not.toHaveClass(/running/);

  await page.locator('.pomo-start').click();
  await expect(toggle).toHaveClass(/running/);

  await page.locator('.pomo-reset').click();
  await expect(toggle).not.toHaveClass(/running/);
});
