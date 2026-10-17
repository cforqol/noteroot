import { VimWasm, checkBrowserCompatibility } from '/vim/vimwasm.js';
import { uploadURL, markdownFor, rewriteImageThumbs, presignUpload } from '/lib/upload.js';
import {
  getVimModeState,
  recordVimFailure,
  shouldUseVim as shouldUseVimCore,
  resetVimCircuitBreaker,
  getEditorPreference,
  setEditorPreference,
} from '/lib/vim-mode.js';
import { fetchPanels, renderPanelChips } from '/lib/panels.js';
import { describeStatus } from '/lib/stats.js';
function shouldUseVim() {
  return shouldUseVimCore({ checkCompatibility: () => checkBrowserCompatibility() });
}
import { VimEditor } from '/lib/vim-editor.js';
import {
  normalizeNote, changedRange, readFrontmatter, deriveTitle, localDate, isValidId,
} from '/lib/frontmatter.js';
import { indentEdit } from '/lib/textedit.js';
import {
  formatTreeStatus, folderPaths, parentDir, joinPath, ancestors, isWithin,
  cleanRelPath, resolveChild, newNoteContent, baseName,
  folderChoices, displayName, renameTarget, moveTarget,
} from '/lib/tree.js';
import { renderTree } from '/lib/tree-view.js';
import { formDialog, confirmDialog, infoDialog, showMenu } from '/lib/dialog.js';
import * as notesApi from '/lib/notes-api.js';
import {
  isArchive, isMarkdown, exportFileName, summarizeImport, describeImport, uniquePath, downloadBlob,
} from '/lib/transfer.js';
import { renderHelp, isTypingTarget, shortcutFor } from '/lib/help.js';
import {
  RETENTION_NEVER, normalizeRetention, describeRetention, describeExpiry, retentionChoices,
} from '/lib/trash.js';
import {
  getTheme, setTheme, nextTheme, applyTheme as applyUserTheme,
  getPlacement, setPlacement, togglePlacement, applyPlacement,
  getDecorations, setDecoration, getCollapsed, setCollapsed, replaceCollapsed,
  moveFolderPrefs, removeFolderPrefs,
  getSetting, setSetting, getSettings, STATUS_FIELDS, getStatusFields, setStatusField,
  isHelpSeen, markHelpSeen, resetPreferences, clearDrafts,
} from '/lib/prefs.js';

const $ = (id) => document.getElementById(id);
const q = (sel) => document.querySelector(sel);
const qa = (sel) => [...document.querySelectorAll(sel)];

let current = null; // path of the note shown in the reader
let view = "empty"; // "empty" | "read" | "edit" — see setView
let slug = null;
let lastNote = null; // {path, sha, id, title, ready: Promise}
let openSeq = 0; // invalidates stale async work after navigation

function esc(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// --- tree (collapsible folders, decorations, counts) ---

let treeData = null;
let collapsed = new Set();
let decorations = {};
let selectedFolder = null; // folder chosen for "+ Note" / "+ Folder"

function drawTree() {
  if (!treeData) return;
  const totals = renderTree($("tree"), treeData, {
    current,
    selected: selectedFolder,
    collapsed,
    decorations,
    onOpen: (path, el) => { selectedFolder = null; openNote(path, el); },
    onToggle: (path, isCollapsed) => {
      selectedFolder = path;
      collapsed = setCollapsed(slug, path, isCollapsed);
      drawTree();
    },
    onMenu: showNodeMenu,
  });
  $("tree-status").textContent = formatTreeStatus(totals);
}

async function refreshTree() {
  if (!slug) return;
  treeData = await notesApi.getTree(slug);
  drawTree();
}

function findNode(root, path) {
  if (!root) return null;
  for (const c of root.children || []) {
    if (c.path === path) return c;
    const hit = c.isDir ? findNode(c, path) : null;
    if (hit) return hit;
  }
  return null;
}

// Calendar date of a file's last change, for a note missing `created`.
function fileDate(path) {
  const node = findNode(treeData, path);
  return node && node.modTime ? localDate(new Date(node.modTime * 1000)) : "";
}

// --- reader ---

function emptyState({ icon, title, body, action }) {
  const card = document.createElement("div");
  card.className = "empty-state";
  const ic = document.createElement("div");
  ic.className = "empty-icon";
  ic.textContent = icon;
  const h = document.createElement("h2");
  h.textContent = title;
  const p = document.createElement("p");
  p.textContent = body;
  card.append(ic, h, p);
  if (action) {
    const row = document.createElement("div");
    row.className = "actions";
    row.append(action);
    card.append(row);
  }
  return card;
}

function actionButton(label, onClick) {
  const b = document.createElement("button");
  b.className = "btn primary";
  b.textContent = label;
  b.onclick = onClick;
  return b;
}

function showNoNotebooks() {
  $("note").replaceChildren(emptyState({
    icon: "📓",
    title: "No notebooks yet",
    body: "Create one with ./scripts/new-notebook.sh <slug>, then reload.",
  }));
  setView("empty");
}

function showNoSelection() {
  $("note").replaceChildren(emptyState({
    icon: "🗒",
    title: "Nothing open",
    body: "Pick a note from the list, or start a new one.",
    action: actionButton("New note", () => actionNewNote()),
  }));
  setView("empty");
}

/**
 * The one place that decides what the content pane shows and which
 * buttons go with it. Every transition — opening a note, opening or
 * closing the editor, a note disappearing under us, search results —
 * goes through here, so Edit/Save/Done can never disagree with what is
 * actually on screen.
 *
 *   "empty" nothing open: the pane explains itself, no note actions
 *   "read"  a rendered note: Edit and the ⋯ menu
 *   "edit"  the editor: Save/Done in its own toolbar, no Edit button
 *
 * @param {"empty"|"read"|"edit"} next
 */
function setView(next) {
  view = next;
  const editingView = next === "edit";
  $("note").hidden = editingView;
  $("editor").hidden = !editingView;
  // Edit belongs to reading only: while the editor is open, Done is the
  // way out, and an empty pane has nothing to edit.
  $("editbtn").hidden = next !== "read";
  $("notemenu").hidden = next === "empty";
}

async function openNote(path, btn) {
  if (!slug) return;
  if (path !== current) await flushEditor();
  current = path;
  const seq = ++openSeq;
  if (btn !== undefined) {
    document.querySelectorAll(".node.active").forEach((n) => n.classList.remove("active"));
    btn?.classList.add("active");
  }
  setStatus("Read", { text: null });
  revealPath(path);
  try {
    const [n, src] = await Promise.all([
      notesApi.getNote(slug, path),
      notesApi.getSource(slug, path),
    ]);
    if (seq !== openSeq) return;
    lastNote = { path, sha: src.sha, id: n.id || "", title: n.title || path, ready: Promise.resolve() };
    document.title = (n.title || path) + " — noteroot";
    renderRead(n);
    setView("read");
    if (isPhone()) setDrawer(false);
    setStatus("Read", { text: src.text });
    lastNote.ready = ensureFrontmatter(path, n, src, seq);
  } catch (e) {
    if (seq !== openSeq) return;
    lastNote = null;
    document.title = "noteroot";
    $("note").replaceChildren(emptyState({
      icon: "⚠️",
      title: "Could not open this note",
      body: e.message,
    }));
    setView("empty");
  }
}

function renderRead(n) {
  const parts = [];
  if (n.frontmatterError) parts.push(`<div class="warn">frontmatter: ${esc(n.frontmatterError)}</div>`);
  if (n.renderError) parts.push(`<div class="warn">render: ${esc(n.renderError)}</div>`);
  parts.push(rewriteImageThumbs(n.html || "", slug, { maxWidth: 800, maxHeight: 600 }));
  $("note").innerHTML = parts.join("");
  $("note").append(linksPanels(n));
  $("note").scrollTop = 0;
}

/**
 * Give the note a complete header (id, title, created, updated) on open.
 * Textual like the server's WithID: only missing keys are added, guarded
 * by the sha just read, and skipped whenever a local draft exists (a
 * draft always wins). Failures are quiet: the next open retries.
 */
async function ensureFrontmatter(path, rendered, src, seq) {
  if (readPad(path)) return;
  const fm = readFrontmatter(src.text);
  if (isValidId(fm.id) && fm.title && fm.created && fm.updated) return;
  const created = fm.created || fileDate(path) || localDate();
  const norm = normalizeNote(src.text, {
    path,
    id: rendered.id,
    created,
    updated: fm.updated || created,
  });
  if (!norm.changed) return;
  try {
    const res = await notesApi.saveNote(slug, path, norm.content, { baseSha: src.sha });
    if (seq !== openSeq || !lastNote || lastNote.path !== path) return;
    lastNote.sha = res.sha || src.sha;
    lastNote.id = res.id || norm.id;
    const meta = readFrontmatter(norm.content);
    lastNote.title = meta.title || lastNote.title;
    document.title = lastNote.title + " — noteroot";
    if (!editing && current === path) {
      const fresh = await notesApi.getNote(slug, path);
      if (seq !== openSeq || current !== path || editing) return;
      renderRead(fresh);
      setStatus("Read", { text: norm.content });
    }
  } catch {
    /* read-only, offline or a concurrent edit: keep reading, retry next time */
  }
}

// --- linking panels (design §8): outgoing + backlinks below the note ---

function panel(title, items, makeItem) {
  if (!items || !items.length) return null;
  const sec = document.createElement("section");
  sec.className = "links-panel";
  const h = document.createElement("h2");
  h.textContent = title;
  sec.append(h);
  const ul = document.createElement("ul");
  for (const item of items) ul.append(makeItem(item));
  sec.append(ul);
  return sec;
}

function noteLink(path, text) {
  const a = document.createElement("a");
  a.href = `/n/${encodeURIComponent(slug)}/note?path=${encodeURIComponent(path)}`;
  a.className = "wikilink";
  a.dataset.path = path;
  a.textContent = text;
  return a;
}

function linksPanels(n) {
  const frag = document.createDocumentFragment();
  const out = panel("Outgoing links", n.outgoing, (l) => {
    const li = document.createElement("li");
    if (l.path) {
      li.append(noteLink(l.path, l.label || l.title || l.target));
      const m = document.createElement("span");
      m.className = "muted";
      m.textContent = ` — ${l.path}`;
      li.append(m);
    } else {
      const s = document.createElement("span");
      s.className = "wikilink dead";
      s.textContent = l.label || l.target;
      s.title = "unresolved";
      li.append(s);
    }
    return li;
  });
  const back = panel("Backlinks", n.backlinks, (l) => {
    const li = document.createElement("li");
    li.append(noteLink(l.path, l.title || l.path));
    return li;
  });
  if (out) frag.append(out);
  if (back) frag.append(back);
  return frag;
}

$("note").addEventListener("click", (e) => {
  const a = e.target.closest?.("a.wikilink[data-path]");
  if (!a) return;
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  openNote(a.dataset.path, null);
});

async function boot() {
  const { notebooks } = await api("/api/v1/notebooks");
  const sel = $("notebooks");
  if (!notebooks.length) {
    showNoNotebooks();
    return;
  }
  for (const nb of notebooks) {
    const o = document.createElement("option");
    o.value = o.textContent = nb.slug;
    sel.append(o);
  }
  sel.onchange = () => switchNotebook(sel.value);
  await loadTree(notebooks[0].slug);
}

async function switchNotebook(s) {
  if (s === slug) return;
  await flushEditor();
  await loadTree(s);
}

async function loadTree(s) {
  if (slug) await flushEditor();
  slug = s;
  current = null;
  lastNote = null;
  openSeq += 1;
  closeEditorNow();
  $("q").value = "";
  selectedFolder = null;
  collapsed = getCollapsed(s);
  decorations = getDecorations(s);
  setStatus("Read", { text: null });
  showNoSelection();
  treeData = await notesApi.getTree(s);
  drawTree();
  if (isPhone()) setDrawer(true);
  refreshPluginPanels(s);
  refreshTrashState();
}

async function api(path) {
  const r = await fetch(path, { headers: { Accept: "application/json" } });
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return r.json();
}

// Plugin panels (stage 9): degradation-grade — a missing endpoint
// or malformed spec hides the strip, never breaks the notebook.
let panelRefreshToken = 0;
async function refreshPluginPanels(s) {
  if (!s) return;
  const token = ++panelRefreshToken;
  const panels = await fetchPanels(s);
  if (token !== panelRefreshToken || s !== slug) return;
  const footer = $("plugin-status");
  const has = panels && Object.keys(panels).length > 0;
  footer.hidden = !has;
  if (has) {
    renderPanelChips($("plugin-panels"), panels);
    if (getTheme() !== "auto") applyUserTheme(getTheme());
  }
}

async function search(query) {
  const text = query.trim();
  if (!slug || !text) return;
  // Results take over the note pane, so leave the editor first (saving
  // on the way out) and drop the open note. Edit must never act on
  // something the pane has stopped showing.
  await flushEditor();
  await closeEditor();
  current = null;
  lastNote = null;
  openSeq += 1;
  document.querySelectorAll(".node.active").forEach((n) => n.classList.remove("active"));
  setStatus("Read", { text: null });
  setView("empty");
  try {
    const r = await api(`/n/${encodeURIComponent(slug)}/search?q=${encodeURIComponent(text)}&limit=50`);
    if (!r.hits.length) {
      $("note").replaceChildren(emptyState({
        icon: "🔍",
        title: "No matches",
        body: `Nothing found for “${text}”.`,
      }));
      return;
    }
    const frag = document.createDocumentFragment();
    const h = document.createElement("p");
    h.className = "muted";
    h.textContent = `${r.hits.length} result${r.hits.length === 1 ? "" : "s"} for “${text}”.`;
    frag.append(h);
    for (const hit of r.hits) {
      const b = document.createElement("button");
      b.className = "node hit";
      const title = document.createElement("div");
      title.className = "hit-title";
      title.textContent = hit.title || hit.path;
      const snippet = document.createElement("div");
      snippet.className = "hit-snippet";
      snippet.innerHTML = hit.snippet; // server-sanitized; only <b> survives
      const path = document.createElement("div");
      path.className = "muted";
      path.textContent = hit.path;
      b.append(title, snippet, path);
      b.onclick = () => openNote(hit.path, null);
      frag.append(b);
    }
    $("note").replaceChildren(frag);
    $("note").scrollTop = 0;
  } catch (e) {
    $("note").replaceChildren(emptyState({ icon: "⚠️", title: "Search failed", body: e.message }));
  }
}

$("q")?.addEventListener("keydown", (e) => {
  if (e.key === "Enter") search(e.target.value);
});

// --- status bar: mode, position, words, selection, size, writer stats ---

const FIELD_ELEMENTS = {
  words: "sb-words", selected: "sb-selected", position: "sb-pos", chars: "sb-chars",
  paragraphs: "sb-paras", reading: "sb-reading", size: "sb-size",
};

function setStatus(mode, info) {
  $("sb-mode").textContent = mode;
  const d = describeStatus({ ...info, wpm: Number(getSetting("readingSpeed")) || undefined });
  for (const [key, id] of Object.entries(FIELD_ELEMENTS)) $(id).textContent = d[key];
}

function updateEditorStatus() {
  if (!editing) return;
  if (editing.mode === "textarea") {
    const ta = $("editbody");
    setStatus("Plain", {
      text: ta.value,
      selection: { start: ta.selectionStart, end: ta.selectionEnd },
      cursor: true,
    });
  } else {
    setStatus("Vim", { text: editing.editor?.content ?? editing.lastSaved });
  }
}

async function refreshViewStatus() {
  const path = current;
  if (!path || !slug || editing) return;
  try {
    const { text } = await notesApi.getSource(slug, path);
    if (current === path && !editing) setStatus("Read", { text });
  } catch {
    /* ignore */
  }
}

// --- editor session (plain text + Vim) ---

let editing = null; // {path, mode, editor, baseSha, lastSaved, dirty, noteId, created, updated, saveTimer, lockTimer}
let tabId = null;
let currentEditorMode = null; // 'vim' | 'textarea'
let saveChain = Promise.resolve();
let padTimer = 0;

function myTab() {
  if (!tabId) {
    tabId = sessionStorage.getItem("nr.tab");
    if (!tabId) {
      tabId = (crypto.randomUUID ? crypto.randomUUID() : String(Math.random()));
      sessionStorage.setItem("nr.tab", tabId);
    }
  }
  return tabId;
}

function padKey(path) {
  return `nr.pad.${slug}.${path}`;
}

function readPad(path) {
  try {
    const raw = localStorage.getItem(padKey(path));
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

async function writePad() {
  if (!editing) return;
  try {
    const content = editing.mode === "vim" && editing.editor
      ? editing.editor.content
      : $("editbody").value;
    localStorage.setItem(padKey(editing.path), JSON.stringify({
      tab: myTab(),
      baseSha: editing.baseSha,
      content,
      savedAt: Date.now(),
    }));
  } catch {
    /* quota or privacy mode: editing continues without a pad */
  }
}

function clearPad(path) {
  try {
    localStorage.removeItem(padKey(path || (editing && editing.path)));
  } catch {
    /* ignore */
  }
}

function setBanner(html) {
  const b = $("banner");
  b.innerHTML = html;
  b.hidden = !html;
}

function setSaveState(text, state) {
  const el = $("savestate");
  el.textContent = text;
  if (state) el.dataset.state = state;
  else delete el.dataset.state;
}

async function takeLock(path) {
  try {
    const r = await fetch(
      `/n/${encodeURIComponent(slug)}/lock?path=${encodeURIComponent(path)}&holder=${encodeURIComponent(myTab())}&ttl=300`,
      { method: "PUT" });
    if (!editing) return;
    if (r.ok) {
      $("lockstate").textContent = "Locked by this tab";
      return;
    }
    const b = await r.json().catch(() => ({}));
    $("lockstate").textContent = b.holder
      ? `Locked by another tab until ${new Date(b.expiresAt * 1000).toLocaleTimeString()} — edits may conflict`
      : "Lock unavailable — edits may conflict";
  } catch {
    $("lockstate").textContent = "";
  }
}

function releaseLock(path, keepalive) {
  if (!path) return;
  const url = `/n/${encodeURIComponent(slug)}/lock?path=${encodeURIComponent(path)}&holder=${encodeURIComponent(myTab())}`;
  if (keepalive) {
    try { fetch(url, { method: "DELETE", keepalive: true }); } catch { /* ignore */ }
  } else {
    fetch(url, { method: "DELETE" }).catch(() => {});
  }
}

async function openEditor() {
  if (!lastNote || editing) return;
  if (lastNote.ready) await lastNote.ready; // let an on-open header fix land first
  const path = lastNote.path;
  if (current !== path) return;
  const seq = openSeq;
  let src;
  try {
    src = await notesApi.getSource(slug, path);
  } catch (e) {
    toast(e.message, true);
    return;
  }
  if (seq !== openSeq || current !== path) return;

  const useVim = shouldUseVim();
  const fm = readFrontmatter(src.text);
  editing = {
    path,
    mode: useVim ? "vim" : "textarea",
    editor: null,
    baseSha: src.sha,
    lastSaved: src.text,
    dirty: false,
    noteId: fm.id || lastNote.id || "",
    created: fm.created || "",
    updated: fm.updated || "",
    saveTimer: 0,
    lockTimer: 0,
  };
  currentEditorMode = editing.mode;
  setView("edit");
  setSaveState("", "");
  $("lockstate").textContent = "";
  setBanner("");
  if (useVim) await openVimEditor(src.text);
  else openTextareaEditor(src.text);
  if (!editing) return;
  takeLock(path);
  editing.lockTimer = setInterval(() => takeLock(path), 60000);
}

async function openVimEditor(content) {
  $("editormode").textContent = "vim";
  $("editbody").hidden = true;
  const editor = new VimEditor({
    VimWasm,
    canvas: $("vim-canvas"),
    input: $("vim-input"),
    container: $("vim-container"),
    loading: $("vim-loading"),
    onFailure: (err) => { recordVimFailure(); console.error("[vim]", err); },
  });
  editing.editor = editor;
  editor.onDirty(() => markDirty());
  editor.onSave((text) => enqueueSave(text, false));
  editor.onClose(({ crashed }) => {
    if (!editing || editing.editor !== editor) return;
    if (crashed) {
      editing.mode = "textarea";
      editing.editor = null;
      currentEditorMode = "textarea";
      openTextareaEditor(editor.content || content);
      setBanner(`<div class="warn">Vim stopped unexpectedly — your text is here in the plain editor.</div>`);
    } else {
      closeEditor();
    }
  });
  try {
    await editor.open(editing.path, content);
  } catch (err) {
    recordVimFailure();
    if (editing && editing.editor === editor) {
      editing.mode = "textarea";
      editing.editor = null;
      currentEditorMode = "textarea";
      openTextareaEditor(content);
    }
    return;
  }
  if (!editing || editing.editor !== editor) return;
  updateEditorStatus();
  offerDraft(content, (text) => editor.replace(text));
}

function openTextareaEditor(content) {
  $("editormode").textContent = "plain text";
  $("editbody").hidden = false;
  $("editbody").value = content;
  $("editbody").oninput = () => markDirty();
  applyEditorPrefs();
  offerDraft(content, (text) => { $("editbody").value = text; });
  updateEditorStatus();
}

// Offer a locally-kept draft (a failed or in-flight save from this or an
// earlier session) when it differs from what the server has.
function offerDraft(base, apply) {
  const pad = readPad(editing.path);
  if (!pad || typeof pad.content !== "string" || pad.content === base) return;
  const when = pad.savedAt ? new Date(pad.savedAt).toLocaleString() : "unknown time";
  setBanner(`<div class="warn">Unsaved changes from ${esc(when)}.` +
    ` <button class="btn" id="padrestore">Restore</button>` +
    ` <button class="btn" id="paddiscard">Discard</button></div>`);
  $("padrestore").onclick = () => { apply(pad.content); setBanner(""); markDirty(); };
  $("paddiscard").onclick = () => { clearPad(editing.path); setBanner(""); };
}

function markDirty() {
  if (!editing) return;
  editing.dirty = true;
  clearTimeout(editing.saveTimer);
  const ms = Number(getSetting("autosave"));
  if (ms > 0) editing.saveTimer = setTimeout(() => { flushEditor(); }, ms);
  clearTimeout(padTimer);
  padTimer = setTimeout(writePad, 500);
  setSaveState("Editing…", "dirty");
  updateEditorStatus();
}

async function readEditorContent() {
  if (!editing) return "";
  if (editing.mode === "vim" && editing.editor) {
    try {
      return await editing.editor.getContent();
    } catch {
      setSaveState("Could not read Vim; use :w in Vim", "error");
      throw new Error("vim-read");
    }
  }
  return $("editbody").value;
}

/**
 * Save any pending edits. Called before every navigation (opening another
 * note, switching notebooks, closing) so nothing is lost. A Vim buffer
 * that cannot be read falls back to a local draft.
 */
async function flushEditor() {
  if (!editing) return true;
  clearTimeout(editing.saveTimer);
  let content;
  try {
    content = await readEditorContent();
  } catch {
    await writePad();
    return false;
  }
  if (content === editing.lastSaved) {
    clearPad(editing.path);
    return true;
  }
  return enqueueSave(content, false);
}

// Saves run one at a time, so a second edit never races the first one's
// base sha (which would 412 needlessly).
function enqueueSave(content, manual) {
  const run = saveChain.then(
    () => saveNoteContent(content, manual),
    () => saveNoteContent(content, manual),
  );
  saveChain = run.then(() => {}, () => {});
  return run;
}

async function saveNote(manual) {
  if (!editing) return;
  let content;
  try {
    content = await readEditorContent();
  } catch {
    return;
  }
  await enqueueSave(content, manual);
}

async function saveNoteContent(content, manual) {
  const ed = editing;
  if (!ed) return false;
  clearTimeout(ed.saveTimer);
  if (content === ed.lastSaved) {
    if (manual) setSaveState("No changes.", "");
    return true;
  }
  setSaveState("Saving…", "dirty");
  // Keep the header truthful: stamp `updated` (adds a block if missing).
  const norm = normalizeNote(content, {
    path: ed.path,
    id: ed.noteId,
    created: ed.created,
    updated: ed.updated,
    touch: true,
  });
  const sent = norm.changed ? norm.content : content;
  if (norm.changed) await applyFixedContent(ed, sent);
  try {
    const res = await notesApi.saveNote(slug, ed.path, sent, { baseSha: ed.baseSha });
    if (editing !== ed) return true;
    ed.baseSha = res.sha;
    ed.noteId = res.id || norm.id || ed.noteId;
    ed.lastSaved = sent;
    const meta = readFrontmatter(sent);
    ed.created = meta.created || ed.created;
    ed.updated = meta.updated || ed.updated;
    ed.dirty = false;
    clearPad(ed.path);
    setSaveState("Saved.", "ok");
    updateEditorStatus();
    setTimeout(() => refreshPluginPanels(slug), 900);
    return true;
  } catch (e) {
    if (e.status === 412) {
      setSaveState("Changed elsewhere", "error");
      conflictBanner(sent);
    } else {
      setSaveState(`Save failed: ${e.message}`, "error");
      await writePad();
    }
    return false;
  }
}

// Put the header fix into the live buffer without moving the caret, so
// the editor and the file agree and the next save keeps the id.
async function applyFixedContent(ed, sent) {
  if (ed.mode === "vim" && ed.editor) {
    try {
      await ed.editor.replace(sent);
    } catch {
      /* Vim unavailable: the file is still saved correctly below */
    }
    return;
  }
  const ta = $("editbody");
  const range = changedRange(ta.value, sent);
  if (range) {
    ta.setRangeText(range.text, range.start, range.end, "preserve");
    updateEditorStatus();
  }
}

function conflictBanner(mine) {
  setBanner(`<div class="warn">Changed on the server.` +
    ` <button class="btn" id="cfreload">Reload (lose mine)</button>` +
    ` <button class="btn" id="cfforce">Keep mine</button></div>`);

  $("cfreload").onclick = async () => {
    const ed = editing;
    if (!ed) return;
    try {
      const src = await notesApi.getSource(slug, ed.path);
      if (editing !== ed) return;
      ed.baseSha = src.sha;
      ed.lastSaved = src.text;
      const meta = readFrontmatter(src.text);
      ed.noteId = meta.id || ed.noteId;
      ed.created = meta.created || ed.created;
      ed.updated = meta.updated || ed.updated;
      ed.dirty = false;
      if (ed.mode === "vim" && ed.editor) await ed.editor.replace(src.text);
      else { $("editbody").value = src.text; updateEditorStatus(); }
      clearPad(ed.path);
      setBanner("");
      setSaveState("", "");
    } catch (e) {
      setSaveState(`Reload failed: ${e.message}`, "error");
    }
  };

  $("cfforce").onclick = async () => {
    const ed = editing;
    if (!ed) return;
    try {
      const res = await notesApi.saveNote(slug, ed.path, mine, { baseSha: ed.baseSha, force: true });
      if (editing !== ed) return;
      ed.baseSha = res.sha;
      ed.noteId = res.id || ed.noteId;
      ed.lastSaved = mine;
      ed.dirty = false;
      clearPad(ed.path);
      setBanner("");
      setSaveState("Saved.", "ok");
    } catch (e) {
      setSaveState(`Force save failed: ${e.message}`, "error");
    }
  };

  writePad();
}

// Tear the editor down without saving; the caller has flushed already.
function closeEditorNow() {
  const ed = editing;
  if (!ed) return;
  clearTimeout(ed.saveTimer);
  clearInterval(ed.lockTimer);
  clearTimeout(padTimer);
  releaseLock(ed.path, false);
  if (ed.mode === "vim" && ed.editor) ed.editor.close();
  editing = null;
  currentEditorMode = null;
  // Back to whatever the pane was showing before: a note if one is still
  // open, the empty state if it was deleted or the notebook changed.
  setView(current && lastNote ? "read" : "empty");
  $("editbody").hidden = false;
  setBanner("");
  setSaveState("", "");
  setStatus("Read", { text: null });
}

async function closeEditor() {
  const ed = editing;
  if (!ed) return;
  closeEditorNow();
  if (lastNote && lastNote.path === ed.path) lastNote.sha = ed.baseSha;
  await refreshViewStatus();
  maybeWelcome();
}

// --- plain-text editor niceties (Tab, spell check, wrap) ---

function applyEditorPrefs() {
  const ta = $("editbody");
  ta.spellcheck = getSetting("spellcheck") === "on";
  ta.style.whiteSpace = getSetting("wrap") === "on" ? "pre-wrap" : "pre";
  ta.style.overflowWrap = getSetting("wrap") === "on" ? "break-word" : "normal";
}

function tabUnit() {
  const t = getSetting("tabKey");
  if (t === "tab") return "\t";
  if (t === "2") return "  ";
  if (t === "4") return "    ";
  return null; // "focus": leave Tab to the browser
}

$("editbody")?.addEventListener("keydown", (e) => {
  if (e.key !== "Tab" || !editing || editing.mode !== "textarea") return;
  const unit = tabUnit();
  if (!unit) return;
  e.preventDefault();
  const ta = $("editbody");
  const edit = indentEdit(ta.value, ta.selectionStart, ta.selectionEnd, unit, e.shiftKey);
  ta.setRangeText(edit.text, edit.start, edit.end, "preserve");
  ta.setSelectionRange(edit.selStart, edit.selEnd);
  markDirty();
});

// --- Resource Upload (stage 5) ---

async function uploadResource(file) {
  if (!slug) throw new Error("no notebook selected");
  const pre = await presignUpload(slug, file);
  if (pre.mode === "exists") {
    return { sha: pre.sha, filename: file.name, mime: file.type, created: false };
  }
  if (pre.mode === "s3") return pre.result;
  if (pre.mode === "error") throw new Error(pre.error);
  const url = uploadURL(slug, file.name);
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": file.type || "application/octet-stream" },
    body: file,
  });
  if (!res.ok) {
    let msg = `upload failed (${res.status})`;
    try {
      const j = await res.json();
      if (j.error) msg = j.error;
    } catch (_) { /* non-JSON error body */ }
    throw new Error(msg);
  }
  return res.json();
}

function insertAtCursor(text) {
  const ta = $("editbody");
  const start = ta.selectionStart;
  const end = ta.selectionEnd;
  ta.value = ta.value.slice(0, start) + text + ta.value.slice(end);
  const pos = start + text.length;
  ta.selectionStart = ta.selectionEnd = pos;
  ta.focus();
}

async function insertIntoEditor(text) {
  if (!editing) return;
  if (editing.mode === "textarea") {
    insertAtCursor(text);
    markDirty();
  } else if (editing.mode === "vim" && editing.editor) {
    const ok = await editing.editor.insert(text);
    if (ok) {
      markDirty();
    } else {
      setBanner(`<div class="warn">Uploaded, but Vim would not take the insert.` +
        ` Paste it yourself: <code>${esc(text)}</code></div>`);
    }
  }
}

async function handleFileUpload(file) {
  if (!editing) return;
  setSaveState(`Uploading ${file.name}…`, "dirty");
  try {
    const res = await uploadResource(file);
    if (!res || !/^[a-f0-9]{64}$/.test(res.sha || "")) {
      throw new Error("upload returned no content hash");
    }
    const md = markdownFor(slug, res);
    await insertIntoEditor(md);
    // Dirty the note first: markDirty() writes "Editing…", so the upload
    // result has to come after it or the user would never see it.
    markDirty();
    setSaveState(res.created
      ? `Uploaded ${file.name} (${res.sha.slice(0, 8)}…)`
      : `${file.name} already uploaded (dedup)`, "dirty");
  } catch (e) {
    setSaveState(`Upload failed: ${e.message}`, "error");
  }
}

// Drag-and-drop onto the editor uploads and embeds; paste does the same
// for clipboard images.
function setupEditorDropZone() {
  const editorEl = $("editor");
  editorEl.addEventListener("dragover", (e) => {
    if (!editing) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
    editorEl.classList.add("drag-over");
  });
  editorEl.addEventListener("dragleave", (e) => {
    if (e.target === editorEl) editorEl.classList.remove("drag-over");
  });
  editorEl.addEventListener("drop", (e) => {
    if (!editing) return;
    e.preventDefault();
    editorEl.classList.remove("drag-over");
    const files = e.dataTransfer?.files;
    if (!files || files.length === 0) return;
    for (const file of files) handleFileUpload(file);
  });
  editorEl.addEventListener("paste", (e) => {
    if (!editing) return;
    const items = e.clipboardData?.items;
    if (!items) return;
    for (const item of items) {
      if (item.type.startsWith("image/")) {
        const file = item.getAsFile();
        if (file) {
          e.preventDefault();
          handleFileUpload(file);
        }
      }
    }
  });
}

setupEditorDropZone();

for (const ev of ["keyup", "click", "select", "focus"]) {
  $("editbody")?.addEventListener(ev, updateEditorStatus);
}
document.addEventListener("selectionchange", () => {
  if (document.activeElement === $("editbody")) updateEditorStatus();
});

$("editbtn")?.addEventListener("click", openEditor);
$("savebtn")?.addEventListener("click", () => saveNote(true));
$("closebtn")?.addEventListener("click", async () => {
  const path = editing && editing.path;
  await flushEditor();
  await closeEditor();
  if (path && slug) await openNote(path, null);
});

window.addEventListener("pagehide", () => {
  if (editing) {
    writePad();
    releaseLock(editing.path, true);
  }
});

// --- keyboard shortcuts (single source of truth: lib/help.js) ---

window.addEventListener("keydown", (e) => {
  const typing = isTypingTarget(e.target) || (editing != null);
  const action = shortcutFor(e, { typing });
  if (!action) return;
  if (action === "save") {
    if (editing) {
      e.preventDefault();
      saveNote(true);
    }
    return;
  }
  if (action === "help") {
    e.preventDefault();
    openHelp();
    return;
  }
  if (action === "search") {
    e.preventDefault();
    $("q").focus();
    $("q").select?.();
    return;
  }
  if (action === "edit") {
    if (current) { e.preventDefault(); openEditor(); }
    return;
  }
  if (action === "new-note") {
    if (slug) { e.preventDefault(); actionNewNote(); }
  }
});

// --- toast ---

let toastTimer = 0;
function toast(msg, isError = false) {
  const t = $("toast");
  t.textContent = msg;
  t.className = isError ? "toast error" : "toast";
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, isError ? 6000 : 3000);
}

// --- note / folder CRUD ---

function isPhone() {
  return window.matchMedia("(max-width: 720px)").matches;
}

function contextDir() {
  if (selectedFolder !== null) return selectedFolder;
  return current ? parentDir(current) : "";
}

function revealPath(path) {
  let changed = false;
  for (const dir of ancestors(path)) {
    if (collapsed.has(dir)) {
      collapsed = setCollapsed(slug, dir, false);
      changed = true;
    }
  }
  if (changed) drawTree();
}

function ensureExpanded(dir) {
  if (!dir) return;
  for (const d of ancestors(`${dir}/x`)) {
    if (collapsed.has(d)) collapsed = setCollapsed(slug, d, false);
  }
}

function editorBlocks(path, isDir) {
  if (!editing) return false;
  const hit = isDir ? isWithin(editing.path, path) : editing.path === path;
  if (hit) toast("Close the editor on this note first.", true);
  return hit;
}

function followMove(from, to, isDir) {
  if (current && (isDir ? isWithin(current, from) : current === from)) {
    current = isDir ? to + current.slice(from.length) : to;
    if (lastNote) lastNote.path = current;
    if (editing) editing.path = current;
  }
}

function clearViewIfGone(path, isDir) {
  if (current && (isDir ? isWithin(current, path) : current === path)) {
    current = null;
    lastNote = null;
    openSeq += 1;
    showNoSelection();
    setStatus("Read", { text: null });
  }
}

async function guarded(fn) {
  try {
    await fn();
  } catch (e) {
    toast(e.status === 412 ? "That name already exists." : e.message, true);
  }
}

async function actionNewNote(dir = contextDir()) {
  const r = await formDialog({
    title: "New note",
    message: dir ? `In folder “${dir}”.` : "At the notebook root.",
    fields: [{ name: "name", label: "Name", placeholder: "ideas/todo", hint: "Subfolders are created as needed; .md is added for you." }],
    okLabel: "Create",
    validate: (v) => { const c = resolveChild(dir, v.name, { asNote: true }); return c.ok ? null : c.error; },
  });
  if (!r) return;
  await guarded(async () => {
    const { path } = resolveChild(dir, r.name, { asNote: true });
    await notesApi.createNote(slug, path, newNoteContent(path));
    ensureExpanded(parentDir(path));
    await refreshTree();
    await openNote(path, null);
    if (lastNote && lastNote.path === path) await openEditor();
  });
}

async function actionNewFolder(dir = contextDir()) {
  const r = await formDialog({
    title: "New folder",
    message: dir ? `Inside “${dir}”.` : "At the notebook root.",
    fields: [{ name: "name", label: "Name", placeholder: "projects" }],
    okLabel: "Create",
    validate: (v) => { const c = resolveChild(dir, v.name); return c.ok ? null : c.error; },
  });
  if (!r) return;
  await guarded(async () => {
    const { path } = resolveChild(dir, r.name);
    await notesApi.createFolder(slug, path);
    ensureExpanded(path);
    selectedFolder = path;
    await refreshTree();
    toast(`Created folder ${path}`);
  });
}

async function actionRename(node) {
  if (editorBlocks(node.path, node.isDir)) return;
  const parent = parentDir(node.path);
  const r = await formDialog({
    title: node.isDir ? "Rename folder" : "Rename note",
    message: `New name ${parent ? `inside “${parent}”` : "at the notebook root"}.`,
    fields: [{ name: "name", label: "Name", value: displayName(node.path, node.isDir), hint: "To change folders, use Move…." }],
    okLabel: "Rename",
    preview: (v) => {
      if (!v.name) return "";
      return `${parent ? `${parent}/` : ""}${v.name}${node.isDir ? "" : ".md"}`;
    },
    validate: (v) => { const t = renameTarget(node.path, v.name, node.isDir); return t.ok ? null : t.error; },
  });
  if (!r) return;
  const t = renameTarget(node.path, r.name, node.isDir);
  if (!t.ok || t.same) return;
  await guarded(async () => {
    if (node.isDir) {
      await notesApi.moveFolder(slug, node.path, t.path);
      moveFolderPrefs(slug, node.path, t.path);
      collapsed = getCollapsed(slug);
      decorations = getDecorations(slug);
      if (selectedFolder && isWithin(selectedFolder, node.path)) selectedFolder = t.path + selectedFolder.slice(node.path.length);
    } else {
      await notesApi.moveNote(slug, node.path, t.path);
    }
    followMove(node.path, t.path, node.isDir);
    ensureExpanded(node.isDir ? t.path : parentDir(t.path));
    await refreshTree();
    toast(`Renamed to ${t.path}`);
  });
}

async function actionMove(node) {
  if (editorBlocks(node.path, node.isDir)) return;
  const choices = folderChoices(treeData).map((c) => ({
    value: c.value,
    label: `${"\u2003".repeat(c.depth)}${c.label}`,
    disabled: node.isDir && c.value === node.path,
  }));
  const r = await formDialog({
    title: node.isDir ? "Move folder" : "Move note",
    message: `Where should “${node.path}” go?`,
    fields: [
      { name: "dest", label: "Destination folder", type: "select", options: choices, value: parentDir(node.path) },
      { name: "newFolder", label: "…or a new folder there", placeholder: "archive", hint: "Leave empty to use the folder above." },
    ],
    okLabel: "Move",
    wide: false,
    preview: (v) => {
      const t = moveTarget(node.path, node.isDir, v.dest, v.newFolder);
      return t.ok ? `${node.path} → ${t.path}` : t.error;
    },
    validate: (v) => { const t = moveTarget(node.path, node.isDir, v.dest, v.newFolder); return t.ok ? null : t.error; },
  });
  if (!r) return;
  const t = moveTarget(node.path, node.isDir, r.dest, r.newFolder);
  if (!t.ok || t.path === node.path) return;
  await guarded(async () => {
    if (String(r.newFolder ?? "").trim()) {
      try {
        await notesApi.createFolder(slug, t.dir);
      } catch (e) {
        if (e.status !== 409) throw e; // 409 = the folder already exists
      }
    }
    if (node.isDir) {
      await notesApi.moveFolder(slug, node.path, t.path);
      moveFolderPrefs(slug, node.path, t.path);
      collapsed = getCollapsed(slug);
      decorations = getDecorations(slug);
      if (selectedFolder && isWithin(selectedFolder, node.path)) selectedFolder = t.path + selectedFolder.slice(node.path.length);
    } else {
      await notesApi.moveNote(slug, node.path, t.path);
    }
    followMove(node.path, t.path, node.isDir);
    ensureExpanded(node.isDir ? t.path : parentDir(t.path));
    await refreshTree();
    toast(`Moved to ${t.path}`);
  });
}

async function actionDelete(node) {
  if (editorBlocks(node.path, node.isDir)) return;
  const n = node.isDir ? node.noteCount : 1;
  const ok = await confirmDialog({
    title: node.isDir ? "Delete folder" : "Delete note",
    message: node.isDir
      ? `Move ${n} note${n === 1 ? "" : "s"} in “${node.path}” to the trash and remove the folder?`
      : `Move “${node.path}” to the trash?`,
    okLabel: "Delete",
    danger: true,
  });
  if (!ok) return;
  await guarded(async () => {
    if (node.isDir) {
      await notesApi.deleteFolder(slug, node.path);
      removeFolderPrefs(slug, node.path);
      collapsed = getCollapsed(slug);
      decorations = getDecorations(slug);
      if (selectedFolder && isWithin(selectedFolder, node.path)) selectedFolder = null;
    } else {
      await notesApi.deleteNote(slug, node.path);
    }
    clearViewIfGone(node.path, node.isDir);
    await refreshTree();
    toast("Moved to trash. Restore it from the trash panel.");
  });
}

// Download one note as a .md file.
async function actionDownloadNote(node) {
  await guarded(async () => {
    const { text } = await notesApi.getSource(slug, node.path);
    downloadBlob(new Blob([text], { type: "text/markdown;charset=utf-8" }), baseName(node.path));
    toast(`Downloaded ${baseName(node.path)}`);
  });
}

const DEFAULT_FOLDER_COLOR = "#2563eb";

async function actionDecorate(node) {
  const cur = decorations[node.path] || {};
  const r = await formDialog({
    title: `Icon & color — ${baseName(node.path)}`,
    message: "Shown in this browser only.",
    fields: [
      { name: "emoji", label: "Icon (emoji)", value: cur.emoji || "", placeholder: "📁" },
      { name: "useColor", label: "Custom name color", type: "checkbox", value: Boolean(cur.color) },
      { name: "color", label: "Color", type: "color", value: cur.color || DEFAULT_FOLDER_COLOR },
    ],
    okLabel: "Save",
    actions: [{ label: "Reset", name: "reset" }],
  });
  if (!r) return;
  const deco = r.__action === "reset" ? {} : { emoji: r.emoji, color: r.useColor ? r.color : "" };
  setDecoration(slug, node.path, deco);
  decorations = getDecorations(slug);
  drawTree();
}

function showNodeMenu(node, x, y) {
  const items = node.isDir
    ? [
      { label: "New note here", onSelect: () => actionNewNote(node.path) },
      { label: "New folder here", onSelect: () => actionNewFolder(node.path) },
      { separator: true },
      { label: "Rename…", onSelect: () => actionRename(node) },
      { label: "Move…", onSelect: () => actionMove(node) },
      { label: "Icon & color…", onSelect: () => actionDecorate(node) },
      { separator: true },
      { label: "Delete folder…", danger: true, onSelect: () => actionDelete(node) },
    ]
    : [
      { label: "Open", onSelect: () => openNote(node.path, null) },
      { label: "Rename…", onSelect: () => actionRename(node) },
      { label: "Move…", onSelect: () => actionMove(node) },
      { label: "Download .md", onSelect: () => actionDownloadNote(node) },
      { separator: true },
      { label: "Delete note…", danger: true, onSelect: () => actionDelete(node) },
    ];
  showMenu(x, y, items);
}

// The ⋯ button in the header opens the menu for the open note.
$("notemenu")?.addEventListener("click", (e) => {
  if (!current) return;
  const r = e.currentTarget.getBoundingClientRect();
  showNodeMenu({ path: current, isDir: false }, r.left, r.bottom + 4);
});

$("new-note")?.addEventListener("click", () => actionNewNote());
$("new-folder")?.addEventListener("click", () => actionNewFolder());
$("tree-expand")?.addEventListener("click", () => {
  collapsed = replaceCollapsed(slug, []);
  drawTree();
});
$("tree-collapse")?.addEventListener("click", () => {
  if (!treeData) return;
  collapsed = replaceCollapsed(slug, folderPaths(treeData));
  drawTree();
});

// --- trash panel ---

// The notebook's retention, as last read from the server. Settings and
// the trash panel both render from this so they cannot disagree.
let trashRetention = RETENTION_NEVER;
let trashCount = 0;

async function renderTrash() {
  const list = $("trash-list");
  list.replaceChildren();
  let entries = [];
  try {
    const data = await notesApi.listTrash(slug);
    entries = data.entries;
    trashRetention = normalizeRetention(data.retentionDays);
    trashCount = entries.length;
  } catch (e) {
    list.textContent = e.message;
    return;
  }
  $("trash-retention-note").textContent = describeRetention(trashRetention);
  syncTrashSettings();
  $("trash-empty-modal").disabled = !trashCount;
  if (!entries.length) {
    const li = document.createElement("li");
    li.className = "muted";
    li.textContent = "Trash is empty.";
    list.append(li);
    return;
  }
  for (const en of entries) {
    list.append(trashRow(en));
  }
}

function trashRow(en) {
  const li = document.createElement("li");
  const path = document.createElement("span");
  path.className = "trash-path";
  path.textContent = en.origPath;
  const when = document.createElement("span");
  when.className = "trash-when";
  when.textContent = new Date(en.deletedAt * 1000).toLocaleString();
  const due = describeExpiry(en.expiresAt);
  if (due) {
    const tag = document.createElement("span");
    tag.className = "trash-due";
    tag.textContent = due;
    when.append(" · ", tag);
  }

  const restore = document.createElement("button");
  restore.className = "btn small";
  restore.textContent = "Restore";
  restore.onclick = () => guarded(async () => {
    const res = await notesApi.restoreTrash(slug, en.name);
    toast(`Restored ${res.path}`);
    await refreshTree();
    await renderTrash();
  });

  const forget = document.createElement("button");
  forget.className = "btn small danger";
  forget.textContent = "Delete";
  forget.title = "Delete this note permanently";
  forget.onclick = () => guarded(async () => {
    const ok = await confirmDialog({
      title: "Delete permanently",
      message: `Delete “${en.origPath}” for good? This cannot be undone.`,
      okLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    await notesApi.deleteTrashEntry(slug, en.name);
    toast(`Deleted ${en.origPath}`);
    await renderTrash();
  });

  li.append(path, when, restore, forget);
  return li;
}

// --- trash retention (a notebook setting, not a browser preference) ---

/** Mirror the known retention into the Settings panel. */
function syncTrashSettings() {
  const sel = $("trash-retention");
  if (!sel) return;
  const choices = retentionChoices(trashRetention);
  if (sel.options.length !== choices.length
    || [...sel.options].some((o, i) => Number(o.value) !== choices[i].value)) {
    sel.replaceChildren(...choices.map((c) => {
      const o = document.createElement("option");
      o.value = String(c.value);
      o.textContent = c.label;
      return o;
    }));
  }
  sel.value = String(trashRetention);
  sel.disabled = !slug;
  $("retention-info").textContent = describeRetention(trashRetention);
  $("trash-purge-now").disabled = !slug || trashRetention === RETENTION_NEVER;
  $("trash-empty").disabled = !slug || !trashCount;
  $("trash-count").textContent = slug
    ? `${trashCount} note${trashCount === 1 ? "" : "s"} in the trash.`
    : "Open a notebook to see its trash.";
}

/** Read the trash without opening the panel, to keep Settings honest. */
async function refreshTrashState() {
  if (!slug) {
    trashRetention = RETENTION_NEVER;
    trashCount = 0;
    syncTrashSettings();
    return;
  }
  try {
    const data = await notesApi.listTrash(slug);
    trashRetention = normalizeRetention(data.retentionDays);
    trashCount = data.entries.length;
  } catch {
    /* the panel reports its own failures; Settings just goes stale */
  }
  syncTrashSettings();
}

async function applyRetention(days) {
  await guarded(async () => {
    const res = await notesApi.setTrashRetention(slug, days);
    trashRetention = normalizeRetention(res.retentionDays);
    toast(res.purged
      ? `Saved. ${res.purged} trashed note${res.purged === 1 ? "" : "s"} removed.`
      : "Saved.");
    await refreshTrashState();
    if (!$("trash-modal").hidden) await renderTrash();
  });
  // A rejected change must not leave the select showing something the
  // server never accepted.
  syncTrashSettings();
}

async function emptyTrash() {
  const ok = await confirmDialog({
    title: "Empty trash",
    message: `Delete ${trashCount} trashed note${trashCount === 1 ? "" : "s"} for good? This cannot be undone.`,
    okLabel: "Empty trash",
    danger: true,
  });
  if (!ok) return;
  await guarded(async () => {
    const res = await notesApi.purgeTrash(slug, { all: true });
    toast(`Deleted ${res.purged} note${res.purged === 1 ? "" : "s"}.`);
    await refreshTrashState();
    if (!$("trash-modal").hidden) await renderTrash();
  });
}

$("trash-retention")?.addEventListener("change", (e) => applyRetention(e.target.value));
$("trash-purge-now")?.addEventListener("click", () => guarded(async () => {
  const res = await notesApi.purgeTrash(slug, {});
  toast(res.purged
    ? `Removed ${res.purged} expired note${res.purged === 1 ? "" : "s"}.`
    : "Nothing was due for deletion.");
  await refreshTrashState();
  if (!$("trash-modal").hidden) await renderTrash();
}));
$("trash-empty")?.addEventListener("click", emptyTrash);
$("trash-empty-modal")?.addEventListener("click", emptyTrash);
$("trash-settings")?.addEventListener("click", () => {
  closeTrash();
  openSettingsModal("set-trash");
});

async function openTrash() {
  if (!slug) return;
  $("trash-modal").hidden = false;
  await renderTrash();
}
const closeTrash = () => { $("trash-modal").hidden = true; };
$("trash-btn")?.addEventListener("click", openTrash);
$("trash-close")?.addEventListener("click", closeTrash);
$("trash-modal")?.querySelector(".modal-backdrop")?.addEventListener("click", closeTrash);

// --- import / export ---

async function actionExportNotebook() {
  if (!slug) return;
  await guarded(async () => {
    const blob = await notesApi.exportNotebook(slug);
    downloadBlob(blob, exportFileName(slug));
    toast("Export downloaded");
  });
}

// Render the dry-run plan (or the result) of an import.
function planNode(entries) {
  const s = summarizeImport(entries);
  const box = document.createElement("div");
  const summary = document.createElement("p");
  summary.className = "plan-summary";
  summary.textContent = describeImport(s, { dryRun: entries.__dryRun });
  box.append(summary);
  const ul = document.createElement("ul");
  ul.className = "plan-list";
  for (const e of entries) {
    if (!e || e.status === "skipped") continue;
    const li = document.createElement("li");
    const chip = document.createElement("span");
    chip.className = `chip ${e.status}`;
    chip.textContent = e.status;
    const path = document.createElement("span");
    path.className = "plan-path";
    path.textContent = e.path;
    li.append(chip, path);
    if (e.message) {
      const m = document.createElement("span");
      m.className = "plan-msg";
      m.textContent = e.message;
      li.append(m);
    }
    ul.append(li);
  }
  box.append(ul);
  return box;
}

async function importArchive(file) {
  const dry = await notesApi.importNotebook(slug, file, { dryRun: true });
  const entries = dry.entries || [];
  entries.__dryRun = true;
  const r = await formDialog({
    title: `Import ${file.name}`,
    message: "Nothing has changed yet. Review and confirm.",
    extra: planNode(entries),
    okLabel: "Import",
    wide: true,
  });
  if (!r) return;
  await guarded(async () => {
    const res = await notesApi.importNotebook(slug, file, {});
    await refreshTree();
    await infoDialog({
      title: "Import complete",
      message: describeImport(summarizeImport(res.entries || []), {}),
      extra: planNode(res.entries || []),
      wide: true,
    });
  });
}

async function importMarkdown(files) {
  if (!treeData) return;
  const choices = folderChoices(treeData).map((c) => ({
    value: c.value,
    label: `${"\u2003".repeat(c.depth)}${c.label}`,
  }));
  const r = await formDialog({
    title: files.length === 1 ? "Import note" : `Import ${files.length} notes`,
    message: "Choose where the notes should go. Existing names are kept.",
    fields: [{ name: "dest", label: "Folder", type: "select", options: choices, value: contextDir() }],
    okLabel: "Import",
  });
  if (!r) return;
  await guarded(async () => {
    const taken = new Set();
    const collect = (root) => {
      for (const c of root.children || []) {
        if (c.isDir) collect(c); else taken.add(c.path);
      }
    };
    if (treeData) collect(treeData);
    let count = 0;
    for (const file of files) {
      const name = (file.name || "note.md").replace(/\.(md|markdown)$/i, "");
      const target = uniquePath(joinPath(r.dest, `${cleanRelPath(name).ok ? cleanRelPath(name).path : name}.md`), (p) => taken.has(p) || false);
      const text = await file.text();
      await notesApi.saveNote(slug, target, text, { createOnly: true });
      taken.add(target);
      count += 1;
    }
    ensureExpanded(r.dest);
    await refreshTree();
    toast(`Imported ${count} note${count === 1 ? "" : "s"}`);
  });
}

function importFiles(files) {
  if (!slug || !files || !files.length) return;
  const list = [...files];
  if (list.some((f) => isArchive(f.name))) {
    importArchive(list.find((f) => isArchive(f.name)));
  } else if (list.every((f) => isMarkdown(f.name))) {
    importMarkdown(list);
  } else {
    toast("Import accepts a .tar.gz export or .md files.", true);
  }
}

// Drag-and-drop a file onto the tree to import it.
function setupTreeDropZone() {
  const side = $("sidebar");
  let depth = 0;
  side.addEventListener("dragover", (e) => {
    if (!slug || !e.dataTransfer?.types?.includes("Files")) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
    side.classList.add("drop-target");
  });
  side.addEventListener("dragenter", (e) => {
    if (!slug || !e.dataTransfer?.types?.includes("Files")) return;
    depth += 1;
  });
  side.addEventListener("dragleave", () => {
    depth = Math.max(0, depth - 1);
    if (depth === 0) side.classList.remove("drop-target");
  });
  side.addEventListener("drop", (e) => {
    if (!slug) return;
    e.preventDefault();
    depth = 0;
    side.classList.remove("drop-target");
    if (e.dataTransfer?.files?.length) importFiles(e.dataTransfer.files);
  });
}

setupTreeDropZone();

// --- theme, tree placement, phone drawer ---

const THEME_ICON = { auto: "◐", light: "☀", dark: "☾" };

function syncThemeUI() {
  const t = getTheme();
  const b = $("themebtn");
  b.textContent = THEME_ICON[t];
  b.setAttribute("aria-label", `Theme: ${t}`);
  b.title = `Theme: ${t} (click to change)`;
  const radio = q(`input[name="theme"][value="${t}"]`);
  if (radio) radio.checked = true;
}

function chooseTheme(theme) {
  const t = setTheme(theme);
  applyUserTheme(t);
  if (t === "auto") refreshPluginPanels(slug);
  syncThemeUI();
}

function syncPlacementUI() {
  const p = getPlacement();
  applyPlacement(p);
  const radio = q(`input[name="tree-placement"][value="${p}"]`);
  if (radio) radio.checked = true;
  $("treebtn").setAttribute("aria-expanded", String(isPhone() ? document.body.classList.contains("tree-open") : p !== "hidden"));
}

function setDrawer(open) {
  document.body.classList.toggle("tree-open", open);
  $("treebtn").setAttribute("aria-expanded", String(open));
}

$("themebtn")?.addEventListener("click", () => chooseTheme(nextTheme(getTheme())));
$("treebtn")?.addEventListener("click", () => {
  if (isPhone()) setDrawer(!document.body.classList.contains("tree-open"));
  else { togglePlacement(); syncPlacementUI(); }
});
$("tree-scrim")?.addEventListener("click", () => setDrawer(false));
window.matchMedia("(max-width: 720px)").addEventListener?.("change", (e) => {
  if (!e.matches) setDrawer(false);
  syncPlacementUI();
});

qa('input[name="theme"]').forEach((r) => r.addEventListener("change", () => chooseTheme(r.value)));
qa('input[name="tree-placement"]').forEach((r) => r.addEventListener("change", () => {
  setPlacement(r.value);
  syncPlacementUI();
}));

// --- settings modal ---

const SETTING_KEYS = Object.keys(getSettings());

function applyPreferences() {
  const s = getSettings();
  document.body.dataset.font = s.fontSize;
  document.body.dataset.width = s.readingWidth;
  document.body.dataset.counts = s.treeCounts;
  $("statusbar").hidden = s.statusBar !== "on";
  applyStatusFields();
  if (editing && editing.mode === "textarea") applyEditorPrefs();
  if (current && !editing) refreshViewStatus();
}

function applyStatusFields() {
  const fields = getStatusFields();
  for (const field of STATUS_FIELDS) {
    const el = q(`#statusbar [data-sb="${field}"]`);
    if (el) el.hidden = !fields[field];
  }
}

// Bind every control carrying a data-setting attribute to the registry.
function syncSettingsForm() {
  for (const key of SETTING_KEYS) {
    const el = q(`[data-setting="${key}"]`);
    if (!el) continue;
    if (el.type === "checkbox") el.checked = getSetting(key) === "on";
    else el.value = getSetting(key);
  }
  const fields = getStatusFields();
  for (const field of STATUS_FIELDS) {
    const el = q(`[data-status-field="${field}"]`);
    if (el) el.checked = fields[field];
  }
  const pref = getEditorPreference();
  const radio = q(`input[name="editor-mode"][value="${pref}"]`);
  if (radio) radio.checked = true;
  syncThemeUI();
  syncPlacementUI();
  updateVimStatus();
  updateServerInfo();
  syncTrashSettings();
  refreshTrashState();
}

function updateVimStatus() {
  const state = getVimModeState();
  const st = $("cb-status");
  st.textContent = state.enabled ? "Enabled" : "Disabled";
  st.className = state.enabled ? "status-value enabled" : "status-value disabled";
  $("cb-failures").textContent = String(state.failures);
  $("cb-lastfail").textContent = state.lastFailure ? new Date(state.lastFailure).toLocaleString() : "Never";
}

async function updateServerInfo() {
  const dl = $("server-info");
  if (!dl) return;
  if (!slug) {
    dl.replaceChildren(infoRow("Status", "Open a notebook to see its storage."));
    return;
  }
  dl.replaceChildren(infoRow("Notebook", slug), infoRow("Storage", "Loading…"));
  try {
    const s = await notesApi.resourceStats(slug);
    if ($("server-info") !== dl || !slug) return;
    const used = Number(s.usedBytes) || 0;
    const reserved = Number(s.reservedBytes) || 0;
    const quota = Number(s.quotaBytes) || 0;
    const journal = Number(s.journal) || 0;
    dl.replaceChildren(
      infoRow("Notebook", slug),
      infoRow("Backend", s.backend || "—"),
      infoRow("Used", humanBytes(used + reserved)),
      infoRow("Quota", quota > 0 ? humanSizePair(used, quota) : "unlimited"),
      infoRow("Pending uploads", String(journal)),
    );
    if (quota > 0) dl.append(meterFor(used, quota));
  } catch (e) {
    if ($("server-info") === dl) dl.replaceChildren(infoRow("Notebook", slug), infoRow("Storage", e.message));
  }
}

function humanBytes(n) {
  if (!Number.isFinite(n) || n < 0) return "—";
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
  return `${v >= 10 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

function humanSizePair(used, quota) {
  return `${humanBytes(used)} of ${humanBytes(quota)}`;
}

function infoRow(label, value) {
  const frag = document.createDocumentFragment();
  const dt = document.createElement("dt");
  dt.textContent = label;
  const dd = document.createElement("dd");
  dd.textContent = value;
  frag.append(dt, dd);
  return frag;
}

function meterFor(used, quota) {
  const dd = document.createElement("dd");
  const meter = document.createElement("div");
  const pct = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
  meter.className = pct >= 90 ? "meter high" : "meter";
  const fill = document.createElement("span");
  fill.style.width = `${pct}%`;
  meter.append(fill);
  dd.append(meter);
  return dd;
}

function openSettingsModal(section) {
  $("settings-modal").hidden = false;
  syncSettingsForm();
  if (section) focusSettingsSection(section);
}

function closeSettingsModal() {
  $("settings-modal").hidden = true;
}

function focusSettingsSection(id) {
  const sec = document.getElementById(id);
  if (sec) sec.scrollIntoView({ block: "start" });
}

$("settingsbtn")?.addEventListener("click", () => openSettingsModal());
$("settings-close")?.addEventListener("click", closeSettingsModal);
$("settings-modal")?.querySelector(".modal-backdrop")?.addEventListener("click", closeSettingsModal);

// Setting controls: any [data-setting] change persists and applies.
document.addEventListener("change", (e) => {
  const el = e.target.closest?.("[data-setting]");
  if (!el) return;
  const key = el.dataset.setting;
  const value = el.type === "checkbox" ? (el.checked ? "on" : "off") : el.value;
  setSetting(key, value);
  applyPreferences();
});

document.addEventListener("change", (e) => {
  const el = e.target.closest?.("[data-status-field]");
  if (!el) return;
  setStatusField(el.dataset.statusField, el.checked);
  applyStatusFields();
});

qa('input[name="editor-mode"]').forEach((radio) => {
  radio.addEventListener("change", () => setEditorPreference(radio.value));
});

$("settings-modal")?.querySelectorAll(".settings-nav button").forEach((btn) => {
  btn.addEventListener("click", () => {
    focusSettingsSection(btn.dataset.target);
    for (const b of qa("#settings-modal .settings-nav button")) b.classList.toggle("active", b === btn);
  });
});

$("reset-vim")?.addEventListener("click", () => {
  resetVimCircuitBreaker();
  updateVimStatus();
  toast("Vim mode re-enabled.");
});

$("export-notebook")?.addEventListener("click", actionExportNotebook);
$("import-notes")?.addEventListener("click", () => $("import-file").click());
$("import-file")?.addEventListener("change", (e) => {
  if (e.target.files?.length) importFiles(e.target.files);
  e.target.value = "";
});

$("create-backup")?.addEventListener("click", () => guarded(async () => {
  const m = await notesApi.createBackup(slug);
  const box = document.createElement("div");
  const rows = [
    ["Created", m.createdAt ? new Date(m.createdAt).toLocaleString() : "—"],
    ["Bundle", m.bundlePath || "—"],
    ["State", m.statePath || "—"],
  ];
  const dl = document.createElement("dl");
  dl.className = "info-list";
  for (const [k, v] of rows) dl.append(infoRow(k, v));
  box.append(dl);
  if (Array.isArray(m.errors) && m.errors.length) {
    const p = document.createElement("p");
    p.className = "help-text";
    p.textContent = `Warnings: ${m.errors.join("; ")}`;
    box.append(p);
  }
  await infoDialog({ title: "Backup complete", message: "A full snapshot was written on the server.", extra: box });
}));

$("clear-drafts")?.addEventListener("click", async () => {
  const n = clearDrafts();
  toast(n ? `Cleared ${n} draft${n === 1 ? "" : "s"}.` : "No drafts to clear.");
  updateServerInfo();
});

$("reset-prefs")?.addEventListener("click", () => {
  resetPreferences();
  applyPreferences();
  syncSettingsForm();
  toast("Preferences reset.");
});

$("open-help")?.addEventListener("click", () => { closeSettingsModal(); openHelp(); });

// --- help overlay ---

function openHelp({ welcome = false } = {}) {
  renderHelp($("help-body"), { welcome });
  $("help-modal").hidden = false;
  $("help-done").textContent = welcome ? "Got it" : "Close";
  $("help-done").focus();
  if (welcome) markHelpSeen();
}

function closeHelp() {
  $("help-modal").hidden = true;
}

$("helpbtn")?.addEventListener("click", () => openHelp());
$("help-close")?.addEventListener("click", closeHelp);
$("help-done")?.addEventListener("click", closeHelp);
$("help-modal")?.querySelector(".modal-backdrop")?.addEventListener("click", closeHelp);

// First visit: open the guide once, automatically.
let welcomeShown = false;
function maybeWelcome() {
  if (welcomeShown || isHelpSeen()) return;
  welcomeShown = true;
  openHelp({ welcome: true });
}

// --- global Escape / modal close ---

window.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  if (!$("help-modal").hidden) closeHelp();
  else if (!$("settings-modal").hidden) closeSettingsModal();
  else if (!$("trash-modal").hidden) closeTrash();
});

// --- boot ---

applyUserTheme(getTheme());
applyPlacement(getPlacement());
syncThemeUI();
syncPlacementUI();
applyPreferences();
window.matchMedia("(max-width: 720px)").addEventListener?.("change", applyPreferences);

boot()
  .then(() => maybeWelcome())
  .catch((e) => {
    $("note").replaceChildren(emptyState({ icon: "⚠️", title: "Could not start", body: e.message }));
  });

// Expose a couple of helpers for debugging in the console.
window.nr = { openNote, openEditor, current: () => current };
