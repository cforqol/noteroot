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
    html += n.html;
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

// --- vim mode (stage 4): desktop editor with wasi-vim in sandboxed iframe ---

class VimEditor {
  constructor() {
    this.iframe = null;
    this.ready = false;
    this.onDirtyCallback = null;
    this.onSaveCallback = null;
    this.onCloseCallback = null;
    this.heartbeatTimeout = null;
    this.lastHeartbeat = 0;
    this.messageHandler = this.handleMessage.bind(this);
  }

  async open(path, content, baseSha) {
    return new Promise((resolve, reject) => {
      // Create iframe
      this.iframe = document.createElement('iframe');
      this.iframe.id = 'vim-frame';
      this.iframe.src = '/vim/vim-frame.html';
      this.iframe.sandbox = 'allow-scripts allow-same-origin';
      this.iframe.allow = 'clipboard-read; clipboard-write';
      this.iframe.style.cssText = 'width:100%; height:100%; border:0; display:block;';

      // Setup message listener
      window.addEventListener('message', this.messageHandler);

      // Timeout for initialization
      const initTimeout = setTimeout(() => {
        this.cleanup();
        reject(new Error('Vim initialization timeout'));
      }, 5000);

      // Wait for ready message
      const readyHandler = (msg) => {
        if (msg.type === 'vim:ready') {
          clearTimeout(initTimeout);
          this.ready = true;
          this.startHeartbeatMonitor();
          
          // Load content
          this.sendMessage({
            type: 'vim:load',
            path,
            content,
            baseSha
          });
          
          resolve();
        }
      };

      this.onReadyCallback = readyHandler;

      // Mount iframe
      const container = $('vim-container');
      container.appendChild(this.iframe);
      container.hidden = false;
    });
  }

  async save() {
    return new Promise((resolve) => {
      // Request current content from vim
      const handler = (msg) => {
        if (msg.type === 'vim:save') {
          this.removeMessageListener('vim:save', handler);
          resolve({ content: msg.content, changed: true });
        }
      };
      
      this.addMessageListener('vim:save', handler);
      // Vim will send vim:save on :w command automatically
    });
  }

  async close() {
    this.cleanup();
  }

  async getContent() {
    // Request current buffer content for draft autosave
    return new Promise((resolve) => {
      const timeout = setTimeout(() => {
        this.removeMessageListener('vim:content', handler);
        resolve(''); // Return empty on timeout
      }, 1000);
      
      const handler = (msg) => {
        clearTimeout(timeout);
        this.removeMessageListener('vim:content', handler);
        resolve(msg.content || '');
      };
      
      this.addMessageListener('vim:content', handler);
      this.sendMessage({ type: 'vim:get-content' });
    });
  }

  onDirty(callback) {
    this.onDirtyCallback = callback;
  }

  onSave(callback) {
    this.onSaveCallback = callback;
  }

  onClose(callback) {
    this.onCloseCallback = callback;
  }

  focus() {
    if (this.iframe && this.ready) {
      this.sendMessage({ type: 'vim:focus' });
    }
  }

  sendMessage(message) {
    if (this.iframe && this.iframe.contentWindow) {
      this.iframe.contentWindow.postMessage(message, window.location.origin);
    }
  }

  handleMessage(event) {
    if (event.origin !== window.location.origin) return;
    if (event.source !== this.iframe?.contentWindow) return;

    const msg = event.data;

    // Handle ready
    if (msg.type === 'vim:ready' && this.onReadyCallback) {
      this.onReadyCallback(msg);
      return;
    }

    // Handle dirty
    if (msg.type === 'vim:dirty') {
      if (this.onDirtyCallback) {
        this.onDirtyCallback();
      }
    }

    // Handle save
    if (msg.type === 'vim:save') {
      if (this.onSaveCallback) {
        this.onSaveCallback(msg.content);
      }
    }

    // Handle close
    if (msg.type === 'vim:close') {
      if (this.onCloseCallback) {
        this.onCloseCallback(msg.force);
      }
    }

    // Handle heartbeat
    if (msg.type === 'vim:heartbeat') {
      this.lastHeartbeat = Date.now();
    }

    // Handle content response
    if (msg.type === 'vim:content') {
      // Handled by getContent() promise
    }

    // Handle errors
    if (msg.type === 'vim:error') {
      console.error('[VimEditor] Error from iframe:', msg.message);
      if (msg.fatal) {
        recordVimFailure();
        this.cleanup();
        // Trigger fallback
        if (this.onCloseCallback) {
          this.onCloseCallback(true);
        }
      }
    }
  }

  addMessageListener(type, handler) {
    const wrappedHandler = (event) => {
      if (event.origin !== window.location.origin) return;
      if (event.data.type === type) {
        handler(event.data);
      }
    };
    window.addEventListener('message', wrappedHandler);
    return wrappedHandler;
  }

  removeMessageListener(type, handler) {
    window.removeEventListener('message', handler);
  }

  startHeartbeatMonitor() {
    this.lastHeartbeat = Date.now();
    this.heartbeatTimeout = setInterval(() => {
      const elapsed = Date.now() - this.lastHeartbeat;
      if (elapsed > 30000) { // 30 second timeout
        console.error('[VimEditor] Heartbeat timeout - vim may have crashed');
        recordVimFailure();
        this.cleanup();
        if (this.onCloseCallback) {
          this.onCloseCallback(true);
        }
      }
    }, 10000);
  }

  cleanup() {
    if (this.heartbeatTimeout) {
      clearInterval(this.heartbeatTimeout);
    }
    if (this.iframe) {
      this.sendMessage({ type: 'vim:close' });
      window.removeEventListener('message', this.messageHandler);
      if (this.iframe.parentNode) {
        this.iframe.parentNode.removeChild(this.iframe);
      }
      this.iframe = null;
    }
    const container = $('vim-container');
    if (container) {
      container.hidden = true;
    }
    this.ready = false;
  }
}

// Circuit breaker for vim mode
function getVimModeState() {
  try {
    const state = localStorage.getItem('nr.vimMode');
    return state ? JSON.parse(state) : { enabled: true, failures: 0, lastFailure: 0 };
  } catch {
    return { enabled: true, failures: 0, lastFailure: 0 };
  }
}

function setVimModeState(state) {
  try {
    localStorage.setItem('nr.vimMode', JSON.stringify(state));
  } catch {
    // ignore
  }
}

function recordVimFailure() {
  const state = getVimModeState();
  state.failures++;
  state.lastFailure = Date.now();
  
  // Disable after 3 consecutive failures
  if (state.failures >= 3) {
    state.enabled = false;
    console.warn('[VimEditor] Disabled after 3 failures. Reset in settings to retry.');
  }
  
  setVimModeState(state);
}

function shouldUseVim() {
  // Check user preference
  const pref = localStorage.getItem('nr.editorPref') || 'auto';
  
  // If user explicitly chose textarea, respect that
  if (pref === 'textarea') {
    return false;
  }

  // Check viewport width (desktop: >= 768px)
  // Skip this check if user explicitly chose vim
  if (pref !== 'vim' && window.innerWidth < 768) {
    return false;
  }

  // Check WebAssembly support (for future wasi-vim integration)
  if (typeof WebAssembly === 'undefined') {
    return false;
  }

  // Check circuit breaker state
  const state = getVimModeState();
  if (!state.enabled) {
    return false;
  }

  // Reset failure counter after 24 hours
  if (state.failures > 0 && Date.now() - state.lastFailure > 86400000) {
    state.failures = 0;
    setVimModeState(state);
  }

  return true;
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
    // For vim, we need to request content via message
    // In vim mode, saves are triggered by :w command which sends vim:save message
    // Manual save button should also work
    content = editing.lastSaved; // Vim manages content internally
    // TODO: Implement manual save for vim mode if needed
    $("savestate").textContent = "Use :w in Vim to save";
    return;
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
  const pref = localStorage.getItem('nr.editorPref') || 'auto';
  document.querySelector(`input[name="editor-mode"][value="${pref}"]`).checked = true;
  
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
    localStorage.setItem('nr.editorPref', selected.value);
  }
}

function resetCircuitBreaker() {
  setVimModeState({ enabled: true, failures: 0, lastFailure: 0 });
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
