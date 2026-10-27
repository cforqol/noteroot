# Workspace UI guide

How the browser UI behaves. Everything here is per browser: preferences live
in `localStorage` and are never sent to the server.

## Sidebar

- **Folders** collapse and expand on click; the state is remembered per notebook.
  `⊞` / `⊟` expand or collapse everything.
- Each folder shows a **`[n]`** badge — the number of notes anywhere beneath
  it, visible even when collapsed. The line under the sidebar reads
  `N notes · M folders` for the whole notebook.
- **Icon and name color** are assigned by you: ⋯ (or right-click) →
  *Icon & color…*. Unset folders show 📁 / 📂 by state. *Reset* clears both.
  Decorations follow a folder when you rename or move it and are forgotten
  when you delete it.  The picker shows ~300 icons split across 10 named
  category tabs (Files, Docs, Work, Tech, Creative, Life, Fitness, Travel,
  Nature, Symbols) plus an *All* tab. A filter input above the tabs narrows
  the grid as you type.
- The folder you last clicked is underlined and is where **+ Note** and
  **+ Folder** create things; otherwise they use the open note's folder, then
  the notebook root.

## Create, rename, move, delete

| Action | How | Notes |
|---|---|---|
| New note | **+ Note**, or ⋯ → *New note here* | `ideas/todo` creates `ideas/todo.md` and any missing folders; opens in the editor. Fails if the note exists. |
| New folder | **+ Folder**, or ⋯ → *New folder here* | Empty folders are real and show in the sidebar. |
| Rename / move | ⋯ → *Rename / move…* | Type the new full path; slashes move between folders. Folders move with all contents; note ids (and so wikilinks) are preserved. |
| Delete | ⋯ → *Delete…* | Notes go to the trash. A folder's notes are trashed individually and the folder is removed. |
| Restore | 🗑 (trash panel) → *Restore* | Puts the note back at its original path, recreating folders as needed. The panel groups entries by their original folder, so a deleted folder shows one header with **Restore folder** / **Delete folder** plus its notes beneath. |
| Retention | ⋯ → *Retention…* | Opens a Settings panel where you can choose how long trashed notes are kept: **Never** (retain forever), a fixed number of **days** (default 30), or **After 3650 days** (~10 years). Trashed notes matching their expiry are automatically removed. The trash panel shows each entry's expiry date and offers **Restore** and **Delete** (permanently) buttons. |

**Drag-and-drop a note to a folder**: grab any note row in the sidebar and drop it onto any folder row to move it there. The note keeps its filename; if a file with the same name already exists in the target folder the server returns an error and the tree is unchanged.

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

## Attachments

The editor toolbar has a **📎 Attach** menu (it only exists while editing and
works in both the plain and Vim editors):

- **Upload file…** — picks one or more files, uploads them and inserts the
  embed at the cursor (images become `![name](…)`, everything else
  `[name](…)`). Drag-and-drop and clipboard paste keep working as before.
- **Insert link (URL)…** — a validated link: `http`, `https`, `mailto` or a
  relative path (`/n/…`, `../x.md`, `#anchor`). `javascript:`/`data:` URLs are
  rejected. The dialog previews the exact markdown as you type; spaces and
  parens in the target are percent-encoded, brackets in the label escaped.
- **Link to note…** — a picker over every note in the notebook. Type to
  filter; a full path, a path without `.md`, or a unique file name anywhere in
  the sidebar all resolve (several notes sharing a name do not). Inserts a
  wikilink `[[path]]` — or `[[path|label]]` when you give the link a label —
  so it resolves through the notebook's index like any other wiki link.

**Playback**: links to uploaded video and audio render as HTML players
(`<video>`/`<audio>` with controls, plus a poster frame for video) instead of
plain links — the server adds them after sanitizing the note. Video is fixed
at 640×360 and shrinks with the reading column. Seeking works: resource
serving honours HTTP byte ranges (`Range` → `206`). Images already render as
resized thumbnails; links to other file types stay download links.

## Task-list checkboxes

Task-list items in a note (`- [ ] …` / `- [x] …`) render as real interactive
checkboxes in read mode. Clicking one immediately:

1. Fetches the note's raw markdown.
2. Toggles the nth `[ ]` / `[x]` pattern (positional — first checkbox is index 0).
3. Saves the result back with a conflict-safe `If-Match` header.

No editor required. The change appears in git history with your user identity.
If the save fails the checkbox reverts and a toast appears.

## Editor mode switch

A **Use Vim** / **Use plain text** button appears in the reading toolbar whenever
a note is open for editing. It swaps the live buffer between Vim (vim.wasm) and
the plain textarea without saving or losing content. If Vim is not yet enabled
in Settings → Editor, the button is disabled with a tooltip pointing there.

## Plugins

Settings → **Plugins** has two sections:

### Built-in UI plugins

Toggled per notebook (state is browser-local). Takes effect immediately.

| Plugin | What it does |
|---|---|
| **HTML renderer** | Replaces ` ```html ` fenced blocks in read mode with sandboxed `<iframe>` previews. A "Show source / Show rendered" toggle appears below each frame. The iframe uses `sandbox="allow-scripts"` — no parent-page access. |
| **Calendar widget** | Adds a 📅 button to the top bar that opens a floating draggable monthly calendar. Navigate months with ‹/›; *Today* jumps back. Drag by the title bar. |
| **Pomodoro timer** | Adds a 🍅 button to the top bar that opens a floating timer in three rows: the phase buttons *Work / Short / Long* across the top, the countdown (SVG ring) in the middle, *Start / Reset* underneath (Start toggles Pause/Resume). Phases default to 25 / 5 / 15 min and completed pomodoros are counted for the session. Drag by the title bar. The ⚙ button opens the settings panel with the phase lengths (whole minutes, 1–180 per phase) and the notifications: **Sound** (off by default) and **Flash widget** (on by default — blinks the widget window for ~3 s when a phase ends). Everything is saved to your plugin preferences the moment you change it (see below). Changing the length of the phase on screen re-arms the countdown if it has not started, or re-scales it (never longer than the new length) if it is already running. Hiding the widget never stops the countdown: the 🍅 button then carries a running indicator (pulsing accent dot, hover text *running*). If a phase ends while the widget is hidden and flashing is enabled, a small **⏰ Time is up** dialog appears instead (OK, or Esc, dismisses it); with flashing off you only get the beep. |

### Lua plugins

Lists every Lua plugin found in `.notebook/plugins/` for the current notebook,
with a loaded / error / disabled badge. **Reload Lua plugins** rescans the
directory on-the-fly — no server restart needed after editing a plugin.

## Startpage

Settings → *Startpage* names any note in the current notebook to act as its
start page. It opens automatically:

- on startup,
- after switching notebooks, and
- whenever you press **Esc** to leave a note.

so its wikilinks work as a dashboard. The section offers **Set to current
note** (adopts whatever you are reading), a free-text path field
(`Home.md`, `dash/start.md`; it reads `_blank` while unset), and **Clear** to return to the usual
"Nothing open" screen. Esc while already on the startpage does nothing.
A path whose file no longer exists falls back to the empty state. Stored
per notebook in the browser (`nr.homepage`).

## Appearance and layout

- **Theme**: the ◐/☀/☾ button cycles *auto → light → dark*; settings has the
  same choice. *Auto* follows the OS (or a theme plugin); an explicit choice
  overrides both.
- **Sidebar placement** (settings): left, right, or hidden. ☰ hides the sidebar and
  brings it back on the side it was on.
- **Phones (≤ 720 px)**: the sidebar is a slide-in drawer (from the side chosen in
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
| `nr.homepage` | `{notebook: note path}` — the start page, if any |
| `nr.editorPref`, `nr.vimMode` | editor preference and the Vim circuit breaker |
| `noteroot_uiplugins_<slug>` | per-notebook built-in UI plugin enabled state (`{html-renderer: true, …}`) |
| `noteroot_uipluginprefs` | per-user built-in UI plugin preferences, e.g. `{"pomodoro":{"work":50,"short":10,"long":15,"sound":true,"flash":false}}` — not tied to a notebook, so a duration or notification you set applies everywhere and survives a restart |
| `nr.trashRetention` | **server-side**: notebook‑level auto‑delete after N days (default 30) or never; stored in `notebook.toml`, not in localStorage |

Settings → *Data* can **Export preferences** (a dated `.json` snapshot of the
keys above plus the editor/status/font choices) and **Import preferences…**
back into any browser. Only allowlisted preference keys are ever written:
drafts (`nr.pad.*`), sessionStorage and unknown keys in the file are counted
as skipped and ignored. **Reset preferences** still only clears the listed
keys in the current browser.

## Server storage

Settings → *Server* reports, for the open notebook:

| Row | Meaning |
|---|---|
| Backend | `local` filesystem or `s3` |
| Used | bytes of committed uploads (attachments — note text is not counted) |
| In-flight uploads | bytes reserved by browser uploads still in progress (row only appears when non-zero) |
| Quota | `used of quota`, or *unlimited* |
| Pending uploads | number of in-flight journal entries |

The quota is the per-notebook attachment cap from `NOTEROOT_QUOTA_BYTES`
(env, `0` = unlimited, restart to apply). When full, uploads fail with HTTP
507. Trash retention is also a server setting: once set, the server sweeps
expired trashed notes automatically when it starts, whenever the setting
changes, and at least every `NOTEROOT_GC_INTERVAL` (default 24h). **Purge
expired** applies the schedule right now — it does not replace it — while
**Empty trash…** deletes everything currently trashed.

The build version shows beside *noteroot* in the top bar and in
Settings → *About*, read once from `GET /healthz` (`version` field;
`noteroot --version` prints the same value on the command line).

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
| `POST /n/{slug}/trash/restore-folder` | `?path=` or `?all=1` | 200 `{"restored":[...],"skipped":[{name,origPath,error}]}`; restores every trashed note from a folder, or the whole trash |
| `DELETE /n/{slug}/trash/folder?path=` | — | 200 `{"purged":M}`; permanently deletes every trashed note from one folder |
| `POST /api/v1/notebooks` | `{"slug","name"}` | 201 `{"slug","name","profile"}`; 400 bad slug, 409 slug taken; creates dirs + manifest and git-inits the vault |
| `PATCH /api/v1/notebooks/{slug}` | `{"slug"}` | 200 `{"slug","name","profile"}`; renames directory and manifest; 400/404/409 as above |
| `DELETE /api/v1/notebooks/{slug}` | — | 200 `{"deleted","archived"}`; archives to `notebooks/.archive/<slug>-<unixts>` (recoverable by hand) |

Notebook CRUD also lives in the notebook selector as pseudo-options (**＋ New notebook…**, **✎ Rename notebook…**, **🗑 Delete notebook…**); there is no separate topbar button.

## Future work / known gaps

- **Delete confirmation dialog**: The delete operation lacks a warning/confirm dialog showing the number of folders and notes that will be affected. This would improve safety for bulk operations.
- **Vim viewport offset**: The Vim edit window may need a slight bottom offset so the top few lines are not cut off on certain viewports. This requires CSS adjustments to `#vim-container` or the Vim drawing logic.
- **Modified note re-edit**: When a note is modified and the user navigates to another note and back, the note cannot be edited again until the page is refreshed. This is a state management issue that needs fixing.
- **Folder icon decorations server-side**: Icon/color decorations are currently browser-local (`localStorage`). Future work would sync them through the server so the same decoration appears across browsers.
- **Calendar reminders**: The calendar widget is display-only. Integration with a reminder or alarm system (e.g. a Lua hook that fires before an event) is deferred.
- **Pomodoro session count**: Phase lengths are saved in your plugin preferences and survive a restart; the session completed count still resets on page reload. A server-side or localStorage-backed counter would preserve it across sessions.
