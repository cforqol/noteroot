/**
 * Integration test for #87: pipe tables are auto-formatted on save,
 * matching the setting's default (on); the editor's own buffer is
 * updated in place (same mechanism the frontmatter `updated` stamp
 * already uses) so what the user sees matches what was saved. Own fake
 * server + own app.js instance.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

const RAW = [
  '---',
  'id: 01ABCDEFGHJKMNPQRSTVWXYZ0100',
  'title: welcome',
  'created: 2026-01-01T00:00',
  'updated: 2026-01-01T00:00',
  '---',
  '',
  '|A|B|',
  '|-|-|',
  '|1|22|',
  '',
].join('\n');

function buildTree() {
  return { name: '', path: '', isDir: true, children: [
    { name: 'welcome.md', path: 'welcome.md', isDir: false, size: 1 },
  ] };
}
const respond = (status, body, headers = {}) => ({
  ok: status >= 200 && status < 300,
  status,
  statusText: String(status),
  headers: { get: (k) => headers[k] || '' },
  json: async () => body,
  text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
});

let currentRaw = RAW;
const requests = [];
async function fakeFetch(url, init = {}) {
  const u = new URL(url, 'http://x');
  const method = (init.method || 'GET').toUpperCase();
  const route = `${method} ${u.pathname}`;
  if (route === 'PUT /n/nb/note') requests.push({ route, body: init.body });
  else requests.push(route);
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, buildTree());
    case 'GET /n/nb/note': return respond(200, { html: '<p>hi</p>', sha: 's1', title: 'welcome' });
    case 'GET /n/nb/raw': return respond(200, currentRaw);
    case 'PUT /n/nb/note': return respond(200, { sha: 's2' });
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

async function openNoteAndEditor() {
  $('tree').querySelector('[data-path="welcome.md"]').click();
  await until(() => !$('editbtn').hidden, 'note opened in read mode');
  $('editbtn').click();
  await until(() => !$('editbody').hidden && $('editbody').value !== '', 'editor opened');
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

// The shared test setup clears localStorage before every test() — restore
// the plain-text pref each time so this file's later tests (which open
// the editor from inside a test body, not beforeAll) don't fall back to
// 'auto' and pick Vim on this desktop-width jsdom viewport.
beforeEach(() => localStorage.setItem('nr.editorPref', 'textarea'));

describe('pipe tables are auto-formatted on save (#87)', () => {
  test('default setting: saving reformats an unaligned table and updates the visible buffer', async () => {
    await openNoteAndEditor();
    expect($('editbody').value).toContain('|A|B|');
    // Saving an untouched buffer is a no-op (nothing changed to save);
    // make a real edit elsewhere in the note so Save actually runs.
    $('editbody').value += '\nmore text\n';
    $('editbody').dispatchEvent(new Event('input'));

    requests.length = 0;
    $('savebtn').click();
    await until(() => requests.some((r) => r.route === 'PUT /n/nb/note'), 'save request sent');
    const put = requests.find((r) => r.route === 'PUT /n/nb/note');
    expect(put.body).toContain('| A   | B   |');
    expect(put.body).toContain('| --- | --- |');
    expect(put.body).toContain('| 1   | 22  |');
    expect(put.body).not.toContain('|A|B|');

    // The textarea itself was corrected in place, same as a frontmatter fix.
    expect($('editbody').value).toContain('| A   | B   |');
    expect($('editbody').value).not.toContain('|A|B|');
  });

  test('turning the setting off leaves the table exactly as typed', async () => {
    $('closebtn').click();
    await until(() => $('editbtn').hidden === false || $('editor').hidden, 'editor closed');

    const { setSetting } = await import('../../lib/prefs.js');
    setSetting('formatTables', 'off');
    currentRaw = RAW; // fresh, unformatted again

    await openNoteAndEditor();
    $('editbody').value += '\nmore text\n';
    $('editbody').dispatchEvent(new Event('input'));
    requests.length = 0;
    $('savebtn').click();
    await until(() => requests.some((r) => r.route === 'PUT /n/nb/note'), 'save request sent with setting off');
    const put = requests.find((r) => r.route === 'PUT /n/nb/note');
    expect(put.body).toContain('|A|B|');
    expect(put.body).not.toContain('| A   | B   |');
  });
});
