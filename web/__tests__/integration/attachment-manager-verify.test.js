/**
 * Integration test for #106: the attachment manager's row list, its
 * per-attachment verify status icons (driven by Settings → Attachments
 * → Verify…'s progress-tracked scan), and the references panel that
 * shows every referencing note — even at 50+ — without truncating,
 * inside its own scrollable panel rather than growing the whole list.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

const OK_SHA = 'a'.repeat(64);
const MISSING_SHA = 'b'.repeat(64);
const MANY_REFS_SHA = 'c'.repeat(64);
const MANY_REFS = Array.from({ length: 50 }, (_, i) => `note${i}.md`);

const respond = (status, body) => ({
  ok: status >= 200 && status < 300,
  status,
  statusText: String(status),
  headers: { get: () => '' },
  json: async () => body,
  text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
});

async function fakeFetch(url) {
  const u = new URL(url, 'http://x');
  const route = `GET ${u.pathname}`;
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, { name: '', path: '', isDir: true, children: [
      { name: 'x.md', path: 'x.md', isDir: false, size: 1 },
    ] });
    case 'GET /n/nb/resources': return respond(200, { resources: [
      { sha: OK_SHA, filename: 'ok.png', mime: 'image/png', size: 10 },
      { sha: MISSING_SHA, filename: 'gone.png', mime: 'image/png', size: 20 },
      { sha: MANY_REFS_SHA, filename: 'popular.png', mime: 'image/png', size: 30 },
    ] });
    case `GET /n/nb/resources/${OK_SHA}/verify`: return respond(200, { sha: OK_SHA, status: 'ok', size: 10 });
    case `GET /n/nb/resources/${MISSING_SHA}/verify`: return respond(200, { sha: MISSING_SHA, status: 'missing', size: 0 });
    case `GET /n/nb/resources/${MANY_REFS_SHA}/verify`: return respond(200, { sha: MANY_REFS_SHA, status: 'ok', size: 30 });
    case `GET /n/nb/resources/${MANY_REFS_SHA}/refs`: return respond(200, { refs: MANY_REFS });
    default: return respond(200, {});
  }
}

const $ = (id) => document.getElementById(id);
const qa = (sel) => [...document.querySelectorAll(sel)];
const sleep = (ms = 0) => new Promise((r) => setTimeout(r, ms));
async function until(cond, label = 'condition') {
  for (let i = 0; i < 400; i += 1) {
    if (cond()) return;
    await sleep(5);
  }
  throw new Error(`timed out waiting for ${label}`);
}

beforeAll(async () => {
  document.documentElement.innerHTML = html.replace(/<script[\s\S]*?<\/script>/g, '');
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (media) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} }),
  });
  global.ResizeObserver = window.ResizeObserver = class {
    observe() {} unobserve() {} disconnect() {}
  };
  globalThis.fetch = fakeFetch;
  window.fetch = fakeFetch;
  await import('../../app.js');
  await until(() => $('tree').querySelectorAll('.node').length > 0, 'initial tree');
});

function rowFor(sha) {
  return $('attachments-grid').querySelector(`.attach-row[data-sha="${sha}"]`);
}

test('the manager lists attachments as rows, unverified by default', async () => {
  $('settingsbtn').click();
  $('attachments-manage').click();
  await until(() => !$('attachments-modal').hidden, 'attachments modal open');
  await until(() => qa('.attach-row').length === 3, 'rows loaded');

  for (const sha of [OK_SHA, MISSING_SHA, MANY_REFS_SHA]) {
    const status = rowFor(sha).querySelector('.attach-status');
    expect(status.classList.contains('unknown')).toBe(true);
    expect(status.title).toMatch(/not yet verified/i);
  }
});

test('Verify… scans every attachment, drives the progress bar, and updates status icons', async () => {
  $('attachments-close').click();
  $('settingsbtn').click();
  await until(() => !$('settings-modal').hidden, 'settings open');

  const progress = $('attachments-verify-progress');
  expect(progress.hidden).toBe(true);
  $('attachments-verify').click();
  await until(() => !progress.hidden, 'progress shown');
  await until(() => $('attachments-verify-text').textContent === '3/3 (100%)', 'verify complete');
  expect($('attachments-verify-bar').style.width).toBe('100%');

  await until(() => $('toast').textContent.includes('1 with issues'), 'summary toast');

  $('attachments-manage').click();
  await until(() => !$('attachments-modal').hidden, 'attachments modal reopened');
  await until(() => qa('.attach-row').length === 3, 'rows loaded');

  const okStatus = rowFor(OK_SHA).querySelector('.attach-status');
  expect(okStatus.classList.contains('ok')).toBe(true);
  expect(okStatus.title).toMatch(/verified/i);

  const missingStatus = rowFor(MISSING_SHA).querySelector('.attach-status');
  expect(missingStatus.classList.contains('bad')).toBe(true);
  expect(missingStatus.title).toMatch(/missing/i);
});

test('tapping a bad status icon toggles an inline hint (mobile has no hover)', async () => {
  const row = rowFor(MISSING_SHA);
  const status = row.querySelector('.attach-status');
  const hint = row.querySelector('.attach-status-hint');
  expect(hint.classList.contains('shown')).toBe(false);
  status.click();
  expect(hint.classList.contains('shown')).toBe(true);
  status.click();
  expect(hint.classList.contains('shown')).toBe(false);
});

test('References shows every referencing note — even 50 — without truncation, in its own scrollable panel', async () => {
  const row = rowFor(MANY_REFS_SHA);
  const refsBtn = [...row.querySelectorAll('.attach-actions button')].find((b) => b.textContent === 'References');
  refsBtn.click();
  await until(() => row.querySelector('.attach-refs-list'), 'refs panel shown');

  const panel = row.querySelector('.attach-refs-list');
  expect(panel.querySelectorAll('a')).toHaveLength(50);
  expect(panel.textContent).toContain('Used in 50 notes');
  // Bounded height so one heavily-referenced row cannot dwarf the rest
  // of the list — set via CSS (jsdom doesn't compute layout, so this
  // just confirms the bounding class is the one actually applied).
  expect(panel.className).toBe('attach-refs-list');

  expect(refsBtn.textContent).toBe('Hide references');
  refsBtn.click();
  expect(row.querySelector('.attach-refs-list')).toBeNull();
  expect(refsBtn.textContent).toBe('References');
});
