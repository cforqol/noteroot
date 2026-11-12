/**
 * Integration test for #76: the in-editor "Use Vim"/"Use plain text"
 * toggle must let the user override Settings → Editor's default for
 * the note they have open right now, even when that default is fixed
 * to one mode ("Always Vim"/"Always Plain Text"), not just "Auto".
 * Own fake server + own app.js instance (a fresh nr.editorPref needs
 * to be set before import, which the shared app-ui.test.js scenario
 * cannot do mid-run).
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
async function fakeFetch(url, init = {}) {
  const u = new URL(url, 'http://x');
  const method = (init.method || 'GET').toUpperCase();
  const route = `${method} ${u.pathname}`;
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, buildTree());
    case 'GET /n/nb/note': return respond(200, { html: '<p>hi</p>', sha: 's1', title: 'welcome' });
    case 'GET /n/nb/raw': return respond(200, 'hi there');
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
  // The setting under test: the default is fixed to plain text, not "auto".
  localStorage.setItem('nr.editorPref', 'textarea');
  await import('../../app.js');
  await until(() => $('tree').querySelectorAll('.node').length > 0, 'initial tree');
  $('tree').querySelector('[data-path="welcome.md"]').click();
  await until(() => !$('editbtn').hidden, 'note opened in read mode');
  $('editbtn').click();
  await until(() => !$('editor').hidden, 'editor opened');
});

describe('editor mode toggle overrides a fixed Settings default (#76)', () => {
  test('opens in plain text, matching the "Always Plain Text" default', () => {
    expect($('editbody').hidden).toBe(false);
    expect($('editormode').textContent).toBe('plain text');
  });

  test('the toggle is visible, enabled, and offers to switch to Vim', () => {
    const btn = $('modeswitch');
    expect(btn.hidden).toBe(false);
    expect(btn.disabled).toBe(false);
    expect(btn.textContent).toBe('Use Vim');
  });

  test('clicking it switches to Vim for this note, overriding the fixed default', async () => {
    $('modeswitch').click();
    // Wait for syncModeSwitch() itself to have settled (its own
    // textContent write), not just the earlier, synchronous
    // $("editormode") flip — they land at different points in
    // openVimEditor()'s async startup.
    await until(() => $('modeswitch').textContent === 'Use plain text', 'toggle to flip to "Use plain text"');
    expect($('editormode').textContent).toBe('vim');
    expect($('editbody').hidden).toBe(true);
  });

  test('the toggle now offers to switch back to plain text, and is not disabled', () => {
    const btn = $('modeswitch');
    expect(btn.hidden).toBe(false);
    expect(btn.disabled).toBe(false);
    expect(btn.textContent).toBe('Use plain text');
  });
});
