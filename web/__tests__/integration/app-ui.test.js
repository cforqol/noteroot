/**
 * UI integration: the real index.html + app.js running in jsdom against
 * an in-memory fake of the noteroot HTTP API. Covers the tree (folders,
 * counts, collapse, decorations), note/folder CRUD through the dialogs,
 * the trash panel, the status bar, theme and tree placement.
 *
 * One app instance per file (ES modules are evaluated once), so the
 * tests form a single ordered scenario.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, '..', '..', 'index.html'), 'utf8');

// --- fake server -------------------------------------------------------

const notes = new Map([
  ['welcome.md', '---\nid: abc\n---\nHello brave new world\n\nSecond paragraph here.'],
  ['work/plan.md', 'plan one two three'],
  ['work/deep/x.md', 'x'],
]);
const folders = new Set(['work', 'work/deep', 'empty']);
const trash = [];
const requests = [];
// Git history for welcome.md, newest first, as the server would report it.
const HISTORY_COMMITS = [
  { hash: 'h1', shortHash: 'aaa1111', date: 1700000200, author: 'a', message: 'third change', isMerge: false },
  { hash: 'h2', shortHash: 'bbb2222', date: 1700000100, author: 'a', message: 'second change', isMerge: false },
  { hash: 'h3', shortHash: 'ccc3333', date: 1700000000, author: 'a', message: 'first change', isMerge: false },
];
// Per-request latency for GET /note/diff, consumed in order (see the
// stale-diff test: the first request must not outshine the second).
let diffDelays = [];
let retention = 0; // notebook trash retention, in days; 0 = never
const DAY = 86400;
const purgeDue = () => {
  let purged = 0;
  for (let i = trash.length - 1; i >= 0; i -= 1) {
    if (retention > 0 && trash[i].deletedAt < Date.now() / 1000 - retention * DAY) {
      trash.splice(i, 1);
      purged += 1;
    }
  }
  return purged;
};

function buildTree() {
  const root = { name: '', path: '', isDir: true, children: [] };
  const byPath = { '': root };
  const dirs = [...folders].sort();
  for (const d of dirs) {
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

// Like the real vault, writes create missing parent folders.
function addParents(path) {
  const parts = path.split('/').slice(0, -1);
  parts.forEach((_, i) => folders.add(parts.slice(0, i + 1).join('/')));
}

const sha = (p) => `sha-${p}-${notes.get(p)?.length}`;
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
  const p = u.searchParams.get('path');
  requests.push(`${method} ${u.pathname}${u.search}`);
  const body = init.body ? (init.headers?.['Content-Type'] === 'application/json' ? JSON.parse(init.body) : init.body) : null;
  const route = `${method} ${u.pathname}`;
  switch (route) {
    case 'GET /api/v1/notebooks': return respond(200, { notebooks: [{ slug: 'nb' }] });
    case 'GET /n/nb/tree': return respond(200, buildTree());
    case 'GET /n/nb/plugins/panels': return respond(200, {});
    case 'GET /n/nb/tags': return respond(200, { tags: { alpha: ['welcome.md'], beta: ['welcome.md', 'fresh-start.md'] } });
    case 'GET /n/nb/note':
      if (!notes.has(p)) return respond(404, { error: 'not found' });
      return respond(200, {
        // welcome.md carries headings (for the panel's table of contents),
        // tags and links; everything else renders as a single paragraph.
        html: `<p>${p}</p>` + (p === 'welcome.md'
          ? '<h1>Alpha heading</h1><h2>Beta heading</h2>' : ''),
        sha: sha(p),
        title: p,
        tags: p === 'welcome.md' ? ['alpha', '#beta'] : [],
        outgoing: p === 'welcome.md' ? [{ path: 'work/plan.md', label: 'the plan' }] : [],
        backlinks: p === 'welcome.md' ? [{ path: 'fresh-start.md', title: 'Fresh start' }] : [],
      });
    case 'GET /n/nb/raw':
      return notes.has(p) ? respond(200, notes.get(p)) : respond(404, { error: 'not found' });
    case 'PUT /n/nb/note':
      if (init.headers['If-None-Match'] === '*' && notes.has(p)) return respond(412, { error: 'precondition failed' });
      notes.set(p, body);
      addParents(p);
      return respond(200, { sha: sha(p) });
    case 'PATCH /n/nb/note':
      notes.set(body.to, notes.get(body.from));
      addParents(body.to);
      notes.delete(body.from);
      return respond(200, body);
    case 'DELETE /n/nb/note':
      trash.push({ name: `t-${trash.length}`, origPath: p, deletedAt: 1700000000 });
      notes.delete(p);
      return respond(200, { trashed: [] });
    case 'POST /n/nb/folder': folders.add(body.path); addParents(body.path); return respond(201, body);
    case 'PATCH /n/nb/folder': {
      for (const d of [...folders]) {
        if (d === body.from || d.startsWith(`${body.from}/`)) { folders.delete(d); folders.add(body.to + d.slice(body.from.length)); }
      }
      for (const n of [...notes.keys()]) {
        if (n.startsWith(`${body.from}/`)) { notes.set(body.to + n.slice(body.from.length), notes.get(n)); notes.delete(n); }
      }
      return respond(200, body);
    }
    case 'DELETE /n/nb/folder':
      for (const n of [...notes.keys()]) {
        if (n.startsWith(`${p}/`)) { trash.push({ name: `t-${trash.length}`, origPath: n, deletedAt: 1700000000 }); notes.delete(n); }
      }
      for (const d of [...folders]) if (d === p || d.startsWith(`${p}/`)) folders.delete(d);
      return respond(200, { path: p, trashed: [] });
    case 'GET /n/nb/trash':
      return respond(200, {
        entries: trash.map((t) => (retention > 0
          ? { ...t, expiresAt: t.deletedAt + retention * DAY } : { ...t })),
        retentionDays: retention,
      });
    case 'POST /n/nb/trash/restore': {
      const i = trash.findIndex((t) => t.name === u.searchParams.get('name'));
      const [e] = trash.splice(i, 1);
      notes.set(e.origPath, 'restored');
      return respond(200, { path: e.origPath });
    }
    case 'DELETE /n/nb/trash': {
      const name = u.searchParams.get('name');
      const i = trash.findIndex((t) => t.name === name);
      if (i >= 0) trash.splice(i, 1);
      return respond(200, { deleted: name });
    }
    case 'PUT /n/nb/trash/settings':
      retention = Number(body.retentionDays) || 0;
      return respond(200, { retentionDays: retention, purged: purgeDue() });
    case 'POST /n/nb/trash/purge': {
      if (u.searchParams.get('all') === '1') {
        const purged = trash.length;
        trash.length = 0;
        return respond(200, { purged });
      }
      return respond(200, { purged: purgeDue() });
    }
    case 'GET /n/nb/note/history':
      return respond(200, { commits: HISTORY_COMMITS, hasMore: false });
    case 'GET /n/nb/note/diff': {
      const ms = diffDelays.shift() || 0;
      if (ms) await sleep(ms);
      const from = u.searchParams.get('from');
      const to = u.searchParams.get('to');
      return respond(200, {
        diff: `diff --git a/${p} b/${p}\n--- a/${p}\n+++ b/${p}\n@@ -1 +1 @@\n-${from}\n+showing ${to}`,
      });
    }
    case 'GET /healthz':
      return respond(200, { status: 'ok', version: '1.2.3-test' });
    case 'GET /n/nb/resources/stats':
      return respond(200, {
        backend: 'local',
        journal: { committed: 3, pending: 1 },
        usedBytes: 12345,
        reservedBytes: 512,
        quotaBytes: 0,
      });
    case 'GET /n/nb/resources':
      return respond(200, [
        { sha: 'b'.repeat(64), filename: 'pic.png', mime: 'image/png', size: 2048 },
        { sha: 'c'.repeat(64), filename: 'paper.pdf', mime: 'application/pdf', size: 1048576 },
      ]);
    case 'POST /n/nb/resources/presign':
      return respond(200, { direct: true });
    case 'POST /n/nb/resources':
      return respond(201, {
        sha: 'a'.repeat(64),
        filename: u.searchParams.get('filename') || 'upload.bin',
        mime: body && typeof body === 'object' && body.type ? body.type : 'text/plain',
        created: true,
        size: 0,
      });
    default: return respond(200, {}); // locks etc.
  }
}

// --- helpers -----------------------------------------------------------

const $ = (id) => document.getElementById(id);
const q = (sel) => document.querySelector(sel);
const qa = (sel) => [...document.querySelectorAll(sel)];
const sleep = (ms = 0) => new Promise((r) => setTimeout(r, ms));
async function until(cond, label = 'condition') {
  for (let i = 0; i < 200; i += 1) {
    if (cond()) return;
    await sleep(5);
  }
  throw new Error(`timed out waiting for ${label}`);
}
const labels = () => qa('#tree .node .label').map((l) => l.textContent);
const node = (path) => q(`#tree [data-path="${path}"]`);
async function submitDialog(values = {}) {
  await until(() => q('.dialog form'), 'dialog');
  for (const [name, v] of Object.entries(values)) {
    const input = q(`.dialog [name="${name}"]`);
    if (input.type === 'checkbox') input.checked = v;
    else input.value = v;
  }
  q('.dialog form').dispatchEvent(new Event('submit', { cancelable: true }));
}
async function menuAction(path, label) {
  const row = node(path).parentElement;
  row.querySelector('.row-menu').click();
  await until(() => q('.ctx-menu'), 'context menu');
  [...qa('.ctx-menu .ctx-item')].find((b) => b.textContent === label).click();
}

beforeAll(async () => {
  document.documentElement.innerHTML = html.replace(/<script[\s\S]*?<\/script>/g, '');
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (media) => ({ matches: window.__phone === true, media, addEventListener() {}, removeEventListener() {} }),
  });
  globalThis.fetch = fakeFetch;
  window.fetch = fakeFetch;
  localStorage.setItem('nr.editorPref', 'textarea'); // jsdom has no vim.wasm
  await import('../../app.js');
  await until(() => qa('#tree .node').length > 0, 'initial tree');
});

// --- scenario ----------------------------------------------------------

// setup.js clears localStorage before every test; jsdom has no vim.wasm.
beforeEach(() => localStorage.setItem('nr.editorPref', 'textarea'));

describe('tree', () => {
  test('renders folders first with recursive note counts and a status line', () => {
    expect(labels()).toEqual(['empty', 'work', 'deep', 'x', 'plan', 'welcome']);
    expect(qa('#tree .node.dir .count').map((c) => c.textContent)).toEqual(['[0]', '[2]', '[1]']);
    expect($('tree-status').textContent).toBe('3 notes · 3 folders');
  });

  test('clicking a folder collapses it, persists, and updates nothing else', () => {
    node('work').click();
    expect(labels()).toEqual(['empty', 'work', 'welcome']);
    expect(JSON.parse(localStorage.getItem('nr.treeCollapsed')).nb).toEqual(['work']);
    expect($('tree-status').textContent).toBe('3 notes · 3 folders');
    node('work').click();
    expect(labels()).toContain('plan');
  });

  test('collapse all / expand all', () => {
    $('tree-collapse').click();
    expect(labels()).toEqual(['empty', 'work', 'welcome']);
    $('tree-expand').click();
    expect(labels()).toHaveLength(6);
  });

  test('opening a note via search-like navigation reveals collapsed ancestors', async () => {
    $('tree-collapse').click();
    node('welcome.md').click();
    await until(() => $('note').textContent.includes('welcome.md'), 'note view');
    $('tree-expand').click();
  });
});

describe('status bar', () => {
  test('reader mode shows source stats, size, and the Read mode', async () => {
    node('welcome.md').click();
    await until(() => $('sb-words').textContent !== '—', 'reader stats');
    expect($('sb-mode').textContent).toBe('Read');
    expect($('sb-words').textContent).toBe('7'); // frontmatter excluded
    expect($('sb-paras').textContent).toBe('2');
    expect($('sb-size').textContent).toMatch(/B$/);
    expect($('sb-pos').textContent).toBe('—');
  });

  test('plain editor: live words, selection, row:col, mode', async () => {
    $('editbtn').click();
    await until(() => !$('editor').hidden, 'editor');
    expect($('sb-mode').textContent).toBe('Plain');
    expect($('editormode').textContent).toBe('plain text');
    const ta = $('editbody');
    ta.value = 'one two\nthree four five';
    ta.setSelectionRange(8, 16); // "three fo"
    ta.dispatchEvent(new Event('input'));
    expect($('sb-words').textContent).toBe('5');
    expect($('sb-selected').textContent).toBe('2');
    expect($('sb-pos').textContent).toBe('2:9');
    ta.setSelectionRange(0, 0);
    ta.dispatchEvent(new Event('keyup'));
    expect($('sb-pos').textContent).toBe('1:1');
    expect($('sb-selected').textContent).toBe('0');
    expect($('sb-size').textContent).toBe('23 B');
  });

  test('closing the editor returns to Read mode', async () => {
    $('closebtn').click();
    await until(() => $('editor').hidden, 'editor closed');
    await until(() => $('sb-mode').textContent === 'Read', 'read mode');
  });
});

describe('plain-text Tab', () => {
  const tab = (shift = false) => {
    const e = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: shift, bubbles: true, cancelable: true });
    $('editbody').dispatchEvent(e);
    return e;
  };

  test('Tab inserts four spaces by default, Shift+Tab removes them', async () => {
    node('welcome.md').click();
    await until(() => !$('note').hidden, 'note open');
    $('editbtn').click();
    await until(() => !$('editor').hidden, 'editor');
    const ta = $('editbody');
    ta.value = 'one';
    ta.setSelectionRange(0, 0);
    expect(tab().defaultPrevented).toBe(true);
    expect(ta.value).toBe('    one');
    expect(ta.selectionStart).toBe(4);
    tab(true);
    expect(ta.value).toBe('one');
    $('closebtn').click();
    await until(() => $('editor').hidden, 'editor closed');
  });

  test('the focus preference leaves Tab to the browser', async () => {
    localStorage.setItem('nr.tabKey', 'focus');
    node('welcome.md').click();
    await until(() => !$('note').hidden, 'note open');
    $('editbtn').click();
    await until(() => !$('editor').hidden, 'editor');
    const ta = $('editbody');
    ta.value = 'one';
    ta.setSelectionRange(0, 0);
    expect(tab().defaultPrevented).toBe(false);
    expect(ta.value).toBe('one');
    $('closebtn').click();
    await until(() => $('editor').hidden, 'editor closed');
  });
});

describe('CRUD', () => {
  test('new note: validated dialog, create-only PUT, opens in the editor', async () => {
    $('new-note').click();
    await submitDialog({ name: '../escape' });
    expect(q('.dialog-error').hidden).toBe(false);
    await submitDialog({ name: 'journal/day one' });
    await until(() => notes.has('journal/day one.md'), 'note created');
    expect(requests).toContain('PUT /n/nb/note?path=journal%2Fday%20one.md');
    const fm = /^---\nid: [0-9A-HJKMNP-TV-Z]{28}\ntitle: day one\ncreated: \d{4}-\d{2}-\d{2}\nupdated: \d{4}-\d{2}-\d{2}\n---\n\n# day one\n\n$/;
    await until(() => fm.test(notes.get('journal/day one.md')), 'frontmatter auto-added');
    await until(() => labels().includes('day one'), 'tree refresh');
    await until(() => !$('editor').hidden, 'editor opened');
    expect($('editbody').value).toMatch(fm);
    $('closebtn').click();
    await until(() => $('editor').hidden);
  });

  test('creating an existing note reports a conflict and changes nothing', async () => {
    // Context folder is the open note's folder ("journal").
    const before = new Map(notes);
    $('new-note').click();
    await submitDialog({ name: 'day one' });
    await until(() => !$('toast').hidden, 'toast');
    expect($('toast').className).toContain('error');
    expect($('toast').textContent).toMatch(/already exists/);
    expect(notes).toEqual(before);
    expect($('editor').hidden).toBe(true);
  });

  test('new folder inside the selected folder', async () => {
    node('work').click(); // collapse + select
    node('work').click(); // expand again, still selected
    $('new-folder').click();
    await submitDialog({ name: 'ideas' });
    await until(() => folders.has('work/ideas'), 'folder created');
    await until(() => labels().includes('ideas'), 'tree refresh');
    expect($('tree-status').textContent).toMatch(/folders$/);
  });

  test('rename a note via the row menu', async () => {
    await menuAction('work/plan.md', 'Rename…');
    await submitDialog({ name: 'plan2' });
    await until(() => notes.has('work/plan2.md'), 'note renamed');
    await until(() => labels().includes('plan2'), 'tree shows renamed note');
    expect(notes.has('work/plan.md')).toBe(false);
  });

  test('move a note into another folder via the row menu', async () => {
    await menuAction('work/plan2.md', 'Move…');
    await submitDialog({ dest: 'work/ideas' });
    await until(() => notes.has('work/ideas/plan2.md'), 'note moved');
    await until(() => labels().includes('plan2'), 'tree shows moved note');
    expect(notes.has('work/plan2.md')).toBe(false);
  });

  test('folder icon & color are user-assigned, stored locally and rendered', async () => {
    await menuAction('empty', 'Icon & color…');
    await until(() => q('.icon-choice[data-icon="🌱"]'), 'icon picker');
    q('.icon-choice[data-icon="🌱"]').click();
    await submitDialog({ useColor: true, color: '#00ff00' });
    await until(() => node('empty').querySelector('.icon').textContent === '🌱', 'icon');
    expect(node('empty').querySelector('.label').style.color).toBe('rgb(0, 255, 0)');
    expect(JSON.parse(localStorage.getItem('nr.folderDeco')).nb.empty).toEqual({ emoji: '🌱', color: '#00ff00' });

    // Reset clears both.
    await menuAction('empty', 'Icon & color…');
    await until(() => q('.dialog'), 'dialog');
    q('.dialog button[data-action=reset]').click();
    await until(() => node('empty').querySelector('.icon').textContent === '📂', 'icon reset');
  });

  test('renaming a folder carries its decoration with it', async () => {
    await menuAction('empty', 'Icon & color…');
    await until(() => q('.icon-choice[data-icon="⭐"]'), 'icon picker');
    q('.icon-choice[data-icon="⭐"]').click();
    await submitDialog({ useColor: false });
    await until(() => node('empty').querySelector('.icon').textContent === '⭐');
    await menuAction('empty', 'Rename…');
    await submitDialog({ name: 'starred' });
    await until(() => node('starred'), 'renamed folder');
    expect(node('starred').querySelector('.icon').textContent).toBe('⭐');
  });

  test('delete note → trash panel → restore', async () => {
    await menuAction('welcome.md', 'Delete note…');
    await submitDialog();
    await until(() => !node('welcome.md'), 'note removed from tree');
    expect(requests).toContain('DELETE /n/nb/note?path=welcome.md');

    $('trash-btn').click();
    await until(() => qa('#trash-list li').length === 1, 'trash entry');
    expect(q('#trash-list .trash-path').textContent).toBe('welcome.md');
    // Each row offers Restore and a permanent Delete.
    expect(qa('#trash-list li button').map((b) => b.textContent)).toEqual(['Restore', 'Delete']);
    q('#trash-list button').click();
    await until(() => node('welcome.md'), 'note restored in tree');
    await until(() => q('#trash-list li.muted'), 'empty trash message');
    $('trash-close').click();
    expect($('trash-modal').hidden).toBe(true);
  });

  test('delete a folder moves its notes to the trash after confirmation', async () => {
    await menuAction('work', 'Delete folder…');
    await until(() => q('.dialog-message'), 'confirm');
    expect(q('.dialog-message').textContent).toMatch(/Move 2 notes/);
    await submitDialog();
    await until(() => !node('work'), 'folder gone');
    expect(trash.map((t) => t.origPath).sort()).toEqual(['work/deep/x.md', 'work/ideas/plan2.md']);
  });

  test('cancelling a destructive dialog changes nothing', async () => {
    const before = notes.size;
    await menuAction('welcome.md', 'Delete note…');
    await until(() => q('.dialog'));
    q('.dialog .modal-backdrop').click();
    await sleep(10);
    expect(notes.size).toBe(before);
  });
});

describe('theme', () => {
  test('header button cycles auto → light → dark → auto and persists', () => {
    const root = document.documentElement;
    expect($('themebtn').getAttribute('aria-label')).toBe('Theme: auto');
    $('themebtn').click();
    expect(root.dataset.theme).toBe('light');
    $('themebtn').click();
    expect(root.dataset.theme).toBe('dark');
    expect(localStorage.getItem('nr.theme')).toBe('dark');
    expect($('themebtn').getAttribute('aria-label')).toBe('Theme: dark');
    $('themebtn').click();
    expect(root.dataset.theme).toBeUndefined();
    expect(localStorage.getItem('nr.theme')).toBe('auto');
  });

  test('settings radio selects a theme', () => {
    const dark = q('input[name="theme"][value="dark"]');
    dark.checked = true;
    dark.dispatchEvent(new Event('change'));
    expect(document.documentElement.dataset.theme).toBe('dark');
    q('input[name="theme"][value="auto"]').checked = true;
    q('input[name="theme"][value="auto"]').dispatchEvent(new Event('change'));
    expect(document.documentElement.dataset.theme).toBeUndefined();
  });
});

describe('tree placement', () => {
  test('defaults to left; settings can move it right or hide it', () => {
    expect(document.body.dataset.tree).toBe('left');
    for (const v of ['right', 'hidden', 'left']) {
      const r = q(`input[name="tree-placement"][value="${v}"]`);
      r.checked = true;
      r.dispatchEvent(new Event('change'));
      expect(document.body.dataset.tree).toBe(v);
      expect(localStorage.getItem('nr.treePlacement')).toBe(v);
    }
  });

  test('☰ hides and restores the tree on desktop', () => {
    $('treebtn').click();
    expect(document.body.dataset.tree).toBe('hidden');
    expect($('treebtn').getAttribute('aria-expanded')).toBe('false');
    $('treebtn').click();
    expect(document.body.dataset.tree).toBe('left');
    expect($('treebtn').getAttribute('aria-expanded')).toBe('true');
  });

  test('settings modal reflects stored choices when opened', () => {
    localStorage.setItem('nr.treePlacement', 'right');
    localStorage.setItem('nr.theme', 'light');
    $('settingsbtn').click();
    expect(q('input[name="tree-placement"][value="right"]').checked).toBe(true);
    expect(q('input[name="theme"][value="light"]').checked).toBe(true);
    $('settings-close').click();
  });
});

describe('phone layout', () => {
  beforeAll(() => { window.__phone = true; });
  afterAll(() => { window.__phone = false; });

  test('☰ opens and closes the slide-in drawer instead of changing placement', () => {
    const placement = document.body.dataset.tree;
    const open = () => document.body.classList.contains('tree-open');
    const start = open();
    $('treebtn').click();
    expect(open()).toBe(!start);
    expect(document.body.dataset.tree).toBe(placement);
    expect($('treebtn').getAttribute('aria-expanded')).toBe(String(!start));
    $('treebtn').click();
    expect(open()).toBe(start);
  });

  test('the scrim closes the drawer', () => {
    $('treebtn').click();
    if (!document.body.classList.contains('tree-open')) $('treebtn').click();
    expect(document.body.classList.contains('tree-open')).toBe(true);
    $('tree-scrim').click();
    expect(document.body.classList.contains('tree-open')).toBe(false);
  });

  test('opening a note from the drawer closes it', async () => {
    document.body.classList.add('tree-open');
    node('welcome.md').click();
    await until(() => !document.body.classList.contains('tree-open'), 'drawer closed');
  });
});

describe('view transitions', () => {
  test('the Edit action shows only while reading, never while editing', async () => {
    node('welcome.md').click();
    await until(() => !$('note').hidden && !$('editbtn').hidden, 'reading');
    expect($('editor').hidden).toBe(true);

    $('editbtn').click();
    await until(() => !$('editor').hidden, 'editor');
    expect($('editbtn').hidden).toBe(true);
    expect($('notemenu').hidden).toBe(false); // note actions stay reachable

    $('closebtn').click();
    await until(() => !$('editbtn').hidden, 'edit action back');
    expect($('editor').hidden).toBe(true);
  });

  test('a note that auto-opens in the editor hides the Edit action', async () => {
    $('new-note').click();
    await submitDialog({ name: 'fresh-start' });
    await until(() => !$('editor').hidden, 'editor opened');
    expect($('editbtn').hidden).toBe(true);
    expect($('notemenu').hidden).toBe(false);
    $('closebtn').click();
    await until(() => !$('editbtn').hidden, 'edit action back');
  });
});

describe('attach', () => {
  const openEditor = async () => {
    if ($('editor').hidden) {
      $('editbtn').click();
      await until(() => !$('editor').hidden, 'editor');
    }
  };
  const openAttachMenu = async () => {
    $('attachbtn').click();
    await until(() => q('.ctx-menu'), 'attach menu');
    return qa('.ctx-menu .ctx-item');
  };
  const setField = (name, value) => {
    const el = q(`.dialog [name="${name}"]`);
    el.value = value;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  };
  const submitOpenDialog = () => {
    q('.dialog form').dispatchEvent(new Event('submit', { cancelable: true }));
  };

  test('the menu only exists while editing and validates URL links', async () => {
    node('welcome.md').click();
    await until(() => $('note').textContent.includes('welcome.md'), 'welcome open');
    expect($('editor').hidden).toBe(true);
    await openEditor();

    const items = await openAttachMenu();
    expect(items.map((b) => b.textContent)).toEqual([
      'Upload file…', 'Attach existing resource…', 'Insert link (URL)…', 'Link to note…',
    ]);

    items[2].click();
    await until(() => q('.dialog form'), 'url dialog');
    setField('url', 'javascript:alert(1)');
    submitOpenDialog();
    await until(() => !q('.dialog .dialog-error').hidden, 'scheme rejected');
    expect(q('.dialog .dialog-error').textContent).toMatch(/http, https or mailto/);

    setField('url', 'https://example.com/a b');
    setField('text', 'Example');
    expect(q('.dialog .dialog-preview').textContent).toBe('[Example](https://example.com/a%20b)');
    submitOpenDialog();
    await until(() => !q('.dialog'), 'dialog closed');
    expect($('editbody').value).toContain('[Example](https://example.com/a%20b)');
    $('closebtn').click();
    await until(() => !$('editbtn').hidden, 'saved and closed');
  });

  test('the note picker filters, resolves and inserts a wikilink', async () => {
    await openEditor();
    const items = await openAttachMenu();
    items[3].click();
    await until(() => q('.attach-note-list'), 'note dialog');

    // The tree at this point holds welcome.md and fresh-start.md (the
    // CRUD tests renamed and trashed the work notes).
    const target = q('.dialog [name="target"]');
    target.value = 'fresh-start';
    target.dispatchEvent(new Event('input', { bubbles: true }));
    expect(qa('.attach-note-item').map((b) => b.textContent)).toEqual(['fresh-start.md']);
    expect(q('.dialog .dialog-preview').textContent).toBe('[[fresh-start.md]]');

    qa('.attach-note-item')[0].click(); // pick fills the field (and preview)
    expect(target.value).toBe('fresh-start.md');

    setField('target', 'plan'); // nothing matches any more
    expect(qa('.attach-note-item')).toHaveLength(0);
    submitOpenDialog();
    await until(() => !q('.dialog .dialog-error').hidden, 'unresolvable rejected');

    setField('target', 'welcome'); // unique bare name resolves anywhere
    expect(q('.dialog .dialog-preview').textContent).toBe('[[welcome.md]]');
    setField('label', 'Hello');
    expect(q('.dialog .dialog-preview').textContent).toBe('[[welcome.md|Hello]]');
    submitOpenDialog();
    await until(() => !q('.dialog'), 'dialog closed');
    expect($('editbody').value).toContain('[[welcome.md|Hello]]');
    $('closebtn').click();
    await until(() => !$('editbtn').hidden, 'saved and closed');
  });

  test('attach existing resource opens the resource manager and inserts via its Insert button', async () => {
    node('welcome.md').click();
    await until(() => $('note').textContent.includes('welcome.md'), 'welcome open');
    await openEditor();
    const items = await openAttachMenu();
    expect(items.map((b) => b.textContent)).toEqual([
      'Upload file…', 'Attach existing resource…', 'Insert link (URL)…', 'Link to note…',
    ]);
    items[1].click(); // opens the attachments manager modal
    await until(() => !$('attachments-modal').hidden, 'attachments modal open');
    await until(() => qa('#attachments-grid .attach-card').length === 2, 'cards loaded');

    // The search box filters by name.
    $('attachments-search').value = 'pic';
    $('attachments-search').dispatchEvent(new Event('input'));
    expect(qa('#attachments-grid .attach-card').length).toBe(1);
    expect(q('#attachments-grid .attach-name').textContent).toBe('pic.png');

    // Clear the filter.
    $('attachments-search').value = '';
    $('attachments-search').dispatchEvent(new Event('input'));
    await until(() => qa('#attachments-grid .attach-card').length === 2, 'all cards back');

    // "Insert into note" inserts the markdown embed and does not upload.
    const picCard = [...qa('#attachments-grid .attach-card')]
      .find((c) => c.querySelector('.attach-name').textContent === 'pic.png');
    picCard.querySelector('button').click(); // first button is Insert into note
    await until(() => $('editbody').value.includes(`/n/nb/resources/${'b'.repeat(64)}`), 'link inserted');
    expect($('editbody').value).toContain(`![pic.png](/n/nb/resources/${'b'.repeat(64)})`);
    expect(requests.some((r) => r.startsWith('POST /n/nb/resources?'))).toBe(false);

    // Modal stays open; close it manually, then close the editor.
    $('attachments-close').click();
    await until(() => $('attachments-modal').hidden, 'modal closed');
    $('closebtn').click();
    await until(() => !$('editbtn').hidden, 'saved and closed');
  });

  test('uploading from the menu embeds a content-addressed link', async () => {
    await openEditor();
    const items = await openAttachMenu();
    items[0].click(); // "Upload file…" — opens the hidden file input
    await until(() => !q('.ctx-menu'), 'menu closed after select');

    const file = new File(['hello attachments'], 'note.txt', { type: 'text/plain' });
    const input = $('attach-file');
    Object.defineProperty(input, 'files', { value: [file], configurable: true });
    input.dispatchEvent(new Event('change'));

    const link = `[note.txt](/n/nb/resources/${'a'.repeat(64)})`;
    await until(() => $('editbody').value.includes(link), 'upload embedded');
    expect(requests).toContain('POST /n/nb/resources?filename=note.txt');
    expect($('savestate').textContent).toMatch(/Uploaded note\.txt/);
    $('closebtn').click();
    await until(() => !$('editbtn').hidden, 'saved and closed');
  });

  test('server storage rows count committed bytes and show the version', async () => {
    $('settingsbtn').click();
    await until(() => q('#settings-modal .settings-nav'), 'settings open');
    q('#settings-modal .settings-nav button[data-target="set-server"]').click();
    await until(() => /Uploading now/.test($('server-info').textContent), 'stats rows');

    const dds = qa('#server-info dd');
    const rows = Object.fromEntries(qa('#server-info dt').map((dt, i) => [dt.textContent, dds[i].textContent]));
    expect(rows['File storage']).toBe('Server disk');
    expect(rows['Uploads stored']).toBe('12 KB'); // committed only — reserved is not double-counted
    expect(rows['Uploading now']).toBe('512 B');
    expect(rows['Upload limit']).toBe('none (unlimited)');
    expect(rows['Unfinished uploads']).toBe('1'); // journal.pending — Number(map) was always 0
    expect(q('#set-server').textContent).toContain('NOTEROOT_QUOTA_BYTES');

    q('#settings-modal .settings-nav button[data-target="set-about"]').click();
    await until(() => $('about-version').textContent === '1.2.3-test', 'version in About');
    expect($('app-version').hidden).toBe(false);
    expect($('app-version').textContent).toBe('1.2.3-test');
    $('settings-close').click();
  });
});

describe('trash retention', () => {
  test('settings report the notebook retention and the trash size', async () => {
    $('settingsbtn').click();
    await until(() => /2 notes/.test($('trash-count').textContent), 'trash count');
    expect($('trash-retention').value).toBe('0'); // the server never set one
    expect($('retention-info').textContent).toMatch(/until you delete them/);
    expect($('trash-empty').disabled).toBe(false);
    $('settings-close').click();
  });

  test('a trashed note can be deleted permanently from the panel', async () => {
    $('trash-btn').click();
    // work/deep/x.md and work/ideas/plan2.md: each folder gets a header
    // row plus one nested note row.
    await until(() => qa('#trash-list li').length === 4, 'grouped entries');
    expect(q('#trash-list li.trash-folder .trash-path').textContent).toMatch(/work\/deep\//);
    expect(q('#trash-list .trash-due')).toBeNull(); // retention is "never"
    // The nested row under the first folder: Restore, Delete.
    const noteRow = q('#trash-list li.nested');
    expect(noteRow.querySelectorAll('button')[1].textContent).toBe('Delete');
    noteRow.querySelectorAll('button')[1].click();
    await until(() => q('.dialog'), 'confirm');
    await submitDialog();
    await until(() => trash.length === 1, 'one entry left');
    await until(() => qa('#trash-list li').length === 2, 'panel refreshed');
    $('trash-close').click();
  });

  test('choosing a retention persists it and sweeps what is already due', async () => {
    $('settingsbtn').click();
    await until(() => $('trash-retention').options.length > 1, 'menu ready');
    const sel = $('trash-retention');
    sel.value = '30';
    sel.dispatchEvent(new Event('change'));
    await until(() => retention === 30, 'retention saved');
    await until(() => trash.length === 0, 'stale entry swept');
    await until(() => /0 notes/.test($('trash-count').textContent), 'count refreshed');
    expect($('retention-info').textContent).toContain('30 days');
    $('settings-close').click();
  });
});

describe('tags', () => {
  test('the sidebar lists tags; one click lists its notes, another opens one', async () => {
    await until(() => qa('#tags-list .tag-item').length === 2, 'tags listed');
    expect($('tags-section').hidden).toBe(false);
    expect(qa('#tags-list .tag-item')[1].textContent).toBe('#beta2');
    qa('#tags-list .tag-item')[1].click();
    await until(() => qa('.dialog .attach-note-item').length === 2, 'tag notes');
    qa('.dialog .attach-note-item')[1].click();
    await until(() => $('note').textContent.includes('fresh-start'), 'note opened from tag');
    expect(q('.dialog')).toBeNull();
  });

  test('the note panel lists the note contents, tags and links; a chip lists the notes with that tag', async () => {
    node('welcome.md').click();
    await until(() => qa('#readpane .note-tags .tag-chip').length === 2, 'chips');
    // The reading column carries only the note itself.
    expect(q('#note .note-tags')).toBeNull();
    expect(q('#note .links-panel')).toBeNull();
    // Contents, from the rendered headings.
    expect(q('#rp-toc').hidden).toBe(false);
    expect(qa('#rp-toc a').map((a) => a.textContent)).toEqual(['Alpha heading', 'Beta heading']);
    // Tags and links live in the panel too.
    expect(q('#rp-tags').hidden).toBe(false);
    expect(qa('#rp-tags .tag-chip').map((c) => c.textContent)).toEqual(['#alpha', '#beta']);
    expect(q('#rp-out').hidden).toBe(false);
    expect(q('#rp-out .rp-list').textContent).toContain('the plan');
    expect(q('#rp-back').hidden).toBe(false);
    expect(q('#rp-back .rp-list').textContent).toContain('Fresh start');
    // The panel can be hidden and shown from the top bar.
    $('readpanebtn').click();
    expect($('readpane').hidden).toBe(true);
    $('readpanebtn').click();
    expect($('readpane').hidden).toBe(false);
    qa('#readpane .tag-chip')[1].click();
    await until(() => qa('.dialog .attach-note-item').length === 2, 'tag notes');
    q('.dialog .dialog-buttons button:last-child').click();
    node('fresh-start.md').click();
    await until(() => $('note').textContent.includes('fresh-start'), 'untagged note');
    expect(q('#note .note-tags')).toBeNull();
    expect(q('#rp-tags').hidden).toBe(true); // nothing to show for tags
  });

  test('the Copy button puts the note body on the clipboard, frontmatter left out', async () => {
    let copied = null;
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async (t) => { copied = t; } },
    });

    // Earlier tests overwrite welcome.md; give it frontmatter again so
    // the assertion below is about this note, not about leftover state.
    notes.set('welcome.md', '---\nid: abc\n---\nHello brave new world\n\nSecond paragraph here.');
    node('welcome.md').click();
    await until(() => !$('copybtn').hidden && !$('note').hidden, 'reading with copy button');
    $('copybtn').click();
    await until(() => copied !== null, 'copied to the clipboard');

    expect(copied.startsWith('---')).toBe(false);
    expect(copied).not.toContain('id: abc');
    expect(copied.trim()).toBe('Hello brave new world\n\nSecond paragraph here.');
    await until(() => !$('toast').hidden, 'toast shown');
    expect($('toast').textContent).toContain('Note copied');

    // The button belongs to read mode only.
    $('editbtn').click();
    await until(() => !$('editor').hidden, 'editor open');
    expect($('copybtn').hidden).toBe(true);
    $('closebtn').click();
    await until(() => !$('copybtn').hidden, 'back in read mode');

    // Hand the suite on with a note open, as the neighbouring tests
    // expect: an editor action taken before the next note finishes
    // opening would abort against the stale path.
    node('fresh-start.md').click();
    await until(() => $('note').textContent.includes('fresh-start'), 'next note open');
  });

  test('saving a note (autosave or manual) refreshes the tag list', async () => {
    await until(() => qa('#tags-list .tag-item').length === 2, 'tags listed');
    node('fresh-start.md').click();
    await until(() => !$('editbtn').hidden, 'reading');
    const before = requests.filter((r) => r === 'GET /n/nb/tags').length;
    $('editbtn').click();
    await until(() => !$('editor').hidden, 'editor open');
    $('editbody').value += '\ntags: [new]';
    $('editbody').dispatchEvent(new Event('input', { bubbles: true }));
    $('savebtn').click();
    await until(() => requests.filter((r) => r === 'GET /n/nb/tags').length > before, 'tags refetched');
    $('closebtn').click();
    await until(() => !$('editbtn').hidden, 'closed');
  });

  test('the Tags header folds the list away and remembers it', async () => {
    await until(() => qa('#tags-list .tag-item').length === 2, 'tags listed');
    $('tags-toggle').click();
    expect($('tags-list').hidden).toBe(true);
    expect(localStorage.getItem('nr.tagsCollapsed')).toBe('1');
    $('tags-toggle').click();
    expect($('tags-list').hidden).toBe(false);
  });
});

describe('homepage', () => {
  // localStorage is cleared before every test (setup.js), so the whole
  // homepage flow lives in one test.
  const esc = () => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

  test('a configured homepage opens after Esc from any note, until cleared', async () => {
    // Adopt the open note as homepage via Settings.
    node('welcome.md').click();
    await until(() => $('note').textContent.includes('welcome.md'), 'welcome open');
    $('settingsbtn').click();
    const nav = q('#settings-modal .settings-nav button[data-target="set-homepage"]');
    expect(nav).toBeTruthy();
    nav.click();
    await until(() => $('homepage-path').disabled === false, 'homepage section ready');
    expect($('homepage-path').value).toBe('_blank');
    $('homepage-use-current').click();
    expect($('homepage-path').value).toBe('welcome.md');
    $('settings-close').click();

    // Esc while ON the homepage is a no-op.
    esc();
    await sleep(30);
    expect($('note').textContent).toContain('welcome.md');

    // Esc from another note falls back to the homepage. (The work folder
    // was deleted by the trash tests; fresh-start.md is root-level.)
    node('fresh-start.md').click();
    await until(() => $('note').textContent.includes('fresh-start'), 'other note open');
    esc();
    await until(() => $('note').textContent.includes('welcome.md'), 'homepage reopened');
    expect(node('welcome.md').classList.contains('active')).toBe(true);

    // A loose path (no .md, other case) still finds the note.
    $('settingsbtn').click();
    nav.click();
    $('homepage-path').value = 'WELCOME';
    $('homepage-path').dispatchEvent(new Event('change'));
    $('settings-close').click();
    node('fresh-start.md').click();
    await until(() => $('note').textContent.includes('fresh-start'), 'other note for loose homepage');
    esc();
    await until(() => $('note').textContent.includes('welcome.md'), 'loose homepage reopened');

    // A stale homepage path falls back to the empty state.
    $('settingsbtn').click();
    nav.click();
    $('homepage-path').value = 'does/not/exist.md';
    $('homepage-path').dispatchEvent(new Event('change'));
    $('settings-close').click();
    node('fresh-start.md').click();
    await until(() => $('note').textContent.includes('fresh-start'), 'other note open again');
    esc();
    await until(() => $('note').textContent.includes('Nothing open'), 'empty fallback');

    // Clearing the homepage restores the plain empty state.
    $('settingsbtn').click();
    nav.click();
    $('homepage-clear').click();
    expect($('homepage-path').value).toBe('_blank');
    // Typing the sentinel (or nothing) is also a clear.
    $('homepage-path').value = 'welcome.md';
    $('homepage-path').dispatchEvent(new Event('change'));
    expect($('homepage-path').value).toBe('welcome.md');
    $('homepage-path').value = '_blank';
    $('homepage-path').dispatchEvent(new Event('change'));
    expect($('homepage-path').value).toBe('_blank');
    $('settings-close').click();
    node('welcome.md').click();
    await until(() => $('note').textContent.includes('welcome.md'), 'welcome again');
    esc();
    await until(() => $('note').textContent.includes('Nothing open'), 'plain empty state');
  });
});

describe('sidebar multi-select', () => {
  test('Ctrl-click picks notes and the bar deletes the picked ones', async () => {
    const click = (path, opts = {}) => node(path).dispatchEvent(new MouseEvent('click', { bubbles: true, ...opts }));
    const picks = () => [...qa('#tree .node.multi')].map((n) => n.dataset.path);
    expect($('multi-bar').hidden).toBe(true);

    // Pick two existing notes: plain clicking elsewhere clears the picks
    // without opening anything.
    const [a, b] = [...qa('#tree .node:not(.dir)')].slice(0, 2).map((n) => n.dataset.path);
    click(a, { ctrlKey: true });
    expect($('multi-bar').hidden).toBe(false);
    expect($('multi-count').textContent).toBe('1 selected');
    click(b, { ctrlKey: true });
    expect(picks()).toEqual([a, b]);
    click(b, { ctrlKey: true }); // toggles off
    expect(picks()).toEqual([a]);
    click(a, { ctrlKey: true });
    expect($('multi-bar').hidden).toBe(true);

    // Shift-click fills a range from the anchor.
    click(a, { ctrlKey: true });
    const order = [...qa('#tree .node:not(.dir)')].map((n) => n.dataset.path);
    click(order[order.length - 1], { shiftKey: true });
    expect(picks().length).toBeGreaterThan(1);
    $('multi-clear').click();
    expect($('multi-bar').hidden).toBe(true);

    // Bulk delete uses the regular delete API, one note at a time.
    const make = async (name) => {
      $('new-note').click();
      await submitDialog({ name });
      await until(() => !$('editor').hidden, 'new note editor');
      $('closebtn').click();
      await until(() => $('editor').hidden, 'editor closed');
    };
    await make('multi-one');
    await make('multi-two');
    const find = (name) => [...qa('#tree .node')].find((n) => n.dataset.path.endsWith(name))?.dataset.path;
    await until(() => find('multi-one.md') && find('multi-two.md'), 'new notes in tree');
    const one = find('multi-one.md');
    const two = find('multi-two.md');
    click(one, { ctrlKey: true });
    click(two, { ctrlKey: true });
    expect($('multi-count').textContent).toBe('2 selected');
    $('multi-delete').click();
    await until(() => q('.dialog .btn[type="submit"]'), 'delete confirm');
    expect(q('.dialog').textContent).toContain('Delete 2 notes');
    q('.dialog .btn[type="submit"]').click();
    await until(() => !node(one) && !node(two), 'picked notes trashed');
    expect(requests).toContain(`DELETE /n/nb/note?path=${encodeURIComponent(one)}`);
    expect(requests).toContain(`DELETE /n/nb/note?path=${encodeURIComponent(two)}`);
    await until(() => $('multi-bar').hidden, 'bar cleared after delete');
  });

  test('Move… button moves selected notes via formDialog', async () => {
    const click = (path, opts = {}) => node(path).dispatchEvent(new MouseEvent('click', { bubbles: true, ...opts }));

    // Create two fresh notes to move
    const make = async (name) => {
      $('new-note').click();
      await submitDialog({ name });
      await until(() => !$('editor').hidden, 'new note editor');
      $('closebtn').click();
      await until(() => $('editor').hidden, 'editor closed');
    };
    await make('move-a');
    await make('move-b');
    const find = (name) => [...qa('#tree .node')].find((n) => n.dataset.path.endsWith(name))?.dataset.path;
    await until(() => find('move-a.md') && find('move-b.md'), 'new notes in tree');
    const pathA = find('move-a.md');
    const pathB = find('move-b.md');
    const before = requests.filter((r) => r.startsWith('PATCH /n/nb/note')).length;

    // Multi-select both
    click(pathA, { ctrlKey: true });
    click(pathB, { ctrlKey: true });
    expect($('multi-count').textContent).toBe('2 selected');
    expect($('multi-bar').hidden).toBe(false);

    // Click Move… — dialog should appear
    $('multi-move').click();
    await until(() => q('.dialog'), 'move dialog');
    expect(q('.dialog').textContent).toContain('Move 2 notes');

    // Pick a destination folder from the select, then submit
    const sel = q('.dialog select[name="dest"]');
    expect(sel).toBeTruthy();
    // Use newFolder to create a new destination
    q('.dialog input[name="newFolder"]').value = 'archive';
    q('.dialog input[name="newFolder"]').dispatchEvent(new Event('input', { bubbles: true }));
    q('.dialog .btn[type="submit"]').click();

    await until(() => !q('.dialog'), 'dialog dismissed');
    await until(() => $('multi-bar').hidden, 'bar cleared after move');

    // Both notes should have been moved (PATCH requests issued)
    const patches = requests.filter((r) => r.startsWith('PATCH /n/nb/note'));
    expect(patches.length).toBe(before + 2);
    expect(node('archive/move-a.md')).toBeTruthy();
    expect(node('archive/move-b.md')).toBeTruthy();
  });

  test('row-menu Move… moves the whole multi-selection, not just the clicked note', async () => {
    const click = (path, opts = {}) => node(path).dispatchEvent(new MouseEvent('click', { bubbles: true, ...opts }));
    const before = requests.filter((r) => r.startsWith('PATCH /n/nb/note')).length;

    // Pick the two notes the previous test moved into archive/
    const pathA = 'archive/move-a.md';
    const pathB = 'archive/move-b.md';
    expect(node(pathA)).toBeTruthy();
    expect(node(pathB)).toBeTruthy();
    click(pathA, { ctrlKey: true });
    click(pathB, { ctrlKey: true });
    expect($('multi-count').textContent).toBe('2 selected');

    // Right-click one of them: the menu must offer a bulk move
    await menuAction(pathB, 'Move 2 notes…');
    await until(() => q('.dialog'), 'move dialog');
    expect(q('.dialog').textContent).toContain('Move 2 notes');
    q('.dialog input[name="newFolder"]').value = 'round2';
    q('.dialog input[name="newFolder"]').dispatchEvent(new Event('input', { bubbles: true }));
    q('.dialog .btn[type="submit"]').click();

    await until(() => !q('.dialog'), 'dialog dismissed');
    await until(() => node('round2/move-a.md') && node('round2/move-b.md'), 'both notes in round2');
    expect(requests.filter((r) => r.startsWith('PATCH /n/nb/note')).length).toBe(before + 2);
    expect(node(pathA)).toBeFalsy();
    expect(node(pathB)).toBeFalsy();
    await until(() => $('multi-bar').hidden, 'bar cleared after menu move');
  });

  test('bulk move refuses same-name collisions instead of moving half', async () => {
    const click = (path, opts = {}) => node(path).dispatchEvent(new MouseEvent('click', { bubbles: true, ...opts }));

    // Create starred/move-a.md so it collides with the existing round2/move-a.md
    // when both are targeted at the notebook root. Clicking the folder pins
    // contextDir() for the new-note dialog.
    node('starred').click();
    $('new-note').click();
    await submitDialog({ name: 'move-a' });
    await until(() => !$('editor').hidden, 'new note editor');
    $('closebtn').click();
    await until(() => $('editor').hidden, 'editor closed');
    await until(() => node('starred/move-a.md') && node('round2/move-a.md'), 'both move-a notes in tree');

    click('starred/move-a.md', { ctrlKey: true });
    click('round2/move-a.md', { ctrlKey: true });
    expect($('multi-count').textContent).toBe('2 selected');
    const before = requests.filter((r) => r.startsWith('PATCH /n/nb/note')).length;

    // Destination: notebook root (the select's default). Both would become
    // "move-a.md", so the plan must refuse rather than move just one.
    $('multi-move').click();
    await until(() => q('.dialog'), 'move dialog');
    q('.dialog .btn[type="submit"]').click();
    await until(() => !q('.dialog .dialog-error').hidden, 'validation error shown');
    expect(q('.dialog .dialog-error').textContent).toContain('both named');
    expect(q('.dialog')).toBeTruthy(); // dialog stays open
    expect(requests.filter((r) => r.startsWith('PATCH /n/nb/note')).length).toBe(before);

    // Cancel, drop one of the twins, and reopen: with a single distinct
    // target the move goes through and only that note moves.
    [...qa('.dialog .btn')].find((b) => b.textContent === 'Cancel').click();
    await until(() => !q('.dialog'), 'dialog cancelled');
    click('starred/move-a.md', { ctrlKey: true }); // toggle off
    expect($('multi-count').textContent).toBe('1 selected');
    $('multi-move').click();
    await until(() => q('.dialog'), 'move dialog reopened');
    q('.dialog .btn[type="submit"]').click();
    await until(() => !q('.dialog'), 'dialog dismissed');
    await until(() => !node('round2/move-a.md'), 'round2/move-a.md moved');
    expect(node('move-a.md')).toBeTruthy(); // now at the root
    expect(node('starred/move-a.md')).toBeTruthy(); // untouched
    expect(requests.filter((r) => r.startsWith('PATCH /n/nb/note')).length).toBe(before + 1);
    await until(() => $('multi-bar').hidden, 'bar cleared');
  });
});

describe('line-number gutter', () => {
  const gutterLines = (id) => [...qa(`#${id} .ln`)].map((s) => s.textContent);

  test('the plain editor gutter tracks the line count as you edit', async () => {
    $('new-note').click();
    await submitDialog({ name: 'gutter-note' });
    await until(() => !$('editor').hidden, 'editor opened');
    const ta = $('editbody');
    ta.value = 'one\ntwo\nthree';
    ta.dispatchEvent(new Event('input'));
    expect(gutterLines('editgutter')).toEqual(['1', '2', '3']);
    ta.value = 'one\ntwo';
    ta.dispatchEvent(new Event('input'));
    expect(gutterLines('editgutter')).toEqual(['1', '2']);
    ta.value = 'one\ntwo\nthree\nfour\nfive';
    ta.dispatchEvent(new Event('input'));
    expect(gutterLines('editgutter')).toEqual(['1', '2', '3', '4', '5']);
    $('closebtn').click();
    await until(() => $('editor').hidden, 'editor closed');
  });
});

describe('paste image upload', () => {
  const pasteImage = (target, name = 'shot.png') => {
    const file = new File(['\x89PNG'], name, { type: 'image/png' });
    const item = { type: 'image/png', getAsFile: () => file };
    const e = new Event('paste', { bubbles: true, cancelable: true });
    e.clipboardData = { items: [item] };
    target.dispatchEvent(e);
    return e;
  };

  test('pasting a clipboard image into the editor uploads and embeds it', async () => {
    $('new-note').click();
    await submitDialog({ name: 'paste-note' });
    await until(() => !$('editor').hidden, 'editor opened');
    pasteImage($('editbody'));
    const link = `![shot.png](/n/nb/resources/${'a'.repeat(64)})`;
    await until(() => $('editbody').value.includes(link), 'image embedded');
    expect(requests).toContain('POST /n/nb/resources?filename=shot.png');
    $('closebtn').click();
    await until(() => $('editor').hidden, 'editor closed');
  });
});

describe('history window', () => {
  const diffText = () => $('history-diff').textContent;
  async function openHistory() {
    node('welcome.md').click();
    await until(() => !$('historybtn').hidden, 'history button in read mode');
    $('historybtn').click();
    await until(() => qa('#history-list .history-item').length === 3, 'commit list');
    await until(() => diffText().includes('showing h1'), 'newest diff shown');
  }

  test('clicking a row opens that commit, not the newest one', async () => {
    await openHistory();
    expect($('history-modal').hidden).toBe(false);
    expect($('history-position').textContent).toBe('1 of 3');
    expect(q('#history-list .history-item.active').dataset.index).toBe('0');

    qa('#history-list .history-item')[1].click();
    await until(() => diffText().includes('showing h2'), 'middle diff');
    expect($('history-position').textContent).toBe('2 of 3');
    expect(q('#history-list .history-item.active').dataset.index).toBe('1');

    qa('#history-list .history-item')[2].click();
    await until(() => diffText().includes('showing h3'), 'oldest diff');
    expect($('history-position').textContent).toBe('3 of 3');
    expect(qa('#history-list .history-item.active').map((li) => li.dataset.index)).toEqual(['2']);
    // the diff is rendered as coloured lines, not raw text
    expect(qa('#history-diff .diff-add').map((d) => d.textContent)).toContain('+showing h3');
    expect(qa('#history-diff .diff-del').map((d) => d.textContent)).toContain('-h3^');
    expect(qa('#history-diff .diff-hunk').length).toBe(1);

    // the ◀/▶ buttons walk on from where the click landed
    $('history-newer').click();
    await until(() => diffText().includes('showing h2'), 'newer diff');
    expect($('history-position').textContent).toBe('2 of 3');
    expect($('history-newer').disabled).toBe(false);
    expect($('history-older').disabled).toBe(false);

    $('history-close').click();
    expect($('history-modal').hidden).toBe(true);
  });

  test('a slow diff for an older click never overwrites a newer one', async () => {
    await openHistory();
    diffDelays = [80, 0]; // the first request is the slow one
    qa('#history-list .history-item')[0].click();
    await sleep(5);
    qa('#history-list .history-item')[1].click();
    await until(() => diffText().includes('showing h2'), 'newer selection rendered');
    await sleep(140); // the stale response lands now and must be dropped
    expect(diffText()).toContain('showing h2');
    expect(diffText()).not.toContain('showing h1');
    expect($('history-position').textContent).toBe('2 of 3');
    diffDelays = [];
    $('history-close').click();
  });
});
