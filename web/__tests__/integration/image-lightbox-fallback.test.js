/**
 * Integration test for the image lightbox's fallback when the original
 * blob 404s. thumbResource serves a cached thumbnail even once its
 * source blob is gone (an old sweep, or a vault restored without its
 * store), so a picture that renders fine inline can still 404 on the
 * full-size fetch the lightbox makes when clicked. The lightbox should
 * fall back to the thumbnail already on screen rather than show a
 * broken image.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

const SHA = 'a'.repeat(64);
const NOTE_HTML = `<p><img src="/n/nb/resources/${SHA}" alt="pic"></p>`;

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
      { name: 'pic.md', path: 'pic.md', isDir: false, size: 1 },
    ] });
    case 'GET /n/nb/note': return respond(200, { html: NOTE_HTML, sha: 's1', title: 'pic' });
    case 'GET /n/nb/raw': return respond(200, '# pic\n');
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

test('clicking a note image opens the lightbox on the original resource URL', async () => {
  $('tree').querySelector('[data-path="pic.md"]').click();
  await until(() => $('note').querySelector('img'), 'note image rendered');
  const thumb = $('note').querySelector('img');
  expect(thumb.src).toContain(`/n/nb/resources/${SHA}/thumb?`);

  thumb.click();
  const box = $('img-lightbox');
  const lbImg = $('img-lightbox-img');
  expect(box.hidden).toBe(false);
  expect(lbImg.src).toBe(`${window.location.origin}/n/nb/resources/${SHA}`);
});

test('a 404 on the original falls back to the thumbnail already on screen', async () => {
  const lbImg = $('img-lightbox-img');
  const thumbSrc = $('note').querySelector('img').src;
  lbImg.dispatchEvent(new Event('error'));
  expect(lbImg.src).toBe(thumbSrc);
  expect($('toast').hidden).toBe(false);
  expect($('toast').textContent).toMatch(/no longer available/i);
});

test('closing and reopening does not carry the stale error handler over', async () => {
  $('img-lightbox-close').click();
  expect($('img-lightbox').hidden).toBe(true);

  const thumb = $('note').querySelector('img');
  thumb.click();
  const lbImg = $('img-lightbox-img');
  expect(lbImg.src).toBe(`${window.location.origin}/n/nb/resources/${SHA}`);
});
