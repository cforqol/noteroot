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
    $("note").scrollTop = 0;
    $("editbtn").hidden = false;
  } catch (e) {
    $("note").innerHTML = `<p class="muted">${esc(e.message)}</p>`;
    $("editbtn").hidden = true;
  }
}

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

// --- editor (phone mode + fallback): textarea, autosave, crash pad, locks ---

let editing = null; // {path, baseSha, lastSaved, dirtyTimer, saveTimer, lockTimer}
let tabId = null;

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

function writePad() {
  if (!editing) return;
  try {
    localStorage.setItem(padKey(editing.path), JSON.stringify({
      tab: myTab(),
      baseSha: editing.baseSha,
      content: $("editbody").value,
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
  editing = { path, baseSha: lastNote.sha, lastSaved: server, dirtyTimer: 0, saveTimer: 0, lockTimer: 0 };
  $("note").hidden = true;
  $("editor").hidden = false;
  $("editbody").value = server;
  $("savestate").textContent = "";
  $("lockstate").textContent = "";
  setBanner("");

  const pad = readPad(path);
  if (pad && typeof pad.content === "string" && pad.content !== server) {
    const when = pad.savedAt ? new Date(pad.savedAt).toLocaleString() : "unknown time";
    setBanner(`<div class="warn">Unsaved changes from ${esc(when)}.` +
      ` <button class="btn" id="padrestore">Restore</button>` +
      ` <button class="btn" id="paddiscard">Discard</button></div>`);
    $("padrestore").onclick = () => { $("editbody").value = pad.content; setBanner(""); markDirty(); };
    $("paddiscard").onclick = () => { clearPad(path); setBanner(""); };
  }

  $("editbody").oninput = markDirty;
  takeLock(path);
  editing.lockTimer = setInterval(() => takeLock(path), 60000);
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
  const content = $("editbody").value;
  if (content === editing.lastSaved) {
    $("savestate").textContent = manual ? "No changes." : "";
    return;
  }
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
      writePad();
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
      $("editbody").value = server;
      clearPad(editing.path);
      setBanner("");
      $("savestate").textContent = "";
    } catch (e) {
      $("savestate").textContent = `Reload failed: ${e.message}`;
    }
  };
  $("cfforce").onclick = async () => {
    // Read the live textarea: the user may have kept typing since the
    // conflict was detected — force-save must not resurrect a stale snapshot.
    const live = $("editbody").value;
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
  editing = null;
  $("editor").hidden = true;
  $("note").hidden = false;
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
