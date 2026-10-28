import { VimWasm, checkBrowserCompatibility } from '/vim/vimwasm.js';
import { uploadURL, markdownFor, rewriteImageThumbs, presignUpload, thumbURL } from '/lib/upload.js';
import {
  getVimModeState,
  recordVimFailure,
  shouldUseVim as shouldUseVimCore,
  resetVimCircuitBreaker,
  getEditorPreference,
  setEditorPreference,
} from '/lib/vim-mode.js';
import { fetchPanels, renderPanelChips } from '/lib/panels.js';
import { getUiPluginEnabled, setUiPluginEnabled, syncUiPlugins, destroyAllUiPlugins } from '/lib/ui-plugins.js';
import { htmlRendererPlugin } from '/lib/plugins/html-renderer.js';
import { calendarPlugin } from '/lib/plugins/calendar.js';
import { pomodoroPlugin } from '/lib/plugins/pomodoro.js';

const UI_PLUGIN_REGISTRY = [htmlRendererPlugin, calendarPlugin, pomodoroPlugin];
import { describeStatus, humanSize } from '/lib/stats.js';
function shouldUseVim() {
  return shouldUseVimCore({ checkCompatibility: () => checkBrowserCompatibility() });
}
import { VimEditor } from '/lib/vim-editor.js';
import {
  normalizeNote, changedRange, readFrontmatter, deriveTitle, localDate, isValidId,
  splitFrontmatter,
} from '/lib/frontmatter.js';
import { indentEdit } from '/lib/textedit.js';
import {
  formatTreeStatus, folderPaths, parentDir, joinPath, ancestors, isWithin,
  cleanRelPath, resolveChild, newNoteContent, baseName,
  folderChoices, displayName, renameTarget, moveTarget,
} from '/lib/tree.js';
import { renderTree } from '/lib/tree-view.js';
import { formDialog, confirmDialog, infoDialog, showMenu } from '/lib/dialog.js';
import {
  isValidLinkUrl, buildLinkMarkdown, buildNoteLinkMarkdown,
  collectNotePaths, resolveNotePath, filterNotePaths, bodyInsert, adjacentNote,
} from '/lib/attach.js';
import * as notesApi from '/lib/notes-api.js';
import { fetchIntegrationStatus, karakeepArchive, slinkUpload, isHttpUrl } from '/lib/services.js';
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
  getHomepage, setHomepage,
  getSetting, setSetting, getSettings, STATUS_FIELDS, getStatusFields, setStatusField,
  isHelpSeen, markHelpSeen, resetPreferences, clearDrafts,
  exportPreferences, importPreferences,
  getBookmarks, toggleBookmark, removeBookmark,
  getTagsCollapsed, setTagsCollapsed, getVimrc, setVimrc,
  getKeybindings, setKeybinding, resetKeybindings, DEFAULT_KEYBINDINGS,
  KEYBINDING_LABELS, matchesBinding,
} from '/lib/prefs.js';

const $ = (id) => document.getElementById(id);
const q = (sel) => document.querySelector(sel);
const qa = (sel) => [...document.querySelectorAll(sel)];

// --- Mermaid diagram rendering ---

let mermaidReady = null; // Promise<mermaid module> | null

async function loadMermaid() {
  if (mermaidReady) return mermaidReady;
  mermaidReady = import("https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs")
    .then((mod) => {
      const m = mod.default || mod;
      m.initialize({ startOnLoad: false, theme: "neutral", securityLevel: "strict" });
      return m;
    })
    .catch(() => null);
  return mermaidReady;
}

let mermaidSeq = 0;

async function renderMermaid(container) {
  const blocks = container.querySelectorAll("pre > code.language-mermaid");
  if (!blocks.length) return;
  const m = await loadMermaid();
  if (!m) return;
  const darkMode = document.documentElement.dataset.theme === "dark"
    || (document.documentElement.dataset.userTheme === "auto"
        && window.matchMedia("(prefers-color-scheme: dark)").matches);
  for (const code of blocks) {
    const pre = code.parentElement;
    const source = code.textContent;
    const id = `mermaid-${++mermaidSeq}`;
    try {
      const { svg } = await m.render(id, source);
      const div = document.createElement("div");
      div.className = "mermaid-diagram";
      div.innerHTML = svg;
      pre.replaceWith(div);
    } catch (_) {
      pre.classList.add("mermaid-error");
    }
  }
  void darkMode; // unused but kept for future theme-aware rendering
}

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

// --- sidebar: tags (modal list of the notes carrying a tag) ---

let tagsSeq = 0;

function drawTagsCollapsed() {
  const on = getTagsCollapsed();
  $("tags-list").hidden = on;
  $("tags-caret").textContent = on ? "▸" : "▾";
  $("tags-toggle").setAttribute("aria-expanded", String(!on));
}

// Tags live in note frontmatter, so any save, rename, move, delete or
// restore can change them. Debounced: autosave must not rescan the vault
// on every pause.
let tagsTimer = 0;
function scheduleTags() {
  clearTimeout(tagsTimer);
  tagsTimer = setTimeout(() => { refreshTags(); }, 800);
}

async function refreshTags() {
  const section = $("tags-section");
  if (!section) return;
  if (!slug) { section.hidden = true; return; }
  const seq = ++tagsSeq;
  const mine = slug;
  let tags = {};
  try {
    ({ tags = {} } = await notesApi.listTags(mine));
  } catch {
    /* tags are a convenience: a failed scan just leaves the section empty */
  }
  if (seq !== tagsSeq) return;
  const names = Object.keys(tags).sort((a, b) => a.localeCompare(b));
  const list = $("tags-list");
  list.replaceChildren();
  if (!names.length) {
    const p = document.createElement("p");
    p.className = "muted small";
    p.textContent = "No tags yet. Add tags: [a, b] to a note's frontmatter.";
    list.append(p);
  }
  for (const name of names) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "tag-item";
    b.setAttribute("role", "listitem");
    const label = document.createElement("span");
    label.textContent = `#${name}`;
    const count = document.createElement("span");
    count.className = "count";
    count.textContent = String(tags[name].length);
    b.append(label, count);
    b.addEventListener("click", () => showTagNotes(name, tags[name]));
    list.append(b);
  }
  section.hidden = false;
  drawTagsCollapsed();
}

async function showTagNotes(name, paths) {
  const ul = document.createElement("div");
  ul.className = "attach-note-list";
  for (const p of paths) {
    const item = document.createElement("button");
    item.type = "button";
    item.className = "attach-note-item";
    item.textContent = p;
    item.addEventListener("click", () => {
      // The dialog's only button is the last one: close it, then open.
      item.closest(".dialog")?.querySelector(".dialog-buttons button:last-child")?.click();
      openNote(p, null);
    });
    ul.append(item);
  }
  await infoDialog({
    title: `#${name} — ${paths.length} note${paths.length === 1 ? "" : "s"}`,
    message: "",
    extra: ul,
    okLabel: "Close",
  });
}

$("tags-toggle")?.addEventListener("click", () => {
  setTagsCollapsed(!getTagsCollapsed());
  drawTagsCollapsed();
});

function drawBookmarks() {
  const section = $("bookmarks-section");
  const list = $("bookmarks-list");
  if (!section || !list) return;
  const pins = slug ? getBookmarks(slug) : [];
  section.hidden = pins.length === 0;
  list.textContent = "";
  for (const path of pins) {
    const item = document.createElement("div");
    item.className = "bookmark-item" + (path === current ? " active" : "");
    const link = document.createElement("a");
    link.href = "#";
    link.className = "bookmark-link";
    link.textContent = baseName(path).replace(/\.md$/i, "");
    link.title = path;
    link.onclick = (e) => { e.preventDefault(); openNote(path, null); };
    const rm = document.createElement("button");
    rm.className = "bookmark-remove btn icon-btn ghost";
    rm.title = "Remove bookmark";
    rm.textContent = "×";
    rm.onclick = (e) => { e.stopPropagation(); removeBookmark(slug, path); drawBookmarks(); };
    item.append(link, rm);
    list.append(item);
  }
}

// --- sidebar: daily notes ---
async function refreshDailyNotes() {
  const section = $("daily-notes-section");
  const list = $("daily-notes-list");
  if (!section || !list) return;
  if (!slug) { section.hidden = true; return; }

  const folder = getSetting('dailyNotesFolder').trim() || 'daily';
  let notes = [];
  try {
    const tree = await notesApi.getTree(slug);
    function collect(node) {
      if (node.isDir) {
        for (const c of node.children || []) collect(c);
      } else if (node.path.startsWith(folder + '/') && node.path.endsWith('.md')) {
        notes.push(node);
      }
    }
    collect(tree);
  } catch {
    section.hidden = true;
    return;
  }

  // Sort by date (newest first)
  notes.sort((a, b) => b.path.localeCompare(a.path));

  list.replaceChildren();
  if (!notes.length) {
    const p = document.createElement("p");
    p.className = "muted small";
    p.textContent = "No daily notes yet. Click 📅 to create today's note.";
    list.append(p);
  }
  for (const note of notes) {
    const item = document.createElement("div");
    item.className = "daily-note-item" + (note.path === current ? " active" : "");
    const link = document.createElement("a");
    link.href = "#";
    link.className = "daily-note-link";
    link.textContent = note.path.replace(folder + '/', '').replace(/\.md$/, '');
    link.title = note.path;
    link.onclick = (e) => { e.preventDefault(); openNote(note.path, null); };
    item.append(link);
    list.append(item);
  }
  section.hidden = false;
}

$("daily-notes-toggle")?.addEventListener("click", () => {
  const on = $("daily-notes-list").hidden;
  $("daily-notes-list").hidden = !on;
  $("daily-notes-caret").textContent = on ? "▸" : "▾";
  $("daily-notes-toggle").setAttribute("aria-expanded", String(on));
});

// --- sidebar multi-select (Ctrl/Cmd-click, Shift-click for a range) ---

function notePathsInOrder() {
  const out = [];
  const walk = (node) => {
    for (const child of node.children || []) {
      if (child.isDir) walk(child);
      else out.push(child.path);
    }
  };
  if (treeData) walk(treeData);
  return out;
}

function updateMultiBar() {
  const bar = $("multi-bar");
  if (!bar) return;
  const n = multiSelected.size;
  bar.hidden = n === 0;
  $("multi-count").textContent = `${n} selected`;
  const names = [...multiSelected];
  bar.title = names.slice(0, 10).join("\n") + (names.length > 10 ? "\n…" : "");
}

function toggleMultiNote(path) {
  if (multiSelected.has(path)) multiSelected.delete(path);
  else multiSelected.add(path);
  multiAnchor = path;
  drawTree();
}

function selectMultiRange(from, to) {
  const order = notePathsInOrder();
  const a = order.indexOf(from);
  const b = order.indexOf(to);
  if (a < 0 || b < 0) return;
  const [lo, hi] = a <= b ? [a, b] : [b, a];
  for (const p of order.slice(lo, hi + 1)) multiSelected.add(p);
  drawTree();
}

function clearMultiNotes() {
  if (multiSelected.size === 0) return;
  multiSelected.clear();
  multiAnchor = null;
  drawTree();
}

// Plan a bulk move for every selected note before anything is written:
// resolves each destination, rejects same-name collisions and occupied
// targets up front (a partial move is worse than no move), and orders the
// moves so a note that must vacate a name goes before the one taking it.
function multiMovePlan(paths, dest, newFolder) {
  let dir = dest;
  if (String(newFolder ?? "").trim()) {
    const sub = resolveChild(dest, newFolder);
    if (!sub.ok) return { error: sub.error };
    dir = sub.path;
  }
  const live = new Set(notePathsInOrder());
  const moves = [];
  const targets = new Map(); // target path -> source path
  for (const from of paths) {
    if (parentDir(from) === dir) continue; // already there
    const to = cleanRelPath(joinPath(dir, baseName(from)));
    if (!to.ok) return { error: to.error };
    if (to.path === from) continue;
    const clash = targets.get(to.path);
    if (clash) {
      return { error: `Two selected notes are both named \u201c${baseName(from)}\u201d; rename one first.` };
    }
    targets.set(to.path, from);
    moves.push({ from, to: to.path });
  }
  if (!moves.length) return { error: "Every selected note is already in that folder." };
  const sources = new Set(moves.map((m) => m.from));
  for (const m of moves) {
    if (live.has(m.to) && !sources.has(m.to)) {
      return { error: `${m.to} already exists in the destination.` };
    }
  }
  // Execute in an order where each target is free by the time it is used;
  // a cycle (notes swapping names) cannot be done without a temp name.
  const occupied = new Set(live);
  const remaining = moves.slice();
  const order = [];
  while (remaining.length) {
    const i = remaining.findIndex((m) => !occupied.has(m.to));
    if (i < 0) {
      return { error: "Those moves would overwrite each other; move one note first." };
    }
    const m = remaining.splice(i, 1)[0];
    occupied.delete(m.from);
    occupied.add(m.to);
    order.push(m);
  }
  return { dir, moves: order };
}

async function moveMultiNotes() {
  const paths = [...multiSelected];
  if (paths.length === 0) return;
  if (paths.some((p) => editorBlocks(p, false))) return;
  const choices = folderChoices(treeData).map((c) => ({
    value: c.value,
    label: `${"\u2003".repeat(c.depth)}${c.label}`,
  }));
  const r = await formDialog({
    title: `Move ${paths.length} note${paths.length === 1 ? "" : "s"}`,
    message: `Where should ${paths.length === 1 ? `"${paths[0]}"` : `these ${paths.length} notes`} go?`,
    fields: [
      { name: "dest", label: "Destination folder", type: "select", options: choices, value: "" },
      { name: "newFolder", label: "\u2026or a new folder there", placeholder: "archive", hint: "Leave empty to use the folder above." },
    ],
    okLabel: "Move",
    wide: false,
    preview: (v) => {
      const p = multiMovePlan(paths, v.dest, v.newFolder);
      if (p.error) return p.error;
      const names = p.moves.slice(0, 3).map((m) => baseName(m.to)).join(", ");
      const more = p.moves.length > 3 ? ", \u2026" : "";
      return `${p.moves.length} note${p.moves.length === 1 ? "" : "s"} \u2192 ${p.dir || "notebook root"} (${names}${more})`;
    },
    validate: (v) => multiMovePlan(paths, v.dest, v.newFolder).error || null,
  });
  if (!r) return;
  const plan = multiMovePlan(paths, r.dest, r.newFolder);
  if (plan.error) { toast(plan.error, true); return; }
  await guarded(async () => {
    if (String(r.newFolder ?? "").trim()) {
      try { await notesApi.createFolder(slug, plan.dir); } catch (e) { if (e.status !== 409) throw e; }
    }
    let done = 0;
    const failed = [];
    for (const m of plan.moves) {
      try {
        await notesApi.moveNote(slug, m.from, m.to);
        followMove(m.from, m.to, false);
        done += 1;
      } catch (e) {
        failed.push(`${baseName(m.from)}: ${e.message}`);
      }
    }
    clearMultiNotes();
    ensureExpanded(plan.dir);
    await refreshTree();
    if (failed.length) {
      toast(`Moved ${done} of ${plan.moves.length} notes. Failed \u2014 ${failed.join("; ")}`, true);
    } else if (done) {
      toast(`Moved ${done} note${done === 1 ? "" : "s"}.`);
    }
  });
}

async function deleteMultiNotes() {
  const paths = [...multiSelected];
  if (paths.length === 0) return;
  if (paths.some((p) => editorBlocks(p, false))) return;
  const ok = await confirmDialog({
    title: paths.length === 1 ? "Delete note" : `Delete ${paths.length} notes`,
    message: `Move ${paths.length === 1 ? "\u201c" + paths[0] + "\u201d" : paths.length + " notes"} to the trash?`,
    okLabel: "Delete",
    danger: true,
  });
  if (!ok) return;
  await guarded(async () => {
    let done = 0;
    for (const path of paths) {
      try {
        await notesApi.deleteNote(slug, path);
        done += 1;
        clearViewIfGone(path, false);
      } catch (e) {
        toast(`Could not delete ${path}: ${e.message}`, true);
      }
    }
    clearMultiNotes();
    await refreshTree();
    if (done) toast(`Moved ${done} note${done === 1 ? "" : "s"} to the trash.`);
  });
}

function drawTree() {
  if (!treeData) return;
  drawBookmarks();
  const totals = renderTree($("tree"), treeData, {
    current,
    splitPath: splitOpen ? splitPath : null,
    multi: multiSelected,
    selected: selectedFolder,
    collapsed,
    decorations,
    onOpen: (path, el, ev) => {
      if (ev && (ev.metaKey || ev.ctrlKey)) {
        ev.preventDefault();
        toggleMultiNote(path);
        return;
      }
      if (ev && ev.shiftKey && multiAnchor) {
        ev.preventDefault();
        selectMultiRange(multiAnchor, path);
        return;
      }
      selectedFolder = null;
      clearMultiNotes();
      openNote(path, el);
    },
    onToggle: (path, isCollapsed) => {
      selectedFolder = path;
      collapsed = setCollapsed(slug, path, isCollapsed);
      drawTree();
    },
    onMenu: showNodeMenu,
    onDropNote: async (notePath, folderPath) => {
      await guarded(async () => {
        const dest = cleanRelPath(joinPath(folderPath, baseName(notePath)));
        if (dest === notePath) return;
        await notesApi.moveNote(slug, notePath, dest);
        followMove(notePath, dest, false);
        ensureExpanded(folderPath);
        await refreshTree();
        toast(`Moved to ${folderPath || "/"}`);
      });
    },
  });
  $("tree-status").textContent = formatTreeStatus(totals);
  updateMultiBar();
}

async function refreshTree() {
  if (!slug) return;
  treeData = await notesApi.getTree(slug);
  const live = new Set(notePathsInOrder());
  for (const p of multiSelected) if (!live.has(p)) multiSelected.delete(p);
  if (multiAnchor && !live.has(multiAnchor)) multiAnchor = null;
  drawTree();
  scheduleTags();
  refreshDailyNotes();
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
    title: "Welcome to noteroot",
    body: "Create your first notebook to get started.",
    action: actionButton("Create notebook\u2026", () => actionNewNotebook()),
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
 * Open the notebook's homepage when one is configured and still exists;
 * otherwise the empty state. Called after the tree loads (boot, notebook
 * switch) and when a note closes, so the homepage works as a dashboard:
 * its wikilinks navigate through the normal note-open path.
 */
// The configured homepage may lack ".md", differ in case or carry stray
// slashes; map it onto a real note path in the tree, or "" if none.
function resolveHomepage(configured) {
  const want = String(configured || "").trim().replace(/^\/+/, "");
  if (!want || !treeData) return "";
  for (const cand of [want, `${want}.md`]) {
    const n = findNode(treeData, cand);
    if (n && !n.isDir) return n.path;
  }
  const lower = want.toLowerCase().replace(/\.md$/, "");
  let hit = "";
  (function walk(n) {
    for (const c of n.children || []) {
      if (hit) return;
      if (c.isDir) walk(c);
      else if (c.path.toLowerCase().replace(/\.md$/, "") === lower) hit = c.path;
    }
  })(treeData);
  return hit;
}

async function openHomepage() {
  if (!slug) return;
  const configured = getHomepage(slug);
  const home = resolveHomepage(configured);
  if (configured && !home) toast(`Startpage note not found: ${configured}`, true);
  if (home) {
    // Pass the tree node so the homepage is highlighted like a click.
    // Attribute-selector quoting is enough: vault paths never contain
    // double quotes (and jsdom lacks CSS.escape).
    const el = document.querySelector(`#tree .node[data-path="${home}"]`);
    await openNote(home, el ?? undefined);
    return;
  }
  showNoSelection();
}

/**
 * The one place that decides what the content pane shows and which
 * buttons go with it. Every transition — opening a note, opening or
 * closing the editor, a note disappearing under us, search results —
 * goes through here, so Edit/Save/Done can never disagree with what is
 * actually on screen.
 *
 *   "empty"   nothing open: the pane explains itself, no note actions
 *   "read"    a rendered note: Edit and the ⋯ menu
 *   "edit"    the editor: Save/Done in its own toolbar, no Edit button
 *   "results" search hits: no note is open
 *   "trash"   the trash, or a trashed note: actions live in the pane
 *
 * @param {"empty"|"read"|"edit"|"results"|"trash"} next
 */
function setView(next) {
  view = next;
  const editingView = next === "edit";
  $("note").hidden = editingView;
  $("editor").hidden = !editingView;
  $("content").classList.toggle("editing", editingView);
  // Edit belongs to reading only: while the editor is open, Done is the
  // way out, and nothing else on screen is an editable note.
  $("editbtn").hidden = next !== "read";
  $("historybtn").hidden = next !== "read";
  $("copybtn").hidden = next !== "read";
  $("note-bar").hidden = next !== "read";
  if (next === "read") $("note-path").textContent = current || "";
  $("notemenu").hidden = next !== "read" && next !== "edit";
  // The panel describes the open note; anything else leaves it empty.
  if (next !== "read") clearReadPane();
}

async function openNote(path, btn) {
  if (!slug) return;
  // Picking the note you are editing keeps you in the editor.
  if (editing && editing.path === path) return;
  // Going anywhere else ends the editing session: save, then close it.
  // A session left running behind the reader still counted as "open
  // for editing": Edit did nothing, and Rename/Delete refused.
  if (editing) {
    const left = editing.path;
    if (!(await flushEditor())) toast(`Could not save “${left}”; your changes are kept as a draft in this browser.`, true);
    closeEditorNow();
  }
  current = path;
  drawBookmarks();
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
    if (e.status === 423) {
      showLockedState(path);
      return;
    }
    $("note").replaceChildren(emptyState({
      icon: "⚠️",
      title: "Could not open this note",
      body: e.message,
    }));
    setView("empty");
  }
}

// The pane shown when a note is behind an unopened lock: an explicit
// unlock button rather than an error, plus the honest caveat.
function showLockedState(path) {
  // Determine kind from the tree if possible so the unlock dialog says the
  // right thing for a locked folder or notebook-covered path.
  let kind = "note";
  const node = findNode(treeData, path);
  if (node) {
    kind = node.isDir ? "folder" : "note";
  } else if (path === "") {
    kind = "notebook";
  }
  $("note").replaceChildren(emptyState({
    icon: "🔒",
    title: kind === "notebook" ? "This notebook is locked" : kind === "folder" ? "This folder is locked" : "This note is locked",
    body: "Enter the password to read it in this browser. (A content lock hides the note in the app; it does not encrypt the file.)",
    action: (() => {
      const b = actionButton("Unlock…", async () => {
        if (await promptUnlock(path, kind)) await openNote(path, null);
      });
      return b;
    })(),
  }));
  setView("empty");
}


function applyExternalLinkTargets(container) {
  const openNew = getSetting("linkTarget") !== "same-tab";
  for (const a of container.querySelectorAll("a[href]")) {
    if (a.classList.contains("wikilink")) continue;
    const href = a.getAttribute("href") || "";
    if (!/^https?:\/\//i.test(href)) continue;
    if (openNew) {
      a.target = "_blank";
      a.rel = "noopener noreferrer";
    } else {
      a.removeAttribute("target");
      a.removeAttribute("rel");
    }
  }
}

// Tag chips shown above a note in read mode; a click lists the notes that
// share the tag (same modal as the sidebar).
function tagChips(tags) {
  const list = (tags || []).map((t) => String(t).replace(/^#/, "")).filter(Boolean);
  if (!list.length) return null;
  const box = document.createElement("div");
  box.className = "note-tags";
  box.setAttribute("aria-label", "Tags");
  for (const t of list) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "tag-chip";
    b.textContent = `#${t}`;
    b.addEventListener("click", async () => {
      try {
        const { tags = {} } = await notesApi.listTags(slug);
        showTagNotes(t, tags[t] || []);
      } catch (e) {
        toast(e.message, true);
      }
    });
    box.append(b);
  }
  return box;
}

function renderRead(n) {
  const parts = [];
  if (n.frontmatterError) parts.push(`<div class="warn">frontmatter: ${esc(n.frontmatterError)}</div>`);
  if (n.renderError) parts.push(`<div class="warn">render: ${esc(n.renderError)}</div>`);
  parts.push(rewriteImageThumbs(n.html || "", slug, { maxWidth: 800, maxHeight: 600 }));
  $("note").innerHTML = parts.join("");
  applyExternalLinkTargets($("note"));
  renderMermaid($("note"));
  activateTaskCheckboxes($("note"), n);
  // The note body stays the note: contents, tags and links live in the
  // panel beside it.
  renderReadPane(n);
  $("note").scrollTop = 0;
  document.dispatchEvent(new CustomEvent("noteread", { detail: { note: n } }));
}

/**
 * Make goldmark's disabled task checkboxes interactive: toggling one
 * fetches the raw markdown, flips the nth `[ ]`/`[x]` occurrence that
 * matches this checkbox's position, and saves back.
 */
function activateTaskCheckboxes(container, n) {
  const boxes = container.querySelectorAll('input[type="checkbox"]');
  if (!boxes.length) return;
  boxes.forEach((cb, idx) => {
    cb.removeAttribute("disabled");
    cb.addEventListener("change", async () => {
      const checked = cb.checked;
      try {
        const { text, sha } = await notesApi.getSource(slug, n.path);
        const updated = toggleNthCheckbox(text, idx, checked);
        if (updated === text) return;
        await notesApi.saveNote(slug, n.path, updated, { baseSha: sha });
        // Refresh the read pane silently (don't re-render scrolling position)
        const fresh = await notesApi.getNote(slug, n.path);
        if (fresh) {
          Object.assign(n, fresh);
          const newBoxes = container.querySelectorAll('input[type="checkbox"]');
          if (newBoxes[idx]) {
            // Swap the inner HTML rather than re-calling renderRead to
            // avoid losing scroll position; the state is already correct.
            newBoxes[idx].checked = checked;
          }
        }
      } catch {
        cb.checked = !checked; // revert on error
        toast("Could not save checkbox change.", true);
      }
    });
  });
}

/**
 * Toggle the `idx`-th markdown task-list checkbox pattern in `src`.
 * Handles `- [ ]`, `* [ ]`, `1. [ ]` and their checked variants.
 */
function toggleNthCheckbox(src, idx, checked) {
  const pattern = /^([ \t]*(?:[-*]|\d+\.)\s+)\[([ xX])\]/gm;
  let count = 0;
  return src.replace(pattern, (match, prefix, mark) => {
    if (count++ === idx) return `${prefix}[${checked ? "x" : " "}]`;
    return match;
  });
}

// --- read-mode side panel: contents, tags, links -----------------------
//
// A hideable panel beside the main pane holding the table of contents,
// the note's tags and its outgoing/backlink lists, so the reading
// column itself carries nothing but the note. Toggled from the top bar
// and from Settings, and parked on either side. Phones have no room to
// spare: the panel starts closed there and only opens on request.

let phonePaneOpen = false;

function readPaneWanted() {
  if (isPhone()) return phonePaneOpen;
  return getSetting("readPane") === "on";
}

function applyReadPane() {
  const on = readPaneWanted();
  const left = getSetting("readPaneSide") === "left";
  const content = $("content");
  if (!content) return;
  content.classList.toggle("pane-on", on);
  content.classList.toggle("pane-left", on && left);
  $("readpanebtn")?.setAttribute("aria-pressed", String(on));
  if ($("readpane")) $("readpane").hidden = !on;
}

function clearReadPane() {
  const pane = $("readpane");
  if (!pane) return;
  for (const sec of pane.querySelectorAll(".rp-sec")) sec.hidden = true;
  for (const list of pane.querySelectorAll(".rp-toc, .rp-tags, .rp-list")) list.replaceChildren();
  const empty = pane.querySelector(".rp-empty");
  if (empty) empty.hidden = false;
}

/** Fill the panel for the note on screen; null clears it. */
function renderReadPane(n) {
  const pane = $("readpane");
  if (!pane) return;
  if (!n) {
    clearReadPane();
    return;
  }
  const art = $("note");

  // Contents — every heading in the rendered note, indented by level.
  const tocSec = pane.querySelector("#rp-toc");
  const nav = tocSec.querySelector(".rp-toc");
  nav.replaceChildren();
  const heads = [...art.querySelectorAll("h1, h2, h3, h4, h5, h6")];
  for (const h of heads) {
    const level = Number(h.tagName[1]) || 1;
    const a = document.createElement("a");
    a.href = "#";
    a.textContent = h.textContent.trim() || "(untitled)";
    a.style.paddingLeft = `${Math.min(level, 5) * 10}px`;
    a.addEventListener("click", (e) => {
      e.preventDefault();
      art.scrollTop += h.getBoundingClientRect().top - art.getBoundingClientRect().top - 8;
    });
    nav.append(a);
  }
  tocSec.hidden = heads.length === 0;

  // Tags — the same chips the note used to carry above its text.
  const tagsSec = pane.querySelector("#rp-tags");
  const tagBox = tagsSec.querySelector(".rp-tags");
  tagBox.replaceChildren();
  const chips = tagChips(n.tags);
  if (chips) tagBox.append(chips);
  tagsSec.hidden = !chips;

  // Outgoing links and backlinks.
  const outSec = pane.querySelector("#rp-out");
  const outUl = outSec.querySelector(".rp-list");
  outUl.replaceChildren(...(n.outgoing || []).map(outgoingItem));
  outSec.hidden = !(n.outgoing && n.outgoing.length);

  const backSec = pane.querySelector("#rp-back");
  const backUl = backSec.querySelector(".rp-list");
  backUl.replaceChildren(...(n.backlinks || []).map(backlinkItem));
  backSec.hidden = !(n.backlinks && n.backlinks.length);

  const empty = pane.querySelector(".rp-empty");
  if (empty) empty.hidden = ![tocSec, tagsSec, outSec, backSec].every((s) => s.hidden);
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

// --- split (vertical) panes ---
//
// Two panes side by side: the main pane keeps the note/editor it always
// had, the second pane shows another note. The second pane can be edited
// too, in plain text, with its own save session — so two files can be
// open at the same time without losing either one.

let splitOpen = false;   // the second pane is on screen
let splitPath = null;    // note shown in the second pane
let splitSession = null; // {path, baseSha, lastSaved, dirty, saveTimer, noteId, created, updated}
let multiSelected = new Set(); // sidebar notes picked for a bulk action
let multiAnchor = null; // last clicked note, for Shift ranges
let splitSeq = 0;

function setSplitSaveState(text, state) {
  const el = $("split-savestate");
  if (!el) return;
  el.textContent = text;
  if (state) el.dataset.state = state;
  else delete el.dataset.state;
}

/** Show or hide the second pane. Hiding it saves what it holds. */
async function setSplitOpen(on) {
  if (on === splitOpen) return;
  splitOpen = on;
  $("content").classList.toggle("split", on);
  $("split-pane").hidden = !on;
  $("splitbtn").setAttribute("aria-pressed", on ? "true" : "false");
  if (on) {
    if (!splitPath) {
      $("split-note").replaceChildren(emptyState({
        icon: "◫",
        title: "Nothing in the second pane",
        body: "Open a note here from the sidebar menu (⋮ → Open in split pane).",
      }));
    }
    return;
  }
  const ok = await flushSplit();
  if (!ok) {
    toast("Could not save the second-pane note; it stays open.", true);
    splitOpen = true;
    $("content").classList.add("split");
    $("split-pane").hidden = false;
    $("splitbtn").setAttribute("aria-pressed", "true");
    return;
  }
  closeSplitPane();
}

function closeSplitPane() {
  splitPath = null;
  if (treeData) drawTree();
  splitSession = null;
  $("split-path").textContent = "Nothing open";
  $("split-note").replaceChildren();
  $("split-note").hidden = false;
  $("split-body").hidden = true;
  $("split-body").value = "";
  $("split-save").hidden = true;
  $("split-done").hidden = true;
  $("split-edit").hidden = false;
  $("split-edit").disabled = true;
  $("split-swap").disabled = true;
  setSplitSaveState("", "");
}

/** Load a note into the second pane, replacing whatever was there. */
async function openInSplit(path) {
  if (!slug || !path) return;
  if (splitSession) {
    await flushSplit();
    closeSplitEditor();
  }
  if (!splitOpen) await setSplitOpen(true);
  const seq = ++splitSeq;
  splitPath = path;
  $("split-path").textContent = path;
  $("split-edit").disabled = false;
  $("split-swap").disabled = !current;
  try {
    const n = await notesApi.getNote(slug, path);
    if (seq !== splitSeq) return;
    renderSplitRead(n);
  } catch (e) {
    if (seq !== splitSeq) return;
    $("split-note").replaceChildren(emptyState({
      icon: "⚠️",
      title: "Could not open this note",
      body: e.message,
    }));
  }
  if (seq === splitSeq) drawTree();
}

function renderSplitRead(n) {
  const parts = [];
  if (n.frontmatterError) parts.push(`<div class="warn">frontmatter: ${esc(n.frontmatterError)}</div>`);
  if (n.renderError) parts.push(`<div class="warn">render: ${esc(n.renderError)}</div>`);
  parts.push(rewriteImageThumbs(n.html || "", slug, { maxWidth: 800, maxHeight: 600 }));
  $("split-note").innerHTML = parts.join("");
  const splitChips = tagChips(n.tags);
  if (splitChips) $("split-note").prepend(splitChips);
  applyExternalLinkTargets($("split-note"));
  renderMermaid($("split-note"));
  $("split-note").append(linksPanels(n));
  $("split-note").scrollTop = 0;
}

// --- editing in the second pane ---

async function openSplitEditor() {
  if (!splitPath || splitSession) return;
  let src;
  try {
    src = await notesApi.getSource(slug, splitPath);
  } catch (e) {
    toast(e.message, true);
    return;
  }
  const fm = readFrontmatter(src.text);
  splitSession = {
    path: splitPath,
    baseSha: src.sha,
    lastSaved: src.text,
    dirty: false,
    noteId: fm.id || "",
    created: fm.created || "",
    updated: fm.updated || "",
    saveTimer: 0,
  };
  const ta = $("split-body");
  ta.value = src.text;
  ta.hidden = false;
  updateLineGutter(ta, $("splitgutter"), $("splitmirror"));
  ta.oninput = () => markSplitDirty();
  $("split-note").hidden = true;
  $("split-save").hidden = false;
  $("split-done").hidden = false;
  $("split-edit").hidden = true;
  setSplitSaveState("", "");
  ta.focus();
}

function markSplitDirty() {
  if (!splitSession) return;
  splitSession.dirty = true;
  clearTimeout(splitSession.saveTimer);
  const ms = Number(getSetting("autosave"));
  if (ms > 0) splitSession.saveTimer = setTimeout(() => { flushSplit(); }, ms);
  setSplitSaveState("Editing…", "dirty");
}

async function flushSplit() {
  if (!splitSession) return true;
  clearTimeout(splitSession.saveTimer);
  const content = $("split-body").value;
  if (content === splitSession.lastSaved) return true;
  return saveSplitContent(content, false);
}

async function saveSplitContent(content, manual) {
  const s = splitSession;
  if (!s) return false;
  setSplitSaveState("Saving…", "dirty");
  const norm = normalizeNote(content, {
    path: s.path,
    id: s.noteId,
    created: s.created,
    updated: s.updated,
    touch: true,
  });
  const sent = norm.changed ? norm.content : content;
  try {
    const res = await notesApi.saveNote(slug, s.path, sent, { baseSha: s.baseSha });
    if (splitSession !== s) return true;
    s.baseSha = res.sha || s.baseSha;
    s.lastSaved = sent;
    s.dirty = false;
    setSplitSaveState(manual ? "Saved." : "Saved", "");
    refreshMainRead(s.path);
    scheduleTags();
    return true;
  } catch (e) {
    setSplitSaveState(`Save failed: ${e.message}`, "error");
    return false;
  }
}

function closeSplitEditor() {
  clearTimeout(splitSession?.saveTimer);
  splitSession = null;
  $("split-body").hidden = true;
  $("split-body").value = "";
  $("split-note").hidden = false;
  $("split-save").hidden = true;
  $("split-done").hidden = true;
  $("split-edit").hidden = false;
  setSplitSaveState("", "");
}

/** Esc in the second pane: save and close its editor, like the main one. */
async function saveAndCloseSplit() {
  if (!splitSession) return;
  const path = splitSession.path;
  const ok = await flushSplit();
  closeSplitEditor();
  if (!ok) {
    toast("Could not save the second-pane note; your text is still there.", true);
    return;
  }
  try {
    const n = await notesApi.getNote(slug, path);
    if (splitPath === path) renderSplitRead(n);
  } catch {
    /* the saved text is on the server; the read view can wait */
  }
}

// With the same note on both sides, the read side follows the edit side.
let paneRefreshTimer = 0;
function refreshSplitRead(path) {
  if (!splitOpen || splitPath !== path || splitSession) return;
  clearTimeout(paneRefreshTimer);
  paneRefreshTimer = setTimeout(async () => {
    try {
      const n = await notesApi.getNote(slug, path);
      if (splitOpen && splitPath === path && !splitSession) renderSplitRead(n);
    } catch { /* the read side can wait for the next save */ }
  }, 400);
}

function refreshMainRead(path) {
  if (!splitOpen || current !== path || editing) return;
  clearTimeout(paneRefreshTimer);
  paneRefreshTimer = setTimeout(async () => {
    try {
      const n = await notesApi.getNote(slug, path);
      if (current === path && !editing) renderRead(n);
    } catch { /* ditto */ }
  }, 400);
}

const SPLIT_MIN_WIDTH = 900;

/**
 * Split button: with a note open, show the same note on both sides — the
 * editor on the side chosen in Settings, a live read view on the other.
 */
async function toggleSplit() {
  if (splitOpen) { await setSplitOpen(false); return; }
  if (window.innerWidth < SPLIT_MIN_WIDTH) {
    toast("The screen is too narrow for split view.", true);
    return;
  }
  const path = current;
  if (!path || !lastNote) { await setSplitOpen(true); return; }
  const editLeft = getSetting("splitEditSide") !== "right";
  if (editLeft) {
    await openInSplit(path);
    if (!editing) await openEditor();
  } else {
    if (editing) await flushEditor();
    await openInSplit(path);
    await openSplitEditor();
  }
}

/** Swap the two panes: the second-pane note moves to the main pane. */
async function swapPanes() {
  if (!splitPath || !current) return;
  const a = current;
  const b = splitPath;
  await openNote(b);
  await openInSplit(a);
}

/** Insert text at the cursor of whichever editor has the focus. */
function insertIntoSplitEditor(text) {
  if (!splitSession) return false;
  const ta = $("split-body");
  if (!ta || ta.hidden) return false;
  // With both panes open, the main editor keeps the insert unless the
  // second pane's textarea is the one being typed in.
  if (editing && document.activeElement !== ta) return false;
  const at = bodyInsert(ta.value, ta.selectionStart, ta.selectionEnd, text);
  ta.value = ta.value.slice(0, at.start) + at.text + ta.value.slice(at.end);
  const pos = at.start + at.text.length;
  ta.selectionStart = ta.selectionEnd = pos;
  ta.focus();
  markSplitDirty();
  return true;
}

$("splitbtn")?.addEventListener("click", () => { toggleSplit(); });
$("readpanebtn")?.addEventListener("click", () => {
  const next = !readPaneWanted();
  if (isPhone()) phonePaneOpen = next;
  else setSetting("readPane", next ? "on" : "off");
  applyReadPane();
});
$("readpane-close")?.addEventListener("click", () => {
  if (isPhone()) phonePaneOpen = false;
  else setSetting("readPane", "off");
  applyReadPane();
});
$("split-close")?.addEventListener("click", () => { setSplitOpen(false); });
$("split-swap")?.addEventListener("click", () => { swapPanes(); });
$("split-edit")?.addEventListener("click", () => { openSplitEditor(); });
$("split-save")?.addEventListener("click", () => {
  if (!splitSession) return;
  saveSplitContent($("split-body").value, true);
});
$("split-done")?.addEventListener("click", () => { saveAndCloseSplit(); });

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
  const out = panel("Outgoing links", n.outgoing, outgoingItem);
  const back = panel("Backlinks", n.backlinks, backlinkItem);
  if (out) frag.append(out);
  if (back) frag.append(back);
  return frag;
}

/** One outgoing link: a note link with its path, or dead-link text. */
function outgoingItem(l) {
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
}

/** One backlink: a link to the note that points here. */
function backlinkItem(l) {
  const li = document.createElement("li");
  li.append(noteLink(l.path, l.title || l.path));
  return li;
}

// --- Note History (dedicated window) ---

const historyState = {
  path: "",
  commits: [],
  index: -1,
  hasMore: true,
  beforeHash: "",
  loading: false,
  seq: 0, // selection token: only the newest diff request may render
};

function openHistoryWindow() {
  if (!current || !slug) return;
  historyState.path = current;
  historyState.commits = [];
  historyState.index = -1;
  historyState.hasMore = true;
  historyState.beforeHash = "";
  historyState.loading = false;
  $("history-path").textContent = current;
  $("history-list").replaceChildren();
  $("history-diff").textContent = "";
  $("history-position").textContent = "";
  $("history-modal").hidden = false;
  loadHistoryCommits().then(() => {
    if (historyState.commits.length) selectHistoryCommit(0);
  });
}

function closeHistoryWindow() {
  $("history-modal").hidden = true;
}

async function loadHistoryCommits() {
  if (historyState.loading || !historyState.hasMore) return;
  historyState.loading = true;
  const btn = $("history-load-more");
  btn.hidden = true;
  btn.textContent = "Loading…";
  btn.disabled = true;
  try {
    const { getHistory } = await import('/lib/notes-api.js');
    const data = await getHistory(slug, historyState.path, { limit: 30, before: historyState.beforeHash });
    const list = $("history-list");
    for (const c of data.commits || []) {
      historyState.commits.push(c);
      // Capture this row's position now: the handler must open the commit
      // that was clicked, not whichever commit happens to be last later on.
      const index = historyState.commits.length - 1;
      const li = document.createElement("li");
      li.className = "history-item";
      li.dataset.index = String(index);
      const date = new Date(c.date * 1000).toLocaleString();
      li.innerHTML = `
        <span class="history-hash" title="${c.hash}">${c.shortHash}</span>
        <span class="history-date">${date}</span>
        <span class="history-message">${esc(c.message)}</span>
        ${c.isMerge ? '<span class="badge merge">merge</span>' : ''}
      `;
      li.addEventListener("click", () => selectHistoryCommit(index));
      list.append(li);
    }
    if (data.commits && data.commits.length) {
      historyState.beforeHash = data.commits[data.commits.length - 1].hash;
    }
    historyState.hasMore = !!data.hasMore;
    if (historyState.hasMore) {
      btn.hidden = false;
      btn.textContent = "Load more…";
      btn.disabled = false;
    }
    updateHistoryNav();
  } catch (e) {
    toast("Failed to load history", true);
    console.error("[history]", e);
  } finally {
    historyState.loading = false;
  }
}

function updateHistoryNav() {
  const n = historyState.commits.length;
  const i = historyState.index;
  $("history-position").textContent = n ? `${i + 1} of ${n}` : "No history";
  // Newer = smaller index; older = larger index.
  $("history-newer").disabled = i <= 0;
  $("history-older").disabled = i < 0 || i >= n - 1;
}

async function selectHistoryCommit(i) {
  const c = historyState.commits[i];
  if (!c) return;
  historyState.index = i;
  // Highlight the selected row.
  $("history-list").querySelectorAll(".history-item").forEach((li) => {
    li.classList.toggle("active", Number(li.dataset.index) === i);
  });
  updateHistoryNav();
  const diffEl = $("history-diff");
  diffEl.textContent = "Loading…";
  const seq = (historyState.seq += 1);
  try {
    const { getDiff } = await import('/lib/notes-api.js');
    const data = await getDiff(slug, historyState.path, c.hash + "^", c.hash);
    if (seq !== historyState.seq) return; // someone clicked on: drop this one
    diffEl.replaceChildren(renderColoredDiff(data.diff || "(no changes)"));
  } catch (e) {
    if (seq !== historyState.seq) return;
    diffEl.textContent = "Failed to load diff.";
    console.error("[history-diff]", e);
  }
}

/** Turn a unified diff into a coloured, line-by-line DOM fragment. */
function renderColoredDiff(diffText) {
  const frag = document.createDocumentFragment();
  for (const line of String(diffText).split("\n")) {
    const div = document.createElement("div");
    div.className = "diff-line";
    if (line.startsWith("@@")) div.classList.add("diff-hunk");
    else if (line.startsWith("+")) div.classList.add("diff-add");
    else if (line.startsWith("-")) div.classList.add("diff-del");
    else if (line.startsWith("diff ") || line.startsWith("index ")
      || line.startsWith("---") || line.startsWith("+++")) div.classList.add("diff-meta");
    div.textContent = line || " ";
    frag.append(div);
  }
  return frag;
}

$("historybtn")?.addEventListener("click", () => openHistoryWindow());

// --- copy the open note to the clipboard, without its frontmatter ------

/** Copy text; false when the browser refuses (no permission, no API). */
async function copyText(text) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through to the legacy path */
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.append(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}

$("copybtn")?.addEventListener("click", async () => {
  if (!slug || !current) return;
  const btn = $("copybtn");
  btn.disabled = true;
  try {
    const { text } = await notesApi.getSource(slug, current);
    // splitFrontmatter().body is the note with the --- block (if any)
    // cut away; a note without frontmatter yields its whole source.
    const body = splitFrontmatter(text).body.replace(/\s+$/, "");
    if (!body) {
      toast("This note has no content to copy.", true);
      return;
    }
    const ok = await copyText(body + "\n");
    toast(ok ? "Note copied (frontmatter left out)." : "Could not reach the clipboard.", !ok);
  } catch (e) {
    toast(e.message, true);
  } finally {
    btn.disabled = false;
  }
});
$("history-close")?.addEventListener("click", closeHistoryWindow);
$("history-modal")?.querySelector(".modal-backdrop")?.addEventListener("click", closeHistoryWindow);
$("history-newer")?.addEventListener("click", () => {
  if (historyState.index > 0) selectHistoryCommit(historyState.index - 1);
});
$("history-older")?.addEventListener("click", () => {
  if (historyState.index < historyState.commits.length - 1) selectHistoryCommit(historyState.index + 1);
});
$("history-load-more")?.addEventListener("click", loadHistoryCommits);

for (const elId of ["note", "readpane", "split-note"]) {
  $(elId)?.addEventListener("click", (e) => {
    const a = e.target.closest?.("a.wikilink[data-path]");
    if (!a) return;
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    openNote(a.dataset.path, null);
  });
}

async function fetchVersion() {
  try {
    const res = await fetch("/healthz", { cache: "no-store" });
    if (!res.ok) return;
    const data = await res.json();
    const v = typeof data?.version === "string" ? data.version.trim() : "";
    if (!v) return;
    const badge = $("app-version");
    if (badge) {
      badge.textContent = v;
      badge.hidden = false;
      badge.title = `noteroot ${v}`;
    }
    const about = $("about-version");
    if (about) about.textContent = v;
  } catch {
    /* offline or old server: keep the placeholders */
  }
}

async function boot() {
  fetchVersion(); // not awaited: never delay the first notebook load
  const { notebooks } = await api("/api/v1/notebooks");
  fillNotebookOptions(notebooks);
  $("notebooks").onchange = () => onNotebookSelect($("notebooks").value);
  if (!notebooks.length) {
    showNoNotebooks();
    return;
  }
  await loadTree(notebooks[0].slug);
}

// Pseudo-options ride at the bottom of the selector. Their values can
// never collide with a real slug: underscores are illegal there.
const NOTEBOOK_ACTIONS = [
  { value: "__new__", label: "＋ New notebook…" },
  { value: "__rename__", label: "✎ Rename notebook…" },
  { value: "__delete__", label: "🗑 Delete notebook…" },
];

function fillNotebookOptions(notebooks) {
  const sel = $("notebooks");
  sel.replaceChildren();
  for (const nb of notebooks) {
    const o = document.createElement("option");
    o.value = o.textContent = nb.slug;
    sel.append(o);
  }
  if (notebooks.length) {
    const sep = document.createElement("option");
    sep.disabled = true;
    sep.textContent = "──────────";
    sel.append(sep);
  }
  for (const a of NOTEBOOK_ACTIONS) {
    const o = document.createElement("option");
    o.value = a.value;
    o.textContent = a.label;
    // Rename and Delete need a notebook to act on.
    if (a.value !== "__new__" && !notebooks.length) o.disabled = true;
    sel.append(o);
  }
  if (slug) sel.value = slug;
}

// Programmatic .value sets fire no change event, so the snap-back is
// safe: the real selection never leaves `slug`.
async function onNotebookSelect(value) {
  const sel = $("notebooks");
  if (value.startsWith("__")) {
    sel.value = slug || "";
    if (value === "__new__") await actionNewNotebook();
    else if (value === "__rename__") await actionRenameNotebook();
    else if (value === "__delete__") await actionDeleteNotebook();
    return;
  }
  await switchNotebook(value);
}

async function refreshNotebookList(selectSlug) {
  const { notebooks } = await api("/api/v1/notebooks");
  fillNotebookOptions(notebooks);
  const target = selectSlug
    || (notebooks.some((n) => n.slug === slug) ? slug : notebooks[0]?.slug);
  if (target) await loadTree(target);
  else {
    slug = null;
    current = null;
    lastNote = null;
    showNoNotebooks();
    refreshTrashState();
  }
}

async function switchNotebook(s) {
  if (s === slug) return;
  await flushEditor();
  await loadTree(s);
}

async function loadTree(s) {
  if (slug) await flushEditor();
  destroyAllUiPlugins();
  // The second pane belongs to the old notebook: save it, then let it go.
  if (splitSession) await flushSplit();
  closeSplitEditor();
  closeSplitPane();
  splitOpen = false;
  $("content").classList.remove("split");
  $("split-pane").hidden = true;
  $("splitbtn").setAttribute("aria-pressed", "false");
  slug = s;
  current = null;
  lastNote = null;
  sessionUnlocked.clear();
  openSeq += 1;
  closeEditorNow();
  $("q").value = "";
  $("notebooks").value = s;
  selectedFolder = null;
  multiSelected = new Set();
  multiAnchor = null;
  collapsed = getCollapsed(s);
  decorations = getDecorations(s);
  setStatus("Read", { text: null });
  showNoSelection();
  treeData = await notesApi.getTree(s);
  drawTree();
  if (isPhone()) setDrawer(true);
  await openHomepage();
  refreshPluginPanels(s);
  syncUiPlugins(s, UI_PLUGIN_REGISTRY);
  refreshTrashState();
  refreshTags();
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
  setView("results");
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

// `draft: false` when switching editors mid-session: the text handed
// over is the live text, so there is no older draft to offer.
async function openVimEditor(content, { draft = true } = {}) {
  $("editormode").textContent = "vim";
  $("editbody").hidden = true;
  const undoBtn = $("undobtn");
  const redoBtn = $("redobtn");
  if (undoBtn) undoBtn.hidden = true;
  if (redoBtn) redoBtn.hidden = true;
  const editor = new VimEditor({
    VimWasm,
    canvas: $("vim-canvas"),
    input: $("vim-input"),
    container: $("vim-container"),
    loading: $("vim-loading"),
    onFailure: (err) => { recordVimFailure(); console.error("[vim]", err); },
    getVimrc,
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
      openTextareaEditor(editor.content || content, { draft: false });
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
      openTextareaEditor(content, { draft });
      if (!draft) toast("Vim could not start; staying in the plain editor.", true);
    }
    return;
  }
  if (!editing || editing.editor !== editor) return;
  updateEditorStatus();
  syncModeSwitch();
  if (draft) offerDraft(content, (text) => editor.replace(text));
}

function openTextareaEditor(content, { draft = true } = {}) {
  $("editormode").textContent = "plain text";
  $("editbody").hidden = false;
  $("editbody").value = content;
  $("editbody").oninput = () => { markDirty(); updateUndoRedoButtons(); };
  updateLineGutter($("editbody"), $("editgutter"), $("editmirror"));
  applyEditorPrefs();
  if (draft) offerDraft(content, (text) => {
    $("editbody").value = text;
    updateLineGutter($("editbody"), $("editgutter"), $("editmirror"));
  });
  updateEditorStatus();
  syncModeSwitch();
  setupUndoRedoButtons();
  updateUndoRedoButtons();
}

// --- Undo/Redo for plain-text editor ---

function setupUndoRedoButtons() {
  const undoBtn = $("undobtn");
  const redoBtn = $("redobtn");
  if (!undoBtn || !redoBtn) return;
  undoBtn.hidden = false;
  redoBtn.hidden = false;
  undoBtn.onclick = () => {
    const ta = $("editbody");
    if (ta) { ta.focus(); document.execCommand("undo"); updateUndoRedoButtons(); }
  };
  redoBtn.onclick = () => {
    const ta = $("editbody");
    if (ta) { ta.focus(); document.execCommand("redo"); updateUndoRedoButtons(); }
  };
}

function updateUndoRedoButtons() {
  const undoBtn = $("undobtn");
  const redoBtn = $("redobtn");
  if (!undoBtn || !redoBtn) return;
  // Note: textarea doesn't expose undoManager reliably across browsers.
  // We enable buttons when editing in plain-text mode and there's content.
  const editingTextarea = editing && editing.mode === "textarea";
  const hasContent = $("editbody")?.value?.length > 0;
  undoBtn.disabled = !editingTextarea || !hasContent;
  redoBtn.disabled = !editingTextarea || !hasContent;
}

// --- switching editors mid-edit (Settings → Editor sets the default) ---

/** Vim can start here: a desktop browser that supports it, breaker closed. */
function vimAvailable() {
  return !isPhone() && !checkBrowserCompatibility() && getVimModeState().enabled;
}

function syncModeSwitch() {
  const b = $("modeswitch");
  if (!editing) {
    b.hidden = true;
    return;
  }
  const toVim = editing.mode !== "vim";
  const canVim = vimAvailable();
  b.hidden = false;
  b.disabled = toVim && !canVim;
  b.textContent = toVim ? "Use Vim" : "Use plain text";
  b.title = toVim && !canVim
    ? "Enable Vim mode in Settings → Editor to use this."
    : `Continue this note in ${toVim ? "Vim" : "the plain-text editor"}.`;
}

let switchingEditor = false;

/** Move the live text into the other editor without saving or losing it. */
async function switchEditorMode() {
  const ed = editing;
  if (!ed || switchingEditor) return;
  switchingEditor = true;
  $("modeswitch").disabled = true;
  try {
    let content;
    try {
      content = await readEditorContent();
    } catch {
      toast("Could not read the Vim buffer; save with :w, then switch.", true);
      return;
    }
    if (editing !== ed) return;
    clearTimeout(padTimer);
    if (ed.mode === "vim") {
      const vim = ed.editor;
      ed.mode = "textarea";
      ed.editor = null; // detach first: Vim's quit must not close the session
      currentEditorMode = "textarea";
      vim?.close();
      openTextareaEditor(content, { draft: false });
      $("editbody").focus();
    } else {
      ed.mode = "vim";
      currentEditorMode = "vim";
      await openVimEditor(content, { draft: false });
      if (editing !== ed) return;
      ed.editor?.focus();
    }
    if (content !== ed.lastSaved) markDirty();
  } finally {
    switchingEditor = false;
    syncModeSwitch();
  }
}

$("modeswitch")?.addEventListener("click", switchEditorMode);

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

// The server's slugRe: lowercase letters, digits and dashes, 1–64
// chars, no leading/trailing dash.
function slugOk(s) {
  return /^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$/.test(s.trim());
}

async function actionNewNotebook() {
  const r = await formDialog({
    title: "New notebook",
    message: "The slug names the folder on disk and can’t be changed to something invalid later.",
    okLabel: "Create",
    fields: [{ name: "slug", label: "Slug", placeholder: "my-notebook" }],
    validate: (v) => (slugOk(v.slug) ? null : "Use lowercase letters, digits and dashes (a-z, 0-9, -)."),
  });
  if (!r) return;
  const id = r.slug.trim().toLowerCase();
  await guarded(async () => {
    await notesApi.createNotebook(id);
    await refreshNotebookList(id);
    toast(`Created notebook “${id}”.`);
  });
}

async function actionRenameNotebook() {
  if (!slug) return;
  const r = await formDialog({
    title: "Rename notebook",
    message: "The slug names the folder on disk and appears in every link.",
    okLabel: "Rename",
    fields: [{ name: "slug", label: "Slug", value: slug }],
    validate: (v) => (slugOk(v.slug) ? null : "Use lowercase letters, digits and dashes (a-z, 0-9, -)."),
  });
  if (!r || r.slug.trim() === slug) return;
  const next = r.slug.trim().toLowerCase();
  await guarded(async () => {
    await flushEditor();
    await notesApi.renameNotebook(slug, next);
    await refreshNotebookList(next);
    toast(`Renamed to “${next}”.`);
  });
}

async function actionDeleteNotebook() {
  if (!slug) return;
  const target = slug;
  const ok = await confirmDialog({
    title: "Delete notebook",
    message: `Archive “${target}”? It leaves the selector; the files stay on the server under notebooks/.archive/ until you restore them by hand.`,
    okLabel: "Delete",
    danger: true,
  });
  if (!ok) return;
  await guarded(async () => {
    await flushEditor();
    await notesApi.deleteNotebook(target);
    toast(`Archived notebook “${target}”.`);
    await refreshNotebookList(null);
  });
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
    refreshSplitRead(ed.path);
    scheduleTags();
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
      else { $("editbody").value = src.text; updateLineGutter($("editbody"), $("editgutter"), $("editmirror")); updateEditorStatus(); }
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
      scheduleTags();
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
  syncModeSwitch();
  // Back to whatever the pane was showing before: a note if one is still
  // open, the empty state if it was deleted or the notebook changed.
  setView(current && lastNote ? "read" : "empty");
  $("editbody").hidden = false;
  const undoBtn = $("undobtn");
  const redoBtn = $("redobtn");
  if (undoBtn) undoBtn.hidden = true;
  if (redoBtn) redoBtn.hidden = true;
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

// Tab indents in both plain-text editors (main and second pane).
function onTabKey(ta, active, dirty) {
  return (e) => {
    if (e.key !== "Tab" || !active()) return;
    const unit = tabUnit();
    if (!unit) return;
    e.preventDefault();
    const edit = indentEdit(ta.value, ta.selectionStart, ta.selectionEnd, unit, e.shiftKey);
    ta.setRangeText(edit.text, edit.start, edit.end, "preserve");
    ta.setSelectionRange(edit.selStart, edit.selEnd);
    dirty();
  };
}
$("editbody")?.addEventListener("keydown",
  onTabKey($("editbody"), () => editing && editing.mode === "textarea", () => markDirty()));
$("split-body")?.addEventListener("keydown",
  onTabKey($("split-body"), () => Boolean(splitSession), () => markSplitDirty()));

// Keep line-number gutters in sync with their textareas.
$("editbody")?.addEventListener("input",  () => updateLineGutter($("editbody"),   $("editgutter"),   $("editmirror")));
$("editbody")?.addEventListener("scroll", () => { if ($("editgutter")) $("editgutter").scrollTop = $("editbody").scrollTop; });
$("split-body")?.addEventListener("input",  () => updateLineGutter($("split-body"), $("splitgutter"), $("splitmirror")));
$("split-body")?.addEventListener("scroll", () => { if ($("splitgutter")) $("splitgutter").scrollTop = $("split-body").scrollTop; });
window.addEventListener("resize", () => {
  if ($("editbody") && !$("editbody").hidden) {
    updateLineGutter($("editbody"), $("editgutter"), $("editmirror"));
  }
  if ($("split-body") && !$("split-body").hidden) {
    updateLineGutter($("split-body"), $("splitgutter"), $("splitmirror"));
  }
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

/* Line-number gutter for a plain-textarea editor. Each number is a block
   whose height matches that logical line's visual height, so numbers stay
   aligned when soft-wrap is on. A hidden mirror with the textarea's font
   and width supplies the per-line heights. */
function updateLineGutter(ta, gutter, mirror) {
  if (!ta || !gutter) return;
  const lines = ta.value.split("\n");
  const n = lines.length;
  const wrap = getComputedStyle(ta).whiteSpace === "pre-wrap";

  // Grow/shrink the gutter to match the line count.
  while (gutter.children.length > n) gutter.lastChild.remove();
  while (gutter.children.length < n) {
    const s = document.createElement("span");
    s.className = "ln";
    s.textContent = gutter.children.length + 1;
    gutter.append(s);
  }

  if (wrap && mirror) {
    // Position the mirror exactly over the textarea's content box.
    const wrapEl = ta.closest(".editor-wrap");
    const wr = wrapEl.getBoundingClientRect();
    const tr = ta.getBoundingClientRect();
    mirror.style.left = `${tr.left - wr.left}px`;
    mirror.style.top = `${tr.top - wr.top}px`;
    mirror.style.width = `${ta.clientWidth}px`;
    const frag = document.createDocumentFragment();
    for (const line of lines) {
      const d = document.createElement("div");
      d.textContent = line || "\u200b";
      frag.append(d);
    }
    mirror.replaceChildren(frag);
    const kids = mirror.children;
    for (let i = 0; i < n; i++) {
      gutter.children[i].style.height = `${kids[i].offsetHeight}px`;
    }
  } else {
    for (const span of gutter.children) span.style.height = "";
  }

  gutter.scrollTop = ta.scrollTop;
}

function insertAtCursor(text) {
  const ta = $("editbody");
  const at = bodyInsert(ta.value, ta.selectionStart, ta.selectionEnd, text);
  ta.value = ta.value.slice(0, at.start) + at.text + ta.value.slice(at.end);
  const pos = at.start + at.text.length;
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
      setBanner(`<div class="warn">Vim would not take the insert.` +
        ` Paste it yourself: <code>${esc(text)}</code></div>`);
    }
  }
}

async function handleFileUpload(file) {
  if (!editing) return;
  setSaveState(`Uploading ${file.name}…`, "dirty");
  try {
    const integ = await fetchIntegrationStatus();
    if (integ.slink && file.type.startsWith('image/')) {
      const hostedUrl = await slinkUpload(file);
      const label = file.name.replace(/([\\[\]()])/g, '\\$1');
      await insertIntoEditor(`![${label}](${hostedUrl})`);
      markDirty();
      setSaveState(`Uploaded ${file.name} via Slink`, "dirty");
      return;
    }
    const res = await uploadResource(file);
    if (!res || !/^[a-f0-9]{64}$/.test(res.sha || "")) {
      throw new Error("upload returned no content hash");
    }
    // Embed at the cursor: a markdown image for pictures (so it renders
    // inline), a markdown link for everything else — the server turns
    // video/audio/PDF links into players. This is the only way media
    // becomes visible in the reader.
    const md = markdownFor(slug, res);
    await insertIntoEditor(md);
    // Dirty after inserting: markDirty() writes "Editing…", so the upload
    // result has to come last or the user would never see it.
    markDirty();
    setSaveState(res.created
      ? `Uploaded ${file.name} (${res.sha.slice(0, 8)}…)`
      : `${file.name} already uploaded (dedup)`, "dirty");
  } catch (e) {
    setSaveState(`Upload failed: ${e.message}`, "error");
  }
}

async function handleSplitFileUpload(file) {
  if (!splitSession) return;
  setSplitSaveState(`Uploading ${file.name}…`, "dirty");
  try {
    const res = await uploadResource(file);
    if (!res || !/^[a-f0-9]{64}$/.test(res.sha || "")) {
      throw new Error("upload returned no content hash");
    }
    insertIntoSplitEditor(markdownFor(slug, res));
    setSplitSaveState(res.created
      ? `Uploaded ${file.name} (${res.sha.slice(0, 8)}…)`
      : `${file.name} already uploaded (dedup)`, "dirty");
  } catch (e) {
    setSplitSaveState(`Upload failed: ${e.message}`, "error");
  }
}

/** Pull image files out of a clipboard event, or [] if there are none. */
function clipboardImages(e) {
  const items = e.clipboardData?.items;
  if (!items) return [];
  const out = [];
  for (const item of items) {
    if (item.type.startsWith("image/")) {
      const file = item.getAsFile();
      if (file) out.push(file);
    }
  }
  return out;
}

/** Handle paste events: images + rich text (HTML → Markdown) + URL archiving. */
async function handlePaste(e, editorEl) {
  if (!editing) return;
  const files = clipboardImages(e);
  if (files.length) {
    e.preventDefault();
    for (const file of files) handleFileUpload(file);
    return;
  }
  // Handle rich text (HTML from Word, Google Docs, web)
  const { htmlToMarkdown, looksLikeRichText, getHtml, getPlainText } = await import('/lib/paste.js');
  const html = getHtml(e);
  if (html && looksLikeRichText(html)) {
    e.preventDefault();
    const md = htmlToMarkdown(html);
    if (md) {
      await insertIntoEditor(md);
    }
    return;
  }
  // Karakeep: archive plain-text HTTP URLs in the background.
  const plain = getPlainText(e.clipboardData);
  if (isHttpUrl(plain)) {
    const integ = await fetchIntegrationStatus();
    if (integ.karakeep) {
      karakeepArchive(plain).then(async (archiveUrl) => {
        await insertIntoEditor(` ([archived](${archiveUrl}))`);
        markDirty();
      }).catch(() => { /* archive failure is non-fatal */ });
    }
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
  editorEl.addEventListener("paste", (e) => handlePaste(e, editorEl));
}

setupEditorDropZone();

// Paste an image into the second pane's editor: upload and embed it there.
function setupSplitPasteZone() {
  const pane = $("split-pane");
  if (!pane) return;
  pane.addEventListener("paste", async (e) => {
    if (!splitSession) return;
    // Only the split textarea, not the main editor that may also be open.
    if (e.target !== $("split-body")) return;
    const files = clipboardImages(e);
    if (files.length) {
      e.preventDefault();
      for (const file of files) handleSplitFileUpload(file);
      return;
    }
    // Handle rich text (HTML from Word, Google Docs, web)
    const { htmlToMarkdown, looksLikeRichText, getHtml, getPlainText } = await import('/lib/paste.js');
    const html = getHtml(e);
    if (html && looksLikeRichText(html)) {
      e.preventDefault();
      const md = htmlToMarkdown(html);
      if (md) {
        await insertIntoSplitEditor(md);
      }
      return;
    }
    // Karakeep: archive plain-text HTTP URLs in the background.
    const plain = getPlainText(e.clipboardData);
    if (isHttpUrl(plain)) {
      const integ = await fetchIntegrationStatus();
      if (integ.karakeep) {
        karakeepArchive(plain).then(async (archiveUrl) => {
          await insertIntoSplitEditor(` ([archived](${archiveUrl}))`);
        }).catch(() => { /* archive failure is non-fatal */ });
      }
    }
  });
}

setupSplitPasteZone();

// --- attach menu (editor toolbar) ---

// Dialogs steal DOM focus; in vim mode the keyboard only reaches the
// editor through #vim-input, so put it back after inserting.
function refocusVim() {
  if (editing?.mode === "vim") $("vim-input")?.focus();
}

async function attachUrlDialog() {
  const values = await formDialog({
    title: "Insert link",
    fields: [
      { label: "Address", name: "url", type: "text", placeholder: "https://… or a relative path", hint: "http, https, mailto or a path inside the app." },
      { label: "Text", name: "text", type: "text", placeholder: "Link text (optional)" },
    ],
    preview: (v) => (v.url?.trim() ? buildLinkMarkdown(v.url, v.text) : ""),
    validate: (v) => {
      const url = (v.url || "").trim();
      if (!url) return "Enter an address.";
      if (!isValidLinkUrl(url)) return "Links must use http, https or mailto — or be a relative path.";
      return null;
    },
    okLabel: "Insert",
  });
  if (!values || !editing) return;
  await insertIntoEditor(buildLinkMarkdown(values.url, values.text));
  refocusVim();
}

async function attachNoteDialog() {
  const paths = collectNotePaths(treeData);
  if (paths.length === 0) {
    toast("This notebook has no notes yet.", true);
    return;
  }
  const list = document.createElement("div");
  list.className = "attach-note-list";
  const dialog = formDialog({
    title: "Link to note",
    message: "Links resolve inside this notebook only.",
    extra: list,
    fields: [
      { label: "Note", name: "target", type: "text", placeholder: "Type a path or pick below", hint: "Full path, path without .md, or a unique name." },
      { label: "Text (optional)", name: "label", type: "text", placeholder: "Shown instead of the note name" },
    ],
    preview: (v) => {
      const resolved = resolveNotePath(paths, v.target);
      return resolved ? buildNoteLinkMarkdown(resolved, v.label) || "" : "";
    },
    validate: (v) => {
      const resolved = resolveNotePath(paths, v.target);
      if (!resolved) return "No single note matches that.";
      v.target = resolved; // canonicalise so the insert uses the real path
      const md = buildNoteLinkMarkdown(resolved, v.label);
      if (!md) return "That label cannot be used in a wikilink (avoid [ ] | and line breaks).";
      return null;
    },
    okLabel: "Insert",
    wide: true,
  });
  // formDialog builds its DOM synchronously; wire the picker to its
  // target field (which doubles as the filter) right away. Rows are
  // rebuilt per keystroke so matches past the cap still appear.
  const target = list.parentElement?.querySelector('[name="target"]');
  const pick = (path) => {
    if (!target) return;
    target.value = path;
    // Programmatic .value sets fire no event; fake one so the preview
    // and the filter see the chosen path.
    target.dispatchEvent(new Event('input', { bubbles: true }));
    target.focus();
  };
  const applyFilter = () => {
    const q = target ? target.value : "";
    list.replaceChildren(...filterNotePaths(paths, q).slice(0, 60).map((p) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "attach-note-item";
      b.textContent = p;
      b.addEventListener("click", () => pick(p));
      return b;
    }));
  };
  target?.addEventListener("input", applyFilter);
  applyFilter();
  const values = await dialog;
  if (!values || !editing) return;
  const md = buildNoteLinkMarkdown(values.target, values.label);
  if (md) await insertIntoEditor(md);
  refocusVim();
}

$("attachbtn")?.addEventListener("click", (e) => {
  if (!editing) return;
  const r = e.currentTarget.getBoundingClientRect();
  showMenu(r.left, Math.min(r.bottom + 4, window.innerHeight - 160), [
    { label: "Upload file…", onSelect: () => $("attach-file")?.click() },
    { label: "Attach existing resource…", onSelect: () => openAttachmentsModal() },
    { label: "Insert link (URL)…", onSelect: () => attachUrlDialog() },
    { label: "Link to note…", onSelect: () => attachNoteDialog() },
  ]);
});

$("attach-file")?.addEventListener("change", (e) => {
  const files = [...(e.target.files || [])];
  e.target.value = "";
  for (const file of files) handleFileUpload(file);
});

for (const ev of ["keyup", "click", "select", "focus"]) {
  $("editbody")?.addEventListener(ev, updateEditorStatus);
}
document.addEventListener("selectionchange", () => {
  if (document.activeElement === $("editbody")) updateEditorStatus();
});

$("editbtn")?.addEventListener("click", openEditor);
$("savebtn")?.addEventListener("click", () => saveNote(true));
/** Save the open editor and return to the rendered note. */
async function saveAndCloseEditor() {
  const path = editing && editing.path;
  await flushEditor();
  await closeEditor();
  if (path && slug) await openNote(path, null);
}

$("closebtn")?.addEventListener("click", () => saveAndCloseEditor());

window.addEventListener("pagehide", () => {
  if (editing) {
    writePad();
    releaseLock(editing.path, true);
  }
});

// --- keyboard shortcuts (single source of truth: lib/help.js) ---

window.addEventListener("keydown", (e) => {
  const typing = isTypingTarget(e.target) || (editing != null);
  const action = shortcutFor(e, { typing, bindings: getKeybindings() });
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
    return;
  }
  if (action === "daily-note") {
    if (slug) { e.preventDefault(); actionTodaysNote(); }
    return;
  }
  if (action === "new-folder") {
    if (slug) { e.preventDefault(); actionNewFolder(); }
    return;
  }
  if (action === "undo") {
    if (editing && editing.mode === "textarea") {
      e.preventDefault();
      const ta = $("editbody");
      if (ta) { ta.focus(); document.execCommand("undo"); updateUndoRedoButtons(); }
    }
    return;
  }
  if (action === "redo") {
    if (editing && editing.mode === "textarea") {
      e.preventDefault();
      const ta = $("editbody");
      if (ta) { ta.focus(); document.execCommand("redo"); updateUndoRedoButtons(); }
    }
    return;
  }
  if (action === "trash") {
    if (slug) { e.preventDefault(); openTrash(); }
    return;
  }
  if (action === "settings") {
    e.preventDefault(); openSettingsModal();
    return;
  }
  if (action === "toggle-split") {
    if (slug) { e.preventDefault(); toggleSplit(); }
    return;
  }
  if (action === "focus-sidebar") {
    e.preventDefault();
    q("#tree")?.querySelector("button, a, [tabindex]")?.focus();
    return;
  }
  if (action === "toggle-sidebar") {
    e.preventDefault();
    if (isPhone()) setDrawer(!document.body.classList.contains("tree-open"));
    else { togglePlacement(); syncPlacementUI(); }
  }
});

// --- reader navigation: j/k step through notes (vim-style) ---

function navigateNotes(delta) {
  if (!slug || !treeData) return false;
  const paths = collectNotePaths(treeData);
  const from = current || getHomepage(slug);
  const target = adjacentNote(paths, from, delta)
    || (delta < 0 ? paths[paths.length - 1] : paths[0]);
  if (!target || target === from) return false;
  openNote(target, null);
  return true;
}

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

// Back to "nothing open": the note, a trashed note or the trash
// overview leaves the pane. Never called while editing. When the
// notebook has a homepage it takes over instead of the empty state.
function closeNote() {
  current = null;
  lastNote = null;
  openSeq += 1;
  document.title = "noteroot";
  document.querySelectorAll("#tree .node.active")
    .forEach((n) => n.classList.remove("active"));
  showNoSelection();
  setStatus("Read", { text: null });
  openHomepage().catch(() => {});
}

function clearViewIfGone(path, isDir) {
  if (current && (isDir ? isWithin(current, path) : current === path)) closeNote();
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

/** Open a template picker, then create a new note from the selected template. */
async function actionNewNoteFromTemplate(dir = contextDir()) {
  if (!slug) return;
  const { getTemplates, getDefaultTemplate } = await import('/lib/prefs.js');
  const { applyTemplate, defaultNoteContent } = await import('/lib/templates.js');
  const { resolveChild } = await import('/lib/tree.js');

  const templates = getTemplates(slug);
  if (Object.keys(templates).length === 0) {
    toast("No templates yet. Create one in Settings → Templates.", true);
    return;
  }

  const defaultT = getDefaultTemplate(slug);
  const items = Object.entries(templates).map(([name, t]) => ({
    name,
    label: name + (t.isDefault ? ' (default)' : ''),
    content: t.content,
  }));

  const r = await formDialog({
    title: "New note from template",
    message: dir ? `In folder “${dir}”.` : "At the notebook root.",
    fields: [
      { name: "name", label: "Name", placeholder: "meeting-notes", hint: "Subfolders are created as needed; .md is added for you." },
      { name: "template", label: "Template", type: "select", options: items.map((t) => t.name), value: defaultT?.name || items[0]?.name },
    ],
    okLabel: "Create",
    validate: (v) => {
      if (!v.name) return "Please enter a name.";
      const c = resolveChild(dir, v.name, { asNote: true });
      return c.ok ? null : c.error;
    },
  });
  if (!r) return;

  const template = templates[r.template];
  const { path } = resolveChild(dir, r.name, { asNote: true });
  const title = path.split('/').pop()?.replace(/\.md$/i, '') || 'Untitled';
  const folder = parentDir(path);
  const content = applyTemplate(template, { title, path, folder });

  await guarded(async () => {
    await notesApi.createNote(slug, path, content);
    ensureExpanded(folder);
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

// --- Daily Notes ---
const DAILY_FORMATS = {
  'YYYY-MM-DD': (d) => d.toISOString().slice(0, 10),
  'YYYY/MM/DD': (d) => d.toISOString().slice(0, 10).replace(/-/g, '/'),
  'DD-MM-YYYY': (d) => `${String(d.getDate()).padStart(2, '0')}-${String(d.getMonth() + 1).padStart(2, '0')}-${d.getFullYear()}`,
};

function formatDailyDate(date) {
  const fmt = getSetting('dailyNotesFormat');
  return DAILY_FORMATS[fmt] ? DAILY_FORMATS[fmt](date) : DAILY_FORMATS['YYYY-MM-DD'](date);
}

function getDailyPath(date) {
  const folder = getSetting('dailyNotesFolder').trim() || 'daily';
  return `${folder}/${formatDailyDate(date)}.md`;
}

/** Open today's daily note, creating it if needed. */
async function actionTodaysNote() {
  if (!slug) return;
  const date = new Date();
  const path = getDailyPath(date);
  await guarded(async () => {
    try {
      await notesApi.getNote(slug, path);
    } catch (e) {
      if (e.status === 404) {
        const templateName = getSetting('dailyNotesTemplate');
        let content;
        if (templateName) {
          const { getTemplate, applyTemplate } = await import('/lib/prefs.js');
          const { applyTemplate: applyTmpl } = await import('/lib/templates.js');
          const template = getTemplate(slug, templateName);
          if (template) {
            const title = formatDailyDate(date);
            content = applyTmpl(template, { title, path, folder: getSetting('dailyNotesFolder').trim() || 'daily' });
          }
        }
        if (!content) {
          const { defaultNoteContent } = await import('/lib/templates.js');
          content = defaultNoteContent(path);
        }
        await notesApi.createNote(slug, path, content);
      } else {
        throw e;
      }
    }
    await refreshTree();
    await openNote(path, null);
  });
}

/** Open a specific daily note by date (shows a date picker). */
async function actionDailyNotePickDate() {
  if (!slug) return;
  const today = new Date();
  const dateStr = today.toISOString().slice(0, 10);
  const r = await formDialog({
    title: "Open daily note",
    message: "Pick a date. The note will be created if it doesn't exist.",
    fields: [{ name: "date", label: "Date", type: "date", value: dateStr }],
    okLabel: "Open",
  });
  if (!r) return;
  const date = new Date(r.date + 'T00:00:00');
  const path = getDailyPath(date);
  await guarded(async () => {
    try {
      await notesApi.getNote(slug, path);
    } catch (e) {
      if (e.status === 404) {
        const templateName = getSetting('dailyNotesTemplate');
        let content;
        if (templateName) {
          const { getTemplate, applyTemplate } = await import('/lib/prefs.js');
          const { applyTemplate: applyTmpl } = await import('/lib/templates.js');
          const template = getTemplate(slug, templateName);
          if (template) {
            const title = formatDailyDate(date);
            content = applyTmpl(template, { title, path, folder: getSetting('dailyNotesFolder').trim() || 'daily' });
          }
        }
        if (!content) {
          const { defaultNoteContent } = await import('/lib/templates.js');
          content = defaultNoteContent(path);
        }
        await notesApi.createNote(slug, path, content);
      } else {
        throw e;
      }
    }
    await refreshTree();
    await openNote(path, null);
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

const FOLDER_ICON_CATEGORIES = {
  "Files":   ["📁","📂","🗂","📋","📌","📍","🗃","🗄","📦","📫","📬","📭","🏷","🔖","📎","🖇","🔗","🗑"],
  "Docs":    ["📝","📖","📓","📔","📕","📗","📘","📙","📄","📃","📑","🗒","🗓","📅","📆","📊","📈","📉","🧾","📜","✏️","🖊","🖋","🖌"],
  "Work":    ["💼","🧰","🛠","🔧","🔨","⚙️","🧱","🔩","🪛","🪚","📐","📏","🗝","🔑","🔒","🔓","🏦","💰","💳","🧮","💡","🎯","🚀","🏆","🥇","🎖","🎓","📢","🔔","📣"],
  "Tech":    ["💻","🖥","⌨️","🖱","🖨","💾","💿","📀","🧑‍💻","🐍","🦀","☕","🐳","☁️","🌐","🔌","🧩","🪲","🐛","✅","❌","⚠️","🚧","🔍","🔎","🛰","📡","⚡","🤖","🦾"],
  "Creative":["🎨","🖼","🎭","🎬","🎵","🎼","🎤","🎧","📷","📸","📹","🎮","🕹","🎲","🧸","🎁","🎉","🎈","🎂","🎊","🪩","🎪","🪆","🎠","🎡","🎢"],
  "Life":    ["🏠","🏡","🏢","🏫","🏥","🏨","🛒","🛁","🪴","🧹","🪑","🛋","🧺","🍳","☕","🍕","🍔","🍎","🍵","🥂","🍫","🍱","🧃","🍶","🫖"],
  "Fitness": ["⚽","🏀","🎾","🏈","🚴","🏃","🧘","💪","🏋","🥊","🏊","⛷","🧗","🤸","🎿","🏂","🏄","🤽","🧜","⛷"],
  "Travel":  ["✈️","🚗","🚌","🚂","🚇","🚁","⛵","🚢","🏕","🏖","⛰","🌋","🗺","🧭","🌍","🌎","🌏","🏙","🗽","🗼","🏯","🎑","🏞"],
  "Nature":  ["🌱","🌲","🌳","🌴","🌵","🌸","🌺","🌻","🌹","🌷","🍀","🍁","🍄","🌿","🌾","🐾","🐱","🐶","🦊","🐻","🐼","🦁","🐯","🦉","🦄","🐝","🦋","🐢","🐟","🐙","🐬","🦈","🌙","☀️","🌈","❄️","🌊","🌤","⛅","🌦"],
  "Symbols": ["⭐","❤️","🔥","🧠","🗣","💬","❓","❗","✨","🌟","💫","🔮","🧿","🪄","💎","🪙","🎯","♾","🆕","🔝","🔜","🔛","🔚","⚜","🏅","🎗","🚩","🏴","🏳","♻️","☮","✝","☯","🕉","☪","🔱"],
};
const FOLDER_ICONS = Object.values(FOLDER_ICON_CATEGORIES).flat();

async function actionDecorate(node) {
  const cur = decorations[node.path] || {};
  let icon = cur.emoji || "";
  let activeCategory = "All";

  const buildGrid = (pool) => {
    const g = document.createElement("div");
    g.className = "icon-grid";
    g.setAttribute("role", "listbox");
    g.setAttribute("aria-label", "Icon");
    for (const ic of pool) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "icon-choice";
      b.dataset.icon = ic;
      b.textContent = ic;
      b.setAttribute("role", "option");
      b.setAttribute("aria-selected", String(ic === icon));
      b.addEventListener("click", () => {
        icon = ic;
        for (const x of grid.querySelectorAll("button")) x.setAttribute("aria-selected", String(x.dataset.icon === icon));
      });
      g.append(b);
    }
    return g;
  };

  // Category tabs
  const tabs = document.createElement("div");
  tabs.className = "icon-tabs";
  const categoryNames = ["All", ...Object.keys(FOLDER_ICON_CATEGORIES)];
  let grid = buildGrid(icon && !FOLDER_ICONS.includes(icon) ? [icon, ...FOLDER_ICONS] : FOLDER_ICONS);
  const gridWrap = document.createElement("div");
  gridWrap.className = "icon-grid-wrap";
  gridWrap.append(grid);

  const activateTab = (name) => {
    activeCategory = name;
    const pool = name === "All"
      ? (icon && !FOLDER_ICONS.includes(icon) ? [icon, ...FOLDER_ICONS] : FOLDER_ICONS)
      : (FOLDER_ICON_CATEGORIES[name] || FOLDER_ICONS);
    grid = buildGrid(pool);
    gridWrap.replaceChildren(grid);
    for (const t of tabs.querySelectorAll("button")) t.setAttribute("aria-pressed", String(t.dataset.cat === name));
  };

  for (const name of categoryNames) {
    const t = document.createElement("button");
    t.type = "button";
    t.className = "icon-tab";
    t.dataset.cat = name;
    t.textContent = name;
    t.setAttribute("aria-pressed", String(name === activeCategory));
    t.addEventListener("click", () => activateTab(name));
    tabs.append(t);
  }

  // Search filter
  const searchWrap = document.createElement("div");
  searchWrap.className = "icon-search-wrap";
  const search = document.createElement("input");
  search.type = "search";
  search.placeholder = "Filter icons…";
  search.className = "icon-search";
  search.setAttribute("aria-label", "Filter icons");
  search.addEventListener("input", () => {
    const q = search.value.trim();
    if (!q) { activateTab(activeCategory); return; }
    // Show any icon that starts or contains the codepoint hex for q or use the raw emoji match
    const pool = FOLDER_ICONS.filter((ic) => ic === q || [...ic].some((ch) => ch.includes(q)));
    grid = buildGrid(pool.length ? pool : FOLDER_ICONS);
    gridWrap.replaceChildren(grid);
    for (const t of tabs.querySelectorAll("button")) t.setAttribute("aria-pressed", "false");
  });
  searchWrap.append(search);

  const iconLabel = document.createElement("div");
  iconLabel.className = "dialog-label";
  iconLabel.textContent = "Icon";

  const row = document.createElement("label");
  row.className = "color-row";
  const useColor = document.createElement("input");
  useColor.type = "checkbox";
  useColor.name = "useColor";
  useColor.checked = Boolean(cur.color);
  useColor.setAttribute("aria-label", "Use a custom name color");
  const color = document.createElement("input");
  color.type = "color";
  color.name = "color";
  color.value = cur.color || DEFAULT_FOLDER_COLOR;
  color.setAttribute("aria-label", "Name color");
  color.addEventListener("input", () => { useColor.checked = true; });
  row.append(useColor, color);

  const extra = document.createElement("div");
  extra.append(iconLabel, searchWrap, tabs, gridWrap, row);

  const pending = formDialog({
    title: `Icon & color — ${baseName(node.path)}`,
    message: "Shown in this browser only.",
    extra,
    okLabel: "Save",
    actions: [{ label: "Reset", name: "reset" }],
  });
  gridWrap.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' });
  const r = await pending;
  if (!r) return;
  const deco = r.__action === "reset" ? {} : { emoji: icon, color: useColor.checked ? color.value : "" };
  setDecoration(slug, node.path, deco);
  decorations = getDecorations(slug);
  drawTree();
}

function showNodeMenu(node, x, y) {
  const lockItems = lockMenuItems(node);
  const items = node.isDir
    ? [
      { label: "New note here", onSelect: () => actionNewNote(node.path) },
      { label: "New note from template…", onSelect: () => actionNewNoteFromTemplate(node.path) },
      { label: "New folder here", onSelect: () => actionNewFolder(node.path) },
      { separator: true },
      { label: "Rename…", onSelect: () => actionRename(node) },
      { label: "Move…", onSelect: () => actionMove(node) },
      { label: "Icon & color…", onSelect: () => actionDecorate(node) },
      { separator: true },
      ...lockItems,
      { label: "Delete folder…", danger: true, onSelect: () => actionDelete(node) },
    ]
    : (() => {
      const pinned = slug && getBookmarks(slug).includes(node.path);
      const bulk = multiSelected.size > 1 && multiSelected.has(node.path);
      const bulkN = multiSelected.size;
      return [
        { label: "Open", onSelect: () => openNote(node.path, null) },
        { label: "Open in split pane", onSelect: () => openInSplit(node.path) },
        { label: pinned ? "Remove bookmark" : "Bookmark", onSelect: () => { toggleBookmark(slug, node.path); drawBookmarks(); } },
        { label: "Rename…", onSelect: () => actionRename(node) },
        { label: bulk ? `Move ${bulkN} notes…` : "Move…", onSelect: () => (bulk ? moveMultiNotes() : actionMove(node)) },
        { label: "Download .md", onSelect: () => actionDownloadNote(node) },
        { separator: true },
        ...lockItems,
        { label: bulk ? `Delete ${bulkN} notes…` : "Delete note…", danger: true, onSelect: () => (bulk ? deleteMultiNotes() : actionDelete(node)) },
      ];
    })();
  showMenu(x, y, items);
}

/** Context-menu entries for the content lock on a node. */
function lockMenuItems(node) {
  const path = node.path !== undefined ? node.path : '';
  if (node.locked) {
    // Node is locked and the current session hasn't opened it yet.
    return [
      { label: "Unlock…", onSelect: () => actionUnlock(node) },
      { separator: true },
    ];
  }
  // A lock exists but the session has already opened it: offer change and remove.
  if (sessionUnlocked.has(path)) {
    return [
      { label: "Lock with password…", onSelect: () => actionLock(node) },
      { label: "Remove lock…", onSelect: () => actionRemoveLock(node) },
      { separator: true },
    ];
  }
  // No lock at all: only offer to set one.
  return [
    { label: "Lock with password…", onSelect: () => actionLock(node) },
    { separator: true },
  ];
}

// --- content locks (advisory password gate) ---
//
// A lock hides a notebook, folder or note behind a password and keeps
// the content out of the UI until it is entered. It is explicitly NOT
// encryption: the note stays plaintext on disk, in git and in backups.
// The server enforces the gate on every read; the UI here prompts for
// the password and reflects what is locked.

const LOCK_MIN_LEN = 4;

// Paths whose lock the current session has opened (unlock cookie set).
// Used so "Remove lock…" only appears when a lock actually exists.
const sessionUnlocked = new Set();

function lockKindFor(node) {
  return node.isDir ? "folder" : "note";
}

/** Human-readable name for a lock target (handles empty path = notebook root). */
function lockDisplayName(path, kind) {
  if (kind === "notebook" || path === "") return slug || "this notebook";
  return displayName(path);
}

/** The password-set / change dialog. */
async function actionLock(node, kindOverride) {
  if (!slug) return;
  const kind = kindOverride || lockKindFor(node);
  const noun = kind === "notebook" ? "notebook" : kind === "folder" ? "folder" : "note";
  const target = node.path !== undefined ? node.path : node.name || '';
  const values = await formDialog({
    title: `Lock ${noun}`,
    message: `Hide “${lockDisplayName(target, kind)}” behind a password. This is a UI lock, not encryption — the note stays readable on disk and in backups.`,
    fields: [
      { name: "password", label: "Password", type: "password" },
      { name: "confirm", label: "Confirm password", type: "password" },
    ],
    okLabel: "Lock",
    validate: (v) => {
      if ((v.password || "").length < LOCK_MIN_LEN) return `Use at least ${LOCK_MIN_LEN} characters.`;
      if (v.password !== v.confirm) return "Passwords do not match.";
      return null;
    },
  });
  if (!values || values.__action) return;
  try {
    await notesApi.setLock(slug, target, kind, values.password);
    toast(`Locked: ${lockDisplayName(target, kind)}`);
    await refreshTree();
    if (current === target) await openNote(target, null);
  } catch (e) {
    if (e.status === 423) {
      await promptUnlock(target, kind);
    } else {
      toast(e.message, true);
    }
  }
}

/**
 * The unlock dialog: enter the password to open a locked scope. A wrong
 * password re-opens the dialog with the error shown, rather than closing
 * and forcing the user to find the button again.
 */
async function promptUnlock(path, kind) {
  if (!slug) return false;
  const noun = kind === "notebook" ? "notebook" : kind === "folder" ? "folder" : "note";
  let problem = "";
  for (;;) {
    const message = problem
      ? `${problem} Try again.`
      : `This ${noun} is locked. Enter its password to open it in this browser.`;
    const values = await formDialog({
      title: "Unlock",
      message,
      fields: [{ name: "password", label: "Password", type: "password" }],
      okLabel: "Unlock",
    });
    if (!values) return false;
    try {
      await notesApi.unlock(slug, path, values.password, kind);
      sessionUnlocked.add(path);
      toast("Unlocked");
      await refreshTree();
      return true;
    } catch (e) {
      problem = e.status === 401 ? "Wrong password." : e.message;
    }
  }
}

async function actionUnlock(node) {
  const kind = lockKindFor(node);
  const path = node.path !== undefined ? node.path : '';
  const ok = await promptUnlock(path, kind);
  if (ok && current === path) await openNote(path, null);
}

/** Re-lock a scope, dropping the session that opened it. */
async function actionRelock(path, kind) {
  if (!slug) return;
  try {
    await notesApi.relock(slug, path, kind);
    // Any session-unlocked paths inside the relocked scope are locked again.
    for (const p of [...sessionUnlocked]) {
      if (p === path || isWithin(p, path)) sessionUnlocked.delete(p);
    }
    toast("Locked again");
    await refreshTree();
    // Hide the current note when it falls inside the relocked scope.
    // An empty path is the notebook root, which covers every note.
    if (current && (path === "" || isWithin(current, path))) {
      current = null;
      showNoSelection();
    }
  } catch (e) {
    toast(e.message, true);
  }
}

/** Remove a lock for good (requires it to be unlocked first). */
async function actionRemoveLock(node) {
  if (!slug) return;
  const kind = lockKindFor(node);
  const path = node.path !== undefined ? node.path : '';
  const ok = await confirmDialog({
    title: "Remove lock",
    message: `Remove the password on "${lockDisplayName(path, kind)}"? It will no longer be hidden.`,
    okLabel: "Remove lock",
    danger: true,
  });
  if (!ok) return;
  // If the scope is still locked (423), prompt for the password first then
  // retry so the user completes the operation in one flow.
  try {
    await notesApi.removeLock(slug, path, kind);
  } catch (e) {
    if (e.status === 423) {
      const unlocked = await promptUnlock(path, kind);
      if (!unlocked) return;
      try {
        await notesApi.removeLock(slug, path, kind);
      } catch (e2) {
        toast(e2.message, true);
        return;
      }
    } else {
      toast(e.message, true);
      return;
    }
  }
  toast("Lock removed");
  sessionUnlocked.delete(path);
  await refreshTree();
}

// The ⋯ button in the header opens the menu for the open note.
$("notemenu")?.addEventListener("click", (e) => {
  if (!current) return;
  const r = e.currentTarget.getBoundingClientRect();
  showNodeMenu({ path: current, isDir: false }, r.left, r.bottom + 4);
});

$("new-note")?.addEventListener("click", () => actionNewNote());
$("todays-note")?.addEventListener("click", () => actionTodaysNote());
$("new-folder")?.addEventListener("click", () => actionNewFolder());
$("import-note-file")?.addEventListener("click", () => importNoteFromFile());
// The caret opens the same actions as a dropdown; #new-folder and
// #import-note-file no longer live in the toolbar but stay in the DOM
// (hidden) so their listeners — and the shortcuts/tests that click them —
// keep working.
$("new-more")?.addEventListener("click", (e) => {
  e.stopPropagation();
  const caret = e.currentTarget;
  const r = caret.getBoundingClientRect();
  caret.setAttribute("aria-expanded", "true");
  // Wrap the menu items so every close path (select, outside, Esc) both runs
  // the action and clears aria-expanded.
  const run = (fn) => () => { caret.setAttribute("aria-expanded", "false"); fn(); };
  showMenu(r.left, Math.min(r.bottom + 4, window.innerHeight - 120), [
    { label: "New note", onSelect: run(() => actionNewNote()) },
    { label: "New note from template…", onSelect: run(() => actionNewNoteFromTemplate()) },
    { label: "New folder", onSelect: run(() => actionNewFolder()) },
    { label: "Import file…", onSelect: run(() => importNoteFromFile()) },
  ]);
  // A plain outside-click/Esc close bypasses onSelect, so also reset on the
  // next pointerdown anywhere.
  const reset = () => { caret.setAttribute("aria-expanded", "false"); document.removeEventListener("pointerdown", reset, true); };
  document.addEventListener("pointerdown", reset, true);
});
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
let trashExpired = 0; // entries already past their deadline

async function renderTrash() {
  const list = $("trash-list");
  list.replaceChildren();
  let entries = [];
  try {
    const data = await notesApi.listTrash(slug);
    entries = data.entries;
    trashRetention = normalizeRetention(data.retentionDays);
    trashCount = entries.length;
    trashExpired = countExpired(entries);
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
    li.textContent = "Trashbin is empty.";
    list.append(li);
    return;
  }
  // Group by originating folder so a deleted folder reads as one unit.
  const groups = new Map();
  for (const en of entries) {
    const p = en.origPath || "";
    const i = p.lastIndexOf("/");
    const folder = i === -1 ? "" : p.slice(0, i);
    if (!groups.has(folder)) groups.set(folder, []);
    groups.get(folder).push(en);
  }
  const folders = [...groups.keys()].sort((a, b) => a.localeCompare(b));
  for (const folder of folders) {
    const members = groups.get(folder);
    if (folder) {
      list.append(trashFolderRow(folder, members.length));
      for (const en of members) {
        const li = trashRow(en);
        li.classList.add("nested");
        list.append(li);
      }
    } else {
      for (const en of members) list.append(trashRow(en));
    }
  }
}

function trashFolderRow(folder, count) {
  const li = document.createElement("li");
  li.className = "trash-folder";
  const path = document.createElement("span");
  path.className = "trash-path";
  path.textContent = `${folder}/  (${count} note${count === 1 ? "" : "s"})`;

  const restore = document.createElement("button");
  restore.className = "btn small";
  restore.textContent = "Restore folder";
  restore.title = `Restore every note from “${folder}/”`;
  restore.onclick = () => guarded(async () => {
    const res = await notesApi.restoreTrashFolder(slug, { path: folder });
    toast(res.skipped.length
      ? `Restored ${res.restored.length}, ${res.skipped.length} stayed in the trash.`
      : `Restored ${res.restored.length} note${res.restored.length === 1 ? "" : "s"}.`);
    await refreshTree();
    await renderTrash();
  });

  const forget = document.createElement("button");
  forget.className = "btn small danger";
  forget.textContent = "Delete folder";
  forget.title = `Delete every note from “${folder}/” permanently`;
  forget.onclick = () => guarded(async () => {
    const ok = await confirmDialog({
      title: "Delete folder permanently",
      message: `Delete all ${count} note${count === 1 ? "" : "s"} from “${folder}/” for good? This cannot be undone.`,
      okLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    const res = await notesApi.deleteTrashFolder(slug, folder);
    toast(`Deleted ${res.purged} note${res.purged === 1 ? "" : "s"}.`);
    await renderTrash();
  });

  li.append(path, restore, forget);
  return li;
}

function trashRow(en) {
  const li = document.createElement("li");
  const path = document.createElement("span");
  path.className = "trash-path";
  path.textContent = en.origPath || en.name;
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

function countExpired(entries) {
  const now = Date.now();
  return entries.filter((e) => Number(e.expiresAt) > 0 && Number(e.expiresAt) * 1000 <= now).length;
}

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
    trashExpired = 0;
    syncTrashSettings();
    return;
  }
  try {
    const data = await notesApi.listTrash(slug);
    trashRetention = normalizeRetention(data.retentionDays);
    trashCount = data.entries.length;
    trashExpired = countExpired(data.entries);
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

// --- notebook-level lock (Settings → Lock) ---

// Last read status for the notebook-root lock, so the buttons and the
// info line agree with the server.
let notebookLock = { locked: false, unlocked: true, exact: false };

async function refreshNotebookLock() {
  if (!slug) {
    notebookLock = { locked: false, unlocked: true, exact: false };
    syncLockSettings();
    return;
  }
  try {
    const s = await notesApi.lockStatus(slug, "", "notebook");
    notebookLock = { locked: !!s.locked, unlocked: !!s.unlocked, exact: !!s.exact };
  } catch {
    /* a failure here just leaves the buttons as they were */
  }
  syncLockSettings();
}

function syncLockSettings() {
  const lock = $("lock-notebook");
  if (!lock) return;
  const info = $("lock-notebook-info");
  const { locked, unlocked, exact } = notebookLock;
  lock.disabled = !slug;
  lock.hidden = locked;
  $("unlock-notebook").hidden = !(locked && !unlocked);
  $("relock-notebook").hidden = !(locked && unlocked);
  $("remove-lock-notebook").hidden = !(locked && unlocked && exact);
  if (!slug) {
    info.textContent = "Open a notebook to lock it.";
  } else if (!locked) {
    info.textContent = "No password set. The whole notebook is open.";
  } else if (unlocked) {
    info.textContent = exact
      ? "Locked. Unlocked in this browser."
      : "Locked by a parent lock. Unlocked in this browser.";
  } else {
    info.textContent = "Locked. Enter the password to open it here.";
  }
}

// --- Templates settings ---
async function syncTemplatesSettings() {
  const list = $("templates-list");
  const createBtn = $("create-template");
  if (!list || !createBtn) return;

  createBtn.onclick = async () => {
    if (!slug) return;
    const { getTemplates } = await import('/lib/prefs.js');
    const templates = getTemplates(slug);
    const name = prompt("Template name:", "meeting-notes");
    if (!name) return;
    if (templates[name]) {
      toast("A template with that name already exists.", true);
      return;
    }
    const { defaultNoteContent } = await import('/lib/templates.js');
    const content = defaultNoteContent("meeting-notes.md");
    const { saveTemplate } = await import('/lib/prefs.js');
    saveTemplate(slug, name, { content });
    syncTemplatesSettings();
    toast(`Template "${name}" created.`);
  };

  // Render template list
  const { getTemplates, getDefaultTemplate, setDefaultTemplate, deleteTemplate } = await import('/lib/prefs.js');
  const templates = getTemplates(slug);

  if (Object.keys(templates).length === 0) {
    list.innerHTML = '<p class="help-text small">No templates yet. Click “Create template…” to add one.</p>';
    return;
  }

  const defaultT = getDefaultTemplate(slug);
  list.innerHTML = '';
  for (const [name, t] of Object.entries(templates)) {
    const row = document.createElement('div');
    row.className = 'template-row';
    row.innerHTML = `
      <span class="template-name">${esc(name)}${t.isDefault ? ' <span class="badge default">default</span>' : ''}</span>
      <div class="template-actions">
        <button class="btn small ghost" data-action="set-default" ${t.isDefault ? 'disabled' : ''}>Make default</button>
        <button class="btn small ghost" data-action="edit">Edit</button>
        <button class="btn small danger" data-action="delete">Delete</button>
      </div>
    `;
    row.querySelector('[data-action="set-default"]')?.addEventListener('click', async () => {
      const { setDefaultTemplate } = await import('/lib/prefs.js');
      setDefaultTemplate(slug, name);
      syncTemplatesSettings();
    });
    row.querySelector('[data-action="edit"]')?.addEventListener('click', async () => {
      if (!slug) return;
      const { saveTemplate, getTemplates } = await import('/lib/prefs.js');
      const newContent = prompt(`Edit template "${name}":`, t.content);
      if (newContent === null) return;
      saveTemplate(slug, name, { content: newContent, variables: t.variables || {}, isDefault: t.isDefault });
      syncTemplatesSettings();
      toast(`Template "${name}" updated.`);
    });
    row.querySelector('[data-action="delete"]')?.addEventListener('click', async () => {
      if (!slug) return;
      const ok = await confirmDialog({
        title: "Delete template",
        message: `Delete template "${name}"? This cannot be undone.`,
        okLabel: "Delete",
        danger: true,
      });
      if (!ok) return;
      const { deleteTemplate } = await import('/lib/prefs.js');
      deleteTemplate(slug, name);
      syncTemplatesSettings();
      toast(`Template "${name}" deleted.`);
    });
    list.append(row);
  }
}

async function notebookLockGuard() {
  // Actions on a locked-but-unlocked notebook need no prompt; the server
  // still enforces the gate.
  return slug;
}

$("lock-notebook")?.addEventListener("click", async () => {
  if (!slug) return;
  await actionLock({ path: "", isDir: true }, "notebook");
  await refreshNotebookLock();
});
$("unlock-notebook")?.addEventListener("click", async () => {
  if (!slug) return;
  if (await promptUnlock("", "notebook")) {
    await refreshNotebookLock();
    await openHomepage();
  }
});
$("relock-notebook")?.addEventListener("click", async () => {
  await actionRelock("", "notebook");
  await refreshNotebookLock();
});
$("remove-lock-notebook")?.addEventListener("click", async () => {
  if (!slug) return;
  const ok = await confirmDialog({
    title: "Remove lock",
    message: "Remove the password on this whole notebook? It will no longer be hidden.",
    okLabel: "Remove lock",
    danger: true,
  });
  if (!ok) return;
  try {
    await notesApi.removeLock(slug, "", "notebook");
    toast("Lock removed");
    await refreshNotebookLock();
    await refreshTree();
  } catch (e) {
    toast(e.message, true);
  }
});

const plural = (n) => `${n} note${n === 1 ? "" : "s"}`;

// One entry point for deleting from the trash. The two things it can do
// were separate buttons that read alike; here each is spelled out.
async function cleanUpTrash() {
  if (!slug) return;
  const expiredOk = trashRetention !== RETENTION_NEVER && trashExpired > 0;
  const options = [
    {
      value: "expired",
      label: trashRetention === RETENTION_NEVER
        ? "Expired notes only — unavailable (set an automatic deletion period first)"
        : `Expired notes only — ${plural(trashExpired)} past the deletion period`,
      disabled: !expiredOk,
    },
    { value: "all", label: `Everything in the trash — ${plural(trashCount)}` },
  ];
  const r = await formDialog({
    title: "Clean up trash",
    message: "Deleted for good; this cannot be undone. The server also removes expired notes by itself, so you rarely need the first option.",
    fields: [{ name: "scope", label: "Delete", type: "select", options, value: expiredOk ? "expired" : "all" }],
    okLabel: "Delete",
    danger: true,
  });
  if (!r) return;
  await guarded(async () => {
    const res = await notesApi.purgeTrash(slug, r.scope === "all" ? { all: true } : {});
    toast(res.purged ? `Deleted ${plural(res.purged)} from the trash.` : "Nothing was due for deletion.");
    await refreshTrashState();
    if (!$("trash-modal").hidden) await renderTrash();
  });
}

$("trash-retention")?.addEventListener("change", (e) => applyRetention(e.target.value));
$("trash-empty")?.addEventListener("click", cleanUpTrash);
$("trash-empty-modal")?.addEventListener("click", cleanUpTrash);
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
      const name = (file.name || "note.md").replace(/\.(md|markdown|txt)$/i, "");
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

function isTextFile(name) {
  return /\.(txt)$/i.test(String(name ?? ""));
}

function importFiles(files) {
  if (!slug || !files || !files.length) return;
  const list = [...files];
  if (list.some((f) => isArchive(f.name))) {
    importArchive(list.find((f) => isArchive(f.name)));
  } else if (list.every((f) => isMarkdown(f.name) || isTextFile(f.name))) {
    importMarkdown(list);
  } else {
    toast("Import accepts a .tar.gz export, .md or .txt files.", true);
  }
}

async function importNoteFromFile() {
  if (!slug) return;
  const input = document.createElement("input");
  input.type = "file";
  input.accept = ".md,.markdown,.txt,text/markdown,text/plain";
  input.multiple = true;
  input.onchange = () => { if (input.files?.length) importMarkdown([...input.files]); };
  input.click();
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
  applyReadPane();
  if (editing && editing.mode === "textarea") {
    applyEditorPrefs();
    updateLineGutter($("editbody"), $("editgutter"), $("editmirror"));
  }
  if (current && !editing) {
    applyExternalLinkTargets($("note"));
    refreshViewStatus();
  }
}

function applyStatusFields() {
  const fields = getStatusFields();
  for (const field of STATUS_FIELDS) {
    const el = q(`#statusbar [data-sb="${field}"]`);
    if (el) el.hidden = !fields[field];
  }
}

// The startpage box reads "_blank" while none is set (typing it, or
// nothing, clears the setting).
const NO_STARTPAGE = "_blank";

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
  const home = $("homepage-path");
  if (home) {
    home.value = slug ? (getHomepage(slug) || NO_STARTPAGE) : "";
    home.disabled = !slug;
  }
  syncThemeUI();
  syncPlacementUI();
  updateVimStatus();
  syncVimrcInfo();
  updateServerInfo();
  syncTrashSettings();
  refreshTrashState();
  syncLockSettings();
  refreshNotebookLock();
  syncTemplatesSettings();
  renderKeybindings();
  renderPluginsSection();
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
    // journal is a state→count map, not a number (Number(map) === 0).
    const counts = (s.journal && typeof s.journal === "object") ? s.journal : {};
    const pending = Number(counts.pending) || 0;
    const rows = [
      infoRow("Notebook", slug),
      infoRow("File storage", s.backend === "s3" ? "S3 bucket" : "Server disk"),
      infoRow("Uploads stored", humanBytes(used)),
    ];
    if (reserved > 0) rows.push(infoRow("Uploading now", humanBytes(reserved)));
    rows.push(
      infoRow("Upload limit", quota > 0 ? humanSizePair(used + reserved, quota) : "none (unlimited)"),
      infoRow("Unfinished uploads", String(pending)),
    );
    dl.replaceChildren(...rows);
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

$("reload-lua-plugins")?.addEventListener("click", async () => {
  if (!slug) return;
  await guarded(async () => {
    await fetch(`/n/${encodeURIComponent(slug)}/plugins/reload`, {
      method: "POST",
      headers: { "X-CSRF-Token": "1" },
    });
    renderPluginsSection();
    refreshPluginPanels(slug);
    toast("Lua plugins reloaded.");
  });
});

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

// --- startpage (per notebook, browser-local) ---

function saveHomepageFromInput() {
  if (!slug) return;
  const typed = ($("homepage-path")?.value || "").trim();
  const v = setHomepage(slug, typed === NO_STARTPAGE ? "" : typed);
  if ($("homepage-path")) $("homepage-path").value = v || NO_STARTPAGE;
  toast(v ? `Startpage: ${v}` : "Startpage cleared.");
}

$("homepage-path")?.addEventListener("change", saveHomepageFromInput);
$("homepage-use-current")?.addEventListener("click", () => {
  if (!slug || !current) {
    toast("Open a note first.", true);
    return;
  }
  if ($("homepage-path")) $("homepage-path").value = current;
  saveHomepageFromInput();
});
$("homepage-clear")?.addEventListener("click", () => {
  if ($("homepage-path")) $("homepage-path").value = "";
  saveHomepageFromInput();
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
  applyUserTheme(getTheme());
  applyPlacement(getPlacement());
  syncSettingsForm();
  toast("Preferences reset.");
});

$("export-prefs")?.addEventListener("click", () => {
  const data = exportPreferences();
  const blob = new Blob([`${JSON.stringify(data, null, 2)}\n`], { type: "application/json" });
  const day = new Date().toISOString().slice(0, 10);
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = `noteroot-prefs-${day}.json`;
  a.click();
  URL.revokeObjectURL(a.href);
  toast("Preferences exported.");
});

$("import-prefs")?.addEventListener("click", () => $("import-prefs-file")?.click());
$("import-prefs-file")?.addEventListener("change", async (e) => {
  const file = e.target.files?.[0];
  e.target.value = "";
  if (!file) return;
  let data;
  try {
    data = JSON.parse(await file.text());
  } catch {
    toast("That file is not valid JSON.", true);
    return;
  }
  const res = importPreferences(data);
  if (res.error) {
    toast(res.error, true);
    return;
  }
  applyPreferences();
  applyUserTheme(getTheme());
  applyPlacement(getPlacement());
  if (treeData) drawTree();
  syncSettingsForm();
  toast(res.skipped
    ? `Imported ${res.imported} preference${res.imported === 1 ? "" : "s"} (${res.skipped} skipped).`
    : `Imported ${res.imported} preference${res.imported === 1 ? "" : "s"}.`);
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

// --- keybindings editor (Settings → Shortcuts) ---

const MOD_LABEL = "Ctrl/⌘";

function bindingChips(binding) {
  if (!binding) return [];
  const parts = [];
  if (binding.ctrl) parts.push(MOD_LABEL);
  if (binding.alt) parts.push("Alt");
  if (binding.shift) parts.push("Shift");
  parts.push(binding.key === "Escape" ? "Esc" : binding.key);
  return parts;
}

/** True when two bindings (possibly one bare default) are equivalent. */
function sameBinding(a, b) {
  if (!a || !b) return !a && !b;
  return String(a.key).toLowerCase() === String(b.key).toLowerCase()
    && Boolean(a.ctrl) === Boolean(b.ctrl)
    && Boolean(a.shift) === Boolean(b.shift)
    && Boolean(a.alt) === Boolean(b.alt);
}

function specFromEvent(e) {
  if (e.key === "Escape") return { cancel: true };
  if (e.key === "Backspace" || e.key === "Delete") return null;
  const key = e.key;
  if (["Control", "Meta", "Alt", "Shift"].includes(key)) return undefined;
  const named = key.length !== 1;
  return {
    key: named ? key : key.toLowerCase(),
    ctrl: Boolean(e.ctrlKey || e.metaKey),
    shift: Boolean(e.shiftKey),
    alt: Boolean(e.altKey),
  };
}

function findConflict(bindings, action, spec) {
  if (!spec) return null;
  for (const [other, b] of Object.entries(bindings)) {
    if (other === action || !b) continue;
    if (
      String(b.key).toLowerCase() === String(spec.key).toLowerCase()
      && Boolean(b.ctrl) === Boolean(spec.ctrl)
      && Boolean(b.shift) === Boolean(spec.shift)
      && Boolean(b.alt) === Boolean(spec.alt)
    ) return other;
  }
  return null;
}

function renderPluginsSection() {
  const uiList = $("ui-plugins-list");
  const luaList = $("lua-plugins-list");
  if (!uiList) return;

  // Built-in UI plugins
  uiList.replaceChildren();
  for (const plugin of UI_PLUGIN_REGISTRY) {
    const row = document.createElement("div");
    row.className = "plugin-row";

    const label = document.createElement("label");
    label.className = "plugin-toggle-label";

    const toggle = document.createElement("input");
    toggle.type = "checkbox";
    toggle.checked = slug ? getUiPluginEnabled(slug, plugin.id) : false;
    toggle.disabled = !slug;
    toggle.addEventListener("change", () => {
      if (!slug) return;
      setUiPluginEnabled(slug, plugin.id, toggle.checked);
      syncUiPlugins(slug, UI_PLUGIN_REGISTRY);
    });

    const nameSpan = document.createElement("span");
    nameSpan.className = "plugin-name";
    nameSpan.textContent = plugin.name;

    const descSpan = document.createElement("span");
    descSpan.className = "plugin-desc";
    descSpan.textContent = plugin.description;

    label.append(toggle, nameSpan);
    row.append(label, descSpan);
    uiList.append(row);
  }

  // Lua plugins (async refresh)
  if (!luaList) return;
  luaList.replaceChildren();
  if (!slug) {
    const p = document.createElement("p");
    p.className = "help-text muted";
    p.textContent = "Open a notebook to view its Lua plugins.";
    luaList.append(p);
    return;
  }
  fetch(`/n/${encodeURIComponent(slug)}/plugins`, { headers: { Accept: "application/json" } })
    .then((r) => r.ok ? r.json() : [])
    .then((infos) => {
      luaList.replaceChildren();
      if (!infos.length) {
        const p = document.createElement("p");
        p.className = "help-text muted";
        p.textContent = "No Lua plugins found.";
        luaList.append(p);
        return;
      }
      for (const info of infos) {
        const row = document.createElement("div");
        row.className = "plugin-row";
        const badge = document.createElement("span");
        badge.className = `plugin-badge ${info.loaded ? "badge-ok" : (info.error ? "badge-err" : "badge-off")}`;
        badge.textContent = info.loaded ? "loaded" : (info.error ? "error" : "disabled");
        const name = document.createElement("span");
        name.className = "plugin-name";
        name.textContent = info.name || info.dir || "?";
        const desc = document.createElement("span");
        desc.className = "plugin-desc";
        desc.textContent = info.description || (info.error ? `Error: ${info.error}` : "");
        row.append(badge, name, desc);
        luaList.append(row);
      }
    })
    .catch(() => {});
}

function renderKeybindings() {
  const list = $("keybindings-list");
  if (!list) return;
  const bindings = getKeybindings();
  const rows = [];
  for (const action of Object.keys(DEFAULT_KEYBINDINGS)) {
    const row = document.createElement("div");
    row.className = "keybinding-row";
    row.setAttribute("role", "listitem");
    row.dataset.action = action;

    const label = document.createElement("span");
    label.className = "keybinding-label";
    label.textContent = KEYBINDING_LABELS[action] || action;

    const capture = document.createElement("button");
    capture.type = "button";
    capture.className = "keybinding-capture";
    fillCapture(capture, bindings[action]);

    const reset = document.createElement("button");
    reset.type = "button";
    reset.className = "keybinding-reset";
    reset.textContent = "↺";
    reset.title = "Reset to default";
    reset.setAttribute("aria-label", `Reset ${label.textContent}`);
    reset.hidden = sameBinding(bindings[action], DEFAULT_KEYBINDINGS[action]);
    reset.addEventListener("click", () => {
      resetKeybindings(action);
      renderKeybindings();
    });

    capture.addEventListener("click", () => beginCapture(action, capture));

    row.append(label, capture, reset);
    rows.push(row);
  }
  list.replaceChildren(...rows);
}

function fillCapture(btn, binding) {
  btn.replaceChildren();
  btn.classList.remove("capturing", "kb-empty", "kb-conflict");
  if (!binding) {
    btn.classList.add("kb-empty");
    btn.textContent = "Unbound";
    return;
  }
  const chips = bindingChips(binding);
  chips.forEach((c, i) => {
    if (i) {
      const plus = document.createElement("span");
      plus.className = "kb-plus";
      plus.textContent = "+";
      btn.append(plus);
    }
    const kbd = document.createElement("kbd");
    kbd.textContent = c;
    btn.append(kbd);
  });
}

let capturing = null;

function beginCapture(action, btn) {
  if (capturing) endCapture(false);
  capturing = { action, btn };
  btn.replaceChildren(document.createTextNode("Press keys…"));
  btn.classList.add("capturing");
  window.addEventListener("keydown", onCaptureKey, true);
}

function endCapture(applied) {
  if (!capturing) return;
  window.removeEventListener("keydown", onCaptureKey, true);
  capturing = null;
  if (!applied) renderKeybindings();
}

function onCaptureKey(e) {
  e.preventDefault();
  e.stopPropagation();
  const spec = specFromEvent(e);
  if (spec === undefined) return;
  if (spec && spec.cancel) {
    endCapture(false);
    return;
  }
  const { action } = capturing;
  setKeybinding(action, spec);
  if (spec) {
    const conflict = findConflict(getKeybindings(), action, spec);
    if (conflict) toast(`“${KEYBINDING_LABELS[conflict] || conflict}” uses the same keys as well.`, true);
  }
  endCapture(true);
  renderKeybindings();
}

$("keybindings-reset")?.addEventListener("click", () => {
  resetKeybindings();
  renderKeybindings();
  toast("Keyboard shortcuts reset to defaults.");
});

// --- shortcuts manager (separate window, like attachments) ---

function openShortcutsModal() {
  const m = $("shortcuts-modal");
  if (m) m.hidden = false;
  renderKeybindings();
}

function closeShortcutsModal() {
  const m = $("shortcuts-modal");
  if (m) m.hidden = true;
  if (capturing) endCapture(false);
}

$("shortcuts-manage")?.addEventListener("click", () => {
  closeSettingsModal();
  openShortcutsModal();
});
$("shortcuts-close")?.addEventListener("click", closeShortcutsModal);
$("shortcuts-modal")?.querySelector(".modal-backdrop")?.addEventListener("click", closeShortcutsModal);

// --- vimrc editor (separate plain-text window) ---

function openVimrcModal() {
  $("vimrc-body").value = getVimrc();
  $("vimrc-modal").hidden = false;
  $("vimrc-body").focus();
}

function closeVimrcModal() {
  $("vimrc-modal").hidden = true;
  syncVimrcInfo();
}

function syncVimrcInfo() {
  const el = $("vimrc-info");
  if (!el) return;
  const lines = getVimrc().split("\n").filter((l) => l.trim()).length;
  el.textContent = lines
    ? `${lines} line${lines === 1 ? "" : "s"} set. Applies the next time Vim starts.`
    : "No vimrc set. Applies the next time Vim starts.";
}

$("multi-move")?.addEventListener("click", moveMultiNotes);
$("multi-delete")?.addEventListener("click", deleteMultiNotes);
$("multi-clear")?.addEventListener("click", clearMultiNotes);

$("edit-vimrc")?.addEventListener("click", openVimrcModal);
$("vimrc-cancel")?.addEventListener("click", closeVimrcModal);
$("vimrc-close")?.addEventListener("click", closeVimrcModal);
$("vimrc-modal")?.querySelector(".modal-backdrop")?.addEventListener("click", closeVimrcModal);
$("vimrc-save")?.addEventListener("click", () => {
  setVimrc($("vimrc-body").value);
  closeVimrcModal();
  toast("vimrc saved. It applies the next time Vim starts.");
});
$("vimrc-clear")?.addEventListener("click", () => { $("vimrc-body").value = ""; $("vimrc-body").focus(); });
// Tab types a tab here: it is a config file, not a form.
$("vimrc-body")?.addEventListener("keydown", (e) => {
  if (e.key !== "Tab") return;
  e.preventDefault();
  const ta = e.target;
  ta.setRangeText("\t", ta.selectionStart, ta.selectionEnd, "end");
});

// --- global Escape / modal close ---

// Esc closes the innermost thing: an overlay, then the phone drawer,
// then whatever the pane shows. Dialogs and menus handle their own Esc
// first (and stop it), Vim keeps its Esc for mode switching, and the
// Vim editor is never closed by a stray key: Done is the way out.
// In the plain-text editor Esc saves and closes (there is no other key
// that could mean "leave insert mode" here).
window.addEventListener("keydown", (e) => {
  if (!matchesBinding(e, getKeybindings()["close-note"]) || e.defaultPrevented) return;
  if (!$("vimrc-modal").hidden) closeVimrcModal();
  else if (!$("help-modal").hidden) closeHelp();
  else if (!$("settings-modal").hidden) closeSettingsModal();
  else if (!$("trash-modal").hidden) closeTrash();
  else if (!$("attachments-modal").hidden) closeAttachmentsModal();
  else if (!$("shortcuts-modal").hidden) closeShortcutsModal();
  else if (!$("history-modal").hidden) closeHistoryWindow();
  else if (isPhone() && document.body.classList.contains("tree-open")) setDrawer(false);
  else if (splitSession && (e.target === $("split-body")
    || (e.target && e.target.closest && e.target.closest("#split-pane")))) {
    // The second pane's plain-text editor: Esc = save and close.
    e.preventDefault();
    saveAndCloseSplit();
  }
  else if (editing && editing.mode === "textarea") {
    // Plain-text editor: Esc = save and close, like Vim's :wq.
    e.preventDefault();
    saveAndCloseEditor();
  }
  // Esc on the homepage itself does nothing: closing it would just
  // reopen it.
  else if (!editing && !isTypingTarget(e.target) && view !== "empty"
    && current !== resolveHomepage(getHomepage(slug))) closeNote();
});

// --- reader vim navigation (j/k) ---

// While reading a note, j/k step to the next/previous note in tree
// order, matching the Vim editor's motions. Ignored while typing, in
// the editor, in search results, or when a modal is open.
window.addEventListener("keydown", (e) => {
  if (editing || e.defaultPrevented) return;
  if (view !== "read" || isTypingTarget(e.target)) return;
  if (!$("help-modal").hidden || !$("settings-modal").hidden || !$("trash-modal").hidden || !$("attachments-modal").hidden || !$("vimrc-modal").hidden) return;
  if (document.querySelector(".ctx-menu")) return;
  const kb = getKeybindings();
  let delta = 0;
  if (matchesBinding(e, kb["next-note"])) delta = 1;
  else if (matchesBinding(e, kb["prev-note"])) delta = -1;
  else return;
  e.preventDefault();
  navigateNotes(delta);
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

// --- Attachments manager (dedicated window) ---

let _attachmentsCache = [];

function openAttachmentsModal() {
  if (!slug) { toast("Open a notebook first.", true); return; }
  const m = $("attachments-modal");
  if (m) m.hidden = false;
  const s = $("attachments-search"); if (s) s.value = "";
  const f = $("attachments-filter"); if (f) f.value = "all";
  loadAttachmentsManager();
}

function closeAttachmentsModal() {
  const m = $("attachments-modal");
  if (m) m.hidden = true;
}

function attachKind(mime, name = "") {
  const m = (mime || "").toLowerCase();
  const n = (name || "").toLowerCase();
  if (m.startsWith("image/")) return "image";
  if (m.startsWith("video/")) return "video";
  if (m.startsWith("audio/")) return "audio";
  if (m === "application/pdf" || n.endsWith(".pdf")) return "pdf";
  if (n.endsWith(".zip") || n.endsWith(".gz") || n.endsWith(".tar") || n.endsWith(".7z")) return "archive";
  if (m.includes("json") || m.includes("xml") || n.endsWith(".md") || n.endsWith(".txt") || n.endsWith(".csv")) return "text";
  return "other";
}

async function loadAttachmentsManager() {
  const grid = $("attachments-grid");
  const sum = $("attachments-summary");
  if (!grid) return;
  if (!slug) {
    grid.innerHTML = '<p class="muted small">Open a notebook to see its attachments.</p>';
    if (sum) sum.textContent = "";
    return;
  }
  grid.innerHTML = '<p class="muted small">Loading…</p>';
  if (sum) sum.textContent = "Loading…";
  try {
    const data = await notesApi.listResources(slug, "committed");
    const items = Array.isArray(data) ? data : (data?.resources || []);
    _attachmentsCache = items;
    renderAttachmentsManager(items);
  } catch (e) {
    grid.innerHTML = `<p class="muted small">Error loading attachments: ${esc(e.message)}</p>`;
    if (sum) sum.textContent = "Error";
  }
}

function renderAttachmentsManager(items) {
  const grid = $("attachments-grid");
  const sum = $("attachments-summary");
  if (!grid) return;
  const qv = ($("attachments-search")?.value || "").toLowerCase();
  const fv = $("attachments-filter")?.value || "all";
  let filtered = items.filter((res) => {
    const sha = (res.SHA || res.sha || "").toLowerCase();
    const name = (res.Filename || res.filename || "").toLowerCase();
    const mime = (res.MIME || res.mime || "").toLowerCase();
    if (fv !== "all" && attachKind(mime, name) !== fv) return false;
    if (!qv) return true;
    return name.includes(qv) || mime.includes(qv) || sha.includes(qv);
  });
  if (sum) sum.textContent = `${filtered.length} of ${items.length} file${items.length === 1 ? "" : "s"}`;
  if (filtered.length === 0) {
    grid.innerHTML = '<p class="muted small attachments-empty">No attachments match.</p>';
    return;
  }
  grid.textContent = "";
  for (const res of filtered) {
    const sha = res.SHA || res.sha || "";
    const name = res.Filename || res.filename || sha;
    const mime = res.MIME || res.mime || "—";
    const size = humanBytes(Number(res.Size || res.size) || 0);
    const kind = attachKind(mime, name);
    const url = `/n/${encodeURIComponent(slug)}/resources/${sha}`;
    const card = document.createElement("article");
    card.className = "attach-card";
    const thumb = document.createElement("div");
    thumb.className = "attach-thumb";
    if (kind === "image" && sha) {
      const img = document.createElement("img");
      img.src = thumbURL(slug, sha, 320, 236);
      img.alt = name;
      img.loading = "lazy";
      thumb.append(img);
    } else {
      const k = document.createElement("div");
      k.className = "attach-kind";
      k.textContent = kind === "other" ? (mime.split("/")[0] || "file") : kind;
      thumb.append(k);
    }
    const meta = document.createElement("div");
    meta.className = "attach-meta";
    const nm = document.createElement("div");
    nm.className = "attach-name";
    nm.title = name;
    nm.textContent = name;
    const sub = document.createElement("div");
    sub.className = "attach-sub";
    sub.textContent = `${mime} · ${size} · ${sha.slice(0, 8)}`;
    meta.append(nm, sub);
    const acts = document.createElement("div");
    acts.className = "attach-actions";
    const ins = document.createElement("button");
    ins.className = "btn small";
    ins.textContent = editing ? "Insert into note" : "Insert link";
    ins.onclick = async () => {
      const md = markdownFor(slug, { sha, filename: name, mime });
      if (!editing && !splitSession) {
        try { await navigator.clipboard.writeText(md); toast("Copied link to clipboard."); } catch { toast(md, false); }
        return;
      }
      // The pane that is being edited gets the link; the other one is
      // left alone.
      if (!insertIntoSplitEditor(md)) {
        await insertIntoEditor(md);
        markDirty();
      }
      toast("Inserted attachment link.");
    };
    const openBtn = document.createElement("button");
    openBtn.className = "btn small ghost";
    openBtn.textContent = "Open";
    openBtn.onclick = () => window.open(url, "_blank", "noopener,noreferrer");
    const copy = document.createElement("button");
    copy.className = "btn small ghost";
    copy.textContent = "Copy URL";
    copy.onclick = async () => {
      try { await navigator.clipboard.writeText(url); toast("Copied URL."); } catch { toast(url, false); }
    };
    const refs = document.createElement("button");
    refs.className = "btn small ghost";
    refs.textContent = "References";
    refs.onclick = async () => {
      refs.disabled = true;
      refs.textContent = "…";
      try {
        const r = await fetch(`/n/${encodeURIComponent(slug)}/resources/${sha}/refs`);
        const rd = await r.json();
        const paths = rd.refs || [];
        let box = card.querySelector(".attach-refs");
        if (!box) { box = document.createElement("div"); box.className = "attach-refs"; card.append(box); }
        box.textContent = paths.length ? "Used in: " : "Used in: none";
        paths.forEach((p, i) => {
          if (i > 0) box.append(", ");
          const a = document.createElement("a");
          a.href = "#";
          a.textContent = p;
          a.onclick = (e) => { e.preventDefault(); closeAttachmentsModal(); closeSettingsModal(); openNote(p, null); };
          box.append(a);
        });
      } catch {
        refs.disabled = false; refs.textContent = "References";
        toast("Could not load references.", true);
      }
    };
    const del = document.createElement("button");
    del.className = "btn small ghost";
    del.style.color = "var(--danger)";
    del.textContent = "Delete";
    del.onclick = async () => {
      if (!confirm(`Delete "${name}"?\n\nThis removes the file permanently. Any notes linking to it will show broken links.`)) return;
      del.disabled = true;
      try {
        await notesApi.deleteResource(slug, sha);
        _attachmentsCache = _attachmentsCache.filter((x) => (x.SHA||x.sha) !== sha);
        renderAttachmentsManager(_attachmentsCache);
      } catch (e) {
        del.disabled = false;
        toast(`Delete failed: ${e.message}`, true);
      }
    };
    acts.append(ins, openBtn, copy, refs, del);
    card.append(thumb, meta, acts);
    grid.append(card);
  }
}

document.addEventListener("click", (e) => {
  if (e.target && e.target.id === "attachments-manage") {
    e.preventDefault();
    closeSettingsModal();
    openAttachmentsModal();
    return;
  }
});

function initAttachmentsEvents() {
  $("attachments-close")?.addEventListener("click", closeAttachmentsModal);
  $("attachments-modal")?.querySelector(".modal-backdrop")?.addEventListener("click", closeAttachmentsModal);
  $("attachments-refresh2")?.addEventListener("click", loadAttachmentsManager);
  $("attachments-search")?.addEventListener("input", () => renderAttachmentsManager(_attachmentsCache));
  $("attachments-filter")?.addEventListener("change", () => renderAttachmentsManager(_attachmentsCache));
  $("attachments-upload")?.addEventListener("click", () => $("attachments-upload-file")?.click());
  const uploadFileInput = $("attachments-upload-file");
  if (uploadFileInput) {
    uploadFileInput.addEventListener("change", async (e) => {
      const files = [...(e.target.files || [])];
      e.target.value = "";
      for (const file of files) {
        try {
          const res = await uploadResource(file);
          if (res && res.sha) {
            toast(`Uploaded ${file.name}. Use Insert into note to embed it.`);
          }
        } catch (ex) {
          toast(`Upload failed: ${ex.message}`, true);
        }
      }
      loadAttachmentsManager();
    });
  }
}

// Module loads after DOM in both the browser (type=module defer) and jsdom tests.
initAttachmentsEvents();

// Warm the integration status cache so paste handlers don't add latency.
fetchIntegrationStatus().catch(() => {});

