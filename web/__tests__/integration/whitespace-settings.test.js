/**
 * Integration test for #100: plain-text editor "automatic formatting"
 * settings — trim trailing spaces on save, Backspace removing a whole
 * soft-tab indent, and the show-whitespace overlay. Own fake server +
 * own app.js instance, same pattern as table-format-on-save.test.js.
 *
 * Each test opens its own note file. closeEditorNow() never clears
 * #editbody (its buffer is kept across close/reopen for other reasons),
 * so the generic "editor opened" wait must check content specific to
 * the note being opened — otherwise it can resolve against a leftover
 * value from whichever note was open before.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

function rawFor(title, id) {
  return [
    '---',
    `id: ${id}`,
    `title: ${title}`,
    'created: 2026-01-01T00:00',
    'updated: 2026-01-01T00:00',
    '---',
    '',
    'hello',
    '',
  ].join('\n');
}

const NOTES = ['ws-trim-off', 'ws-trim-on', 'ws-backspace-on', 'ws-backspace-off', 'ws-overlay'];
const rawByPath = Object.fromEntries(NOTES.map((n, i) => [
  `${n}.md`, rawFor(n, `01ABCDEFGHJKMNPQRSTVWXYZ020${i}`),
]));

function buildTree() {
  return { name: '', path: '', isDir: true, children: NOTES.map((n) => (
    { name: `${n}.md`, path: `${n}.md`, isDir: false, size: 1 }
  )) };
}
const respond = (status, body, headers = {}) => ({
  ok: status >= 200 && status < 300,
  status,
  statusText: String(status),
  headers: { get: (k) => headers[k] || '' },
  json: async () => body,
  text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
});

const requests = [];
async function fakeFetch(url, init = {}) {
  const u = new URL(url, 'http://x');
  const method = (init.method || 'GET').toUpperCase();
  const route = `${method} ${u.pathname}`;
  const path = u.searchParams.get('path') || '';
  if (route === 'PUT /n/nb/note') requests.push({ route, body: init.body, path });
  else requests.push(route);
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, buildTree());
    case 'GET /n/nb/note': return respond(200, { html: '<p>hi</p>', sha: 's1', title: path });
    case 'GET /n/nb/raw': return respond(200, rawByPath[path] || '');
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

async function openNoteAndEditor(name) {
  $('tree').querySelector(`[data-path="${name}.md"]`).click();
  await until(() => !$('editbtn').hidden, 'note opened in read mode');
  $('editbtn').click();
  // Specific to this note: #editbody keeps its previous value across a
  // close, so a generic "non-empty" check can resolve against stale
  // content left by whichever note was open before this one.
  await until(() => !$('editbody').hidden && $('editbody').value.includes(name), 'editor opened');
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

beforeEach(() => localStorage.setItem('nr.editorPref', 'textarea'));

describe('trim trailing spaces on save', () => {
  test('off by default: trailing spaces are kept', async () => {
    await openNoteAndEditor('ws-trim-off');
    $('editbody').value += 'trailing   \n';
    $('editbody').dispatchEvent(new Event('input'));
    requests.length = 0;
    $('savebtn').click();
    await until(() => requests.some((r) => r.route === 'PUT /n/nb/note'), 'save request sent');
    expect(requests.find((r) => r.route === 'PUT /n/nb/note').body).toContain('trailing   \n');
  });

  test('turning the setting on strips trailing spaces and updates the visible buffer', async () => {
    const { setSetting } = await import('../../lib/prefs.js');
    setSetting('trimTrailingSpaces', 'on');

    await openNoteAndEditor('ws-trim-on');
    $('editbody').value += 'trailing   \n';
    $('editbody').dispatchEvent(new Event('input'));
    requests.length = 0;
    $('savebtn').click();
    await until(() => requests.some((r) => r.route === 'PUT /n/nb/note'), 'save request sent');
    const put = requests.find((r) => r.route === 'PUT /n/nb/note');
    expect(put.body).toContain('trailing\n');
    expect(put.body).not.toContain('trailing   \n');
    expect($('editbody').value).not.toMatch(/trailing +\n/);

    setSetting('trimTrailingSpaces', 'off');
  });
});

describe('Backspace removes a whole soft-tab indent', () => {
  test('default on: Backspace right after a 4-space indent removes all 4', async () => {
    const { setSetting } = await import('../../lib/prefs.js');
    setSetting('tabKey', '4');
    await openNoteAndEditor('ws-backspace-on');
    const ta = $('editbody');
    ta.value = '    x';
    ta.selectionStart = ta.selectionEnd = 4;
    ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Backspace', cancelable: true, bubbles: true }));
    expect(ta.value).toBe('x');
    expect(ta.selectionStart).toBe(0);
  });

  test('off: Backspace is left to the browser', async () => {
    const { setSetting } = await import('../../lib/prefs.js');
    setSetting('tabKey', '4');
    setSetting('backspaceDedent', 'off');
    await openNoteAndEditor('ws-backspace-off');
    const ta = $('editbody');
    ta.value = '    x';
    ta.selectionStart = ta.selectionEnd = 4;
    const e = new KeyboardEvent('keydown', { key: 'Backspace', cancelable: true, bubbles: true });
    ta.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(false);
    expect(ta.value).toBe('    x'); // jsdom doesn't apply the browser's default edit
    setSetting('backspaceDedent', 'on');
  });
});

describe('show whitespace overlay', () => {
  test('hidden by default; enabling it renders space and EOL markers', async () => {
    await openNoteAndEditor('ws-overlay');
    const overlay = $('editbody').closest('.editor-wrap').querySelector('.ws-overlay');
    expect(overlay.hidden).toBe(true);

    const { setSetting } = await import('../../lib/prefs.js');
    setSetting('showWhitespace', 'on');
    setSetting('spaceChar', '·');
    setSetting('eolChar', '°');
    $('editbody').value = 'a b\nc';
    $('editbody').dispatchEvent(new Event('input'));
    expect(overlay.hidden).toBe(false);
    expect(overlay.textContent).toBe('a·b°\nc');

    setSetting('showWhitespace', 'off');
  });
});
