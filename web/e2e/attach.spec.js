// E2E: the editor's Attach menu (upload, URL link, note link), inline
// video playback with byte ranges, preferences export/import and the
// build version badge, against the real binary in a real browser.
// Run with: npx playwright test --config web/playwright.config.js e2e/attach.spec.js
// Requires `make build`.

import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = 18500;
const BASE = `http://127.0.0.1:${PORT}`;
const SLUG = 'attachnb';
const ROOT = join(import.meta.dirname, '..', '..');
const BIN = join(ROOT, 'bin/noteroot');
let serverProc = null;
let dataDir = null;
let scratchDir = null;

async function put(path, body) {
  const r = await fetch(`${BASE}/n/${SLUG}/note?path=${encodeURIComponent(path)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    body,
  });
  if (!r.ok) throw new Error(`seed ${path}: ${r.status}`);
}

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'nr-e2e-attach-'));
  scratchDir = mkdtempSync(join(tmpdir(), 'nr-e2e-attach-clip-'));
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
  await put('start.md', '# Start\n\nAttach here.\n');
  await put('links.md', '# Links\n\nSee:\n');
  await put('uploads.md', '# Uploads\n\nFiles:\n');
  await put('media.md', '# Media\n\nClips:\n');
  await put('vim.md', '# Vim note\n\nbody\n');
  await put('ref/deep-dive.md', '# Deep dive\n\nDetails live here.\n');
});

test.afterAll(async () => {
  if (serverProc) serverProc.kill();
  if (dataDir) rmSync(dataDir, { recursive: true, force: true });
  if (scratchDir) rmSync(scratchDir, { recursive: true, force: true });
});

// Plain-text editor unless a test opts in to Vim; no welcome modal.
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

async function openNote(page, path, text) {
  await openApp(page);
  await page.locator(`#tree [data-path="${path}"]`).click();
  await expect(page.locator('#note')).toContainText(text);
}

// Open the plain editor with the caret at the end of the note.
async function editAtEnd(page) {
  await page.click('#editbtn');
  await expect(page.locator('#editormode')).toHaveText('plain text');
  await page.locator('#editbody').press('Control+End');
}

async function attachMenu(page, item) {
  await page.click('#attachbtn');
  await page.locator('.ctx-menu .ctx-item', { hasText: item }).click();
}

async function openSettings(page, section) {
  await page.click('#settingsbtn');
  await page.click(`#settings-modal .settings-nav button[data-target="${section}"]`);
}

// A one-second VP8 clip: Playwright's Chromium has no H.264 decoder.
function makeClip() {
  const out = join(scratchDir, 'clip.webm');
  try {
    execSync(
      `ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc=duration=1:size=160x120:rate=10 -c:v libvpx -b:v 200k ${out}`,
      { stdio: 'ignore' },
    );
    return out;
  } catch {
    return null;
  }
}

test.describe.configure({ mode: 'serial' });

test('the top bar and About show the server build version', async ({ page }) => {
  const { version } = await (await fetch(`${BASE}/healthz`)).json();
  expect(version).toBeTruthy();
  expect(execSync(`${BIN} --version`).toString().trim()).toBe(version);

  await openApp(page);
  await expect(page.locator('#app-version')).toBeVisible();
  await expect(page.locator('#app-version')).toHaveText(version);
  await openSettings(page, 'set-about');
  await expect(page.locator('#about-version')).toHaveText(version);
});

test('Insert link (URL) validates, previews and inserts a markdown link', async ({ page }) => {
  await openNote(page, 'start.md', 'Attach here.');
  await expect(page.locator('#attachbtn')).toBeHidden(); // read mode has no attach
  await editAtEnd(page);
  await expect(page.locator('#attachbtn')).toBeVisible();

  await attachMenu(page, 'Insert link');
  const dlg = page.locator('.dialog');
  await dlg.locator('[name=url]').fill('javascript:alert(1)');
  await dlg.locator('button[type=submit]').click();
  await expect(dlg.locator('.dialog-error')).toContainText('http, https or mailto');

  await dlg.locator('[name=url]').fill('https://example.com/a b');
  await expect(dlg.locator('.dialog-error')).toBeHidden();
  await dlg.locator('[name=text]').fill('Example');
  await expect(dlg.locator('.dialog-preview')).toHaveText('[Example](https://example.com/a%20b)');
  await dlg.locator('button[type=submit]').click();
  await expect(dlg).toHaveCount(0);
  await expect(page.locator('#editbody')).toHaveValue(/Attach here\.\n\[Example\]\(https:\/\/example\.com\/a%20b\)/);

  await page.click('#savebtn');
  await expect(page.locator('#savestate')).toHaveText('Saved.');
  await page.click('#closebtn');
  await expect(page.locator('#note a[href="https://example.com/a%20b"]')).toHaveText('Example');
});

test('Link to note filters, picks and inserts a working wikilink', async ({ page }) => {
  await openNote(page, 'links.md', 'See:');
  await editAtEnd(page);

  await attachMenu(page, 'Link to note');
  const dlg = page.locator('.dialog');
  await expect(dlg.locator('.attach-note-item', { hasText: 'ref/deep-dive.md' })).toBeVisible();
  // A unique file name resolves on its own...
  await dlg.locator('[name=target]').fill('deep-dive');
  await expect(dlg.locator('.dialog-preview')).toHaveText('[[ref/deep-dive.md]]');
  // ...a fragment only filters the list until a row is picked.
  await dlg.locator('[name=target]').fill('deep');
  await expect(dlg.locator('.attach-note-item')).toHaveText(['ref/deep-dive.md']);
  await expect(dlg.locator('.dialog-preview')).toBeHidden();
  await dlg.locator('.attach-note-item').click();
  await expect(dlg.locator('[name=target]')).toHaveValue('ref/deep-dive.md');
  await dlg.locator('[name=label]').fill('Dive in');
  await expect(dlg.locator('.dialog-preview')).toHaveText('[[ref/deep-dive.md|Dive in]]');
  await dlg.locator('button[type=submit]').click();
  await expect(dlg).toHaveCount(0);
  await expect(page.locator('#editbody')).toHaveValue(/See:\n\[\[ref\/deep-dive\.md\|Dive in\]\]/);

  await page.click('#savebtn');
  await expect(page.locator('#savestate')).toHaveText('Saved.');
  await page.click('#closebtn');
  const link = page.locator('#note p a.wikilink[data-path="ref/deep-dive.md"]');
  await expect(link).toHaveText('Dive in');
  // The index picked it up too: it is listed under Outgoing links, in
  // the note panel beside the reading column.
  await expect(page.locator('#readpane #rp-out li a.wikilink[data-path="ref/deep-dive.md"]')).toBeVisible();
  await link.click();
  await expect(page.locator('#note')).toContainText('Details live here.');
});

test('Upload file… inserts a link to the stored blob', async ({ page }) => {
  await openNote(page, 'uploads.md', 'Files:');
  await editAtEnd(page);

  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    attachMenu(page, 'Upload file'),
  ]);
  expect(chooser.isMultiple()).toBe(true);
  await chooser.setFiles({ name: 'hello.txt', mimeType: 'text/plain', buffer: Buffer.from('attached via the menu') });
  await expect(page.locator('#editbody')).toHaveValue(
    new RegExp(`Files:\\n\\[hello\\.txt\\]\\(/n/${SLUG}/resources/[a-f0-9]{64}\\)`),
    { timeout: 15000 },
  );
  await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 }); // autosave

  await page.click('#closebtn');
  const link = page.locator('#note a', { hasText: 'hello.txt' });
  await expect(link).toHaveAttribute('href', new RegExp(`^/n/${SLUG}/resources/[a-f0-9]{64}$`));
  const res = await page.request.get(`${BASE}${await link.getAttribute('href')}`);
  expect(res.status()).toBe(200);
  expect(await res.text()).toBe('attached via the menu');
});

test('an uploaded video plays inline with a poster and serves byte ranges', async ({ page }) => {
  const clip = makeClip();
  test.skip(!clip, 'ffmpeg with libvpx is not installed');

  await openNote(page, 'media.md', 'Clips:');
  await editAtEnd(page);
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    attachMenu(page, 'Upload file'),
  ]);
  await chooser.setFiles(clip);
  await expect(page.locator('#editbody')).toHaveValue(
    new RegExp(`\\[clip\\.webm\\]\\(/n/${SLUG}/resources/[a-f0-9]{64}\\)`),
    { timeout: 15000 },
  );
  await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });
  await page.click('#closebtn');

  const video = page.locator('#note video');
  await expect(video).toBeVisible();
  await expect(video).toHaveAttribute('controls', '');
  const src = await video.getAttribute('src');
  expect(src).toMatch(new RegExp(`^/n/${SLUG}/resources/[a-f0-9]{64}$`));
  await expect(video).toHaveAttribute('poster', `${src}/poster`);

  // The browser loads the metadata and can seek (ranges are honoured).
  await expect.poll(() => video.evaluate((v) => v.readyState), { timeout: 15000 }).toBeGreaterThanOrEqual(1);
  const media = await video.evaluate((v) => ({
    error: v.error ? v.error.code : null,
    duration: v.duration,
    seekableEnd: v.seekable.length ? v.seekable.end(0) : 0,
  }));
  expect(media.error).toBeNull();
  expect(media.duration).toBeGreaterThan(0.5);
  expect(media.seekableEnd).toBeGreaterThan(0.5);

  const part = await page.request.get(`${BASE}${src}`, { headers: { Range: 'bytes=0-99' } });
  expect(part.status()).toBe(206);
  expect(part.headers()['content-range']).toMatch(/^bytes 0-99\/\d+$/);
  expect((await part.body()).length).toBe(100);

  const poster = await page.request.get(`${BASE}${src}/poster`);
  expect(poster.status()).toBe(200);
  expect(poster.headers()['content-type']).toBe('image/jpeg');
});

// A one-second MP3 clip, for the audio-player tests below.
function makeAudioClip() {
  const out = join(scratchDir, 'clip.mp3');
  try {
    execSync(
      `ffmpeg -hide_banner -loglevel error -y -f lavfi -i "sine=frequency=440:duration=1" -c:a libmp3lame -b:a 64k ${out}`,
      { stdio: 'ignore' },
    );
    return out;
  } catch {
    return null;
  }
}

// A plain 100x100 PNG, big enough to click reliably (unlike a 1x1 test
// fixture, which real images never are).
function makeTestImage() {
  const out = join(scratchDir, 'swatch.png');
  try {
    execSync(
      `ffmpeg -hide_banner -loglevel error -y -f lavfi -i "color=c=red:s=100x100" -frames:v 1 ${out}`,
      { stdio: 'ignore' },
    );
    return out;
  } catch {
    return null;
  }
}

test('an uploaded audio file plays inline, even when the browser reports no MIME type for it', async ({ page }) => {
  const clip = makeAudioClip();
  test.skip(!clip, 'ffmpeg with libmp3lame is not installed');

  await openNote(page, 'media.md', 'Clips:');
  await editAtEnd(page);
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    attachMenu(page, 'Upload file'),
  ]);
  // "application/octet-stream" is exactly what app.js's uploadResource()
  // sends when file.type comes back empty — some browsers don't map
  // every audio extension to a MIME type client-side. The server must
  // still recognize it as audio from the filename (see resources.go's
  // upload handler), or it renders as a dead download link instead of
  // a player.
  await chooser.setFiles({ name: 'clip.mp3', mimeType: 'application/octet-stream', buffer: readFileSync(clip) });
  await expect(page.locator('#editbody')).toHaveValue(
    new RegExp(`\\[clip\\.mp3\\]\\(/n/${SLUG}/resources/[a-f0-9]{64}\\)`),
    { timeout: 15000 },
  );
  await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });
  await page.click('#closebtn');

  const audio = page.locator('#note audio');
  await expect(audio).toBeVisible({ timeout: 15000 });
  await expect(audio).toHaveAttribute('controls', '');
  const src = await audio.getAttribute('src');
  expect(src).toMatch(new RegExp(`^/n/${SLUG}/resources/[a-f0-9]{64}$`));
  await expect.poll(() => audio.evaluate((a) => a.readyState), { timeout: 15000 }).toBeGreaterThanOrEqual(1);

  const played = await audio.evaluate((a) => a.play().then(() => {
    return new Promise((resolve) => setTimeout(() => resolve({ paused: a.paused, currentTime: a.currentTime }), 500));
  }));
  expect(played.paused).toBe(false);
  expect(played.currentTime).toBeGreaterThan(0);

  const res = await page.request.get(`${BASE}${src}`);
  expect(res.headers()['content-type']).toBe('audio/mpeg'); // not application/octet-stream
});

test('clicking an image in a note opens it full-size in the lightbox, and Escape closes it', async ({ page }) => {
  const swatch = makeTestImage();
  test.skip(!swatch, 'ffmpeg is not installed');

  await put('gallery.md', '# Gallery\n\nPicture:\n');
  await openNote(page, 'gallery.md', 'Picture:');
  await editAtEnd(page);
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    attachMenu(page, 'Upload file'),
  ]);
  await chooser.setFiles(swatch);
  await expect(page.locator('#editbody')).toHaveValue(/swatch\.png/, { timeout: 15000 });
  await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });
  await page.click('#closebtn');

  const img = page.locator('#note img');
  await expect(img).toBeVisible({ timeout: 15000 });
  const thumbSrc = await img.getAttribute('src');
  expect(thumbSrc).toMatch(/\/thumb\?w=\d+&h=\d+$/); // rewritten to a thumbnail

  await expect(page.locator('#img-lightbox')).toBeHidden();
  await img.click();
  await expect(page.locator('#img-lightbox')).toBeVisible();
  const full = await page.locator('#img-lightbox-img').getAttribute('src');
  expect(new URL(full).pathname).toBe(thumbSrc.replace(/\/thumb\?.*$/, '')); // original, not the thumbnail
  const res = await page.request.get(full);
  expect(res.status()).toBe(200);
  expect(res.headers()['content-type']).toBe('image/png');

  await page.keyboard.press('Escape');
  await expect(page.locator('#img-lightbox')).toBeHidden();
});

test('an image thumbnail in the Attachments manager also opens the lightbox', async ({ page }) => {
  const swatch = makeTestImage();
  test.skip(!swatch, 'ffmpeg is not installed');

  await openNote(page, 'media.md', 'Clips:');
  await editAtEnd(page);
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    attachMenu(page, 'Upload file'),
  ]);
  await chooser.setFiles({ name: 'manager-swatch.png', mimeType: 'image/png', buffer: readFileSync(swatch) });
  await expect(page.locator('#editbody')).toHaveValue(/manager-swatch\.png/, { timeout: 15000 });
  await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });
  await page.click('#closebtn');

  await openSettings(page, 'set-attachments');
  await page.click('#attachments-manage');
  const thumbImg = page.locator('.attach-thumb img').first();
  await expect(thumbImg).toBeVisible({ timeout: 15000 });
  await expect(page.locator('#img-lightbox')).toBeHidden();
  await thumbImg.click();
  await expect(page.locator('#img-lightbox')).toBeVisible();
});


// Extra coverage for #79/#102 (clicking an image shows only its alt
// text / filename instead of the full-size picture): the split pane,
// multiple images in one note, and a fresh page load all exercise
// paths not covered by the single-image main-pane test above.
test('the image lightbox also works in the split pane, after a reload, and with several images in one note', async ({ page }) => {
  await put('gallery2.md', '# Gallery 2\n\nOne:\n\nTwo:\n');
  await openNote(page, 'gallery2.md', 'One:');
  await editAtEnd(page);
  for (const name of ['big.png', 'small.png']) {
    const data = Buffer.from(
      name === 'big.png'
        ? 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII='
        : 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfz0AEYBxVSF+FABJQ308ixENBAAAAAElFTkSuQmCC',
      'base64',
    );
    const [chooser] = await Promise.all([
      page.waitForEvent('filechooser'),
      attachMenu(page, 'Upload file'),
    ]);
    await chooser.setFiles({ name, mimeType: 'image/png', buffer: data });
    await expect(page.locator('#editbody')).toHaveValue(new RegExp(name.replace('.', '\\.')), { timeout: 15000 });
    await page.locator('#editbody').press('Control+End');
  }
  await page.click('#savebtn');
  await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });
  await page.click('#closebtn');

  const checkFullyLoads = async (img) => img.evaluate((el) => new Promise((resolve) => {
    const check = () => resolve({ naturalWidth: el.naturalWidth, complete: el.complete });
    if (el.complete) check();
    else { el.addEventListener('load', check); el.addEventListener('error', check); setTimeout(check, 5000); }
  }));

  // Several images in the same note: each click opens its own full-size
  // image, not a stale reference to a previously-clicked one.
  const imgs = page.locator('#note img');
  await expect(imgs).toHaveCount(2, { timeout: 15000 });
  for (let i = 0; i < 2; i += 1) {
    await imgs.nth(i).click();
    await expect(page.locator('#img-lightbox')).toBeVisible();
    const result = await checkFullyLoads(page.locator('#img-lightbox-img'));
    expect(result.naturalWidth).toBeGreaterThan(0); // not a broken-image placeholder
    await page.locator('#img-lightbox-close').click();
    await expect(page.locator('#img-lightbox')).toBeHidden();
  }

  // A fresh page load (closer to "open a note that already has an
  // image in it" than upload-then-immediately-click) behaves the same.
  await page.reload();
  await page.waitForFunction(() => document.getElementById('notebooks')?.options.length > 0);
  await page.selectOption('#notebooks', SLUG);
  await page.waitForFunction(() => document.getElementById('tree-status')?.textContent !== '');
  await page.locator('#tree [data-path="gallery2.md"]').click();
  await expect(imgs.first()).toBeVisible({ timeout: 15000 });
  await imgs.first().click();
  await expect(page.locator('#img-lightbox')).toBeVisible();
  expect((await checkFullyLoads(page.locator('#img-lightbox-img'))).naturalWidth).toBeGreaterThan(0);
  await page.keyboard.press('Escape');

  // The split pane gets its own working lightbox too.
  await page.locator('#tree [data-path="gallery2.md"]').click({ button: 'right' });
  await page.locator('.ctx-menu .ctx-item', { hasText: 'Open in split pane' }).click();
  const splitImg = page.locator('#split-note img').first();
  await expect(splitImg).toBeVisible({ timeout: 15000 });
  await splitImg.click();
  await expect(page.locator('#img-lightbox')).toBeVisible();
  expect((await checkFullyLoads(page.locator('#img-lightbox-img'))).naturalWidth).toBeGreaterThan(0);
});

test('Vim: an attached link lands in the buffer and the keys go back to Vim', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('nr.editorPref', 'vim'));
  await openNote(page, 'vim.md', 'Vim note');
  await page.click('#editbtn');
  await expect(page.locator('#editormode')).toHaveText('vim', { timeout: 15000 });
  await expect(page.locator('#vim-loading')).toBeHidden({ timeout: 15000 });

  await attachMenu(page, 'Insert link');
  const dlg = page.locator('.dialog');
  await dlg.locator('[name=url]').fill('https://example.org/from-vim');
  await dlg.locator('[name=text]').fill('From Vim');
  await dlg.locator('button[type=submit]').click();
  await expect(dlg).toHaveCount(0);
  // The dialog took the focus; the insert hands it back to Vim.
  await expect.poll(() => page.evaluate(() => document.activeElement?.id)).toBe('vim-input');
  await expect(page.locator('#banner .warn')).toHaveCount(0);

  await page.keyboard.type(':w');
  await page.keyboard.press('Enter');
  await expect(page.locator('#savestate')).toHaveText('Saved.', { timeout: 15000 });
  await page.click('#closebtn');
  await expect(page.locator('#note a[href="https://example.org/from-vim"]')).toHaveText('From Vim');
});

test('preferences export to a file and import into a fresh browser', async ({ page, browser }) => {
  await page.addInitScript(() => localStorage.setItem('nr.theme', 'dark'));
  await openApp(page);
  await openSettings(page, 'set-data');
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.click('#export-prefs'),
  ]);
  expect(download.suggestedFilename()).toMatch(/^noteroot-prefs-\d{4}-\d{2}-\d{2}\.json$/);
  const file = await download.path();
  const data = JSON.parse(readFileSync(file, 'utf8'));
  expect(data).toMatchObject({ app: 'noteroot', kind: 'preferences', version: 1 });
  expect(data.prefs['nr.theme']).toBe('dark');
  expect(data.prefs['nr.editorPref']).toBe('textarea');

  const fresh = await browser.newContext();
  try {
    const other = await fresh.newPage();
    await other.addInitScript(() => localStorage.setItem('nr.helpSeen', '1'));
    await openApp(other);
    await expect(other.locator('html')).not.toHaveAttribute('data-theme', 'dark');
    await openSettings(other, 'set-data');
    const [chooser] = await Promise.all([
      other.waitForEvent('filechooser'),
      other.click('#import-prefs'),
    ]);
    await chooser.setFiles(file);
    await expect(other.locator('#toast')).toContainText(/Imported \d+ preferences?/);
    await expect(other.locator('html')).toHaveAttribute('data-theme', 'dark'); // applied without reload
    expect(await other.evaluate(() => localStorage.getItem('nr.editorPref'))).toBe('textarea');
  } finally {
    await fresh.close();
  }
});
