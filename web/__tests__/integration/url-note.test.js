/**
 * Integration test for #103: a note's `url:` frontmatter field — "New
 * note from URL…" (with optional Karakeep archiving) and the note
 * panel's "Source" link. Own fake server + own app.js instance, so the
 * module-level integration-status cache (fetched once at import) can
 * be set up as "Karakeep configured" from the start.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

const notes = new Map();
const folders = new Set();
const requests = [];

function buildTree() {
  const root = { name: '', path: '', isDir: true, children: [] };
  const byPath = { '': root };
  for (const d of [...folders].sort()) {
    const parent = d.includes('/') ? d.slice(0, d.lastIndexOf('/')) : '';
    const n = { name: d.slice(d.lastIndexOf('/') + 1), path: d, isDir: true, children: [] };
    byPath[d] = n;
    byPath[parent].children.push(n);
  }
  for (const p of [...notes.keys()].sort()) {
    const parent = p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '';
    byPath[parent].children.push({ name: p.slice(p.lastIndexOf('/') + 1), path: p, isDir: false, size: notes.get(p).length });
  }
  return root;
}
function addParents(path) {
  const parts = path.split('/').slice(0, -1);
  parts.forEach((_, i) => folders.add(parts.slice(0, i + 1).join('/')));
}
const sha = (p) => `sha-${p}-${notes.get(p)?.length}`;
// A tiny stand-in for the real server's frontmatter.Parse: just enough
// to pull the url: line back out of the stored markdown for GET /note.
const urlOf = (content) => /^url: "?([^"\n]*)"?$/m.exec(content)?.[1] || '';

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
  const p = u.searchParams.get('path') || '';
  const route = `${method} ${u.pathname}`;
  requests.push(route);
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, buildTree());
    case 'GET /n/nb/tags': return respond(200, { tags: {} });
    case 'GET /api/v1/integrations/status': return respond(200, { slink: false, karakeep: true, s3: false });
    case 'POST /api/v1/integrations/karakeep/archive': {
      const body = JSON.parse(init.body);
      return respond(200, { bookmarkUrl: `https://karakeep.example/bookmarks/${encodeURIComponent(body.url)}` });
    }
    case 'PUT /n/nb/note':
      notes.set(p, init.body);
      addParents(p);
      return respond(200, { sha: sha(p) });
    case 'GET /n/nb/note':
      if (!notes.has(p)) return respond(404, { error: 'not found' });
      return respond(200, { path: p, id: '', title: p, tags: [], url: urlOf(notes.get(p)), sha: sha(p), html: '<p>x</p>' });
    case 'GET /n/nb/raw':
      return notes.has(p) ? respond(200, notes.get(p)) : respond(404, { error: 'not found' });
    default:
      return respond(200, {});
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
async function submitDialog(values = {}) {
  await until(() => q('.dialog form'), 'dialog');
  for (const [name, v] of Object.entries(values)) {
    const input = q(`.dialog [name="${name}"]`);
    if (input.type === 'checkbox') input.checked = v;
    else input.value = v;
  }
  q('.dialog form').dispatchEvent(new Event('submit', { cancelable: true }));
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
  await until(() => requests.includes('GET /n/nb/tree'), 'app booted');
  await until(() => requests.includes('GET /api/v1/integrations/status'), 'integration status fetched');
});

describe('New note from URL (#103)', () => {
  test('the dialog offers a Karakeep checkbox once configured, and checking it archives the page without touching the body', async () => {
    $('new-more').click();
    await until(() => q('.ctx-menu'), 'new-note menu');
    [...qa('.ctx-menu .ctx-item')].find((b) => b.textContent === 'New note from URL…').click();
    await until(() => q('.dialog [name="url"]'), 'new-from-url dialog');
    expect(q('.dialog [name="archive"]')).not.toBeNull();

    await submitDialog({ url: 'https://example.com/post', name: 'example-post', archive: true });
    await until(() => notes.has('example-post.md'), 'note created');
    await until(() => requests.includes('POST /api/v1/integrations/karakeep/archive'), 'archive request sent');

    const content = notes.get('example-post.md');
    expect(content).toMatch(/url: "https:\/\/example\.com\/post"/);
    const body = content.slice(content.indexOf('\n---\n', 4) + 5);
    expect(body.trim()).toBe('# example-post');
    // The URL lives only in frontmatter; the body carries no reference
    // line (accessible via the note panel's Source link instead).
    expect(body).not.toContain('example.com');
    expect(body).not.toContain('archived');

    // The note auto-opens in the editor after creation; close it so the
    // next test's tree click opens the read view, not a no-op on an
    // already-open editor.
    await until(() => !$('editor').hidden, 'editor opened');
    $('closebtn').click();
    await until(() => $('editor').hidden, 'editor closed');
  });

  test('rejects an invalid URL without creating anything', async () => {
    const before = notes.size;
    $('new-more').click();
    await until(() => q('.ctx-menu'), 'new-note menu');
    [...qa('.ctx-menu .ctx-item')].find((b) => b.textContent === 'New note from URL…').click();
    await until(() => q('.dialog [name="url"]'), 'dialog');
    await submitDialog({ url: 'not a url', name: 'bad' });
    expect(q('.dialog-error').hidden).toBe(false);
    q('.dialog .btn').click(); // Cancel
    expect(notes.size).toBe(before);
  });

  test('the note panel shows a clickable Source link for a note with url: frontmatter', async () => {
    q('#tree [data-path="example-post.md"]').click();
    await until(() => !q('#rp-source').hidden, 'source section shown');
    const link = q('#rp-source .rp-source-link');
    expect(link.href).toBe('https://example.com/post');
    expect(link.textContent).toBe('https://example.com/post');
    expect(link.target).toBe('_blank');
    expect(link.rel).toContain('noopener');
  });

  test('a note without url: frontmatter hides the Source section', async () => {
    $('new-note').click();
    await submitDialog({ name: 'plain' });
    await until(() => notes.has('plain.md'), 'plain note created');
    await until(() => q('#tree [data-path="plain.md"]'), 'plain note in tree');
    q('#tree [data-path="plain.md"]').click();
    await until(() => q('#rp-source').hidden, 'source section hidden for a note with no url');
  });
});
