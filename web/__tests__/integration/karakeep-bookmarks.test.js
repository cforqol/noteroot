/**
 * Integration test for #18: the Karakeep bookmarks manager (Settings →
 * Integrations → "Bookmarks…", listing archived URLs and the notes
 * that mention them) and the opt-out for auto-archiving on paste.
 * Own fake server + own app.js instance.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

function buildTree() {
  return { name: '', path: '', isDir: true, children: [
    { name: 'welcome.md', path: 'welcome.md', isDir: false, size: 1 },
  ] };
}
const respond = (status, body) => ({
  ok: status >= 200 && status < 300,
  status,
  statusText: String(status),
  json: async () => body,
  text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
});
const requests = [];
const BOOKMARKS = [
  { url: 'https://example.com/a', archiveUrl: 'https://karakeep.example/bookmarks/1', paths: ['welcome.md', 'other.md'] },
  { url: 'https://example.com/b', archiveUrl: 'https://karakeep.example/bookmarks/2', paths: ['other.md'] },
];
async function fakeFetch(url, init = {}) {
  const u = new URL(url, 'http://x');
  const method = (init.method || 'GET').toUpperCase();
  const route = `${method} ${u.pathname}`;
  requests.push(route);
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, buildTree());
    case 'GET /api/v1/integrations/status': return respond(200, { slink: false, karakeep: true, s3: false });
    case 'GET /n/nb/integrations/karakeep/bookmarks': return respond(200, { bookmarks: BOOKMARKS });
    case 'GET /n/nb/note': return respond(200, { html: '<p>hi</p>', sha: 's1', title: 'welcome' });
    case 'GET /n/nb/raw': return respond(200, 'hi there');
    default: return respond(200, {});
  }
}

const $ = (id) => document.getElementById(id);
const q = (sel) => document.querySelector(sel);
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
  localStorage.setItem('nr.editorPref', 'textarea');
  await import('../../app.js');
  await until(() => $('tree').querySelectorAll('.node').length > 0, 'initial tree');
});

describe('Karakeep bookmarks manager (#18)', () => {
  test('Settings enables the toggle and "Bookmarks…" button once Karakeep is configured', async () => {
    $('settingsbtn').click();
    await until(() => q('#settings-modal .settings-nav') || !$('settings-modal').hidden, 'settings open');
    await until(() => !$('bookmarks-manage').disabled, 'bookmarks button enabled');
    expect($('integ-karakeep-use').disabled).toBe(false);
  });

  test('opening the manager lists archived URLs grouped with every note that mentions them', async () => {
    $('bookmarks-manage').click();
    await until(() => !$('bookmarks-modal').hidden, 'bookmarks modal open');
    await until(() => requests.includes('GET /n/nb/integrations/karakeep/bookmarks'), 'bookmarks fetched');
    await until(() => qa('#bookmarks-grid .attach-card').length === 2, 'two bookmark cards rendered');
    const firstCard = qa('#bookmarks-grid .attach-card')[0];
    expect(firstCard.textContent).toContain('https://example.com/a');
    expect(firstCard.textContent).toContain('welcome.md');
    expect(firstCard.textContent).toContain('other.md');
  });

  test('the search box filters by URL or note path', async () => {
    $('bookmarks-search').value = 'example.com/b';
    $('bookmarks-search').dispatchEvent(new Event('input'));
    await until(() => qa('#bookmarks-grid .attach-card').length === 1, 'filtered to one card');
    expect($('bookmarks-grid').textContent).toContain('https://example.com/b');
    expect($('bookmarks-grid').textContent).not.toContain('https://example.com/a');
  });

  test('clicking a note link closes the manager and opens that note', async () => {
    $('bookmarks-search').value = '';
    $('bookmarks-search').dispatchEvent(new Event('input'));
    await until(() => qa('#bookmarks-grid .attach-card').length === 2, 'cards back');
    requests.length = 0;
    const link = qa('#bookmarks-grid .attach-refs a').find((a) => a.textContent === 'welcome.md');
    link.click();
    await until(() => $('bookmarks-modal').hidden, 'manager closed');
    await until(() => requests.some((req) => req.startsWith('GET /n/nb/note')), 'note fetched');
  });
});
