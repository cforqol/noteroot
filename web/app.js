import { VimWasm, checkBrowserCompatibility } from '/vim/vimwasm.js';
import { uploadURL, markdownFor, rewriteImageThumbs, presignUpload } from '/lib/upload.js';
import {
  getVimModeState,
  setVimModeState,
  recordVimFailure,
  shouldUseVim as shouldUseVimCore,
  resetVimCircuitBreaker,
  getEditorPreference,
  setEditorPreference,
} from '/lib/vim-mode.js';
import { fetchPanels, renderPanelChips } from '/lib/panels.js';

const $ = (id) => document.getElementById(id);
let current = null;
let slug = null;
let lastNote = null;

function esc(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

async function api(path) {
  const r = await fetch(path, { headers: { Accept: "application/json" } });
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return r.json();
}

function renderNode(node, depth) {
  const frag = document.createDocumentFragment();
  for (const child of node.children || []) {
    if (child.isDir) {
      const label = document.createElement("div");
      label.className = "node dir";
      label.style.paddingLeft = depth * 12 + "px";
      label.textContent = child.name + "/";
      frag.append(label);
      frag.append(renderNode(child, depth + 1));
    } else {
      const b = document.createElement("button");
      b.className = "node";
      b.style.paddingLeft = depth * 12 + "px";
      b.textContent = child.name.replace(/\.md$/, "");
      b.dataset.path = child.path;
      b.onclick = () => openNote(child.path, b);
      if (child.path === current) b.classList.add("active");
      frag.append(b);
    }
  }
  return frag;
}

async function openNote(path, btn) {
  closeEditor();
  current = path;
  document.querySelectorAll(".node.active").forEach((n) => n.classList.remove("active"));
  btn?.classList.add("active");
  try {
    const n = await api(`/n/${encodeURIComponent(slug)}/note?path=${encodeURIComponent(path)}`);
    lastNote = { path, sha: n.sha || null, title: n.title || path };
    document.title = (n.title || path) + " — noteroot";
    let html = "";
    if (n.frontmatterError) html += `<div class="warn">frontmatter: ${esc(n.frontmatterError)}</div>`;
    if (n.renderError) html += `<div class="warn">render: ${esc(n.renderError)}</div>`;
    html += rewriteImageThumbs(n.html, slug, { maxWidth: 800, maxHeight: 600 });
    $("note").innerHTML = html;
    $("note").append(linksPanels(n));
    $("note").scrollTop = 0;
    $("editbtn").hidden = false;
  } catch (e) {
    $("note").innerHTML = `<p class="muted">${esc(e.message)}</p>`;
    $("editbtn").hidden = true;
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

// SPA navigation for resolved wikilinks: plain left-click stays in the
// app; middle-click / cmd / ctrl / shift keep the browser default
// (new tab, etc.) because the anchors are real hrefs (no-JS works too).
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
    $("note").innerHTML = '<p class="muted">No notebooks. Run <code>./scripts/new-notebook.sh &lt;slug&gt;</code>.</p>';
    return;
  }
  for (const nb of notebooks) {
    const o = document.createElement("option");
    o.value = o.textContent = nb.slug;
    sel.append(o);
  }
  sel.onchange = () => loadTree(sel.value);
  await loadTree(notebooks[0].slug);
}

async function loadTree(s) {
  slug = s;
  current = null;
  lastNote = null;
  closeEditor();
  $("q").value = "";
  $("editbtn").hidden = true;
  $("note").innerHTML = '<p class="muted">Select a note.</p>';
  const tree = await api(`/n/${encodeURIComponent(s)}/tree`);
  $("tree").replaceChildren(renderNode(tree, 0));
  refreshPluginPanels(s);
}

// Plugin panels (stage 9): degradation-grade — a missing endpoint
// or malformed spec hides the strip, never breaks the notebook.
let panelRefreshToken = 0;
async function refreshPluginPanels(s) {
  if (!s) return;
  const token = ++panelRefreshToken;
  const panels = await fetchPanels(s);
  // A newer refresh (or a notebook switch mid-flight) wins; stale
  // responses never paint into the wrong tree.
  if (token !== panelRefreshToken || s !== slug) return;
  const footer = $("plugin-status");
  const has = panels && Object.keys(panels).length > 0;
  footer.hidden = !has;
  if (has) renderPanelChips($("plugin-panels"), panels);
}

async function search(q) {
  q = q.trim();
  if (!slug || !q) return;
  try {
    const r = await api(`/n/${encodeURIComponent(slug)}/search?q=${encodeURIComponent(q)}&limit=50`);
    if (!r.hits.length) {
      $("note").innerHTML = `<p class="muted">No results for “${esc(q)}”.</p>`;
      return;
    }
    const frag = document.createDocumentFragment();
    const h = document.createElement("p");
    h.className = "muted";
    h.textContent = `${r.hits.length} result${r.hits.length === 1 ? "" : "s"} for “${q}”.`;
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
    $("note").innerHTML = `<p class="muted">${esc(e.message)}</p>`;
  }
}

$("q")?.addEventListener("keydown", (e) => {
  if (e.key === "Enter") search(e.target.value);
});

boot().catch((e) => { $("note").innerHTML = `<p class="muted">${esc(e.message)}</p>`; });

// --- vim mode (stage 4): desktop editor with vim.wasm running in-page ---

class VimEditor {
  constructor() {
    this.vim = null;
    this.onDirtyCallback = null;
    this.onSaveCallback = null;
    this.onCloseCallback = null;
    this.tmpFilePath = null;
    this.currentContent = '';
  }

  async open(path, content, baseSha) {
    try {
      // Show container and loading indicator
      const container = $('vim-container');
      const loading = $('vim-loading');
      container.hidden = false;
      loading.hidden = false;

      // Create tmp file path (vim.wasm uses virtual filesystem)
      this.tmpFilePath = `/tmp/${path.replace(/\//g, '_')}`;
      this.currentContent = content;

      // Initialize VimWasm
      this.vim = new VimWasm({
        canvas: $('vim-canvas'),
        input: $('vim-input'),
        workerScriptPath: '/vim/vim-worker.js',
      });

      // Setup callbacks
      this.vim.onError = (err) => {
        console.error('[VimEditor] Error:', err);
        recordVimFailure();
        loading.hidden = true;
        if (this.onCloseCallback) {
          this.onCloseCallback(true);
        }
      };

      this.vim.onVimInit = () => {
        // Hide loading indicator when Vim is ready
        loading.hidden = true;
        console.log('[VimEditor] Vim initialized and ready');
      };

      this.vim.onVimExit = (status) => {
        console.log('[VimEditor] Vim exited with status', status);
        if (this.onCloseCallback) {
          this.onCloseCallback(false);
        }
      };

      // Start Vim with the file
      await this.vim.start({
        debug: false,
        files: {
          [this.tmpFilePath]: content,
        },
        cmdArgs: [this.tmpFilePath],
      });

      // Vim started successfully
      console.log('[VimEditor] Vim started, editing', this.tmpFilePath);
      
    } catch (error) {
      console.error('[VimEditor] Failed to open:', error);
      const loading = $('vim-loading');
      loading.hidden = true;
      recordVimFailure();
      throw error;
    }
  }

  async getContent() {
    if (!this.vim || !this.tmpFilePath) {
      return this.currentContent;
    }

    // vim.wasm exposes no buffer read API; a forced write to the tmp
    // file round-trips the buffer through onFileExport, which refreshes
    // currentContent. The save callback is suppressed for this
    // programmatic write — callers decide whether to persist.
    this.suppressSave = true;
    try {
      await this.vim.cmdline(`w! ${this.tmpFilePath}`);
    } catch (error) {
      console.error('[VimEditor] Failed to flush buffer:', error);
    } finally {
      this.suppressSave = false;
    }
    return this.currentContent;
  }

  onDirty(callback) {
    this.onDirtyCallback = callback;
    // Note: vim.wasm doesn't have a built-in dirty callback
    // We'll trigger it on any key press as a workaround
  }

  onSave(callback) {
    this.onSaveCallback = callback;
    // Setup file export handler
    if (this.vim) {
      this.vim.onFileExport = (path, contents) => {
        if (path === this.tmpFilePath) {
          const decoder = new TextDecoder();
          this.currentContent = decoder.decode(contents);
          if (!this.suppressSave && this.onSaveCallback) {
            this.onSaveCallback(this.currentContent);
          }
        }
      };
    }
  }

  onClose(callback) {
    this.onCloseCallback = callback;
  }

  focus() {
    if (this.vim) {
      this.vim.focus();
    }
  }

  async sendMessage(message) {
    // vim.wasm doesn't use messages, use cmdline() instead
    if (message.type === 'vim:reload') {
      if (this.vim && message.content) {
        // Reload file with new content
        // We'll need to use Vim commands to replace buffer
        await this.vim.cmdline('enew!');
        this.currentContent = message.content;
        
        // Create new file with updated content
        await this.vim.dropFile(this.tmpFilePath, new TextEncoder().encode(message.content));
        await this.vim.cmdline(`e ${this.tmpFilePath}`);
      }
      return true;
    }
    if (message.type === 'vim:insert' && message.content) {
      // No register API exists; stage the text as a file and :read it
      // below the cursor line. Returns false so callers can surface the
      // snippet instead of silently dropping it.
      if (!this.vim || !this.tmpFilePath) return false;
      try {
        const insertPath = '/tmp/__noteroot_insert';
        await this.vim.dropFile(insertPath, new TextEncoder().encode(message.content));
        await this.vim.cmdline(`read ${insertPath}`);
        return true;
      } catch (error) {
        console.error('[VimEditor] Failed to insert:', error);
        return false;
      }
    }
    return false;
  }

  async cleanup() {
    const container = $('vim-container');
    if (container) {
      container.hidden = true;
    }
    
    // vim.wasm will clean up automatically on exit
    // We don't need to manually terminate since :q will trigger onVimExit
    this.vim = null;
    this.tmpFilePath = null;
  }
}

// Circuit breaker for vim mode (logic lives in lib/vim-mode.js)

function shouldUseVim() {
  const use = shouldUseVimCore({ checkCompatibility: checkBrowserCompatibility });
  if (!use) {
    const state = getVimModeState();
    if (!state.enabled) {
      console.warn('[VimEditor] Disabled after failures. Reset in settings to retry.');
    }
  }
  return use;
}

// --- editor (textarea fallback + vim mode integration) ---

let editing = null; // {path, baseSha, lastSaved, dirtyTimer, saveTimer, lockTimer, mode, editor}
let tabId = null;
let currentEditorMode = null; // 'vim' or 'textarea'

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
    let content;
    if (editing.mode === 'vim' && editing.editor) {
      // Get content from vim asynchronously
      content = await editing.editor.getContent();
    } else {
      // Get content from textarea
      content = $("editbody").value;
    }
    
    localStorage.setItem(padKey(editing.path), JSON.stringify({
      tab: myTab(),
      baseSha: editing.baseSha,
      content: content,
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

async function apiRaw(path) {
  const r = await fetch(`/n/${encodeURIComponent(slug)}/raw?path=${encodeURIComponent(path)}`);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return r.text();
}

async function putNote(path, content, opts) {
  const q = new URLSearchParams({ path });
  if (opts.force) q.set("force", "1");
  const headers = { "Content-Type": "text/markdown; charset=utf-8" };
  if (opts.baseSha) headers["If-Match"] = `"${opts.baseSha}"`;
  else if (opts.createOnly) headers["If-None-Match"] = "*";
  const r = await fetch(`/n/${encodeURIComponent(slug)}/note?${q}`, {
    method: "PUT", headers, body: content,
  });
  const body = await r.json().catch(() => ({}));
  if (!r.ok) {
    const e = new Error(body.error || r.statusText);
    e.status = r.status;
    e.currentSha = body.currentSha;
    throw e;
  }
  return body;
}

async function takeLock(path) {
  try {
    const r = await fetch(
      `/n/${encodeURIComponent(slug)}/lock?path=${encodeURIComponent(path)}&holder=${encodeURIComponent(myTab())}&ttl=300`,
      { method: "PUT" });
    if (!editing) return; // editor closed while the lock request was in flight
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
  const path = lastNote.path;
  let server;
  try {
    server = await apiRaw(path);
  } catch (e) {
    $("note").innerHTML = `<p class="muted">${esc(e.message)}</p>`;
    return;
  }

  // Determine editor mode
  const useVim = shouldUseVim();
  currentEditorMode = useVim ? 'vim' : 'textarea';

  editing = {
    path,
    baseSha: lastNote.sha,
    lastSaved: server,
    dirtyTimer: 0,
    saveTimer: 0,
    lockTimer: 0,
    mode: currentEditorMode,
    editor: null
  };

  $("note").hidden = true;
  $("editor").hidden = false;
  $("savestate").textContent = "";
  $("lockstate").textContent = "";
  setBanner("");

  if (useVim) {
    await openVimEditor(server);
  } else {
    openTextareaEditor(server);
  }

  takeLock(path);
  editing.lockTimer = setInterval(() => takeLock(path), 60000);
}

async function openVimEditor(content) {
  try {
    $("editormode").textContent = "vim";
    $("editbody").hidden = true;

    const vimEditor = new VimEditor();
    editing.editor = vimEditor;

    // Setup callbacks
    vimEditor.onDirty(() => markDirty());
    vimEditor.onSave((content) => {
      // Vim requested save via :w
      editing.lastSaved = content;
      saveNoteContent(content, false);
    });
    vimEditor.onClose((force) => {
      if (force || !isContentDirty()) {
        closeEditor();
      }
    });

    // Open vim editor
    await vimEditor.open(editing.path, content, editing.baseSha);

    // Check for draft
    const pad = readPad(editing.path);
    if (pad && typeof pad.content === "string" && pad.content !== content) {
      const when = pad.savedAt ? new Date(pad.savedAt).toLocaleString() : "unknown time";
      setBanner(`<div class="warn">Unsaved changes from ${esc(when)}.` +
        ` <button class="btn" id="padrestore">Restore</button>` +
        ` <button class="btn" id="paddiscard">Discard</button></div>`);
      $("padrestore").onclick = () => {
        vimEditor.sendMessage({ type: 'vim:reload', content: pad.content, baseSha: editing.baseSha });
        setBanner("");
        markDirty();
      };
      $("paddiscard").onclick = () => {
        clearPad(editing.path);
        setBanner("");
      };
    }

  } catch (error) {
    console.error('[openEditor] Vim mode failed, falling back to textarea:', error);
    recordVimFailure();
    // Fallback to textarea
    currentEditorMode = 'textarea';
    editing.mode = 'textarea';
    editing.editor = null;
    openTextareaEditor(content);
  }
}

function openTextareaEditor(content) {
  $("editormode").textContent = "plain text";
  $("editbody").hidden = false;
  $("editbody").value = content;

  const pad = readPad(editing.path);
  if (pad && typeof pad.content === "string" && pad.content !== content) {
    const when = pad.savedAt ? new Date(pad.savedAt).toLocaleString() : "unknown time";
    setBanner(`<div class="warn">Unsaved changes from ${esc(when)}.` +
      ` <button class="btn" id="padrestore">Restore</button>` +
      ` <button class="btn" id="paddiscard">Discard</button></div>`);
    $("padrestore").onclick = () => { $("editbody").value = pad.content; setBanner(""); markDirty(); };
    $("paddiscard").onclick = () => { clearPad(editing.path); setBanner(""); };
  }

  $("editbody").oninput = markDirty;
}

function isContentDirty() {
  if (!editing) return false;
  if (editing.mode === 'textarea') {
    return $("editbody").value !== editing.lastSaved;
  }
  // For vim, we track dirty via the vim:dirty message
  return false; // Vim manages its own dirty state
}

function markDirty() {
  if (!editing) return;
  clearTimeout(editing.dirtyTimer);
  clearTimeout(editing.saveTimer);
  editing.dirtyTimer = setTimeout(writePad, 500);
  editing.saveTimer = setTimeout(() => saveNote(false), 2000);
  $("savestate").textContent = "Editing…";
}

async function saveNote(manual) {
  if (!editing) return;
  clearTimeout(editing.saveTimer);

  let content;
  if (editing.mode === 'textarea') {
    content = $("editbody").value;
  } else if (editing.mode === 'vim' && editing.editor) {
    // Manual save in vim mode: flush the live buffer, then persist it
    // like any other content (:w keeps working through onFileExport).
    try {
      content = await editing.editor.getContent();
    } catch {
      $("savestate").textContent = "Could not read Vim buffer; use :w in Vim to save";
      return;
    }
  }

  if (content === editing.lastSaved) {
    $("savestate").textContent = manual ? "No changes." : "";
    return;
  }

  await saveNoteContent(content, manual);
}

async function saveNoteContent(content, manual) {
  if (!editing) return;
  $("savestate").textContent = "Saving…";
  try {
    const res = await putNote(editing.path, content, { baseSha: editing.baseSha });
    if (!editing) return; // editor closed while the request was in flight
    editing.baseSha = res.sha;
    editing.lastSaved = content;
    clearPad(editing.path);
    $("savestate").textContent = "Saved.";
    // Plugin hooks react to the write on the bus (500ms coalesce);
    // refresh panels after they've had a chance to land.
    setTimeout(() => refreshPluginPanels(slug), 900);
  } catch (e) {
    if (e.status === 412) {
      conflictBanner();
    } else {
      $("savestate").textContent = `Save failed: ${e.message} (kept locally)`;
      if (editing.mode === 'textarea') {
        writePad();
      }
    }
  }
}

function conflictBanner() {
  setBanner(`<div class="warn">Changed on server by someone or something else.` +
    ` <button class="btn" id="cfreload">Reload (lose mine)</button>` +
    ` <button class="btn" id="cfforce">Force save (keep mine)</button></div>`);
  
  $("cfreload").onclick = async () => {
    try {
      const server = await apiRaw(editing.path);
      const n = await api(`/n/${encodeURIComponent(slug)}/note?path=${encodeURIComponent(editing.path)}`);
      if (!editing) return;
      editing.baseSha = n.sha || null;
      editing.lastSaved = server;
      
      // Update editor content based on mode
      if (editing.mode === 'vim' && editing.editor) {
        editing.editor.sendMessage({ type: 'vim:reload', content: server, baseSha: editing.baseSha });
      } else {
        $("editbody").value = server;
      }
      
      clearPad(editing.path);
      setBanner("");
      $("savestate").textContent = "";
    } catch (e) {
      $("savestate").textContent = `Reload failed: ${e.message}`;
    }
  };
  
  $("cfforce").onclick = async () => {
    // Get current content from editor
    let live;
    if (editing.mode === 'vim' && editing.editor) {
      live = await editing.editor.getContent();
    } else {
      live = $("editbody").value;
    }
    
    try {
      const res = await putNote(editing.path, live, { baseSha: editing.baseSha, force: true });
      if (!editing) return;
      editing.baseSha = res.sha;
      editing.lastSaved = live;
      clearPad(editing.path);
      setBanner("");
      $("savestate").textContent = "Saved (forced).";
    } catch (e) {
      $("savestate").textContent = `Force save failed: ${e.message}`;
    }
  };
  
  writePad();
}

function closeEditor() {
  if (!editing) return;
  clearTimeout(editing.dirtyTimer);
  clearTimeout(editing.saveTimer);
  clearInterval(editing.lockTimer);
  releaseLock(editing.path, false);

  // Cleanup editor-specific resources
  if (editing.mode === 'vim' && editing.editor) {
    editing.editor.cleanup();
  }

  editing = null;
  currentEditorMode = null;
  $("editor").hidden = true;
  $("note").hidden = false;
  $("editbody").hidden = false; // Ensure textarea is visible for next open
  setBanner("");
}

// --- Resource Upload (stage 5) ---

async function uploadResource(file) {
  if (!slug) throw new Error("no notebook selected");
  // Stage 7: try presigned direct-to-S3 upload first.
  const pre = await presignUpload(slug, file);
  if (pre.mode === "exists") {
    return { sha: pre.sha, filename: file.name, mime: file.type, created: false };
  }
  if (pre.mode === "s3") {
    return pre.result;
  }
  if (pre.mode === "error") {
    throw new Error(pre.error); // policy rejection from presign endpoint
  }
  // mode === "direct": no S3 configured (or WebCrypto unavailable) —
  // fall through to the normal server-side upload.
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

// insertAtCursor inserts text into the textarea at the current selection.
function insertAtCursor(text) {
  const ta = $("editbody");
  const start = ta.selectionStart;
  const end = ta.selectionEnd;
  const before = ta.value.slice(0, start);
  const after = ta.value.slice(end);
  ta.value = before + text + after;
  const pos = start + text.length;
  ta.selectionStart = ta.selectionEnd = pos;
  ta.focus();
}

// insertIntoEditor inserts text at the cursor for whichever editor is active.
async function insertIntoEditor(text) {
  if (!editing) return;
  if (editing.mode === 'textarea') {
    insertAtCursor(text);
    markDirty();
  } else if (editing.mode === 'vim' && editing.editor) {
    const ok = await editing.editor.sendMessage({ type: 'vim:insert', content: text });
    if (ok) {
      markDirty();
    } else {
      // Never silently drop an upload embed: surface the snippet so it
      // can be pasted by hand.
      setBanner(`<div class="warn">Uploaded, but Vim would not take the insert.` +
        ` Paste it yourself: <code>${esc(text)}</code></div>`);
    }
  }
}

// handleFileUpload uploads a file and inserts the embed at the cursor.
async function handleFileUpload(file) {
  if (!editing) return;
  $("savestate").textContent = `Uploading ${file.name}…`;
  try {
    const res = await uploadResource(file);
    if (!res || !/^[a-f0-9]{64}$/.test(res.sha || "")) {
      throw new Error("upload returned no content hash");
    }
    const md = markdownFor(slug, res);
    await insertIntoEditor(md);
    $("savestate").textContent = res.created
      ? `Uploaded ${file.name} (${res.sha.slice(0, 8)}…)`
      : `${file.name} already uploaded (dedup)`;
  } catch (e) {
    $("savestate").textContent = `Upload failed: ${e.message}`;
  }
}

// setupDropZone wires drag-and-drop onto the editor container.
function setupDropZone() {
  const editorEl = $("editor");
  if (!editorEl) return;

  editorEl.addEventListener("dragover", (e) => {
    if (!editing) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
    editorEl.classList.add("drag-over");
  });

  editorEl.addEventListener("dragleave", (e) => {
    // Only remove highlight when leaving the editor entirely.
    if (e.target === editorEl) {
      editorEl.classList.remove("drag-over");
    }
  });

  editorEl.addEventListener("drop", (e) => {
    if (!editing) return;
    e.preventDefault();
    editorEl.classList.remove("drag-over");
    const files = e.dataTransfer?.files;
    if (!files || files.length === 0) return;
    for (const file of files) {
      handleFileUpload(file);
    }
  });

  // Paste image from clipboard.
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

setupDropZone();

$("editbtn")?.addEventListener("click", openEditor);
$("savebtn")?.addEventListener("click", () => saveNote(true));
$("closebtn")?.addEventListener("click", async () => {
  const path = editing && editing.path;
  closeEditor();
  if (path && slug) {
    current = null;
    await openNote(path, null);
  }
});
window.addEventListener("pagehide", () => {
  if (editing) {
    writePad();
    releaseLock(editing.path, true);
  }
});

// --- Settings Modal ---

function openSettingsModal() {
  const modal = $("settings-modal");
  modal.hidden = false;
  
  // Load current preference
  const pref = getEditorPreference();
  const radio = document.querySelector(`input[name="editor-mode"][value="${pref}"]`);
  if (radio) radio.checked = true;
  
  // Update circuit breaker status
  updateVimStatus();
}

function closeSettingsModal() {
  $("settings-modal").hidden = true;
}

function updateVimStatus() {
  const state = getVimModeState();
  
  $("cb-status").textContent = state.enabled ? "Enabled" : "Disabled";
  $("cb-status").className = state.enabled ? "status-value enabled" : "status-value disabled";
  
  $("cb-failures").textContent = String(state.failures);
  
  if (state.lastFailure) {
    const date = new Date(state.lastFailure);
    $("cb-lastfail").textContent = date.toLocaleString();
  } else {
    $("cb-lastfail").textContent = "Never";
  }
}

function saveEditorPreference() {
  const selected = document.querySelector('input[name="editor-mode"]:checked');
  if (selected) {
    setEditorPreference(selected.value);
  }
}

function resetCircuitBreaker() {
  resetVimCircuitBreaker();
  updateVimStatus();
  alert("Circuit breaker has been reset. Vim mode is now enabled.");
}

// Settings event listeners
$("settingsbtn")?.addEventListener("click", openSettingsModal);
$("settings-close")?.addEventListener("click", closeSettingsModal);
$("settings-modal")?.querySelector(".modal-backdrop")?.addEventListener("click", closeSettingsModal);
$("reset-vim")?.addEventListener("click", resetCircuitBreaker);

// Save preference when radio buttons change
document.querySelectorAll('input[name="editor-mode"]').forEach(radio => {
  radio.addEventListener("change", saveEditorPreference);
});

// Close modal with Escape key
window.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !$("settings-modal").hidden) {
    closeSettingsModal();
  }
});
