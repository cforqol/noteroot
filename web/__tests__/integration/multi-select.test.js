/**
 * Integration test (#84 follow-up): once multi-select is active, a
 * plain click/tap on a note must add/remove it from the selection, not
 * open it and silently exit multi-select. Only "Clear" leaves
 * multi-select mode. Own fake server + own app.js instance, same
 * pattern as notebook-lock.test.js / heading-fold.test.js.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

function buildTree() {
  return {
    name: '', path: '', isDir: true,
    children: [
      { name: 'a.md', path: 'a.md', isDir: false, size: 1 },
      { name: 'b.md', path: 'b.md', isDir: false, size: 1 },
      { name: 'c.md', path: 'c.md', isDir: false, size: 1 },
    ],
  };
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
    case 'GET /n/nb/note': {
      const path = u.searchParams.get('path');
      return respond(200, { html: `<p>${path}</p>`, sha: 's1', title: path });
    }
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
const noteEl = (path) => $('tree').querySelector(`[data-path="${path}"]`);
const ctrlClick = (el) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }));
const plainClick = (el) => el.click();

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

beforeEach(() => {
  // Each test starts from a clean multi-select state: Clear if active.
  if (!$('multi-bar').hidden) $('multi-clear').click();
});

describe('once active, multi-select only exits via Clear', () => {
  test('a plain click on a different note adds it to the selection instead of opening it', async () => {
    ctrlClick(noteEl('a.md')); // activates multi-select, same as a long-press would
    expect($('multi-bar').hidden).toBe(false);
    expect(noteEl('a.md').classList.contains('multi')).toBe(true);

    plainClick(noteEl('b.md'));
    expect($('multi-bar').hidden).toBe(false); // still active
    expect(noteEl('b.md').classList.contains('multi')).toBe(true);
    expect(noteEl('a.md').classList.contains('multi')).toBe(true); // a.md stays picked too
    // The note pane must not have navigated to b.md.
    expect($('note').textContent).not.toContain('b.md');
  });

  test('clicking an already-selected note again removes it from the selection (still does not open it)', async () => {
    ctrlClick(noteEl('a.md'));
    plainClick(noteEl('b.md'));
    expect(noteEl('b.md').classList.contains('multi')).toBe(true);

    plainClick(noteEl('b.md'));
    expect(noteEl('b.md').classList.contains('multi')).toBe(false);
    expect($('multi-bar').hidden).toBe(false); // a.md is still picked; stays active
    expect($('note').textContent).not.toContain('b.md');
  });

  test('clicking the note that started multi-select does not deselect-and-open it either', async () => {
    ctrlClick(noteEl('a.md'));
    plainClick(noteEl('a.md'));
    expect(noteEl('a.md').classList.contains('multi')).toBe(false); // toggled off...
    expect($('multi-bar').hidden).toBe(true); // ...and that was the last one, so the bar hides
    // Toggling the last pick off is not the same as explicit Clear, but
    // either way nothing got opened.
    expect($('note').textContent).not.toContain('a.md');
  });

  test('Clear is the only plain action that exits multi-select, and a click afterwards opens normally', async () => {
    ctrlClick(noteEl('a.md'));
    plainClick(noteEl('b.md'));
    $('multi-clear').click();
    expect($('multi-bar').hidden).toBe(true);
    expect(noteEl('a.md').classList.contains('multi')).toBe(false);
    expect(noteEl('b.md').classList.contains('multi')).toBe(false);

    plainClick(noteEl('c.md'));
    await until(() => $('note').textContent.includes('c.md'), 'c.md to open');
  });

  test('ctrl/shift-click still work as before while multi-select is already active', async () => {
    ctrlClick(noteEl('a.md'));
    ctrlClick(noteEl('b.md')); // ctrl-click is unaffected by the new plain-click branch
    expect(noteEl('a.md').classList.contains('multi')).toBe(true);
    expect(noteEl('b.md').classList.contains('multi')).toBe(true);
  });
});
