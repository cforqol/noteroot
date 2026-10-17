# Workspace UI guide

How the browser UI behaves. Everything here is per browser: preferences live
in `localStorage` and are never sent to the server.

## Tree

- **Folders** collapse and expand on click; the state is remembered per notebook.
  `⊞` / `⊟` expand or collapse everything.
- Each folder shows a **`[n]`** badge — the number of notes anywhere beneath
  it, visible even when collapsed. The line under the tree reads
  `N notes · M folders` for the whole notebook.
- **Icon and name color** are assigned by you: ⋯ (or right-click) →
  *Icon & color…*. Unset folders show 📁 / 📂 by state. *Reset* clears both.
  Decorations follow a folder when you rename or move it and are forgotten
  when you delete it.
- The folder you last clicked is underlined and is where **+ Note** and
  **+ Folder** create things; otherwise they use the open note's folder, then
  the notebook root.

## Create, rename, move, delete

| Action | How | Notes |
|---|---|---|
| New note | **+ Note**, or ⋯ → *New note here* | `ideas/todo` creates `ideas/todo.md` and any missing folders; opens in the editor. Fails if the note exists. |
| New folder | **+ Folder**, or ⋯ → *New folder here* | Empty folders are real and show in the tree. |
| Rename / move | ⋯ → *Rename / move…* | Type the new full path; slashes move between folders. Folders move with all contents; note ids (and so wikilinks) are preserved. |
| Delete | ⋯ → *Delete…* | Notes go to the trash. A folder's notes are trashed individually and the folder is removed. |
| Restore | 🗑 (trash panel) → *Restore* | Puts the note back at its original path, recreating folders as needed. |
| Retention | ⋯ → *Retention…* | Opens a Settings panel where you can choose how long trashed notes are kept: **Never** (retain forever), a fixed number of **days** (default 30), or **After 3650 days** (~10 years). Trashed notes matching their expiry are automatically removed. The trash panel shows each entry's expiry date and offers **Restore** and **Delete** (permanently) buttons. |

A note that is open in the editor cannot be renamed or deleted until you close
the editor. Names may not start with a dot, contain `..`, or (for folders) end
in `.md`; paths are limited to five levels.

## Status bar

| Field | Meaning |
|---|---|
| Mode | `Read`, `Plain` (textarea) or `Vim` |
| Ln:Col | cursor position (plain editor only) |
| Words / Selected | whole-note and selection word counts (frontmatter excluded) |
| Chars | characters excluding whitespace |
| Paras | blank-line separated paragraphs |
| Read | estimated reading time at 200 words/min |
| Size | UTF-8 size of the file, human readable |

vim.wasm does not expose its buffer, cursor or selection. In Vim mode the bar
shows statistics for the last synced buffer (when the editor opens and after
each `:w`/save) and no line:column.

## Appearance and layout

- **Theme**: the ◐/☀/☾ button cycles *auto → light → dark*; settings has the
  same choice. *Auto* follows the OS (or a theme plugin); an explicit choice
  overrides both.
- **Tree placement** (settings): left, right, or hidden. ☰ hides the tree and
  brings it back on the side it was on.
- **Phones (≤ 720 px)**: the tree is a slide-in drawer (from the side chosen in
  settings) opened with ☰ and closed by choosing a note or tapping outside it.
  The app opens on the note list. The status bar drops characters,
  paragraphs and reading time. Row actions (⋯) are always visible on touch.

## Stored in the browser

| Key | Content |
|---|---|
| `nr.theme` | `auto` / `light` / `dark` |
| `nr.treePlacement`, `nr.treeLast` | `left` / `right` / `hidden`, and the last visible side |
| `nr.folderDeco` | `{notebook: {folder: {emoji, color}}}` |
| `nr.treeCollapsed` | `{notebook: [collapsed folders]}` |
| `nr.editorPref`, `nr.vimMode` | editor preference and the Vim circuit breaker |
| `nr.trashRetention` | **server-side**: notebook‑level auto‑delete after N days (default 30) or never; stored in `notebook.toml`, not in localStorage |

## HTTP API (session surface)

| Route | Body / query | Result |
|---|---|---|
| `POST /n/{slug}/folder` | `{"path"}` | 201; 409 if it exists; 400 for hidden / `.md` / invalid names |
| `PATCH /n/{slug}/folder` | `{"from","to"}` | 200; 404 missing source; 409 destination exists or into itself; 400 if the source is a note |
| `DELETE /n/{slug}/folder?path=` | — | 200 `{"path","trashed":[...]}`; 404 missing; 400 if the path is a note |
| `GET /n/{slug}/trash` | — | 200 `{"entries":[...],"retentionDays":N}`; each entry has `name,origPath,actor,sha,size,deletedAt,expiresAt`; `retentionDays` 0 = never, default 30, max 3650 |
| `PUT /n/{slug}/trash/settings` | `{"retentionDays":N}` | 200 `{"retentionDays":N,"purged":M}`; shortens retention immediately, purges stale entries |
| `POST /n/{slug}/trash/purge` | `?all=1` optional | 200 `{"purged":M}`; removes entries past their expiry; `?all=1` empties the entire trash |
| `DELETE /n/{slug}/trash?name=` | — | 200 `{"deleted":name}`; removes one named entry permanently |
