// E2E tests for noteroot vim mode.
// Run with: npx playwright test --config web/playwright.config.js
// Requires a built binary and a Chromium-based browser.

import { test, expect } from '@playwright/test';
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = 18100;
const BASE = `http://127.0.0.1:${PORT}`;
let serverProc = null;
let dataDir = null;

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'nr-e2e-'));
  const root = join(import.meta.dirname, '..', '..');
  const { execSync } = await import('node:child_process');

  // Create notebook directory directly (no server needed yet)
  execSync(`NOTEROOT_DATA_DIR=${dataDir} ${join(root, 'scripts/new-notebook.sh')} e2enote`, { cwd: root });

  // Start server
  serverProc = spawn(join(root, 'bin/noteroot'), [], {
    env: {
      ...process.env,
      NOTEROOT_DATA_DIR: dataDir,
      NOTEROOT_LISTEN: `127.0.0.1:${PORT}`,
      NOTEROOT_AUTH_MODE: 'none',
    },
    stdio: 'ignore',
  });

  // Wait for server to be ready by polling the endpoint
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
        /* not ready yet */
      }
    }, 200);
  });

  // Seed the note via API (server is now up)
  await fetch(`${BASE}/n/e2enote/note?path=test.md`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    body: '---\ntitle: Test Note\n---\n\nInitial content.\n',
  });
});

test.afterAll(async () => {
  if (serverProc) {
    serverProc.kill();
    serverProc = null;
  }
  if (dataDir) {
    rmSync(dataDir, { recursive: true, force: true });
    dataDir = null;
  }
});

// Skip the first-run welcome modal so it never covers the page under test.
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('nr.helpSeen', '1'));
});

test.describe('Page loads and navigation', () => {
  test('loads the notebook UI', async ({ page }) => {
    await page.goto(BASE);
    await expect(page.locator('.brand')).toContainText('noteroot');
    await expect(page.locator('#notebooks')).toBeVisible();
  });

  test('selects a note and displays content', async ({ page }) => {
    await page.goto(BASE);
    // Wait for notebook list to populate (option elements are hidden in
    // headless Chromium, so wait for the select to have any value)
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    // Select the e2enote notebook
    await page.selectOption('#notebooks', 'e2enote');
    // Wait for tree to load
    await page.waitForSelector('.node', { timeout: 10000 });
    // Click the test note
    await page.click('.node');
    // Verify note content rendered
    await expect(page.locator('#note')).toContainText('Initial content');
  });
});

test.describe('Editor mode detection', () => {
  test('shows vim mode on desktop viewport', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(BASE);
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'e2enote');
    await page.waitForSelector('.node');
    await page.click('.node');

    // Open editor
    await page.click('#editbtn');
    // Verify mode indicator
    await expect(page.locator('#editormode')).toHaveText('vim', { timeout: 10000 });
    // Verify vim container is visible
    await expect(page.locator('#vim-container')).toBeVisible({ timeout: 10000 });
    // Verify textarea is hidden
    await expect(page.locator('#editbody')).toBeHidden();
  });

  test('shows textarea mode on mobile viewport', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(BASE);
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'e2enote');
    await page.waitForSelector('.node');
    await page.click('.node');

    await page.click('#editbtn');
    await expect(page.locator('#editormode')).toHaveText('plain text');
    await expect(page.locator('#editbody')).toBeVisible();
    await expect(page.locator('#vim-container')).toBeHidden();
  });

  test('respects textarea preference on desktop', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    // Pre-seed localStorage preference
    await page.goto(BASE);
    await page.evaluate(() => localStorage.setItem('nr.editorPref', 'textarea'));
    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'e2enote');
    await page.waitForSelector('.node');
    await page.click('.node');

    await page.click('#editbtn');
    await expect(page.locator('#editormode')).toHaveText('plain text');
    await expect(page.locator('#editbody')).toBeVisible();
  });

  test('respects vim preference on narrow viewport', async ({ page }) => {
    await page.setViewportSize({ width: 500, height: 800 });
    await page.goto(BASE);
    await page.evaluate(() => localStorage.setItem('nr.editorPref', 'vim'));
    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'e2enote');
    await page.waitForSelector('.node');
    await page.click('.node');

    await page.click('#editbtn');
    // Should attempt vim even on narrow viewport
    await expect(page.locator('#editormode')).toHaveText('vim', { timeout: 10000 });
  });
});

test.describe('Textarea editor workflow', () => {
  test('edits and saves via textarea', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(BASE);
    await page.evaluate(() => localStorage.setItem('nr.editorPref', 'textarea'));
    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'e2enote');
    await page.waitForSelector('.node');
    await page.click('.node');

    await page.click('#editbtn');
    await expect(page.locator('#editbody')).toBeVisible();

    // Edit content
    await page.fill('#editbody', 'Edited content via textarea.');
    await page.click('#savebtn');
    await expect(page.locator('#savestate')).toContainText('Saved', { timeout: 5000 });

    // Close editor
    await page.click('#closebtn');
    await expect(page.locator('#editor')).toBeHidden();

    // Re-open and verify content persisted (server may add frontmatter)
    await page.click('#editbtn');
    await expect(page.locator('#editbody')).toHaveValue(/Edited content via textarea\./);
  });

  test('close button returns to note view', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(BASE);
    await page.evaluate(() => localStorage.setItem('nr.editorPref', 'textarea'));
    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'e2enote');
    await page.waitForSelector('.node');
    await page.click('.node');
    await page.click('#editbtn');
    await expect(page.locator('#editbody')).toBeVisible();

    await page.click('#closebtn');
    await expect(page.locator('#editor')).toBeHidden();
    await expect(page.locator('#note')).toBeVisible();
  });
});

test.describe('Settings modal', () => {
  test('opens and closes settings modal', async ({ page }) => {
    await page.goto(BASE);
    await page.click('#settingsbtn');
    await expect(page.locator('#settings-modal')).toBeVisible();

    await page.click('#settings-close');
    await expect(page.locator('#settings-modal')).toBeHidden();
  });

  test('closes on Escape key', async ({ page }) => {
    await page.goto(BASE);
    await page.click('#settingsbtn');
    await expect(page.locator('#settings-modal')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(page.locator('#settings-modal')).toBeHidden();
  });

  test('shows circuit breaker status', async ({ page }) => {
    await page.goto(BASE);
    await page.click('#settingsbtn');
    await expect(page.locator('#cb-status')).toHaveText('Enabled');
    await expect(page.locator('#cb-failures')).toHaveText('0');
  });

  test('saves editor preference', async ({ page }) => {
    await page.goto(BASE);
    await page.click('#settingsbtn');

    // Select textarea preference
    await page.click('input[name="editor-mode"][value="textarea"]');
    // Verify it was persisted
    const pref = await page.evaluate(() => localStorage.getItem('nr.editorPref'));
    expect(pref).toBe('textarea');

    // Close and reopen - should load saved preference
    await page.click('#settings-close');
    await page.click('#settingsbtn');
    await expect(page.locator('input[name="editor-mode"][value="textarea"]')).toBeChecked();
  });

  test('reset circuit breaker clears failures', async ({ page }) => {
    // Seed a disabled circuit breaker
    await page.goto(BASE);
    await page.evaluate(() => {
      localStorage.setItem('nr.vimMode', JSON.stringify({
        enabled: false,
        failures: 3,
        lastFailure: Date.now(),
      }));
    });
    await page.reload();
    await page.click('#settingsbtn');

    await expect(page.locator('#cb-status')).toHaveText('Disabled');
    await expect(page.locator('#cb-failures')).toHaveText('3');

    // Handle the alert dialog
    page.on('dialog', (dialog) => dialog.accept());
    await page.click('#reset-vim');

    await expect(page.locator('#cb-status')).toHaveText('Enabled');
    await expect(page.locator('#cb-failures')).toHaveText('0');
  });
});

test.describe('Circuit breaker fallback', () => {
  test('disabled circuit breaker forces textarea on desktop', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(BASE);
    await page.evaluate(() => {
      localStorage.setItem('nr.vimMode', JSON.stringify({
        enabled: false,
        failures: 3,
        lastFailure: Date.now(),
      }));
      localStorage.removeItem('nr.editorPref'); // default auto
    });
    await page.reload();
    await page.waitForFunction(() => {
      const sel = document.getElementById('notebooks');
      return sel && sel.options.length > 0;
    }, { timeout: 10000 });
    await page.selectOption('#notebooks', 'e2enote');
    await page.waitForSelector('.node');
    await page.click('.node');

    await page.click('#editbtn');
    // Should fall back to textarea because circuit breaker is open
    await expect(page.locator('#editormode')).toHaveText('plain text');
    await expect(page.locator('#editbody')).toBeVisible();
  });
});
