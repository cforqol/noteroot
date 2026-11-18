/**
 * Regression test (#27, memory leak prevention): closing a Vim editing
 * session and opening a new one must not leave the previous session's
 * Ctrl+C/Ctrl+V clipboard listener attached to the shared #vim-input
 * element. Two separate bugs combined to cause this:
 *  - closeEditorNow() (a full close, e.g. the Done/closebtn path) never
 *    removed the listener at all — only a mid-session mode switch
 *    (vim -> textarea -> vim) ever ran the remove-before-add guard in
 *    openVimEditor, since that is the only case where the same
 *    `editing` object (and its `vimClipboardHandler` reference) spans
 *    both the old and new listener.
 *  - that remove-before-add guard itself was also broken: the listener
 *    is added with `{ capture: true }`, and removeEventListener must
 *    repeat that option to match and actually remove it — a bare
 *    removeEventListener(type, fn) call silently no-ops.
 * Either bug alone leaves a dead session's listener stacked on top of
 * the live one; because both run in the same capture phase on the same
 * element, registration order decides which one actually wins: the
 * stale listener fires first and swallows the event with
 * stopImmediatePropagation(), so the live session's own copy/paste
 * silently stops working in favor of a dead editor's stale content.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { VimWasm } from '../mocks/vimwasm.js';

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
  localStorage.setItem('nr.editorPref', 'vim');
  await import('../../app.js');
  await until(() => $('tree').querySelectorAll('.node').length > 0, 'initial tree');
});

test('reopening a Vim session does not stack clipboard listeners on #vim-input', async () => {
  const copied = [];
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async (text) => { copied.push(text); } },
  });

  // The clipboard listener is attached asynchronously, some time after
  // Vim becomes "running" (it's wired up only once editor.open()'s
  // promise settles, back in app.js's openVimEditor). Retry the
  // keystroke rather than guessing a fixed delay.
  const ctrlCUntilCopied = async (expectedLen) => {
    for (let i = 0; i < 200; i += 1) {
      $('vim-input').dispatchEvent(new KeyboardEvent('keydown', {
        key: 'c', ctrlKey: true, bubbles: true, cancelable: true,
      }));
      await sleep(5);
      if (copied.length >= expectedLen) return;
    }
    throw new Error(`timed out waiting for ${expectedLen} clipboard writes (got ${copied.length})`);
  };

  // First Vim session.
  $('tree').querySelector('[data-path="welcome.md"]').click();
  await until(() => !$('editbtn').hidden, 'note opened in read mode');
  $('editbtn').click();
  await until(() => VimWasm.last?.running, 'first vim session finished booting');
  const firstVim = VimWasm.last;

  await ctrlCUntilCopied(1);
  expect(copied[0]).toBe('hi there');

  // Close the session fully (not a mode switch) and reopen a new one.
  $('closebtn').click();
  await until(() => !$('editbtn').hidden, 'back in read mode');
  $('editbtn').click();
  await until(() => VimWasm.last !== firstVim && VimWasm.last.running, 'second vim session finished booting');

  // Simulate the user having typed something in this (second, live)
  // session: its buffer now differs from the first session's.
  VimWasm.last.buffer = 'edited in the second session';

  await ctrlCUntilCopied(2);
  // The second copy must be the live second session's content. A stale
  // listener left over from the first (closed, dead) session would run
  // first — same element, same capture phase, registration order —
  // call stopImmediatePropagation(), and copy the first session's
  // stale content instead, silently shadowing the current session's
  // own clipboard listener.
  expect(copied).toEqual(['hi there', 'edited in the second session']);
});
