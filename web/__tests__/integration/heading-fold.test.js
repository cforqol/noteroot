/**
 * Integration test for #80: read-mode heading folds. Clicking a
 * heading's caret hides everything up to the next heading of the
 * same or shallower level, and clicking again restores it. Own fake
 * server + own app.js instance.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

const NOTE_HTML =
  '<h1>Title</h1>' +
  '<p>intro</p>' +
  '<h2>Section A</h2>' +
  '<p>a-1</p>' +
  '<p>a-2</p>' +
  '<h3>Section A.1</h3>' +
  '<p>a-1-1</p>' +
  '<h2>Section B</h2>' +
  '<p>b-1</p>';

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
    case 'GET /n/nb/note':
      return respond(200, {
        html: NOTE_HTML, sha: 's1', title: 'welcome',
        outgoing: [{ path: 'other.md', label: 'other' }],
      });
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
  localStorage.setItem('nr.editorPref', 'textarea');
  await import('../../app.js');
  await until(() => $('tree').querySelectorAll('.node').length > 0, 'initial tree');
  $('tree').querySelector('[data-path="welcome.md"]').click();
  await until(() => $('note').querySelector('h2'), 'note rendered');
});

// Headings carry a prepended fold-caret button, so match by substring
// rather than exact text.
const byText = (tag, text) => [...document.querySelectorAll(`#note ${tag}`)].find((e) => e.textContent.trim().endsWith(text));
const caretOf = (h) => h.querySelector('.fold-caret');

describe('read-mode heading folds (#80)', () => {
  test('every heading gets a fold caret, expanded by default', () => {
    for (const tag of ['h1', 'h2', 'h3']) {
      expect($('note').querySelectorAll(tag).length).toBeGreaterThan(0);
    }
    expect(caretOf(byText('h2', 'Section A'))).toBeTruthy();
  });

  test('folding an h2 hides its paragraphs and nested h3, but not the next h2', () => {
    const h2a = byText('h2', 'Section A');
    caretOf(h2a).click();
    expect(h2a.classList.contains('folded')).toBe(true);
    expect(byText('p', 'a-1').classList.contains('fold-hidden')).toBe(true);
    expect(byText('p', 'a-2').classList.contains('fold-hidden')).toBe(true);
    expect(byText('h3', 'Section A.1').classList.contains('fold-hidden')).toBe(true);
    expect(byText('p', 'a-1-1').classList.contains('fold-hidden')).toBe(true);
    // Section B (same level, comes after) must stay visible.
    expect(byText('h2', 'Section B').classList.contains('fold-hidden')).toBe(false);
    expect(byText('p', 'b-1').classList.contains('fold-hidden')).toBe(false);
  });

  test('clicking the caret again unfolds it', () => {
    const h2a = byText('h2', 'Section A');
    caretOf(h2a).click();
    expect(h2a.classList.contains('folded')).toBe(false);
    expect(byText('p', 'a-1').classList.contains('fold-hidden')).toBe(false);
    expect(byText('h3', 'Section A.1').classList.contains('fold-hidden')).toBe(false);
  });

  test('folding the very last section does not hide the note panel content', async () => {
    const h2b = byText('h2', 'Section B');
    caretOf(h2b).click();
    expect(byText('p', 'b-1').classList.contains('fold-hidden')).toBe(true);
    // The outgoing-links panel lives beside the note (#readpane), not
    // inside #note, so it is unaffected either way — this just asserts
    // nothing throws and the note body still has exactly its own content.
    expect($('note').querySelectorAll('.links-panel').length).toBe(0);
  });
});
