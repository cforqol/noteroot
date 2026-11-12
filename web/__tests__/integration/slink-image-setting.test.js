/**
 * Integration test for #16: Slink being configured server-side must
 * not force every user's image uploads through it — Settings →
 * Integrations → "Use Slink for image uploads" is the opt-out, default
 * on (matching the previous, setting-less behaviour). Own fake server
 * + own app.js instance: attaching a file needs an open editor, which
 * is cheaper to set up fresh than to borrow from another scenario.
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
async function fakeFetch(url, init = {}) {
  const u = new URL(url, 'http://x');
  const method = (init.method || 'GET').toUpperCase();
  const route = `${method} ${u.pathname}`;
  requests.push(`${route}${u.search}`);
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, buildTree());
    case 'GET /n/nb/note': return respond(200, { html: '<p>hi</p>', sha: 's1', title: 'welcome' });
    case 'GET /n/nb/raw': return respond(200, 'hi there');
    // Slink is configured server-side for this whole file.
    case 'GET /api/v1/integrations/status': return respond(200, { slink: true, karakeep: false, s3: false });
    case 'POST /api/v1/integrations/slink/upload': return respond(200, { url: 'https://slink.example/img/abc.png' });
    case 'POST /n/nb/resources': return respond(201, { sha: 'b'.repeat(64), filename: 'pic.png', created: true });
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

let setSetting;
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
  ({ setSetting } = await import('../../lib/prefs.js'));
  await import('../../app.js');
  await until(() => $('tree').querySelectorAll('.node').length > 0, 'initial tree');
  $('tree').querySelector('[data-path="welcome.md"]').click();
  await until(() => !$('editbtn').hidden, 'note opened in read mode');
  $('editbtn').click();
  await until(() => !$('editor').hidden, 'editor opened');
});

function attachImage(name) {
  const file = new File(['pixels'], name, { type: 'image/png' });
  const input = $('attach-file');
  Object.defineProperty(input, 'files', { value: [file], configurable: true });
  input.dispatchEvent(new Event('change'));
}

describe('Slink image upload opt-out (#16)', () => {
  test('default (setting unset) routes an image through Slink when it is configured', async () => {
    attachImage('a.png');
    await until(() => requests.includes('POST /api/v1/integrations/slink/upload'), 'Slink upload request');
    await until(() => $('editbody').value.includes('https://slink.example/img/abc.png'), 'Slink link embedded');
    expect(requests).not.toContain('POST /n/nb/resources?filename=a.png');
  });

  test('turning the setting off falls back to the notebook\'s own resource storage', async () => {
    setSetting('useSlinkForImages', 'off');
    requests.length = 0;
    attachImage('b.png');
    await until(() => requests.includes('POST /n/nb/resources?filename=b.png'), 'local resource upload request');
    expect(requests).not.toContain('POST /api/v1/integrations/slink/upload');
    await until(() => $('editbody').value.includes(`/n/nb/resources/${'b'.repeat(64)}`), 'local resource link embedded');
  });

  test('turning it back on resumes routing through Slink', async () => {
    setSetting('useSlinkForImages', 'on');
    requests.length = 0;
    attachImage('c.png');
    await until(() => requests.includes('POST /api/v1/integrations/slink/upload'), 'Slink upload request again');
    expect(requests).not.toContain('POST /n/nb/resources?filename=c.png');
  });
});
