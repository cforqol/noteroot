/**
 * Integration test: the sidebar's locked-notebook placeholder, and what
 * happens to the sections it hides (daily notes, tags) once the
 * notebook is unlocked. Own fake server + own app.js instance, kept out
 * of app-ui.test.js's larger ordered scenario since this exercises a
 * different lifecycle (boot while locked, then unlock).
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

// The notebook starts locked (and not yet unlocked this session); the
// unlock request flips `unlocked` to true, same as the real server.
const lockState = { locked: true, unlocked: false, exact: true };

function buildTree() {
  return {
    name: '', path: '', isDir: true,
    children: [
      { name: 'daily', path: 'daily', isDir: true, children: [
        { name: 'today.md', path: 'daily/today.md', isDir: false, size: 1 },
      ] },
      { name: 'welcome.md', path: 'welcome.md', isDir: false, size: 1 },
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
    // nb2 is locked too, but never switched to in this file — its entry
    // must come from the listing response alone (see "an inactive but
    // already-locked notebook" below), not a live per-notebook query.
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb', locked: true }, { slug: 'nb2', locked: true }] });
    case 'GET /n/nb/tree': return respond(200, buildTree());
    case 'GET /n/nb/contentlock': return respond(200, lockState);
    case 'POST /n/nb/contentlock/unlock':
      lockState.unlocked = true;
      return respond(200, {});
    case 'GET /n/nb/tags': return respond(200, { tags: {} });
    default: return respond(200, {});
  }
}

const $ = (id) => document.getElementById(id);
const q = (sel) => document.querySelector(sel);
const sleep = (ms = 0) => new Promise((r) => setTimeout(r, ms));
async function until(cond, label = 'condition') {
  for (let i = 0; i < 200; i += 1) {
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
  // jsdom has no ResizeObserver; app.js's topbar search-compact watcher
  // only needs the no-op shape.
  global.ResizeObserver = window.ResizeObserver = class {
    observe() {} unobserve() {} disconnect() {}
  };
  globalThis.fetch = fakeFetch;
  window.fetch = fakeFetch;
  localStorage.setItem('nr.editorPref', 'textarea');
  await import('../../app.js');
  await until(() => !$('tree-locked').hidden, 'locked placeholder on boot');
});

describe('locked notebook', () => {
  test('boots straight into the locked placeholder, tree and daily notes hidden', () => {
    expect($('tree-locked').hidden).toBe(false);
    expect($('tree').hidden).toBe(true);
    expect($('daily-notes-section').hidden).toBe(true);
  });

  test('an inactive but already-locked notebook shows 🔒 in the dropdown without switching to it', () => {
    const opt = [...$('notebooks').options].find((o) => o.value === 'nb2');
    expect(opt.textContent).toBe('nb2 🔒');
  });

  test('unlocking restores the tree and re-populates the daily notes section', async () => {
    $('tree-unlock-btn').click();
    await until(() => q('.dialog form'), 'unlock dialog');
    q('.dialog [name="password"]').value = 'secret';
    q('.dialog form').dispatchEvent(new Event('submit', { cancelable: true }));

    await until(() => $('tree-locked').hidden, 'placeholder to clear');
    expect($('tree').hidden).toBe(false);

    // The real assertion: daily notes only shows real content once
    // refreshDailyNotes() has actually run against the unlocked tree —
    // a stale `hidden` flag left over from boot could not produce this.
    await until(
      () => !$('daily-notes-section').hidden && $('daily-notes-list').textContent.includes('today'),
      'daily notes section to repopulate after unlock',
    );
  });
});
