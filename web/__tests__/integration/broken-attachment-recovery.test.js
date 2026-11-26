/**
 * Integration test for #106: when a note's own attachment fails to
 * load, show an inline recovery card (not a dead image) offering a
 * link into the attachment manager, pre-filtered to the broken sha,
 * and a "Replace…" file picker that uploads a new file and repoints
 * this note's link at it.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

const OLD_SHA = 'a'.repeat(64);
const NEW_SHA = 'b'.repeat(64);
// Complete frontmatter + an existing H1 so ensureFrontmatter's
// fast-path returns synchronously (no save/re-render round trip to
// race against) — this test is about the broken-image recovery UI,
// not the frontmatter-normalization flow.
const NOTE_SRC = `---
id: ${'0'.repeat(28)}
title: Broken
created: 2026-01-01T00:00:00Z
updated: 2026-01-01T00:00:00Z
---
# Broken

![broken](/n/nb/resources/${OLD_SHA})
`;
const NOTE_HTML = `<p><img src="/n/nb/resources/${OLD_SHA}" alt="broken"></p>`;

let noteSrc = NOTE_SRC;
let noteSha = 's1';

const respond = (status, body) => ({
  ok: status >= 200 && status < 300,
  status,
  statusText: String(status),
  headers: { get: (n) => (n === 'ETag' ? `"${noteSha}"` : '') },
  json: async () => body,
  text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
});

async function fakeFetch(url, init = {}) {
  const u = new URL(url, 'http://x');
  const method = (init.method || 'GET').toUpperCase();
  const route = `${method} ${u.pathname}`;
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, { name: '', path: '', isDir: true, children: [
      { name: 'broken.md', path: 'broken.md', isDir: false, size: 1 },
      { name: 'external.md', path: 'external.md', isDir: false, size: 1 },
    ] });
    case 'GET /n/nb/note':
      if (u.searchParams.get('path') === 'external.md') {
        return respond(200, {
          path: 'external.md',
          html: '<p><img src="https://example.com/pic.png" alt="ext"></p>',
          sha: 'ext1',
          title: 'external',
        });
      }
      return respond(200, { path: 'broken.md', html: NOTE_HTML, sha: noteSha, title: 'broken' });
    case 'GET /n/nb/raw': return respond(200, noteSrc);
    case 'POST /n/nb/resources/presign': return respond(200, { direct: true });
    case 'POST /n/nb/resources': {
      return respond(201, { sha: NEW_SHA, filename: 'replacement.png', mime: 'image/png', created: true });
    }
    case 'PUT /n/nb/note': {
      noteSrc = init.body;
      noteSha = 's2';
      return respond(200, { sha: noteSha });
    }
    case 'GET /n/nb/resources/' + OLD_SHA + '/refs':
      return respond(200, { refs: ['broken.md'] });
    default: return respond(200, {});
  }
}

const $ = (id) => document.getElementById(id);
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

test('a broken note image gets a recovery card instead of a dead icon', async () => {
  $('tree').querySelector('[data-path="broken.md"]').click();
  await until(() => $('note').querySelector('img'), 'note image rendered');
  const img = $('note').querySelector('img');

  img.dispatchEvent(new Event('error'));
  await until(() => $('note').querySelector('.broken-attachment'), 'recovery card shown');

  expect($('note').querySelector('img')).toBeNull();
  const card = $('note').querySelector('.broken-attachment');
  expect(card.textContent).toMatch(/unavailable/i);
  const buttons = [...card.querySelectorAll('button')].map((b) => b.textContent);
  expect(buttons).toEqual(['Open attachment manager', 'Replace…']);
});

test('"Open attachment manager" opens the manager pre-filtered to the broken sha', async () => {
  const card = $('note').querySelector('.broken-attachment');
  [...card.querySelectorAll('button')].find((b) => b.textContent === 'Open attachment manager').click();
  await until(() => !$('attachments-modal').hidden, 'attachments modal open');
  expect($('attachments-search').value).toBe(OLD_SHA);
  $('attachments-close').click();
});

test('"Replace…" uploads a new file and repoints this note\'s link at it', async () => {
  const card = $('note').querySelector('.broken-attachment');
  const replaceBtn = [...card.querySelectorAll('button')].find((b) => b.textContent === 'Replace…');
  const fileInput = card.querySelector('input[type="file"]');
  expect(fileInput.hidden).toBe(true);

  const file = new File(['new bytes'], 'replacement.png', { type: 'image/png' });
  Object.defineProperty(fileInput, 'files', { value: [file], configurable: true });
  replaceBtn.click(); // triggers fileInput.click() in the real browser; here we just fire change directly
  fileInput.dispatchEvent(new Event('change'));

  await until(() => !$('note').querySelector('.broken-attachment'), 'note re-rendered after replace');
  expect(noteSrc).toContain(NEW_SHA);
  expect(noteSrc).not.toContain(OLD_SHA);
  await until(() => $('toast').textContent === 'Attachment replaced.', 'replaced toast');
});

test('an external image failing to load is left alone (not one of this notebook\'s own attachments)', async () => {
  $('tree').querySelector('[data-path="external.md"]').click();
  await until(() => $('note').querySelector('img')?.src.includes('example.com'), 'external image rendered');
  $('note').querySelector('img').dispatchEvent(new Event('error'));
  // No recovery card is swapped in: this image is not one of this
  // notebook's own /resources/{sha} attachments, so the broken-image
  // handler never even attached a listener to it.
  expect($('note').querySelector('.broken-attachment')).toBeNull();
  expect($('note').querySelector('img')).not.toBeNull();
});
