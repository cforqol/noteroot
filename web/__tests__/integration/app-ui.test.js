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
    case 'GET /n/nb/note':
      if (!notes.has(p)) return respond(404, { error: 'not found' });
      return respond(200, { html: `<p>${p}</p>`, sha: sha(p), title: p, outgoing: [], backlinks: [] });
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
    await submitDialog({ emoji: '🌱', useColor: true, color: '#00ff00' });
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
    await submitDialog({ emoji: '⭐', useColor: false, color: '#000000' });
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

describe('trash retention', () => {
  test('settings report the notebook retention and the trash size', async () => {
    $('settingsbtn').click();
    await until(() => /2 notes/.test($('trash-count').textContent), 'trash count');
    expect($('trash-retention').value).toBe('0'); // the server never set one
    expect($('retention-info').textContent).toMatch(/until you delete them/);
    expect($('trash-purge-now').disabled).toBe(true); // nothing auto-expires
    expect($('trash-empty').disabled).toBe(false);
    $('settings-close').click();
  });

  test('a trashed note can be deleted permanently from the panel', async () => {
    $('trash-btn').click();
    await until(() => qa('#trash-list li').length === 2, 'entries');
    expect(q('#trash-list .trash-due')).toBeNull(); // retention is "never"
    qa('#trash-list li')[0].querySelectorAll('button')[1].click(); // Delete
    await until(() => q('.dialog'), 'confirm');
    await submitDialog();
    await until(() => trash.length === 1, 'one entry left');
    await until(() => qa('#trash-list li').length === 1, 'panel refreshed');
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
