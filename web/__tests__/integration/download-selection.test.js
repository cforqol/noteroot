/**
 * Integration test for #85: downloading a sidebar multi-selection (or a
 * single note/folder via its context menu) as a zip, with an
 * attachments-or-not choice. Own fake server + own app.js instance.
 */
import { jest } from '@jest/globals';
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
      { name: 'dir', path: 'dir', isDir: true, children: [
        { name: 'c.md', path: 'dir/c.md', isDir: false, size: 1 },
      ] },
    ],
  };
}

const respond = (status, body, isBlob = false) => ({
  ok: status >= 200 && status < 300,
  status,
  statusText: String(status),
  json: async () => body,
  text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  blob: async () => (isBlob ? body : new Blob([typeof body === 'string' ? body : JSON.stringify(body)])),
});

const requests = [];
async function fakeFetch(url, init = {}) {
  const u = new URL(url, 'http://x');
  const method = (init.method || 'GET').toUpperCase();
  const route = `${method} ${u.pathname}${u.search}`;
  requests.push(route);
  if (u.pathname === '/api/v1/notebooks') return respond(200, { notebooks: [{ slug: 'nb' }] });
  if (u.pathname === '/n/nb/tree') return respond(200, buildTree());
  if (u.pathname === '/n/nb/export/selection') {
    return respond(200, new Blob(['zip-bytes']), true);
  }
  return respond(200, {});
}

const $ = (id) => document.getElementById(id);
const q = (sel) => document.querySelector(sel);
const qa = (sel) => [...document.querySelectorAll(sel)];
const node = (path) => document.querySelector(`#tree .node[data-path="${path}"]`);
const sleep = (ms = 0) => new Promise((r) => setTimeout(r, ms));
async function until(cond, label = 'condition') {
  for (let i = 0; i < 400; i += 1) {
    if (cond()) return;
    await sleep(5);
  }
  throw new Error(`timed out waiting for ${label}`);
}

let createdURLs;
beforeAll(async () => {
  document.documentElement.innerHTML = html.replace(/<script[\s\S]*?<\/script>/g, '');
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (media) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} }),
  });
  global.ResizeObserver = window.ResizeObserver = class {
    observe() {} unobserve() {} disconnect() {}
  };
  createdURLs = [];
  window.URL.createObjectURL = jest.fn((blob) => { createdURLs.push(blob); return 'blob:fake'; });
  window.URL.revokeObjectURL = jest.fn();
  jest.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
  globalThis.fetch = fakeFetch;
  window.fetch = fakeFetch;
  localStorage.setItem('nr.editorPref', 'textarea');
  await import('../../app.js');
  await until(() => $('tree').querySelectorAll('.node').length > 0, 'initial tree');
});

const click = (path, opts = {}) => node(path).dispatchEvent(new MouseEvent('click', { bubbles: true, ...opts }));

describe('sidebar download (#85)', () => {
  test('multi-selecting notes reveals the Download button in the multi-bar', () => {
    click('a.md', { ctrlKey: true });
    click('b.md', { ctrlKey: true });
    expect($('multi-bar').hidden).toBe(false);
    expect($('multi-download').hidden).toBe(false);
  });

  test('clicking Download exports the selection without attachments', async () => {
    requests.length = 0;
    $('multi-download').click();
    await until(() => requests.some((r) => r.startsWith('GET /n/nb/export/selection')), 'selection export requested');
    const r = requests.find((r) => r.startsWith('GET /n/nb/export/selection'));
    expect(r).toContain('path=a.md');
    expect(r).toContain('path=b.md');
    expect(r).not.toContain('attachments=1');
    await until(() => createdURLs.length > 0, 'download triggered');
  });

  test('the caret opens a menu offering a with-attachments download', async () => {
    requests.length = 0;
    createdURLs.length = 0;
    $('multi-download-more').click();
    await until(() => q('.ctx-menu'), 'download options menu');
    const items = qa('.ctx-menu .ctx-item').map((b) => b.textContent);
    expect(items).toEqual(['Download', 'Download with attachments']);
    qa('.ctx-menu .ctx-item').find((b) => b.textContent === 'Download with attachments').click();
    await until(() => requests.some((r) => r.includes('attachments=1')), 'attachments export requested');
  });

  test('clearing the selection hides the multi-bar again', () => {
    $('multi-clear').click();
    expect($('multi-bar').hidden).toBe(true);
  });

  test('a folder\'s context menu offers Download…, which opens the same attachments choice', async () => {
    requests.length = 0;
    node('dir').closest('.row').dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }));
    await until(() => q('.ctx-menu'), 'folder context menu');
    const downloadItem = qa('.ctx-menu .ctx-item').find((b) => b.textContent === 'Download…');
    expect(downloadItem).toBeTruthy();
    downloadItem.click();
    await until(() => q('.ctx-menu'), 'attachments submenu for folder');
    const items = qa('.ctx-menu .ctx-item').map((b) => b.textContent);
    expect(items).toEqual(['Download', 'Download with attachments']);
    qa('.ctx-menu .ctx-item').find((b) => b.textContent === 'Download').click();
    await until(() => requests.some((r) => r.startsWith('GET /n/nb/export/selection?path=dir')), 'folder export requested');
  });

  test('a single note\'s context menu offers "Download as zip…" alongside the plain "Download .md"', async () => {
    requests.length = 0;
    node('a.md').closest('.row').dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }));
    await until(() => q('.ctx-menu'), 'note context menu');
    const labels = qa('.ctx-menu .ctx-item').map((b) => b.textContent);
    expect(labels).toContain('Download .md');
    expect(labels).toContain('Download as zip…');
    qa('.ctx-menu .ctx-item').find((b) => b.textContent === 'Download as zip…').click();
    await until(() => q('.ctx-menu'), 'attachments submenu for note');
    qa('.ctx-menu .ctx-item').find((b) => b.textContent === 'Download with attachments').click();
    await until(() => requests.some((r) => r === 'GET /n/nb/export/selection?path=a.md&attachments=1'), 'single-note attachments export requested');
  });
});
