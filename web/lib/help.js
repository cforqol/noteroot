/**
 * In-app guide: the quick start, topic sections and keyboard shortcuts,
 * rendered into the help overlay, plus the keyboard-shortcut mapping
 * the app uses (one source of truth for what the guide promises).
 *
 * Inline markup in guide text: **bold**, `code` and {Key} for a key cap.
 */

export const SHORTCUTS = [
  { keys: ['Ctrl/⌘', 'S'], action: 'Save now (while editing)' },
  { keys: ['E'], action: 'Edit the open note' },
  { keys: ['N'], action: 'New note' },
  { keys: ['/'], action: 'Search' },
  { keys: ['Ctrl/⌘', 'K'], action: 'Search' },
  { keys: ['?'], action: 'Open this guide' },
  { keys: ['Esc'], action: 'Close a dialog, menu or panel' },
];

export const QUICK_START = [
  'Pick a notebook from the list at the top.',
  'Create a note with **+ Note** (or press {N}). Typing `ideas/todo` puts it in a new “ideas” folder.',
  'Write in Markdown. **Edit** (or {E}) opens the editor; changes save by themselves when you pause and before you switch notes. {Ctrl/⌘}+{S} saves right away.',
  'Organize with the **⋯** button or a right-click on any note or folder: **Rename…**, **Move…**, **Delete…**.',
  'Find anything with the search box (press {/}).',
];

export const HELP_SECTIONS = [
  {
    id: 'writing',
    title: 'Writing',
    items: [
      'Notes are plain Markdown files. Link notes with `[[Note name]]`; the notes linking back are listed under each note.',
      'Drop or paste images and files into the editor to upload and embed them.',
      'Choose plain text or Vim in **Settings → Editor**. In Vim, `:w` saves and `:q` closes.',
    ],
  },
  {
    id: 'saving',
    title: 'Saving & drafts',
    items: [
      'Edits save automatically (set the delay in **Settings → Editor**) and whenever you switch notes or press **Done**.',
      'If a save fails, for example offline, your text is kept in this browser and offered back the next time you open the note.',
      'If the note changed elsewhere, you choose: reload theirs or keep yours.',
    ],
  },
  {
    id: 'frontmatter',
    title: 'Note header',
    items: [
      'Each note starts with a short header between `---` lines: `id`, `title`, `created` and `updated`.',
      'noteroot adds anything missing when you open a note, and keeps `updated` current when you save.',
      'The `id` keeps links working when notes are renamed or moved, so leave it as it is.',
    ],
  },
  {
    id: 'organizing',
    title: 'Folders & trash',
    items: [
      'Click a folder to fold it; the number beside it counts every note inside.',
      '**Rename…** changes only the name. **Move…** puts it in another folder, or a new one.',
      'Give a folder an icon and a color with **Icon & color…** (kept in this browser).',
      'Deleted notes go to the trash (🗑 above the tree) and can be restored to where they were.',
    ],
  },
  {
    id: 'data',
    title: 'Import, export & backups',
    items: [
      '**Settings → Data → Export** downloads the whole notebook as a `.tar.gz` archive.',
      '**Import** takes such an archive (you see what will change before anything does) or `.md` files, which go into a folder you pick. You can also drop files onto the tree.',
      'Download a single note as `.md` from its **⋯** menu.',
      '**Back up now** keeps a full snapshot (history and database) on the server.',
    ],
  },
  {
    id: 'layout',
    title: 'Layout & privacy',
    items: [
      'Put the tree on the left or right, or hide it with {☰}. On phones it slides in from the side.',
      'Theme, text size, reading width, the status bar and other preferences are stored in this browser only.',
    ],
  },
];

const TOKEN = /(\*\*[^*]+\*\*|`[^`]+`|\{[^}]+\})/;

/** Text with **bold**, `code` and {Key} turned into elements (never HTML). */
export function inline(text, doc = document) {
  const frag = doc.createDocumentFragment();
  for (const part of String(text).split(TOKEN)) {
    if (!part) continue;
    let node;
    if (part.startsWith('**')) {
      node = doc.createElement('strong');
      node.textContent = part.slice(2, -2);
    } else if (part.startsWith('`')) {
      node = doc.createElement('code');
      node.textContent = part.slice(1, -1);
    } else if (part.startsWith('{')) {
      node = doc.createElement('kbd');
      node.textContent = part.slice(1, -1);
    } else {
      node = doc.createTextNode(part);
    }
    frag.append(node);
  }
  return frag;
}

function section(doc, title, className = 'help-section') {
  const s = doc.createElement('section');
  s.className = className;
  const h = doc.createElement('h3');
  h.textContent = title;
  s.append(h);
  return s;
}

function list(doc, tag, items) {
  const l = doc.createElement(tag);
  for (const item of items) {
    const li = doc.createElement('li');
    li.append(inline(item, doc));
    l.append(li);
  }
  return l;
}

/** The shortcuts table. */
export function shortcutsTable(doc = document) {
  const table = doc.createElement('table');
  table.className = 'shortcuts';
  for (const s of SHORTCUTS) {
    const tr = doc.createElement('tr');
    const keys = doc.createElement('td');
    s.keys.forEach((k, i) => {
      if (i) keys.append(doc.createTextNode(' + '));
      const kbd = doc.createElement('kbd');
      kbd.textContent = k;
      keys.append(kbd);
    });
    const action = doc.createElement('td');
    action.textContent = s.action;
    tr.append(keys, action);
    table.append(tr);
  }
  return table;
}

/**
 * Render the guide. The welcome variant (first visit) is the quick start
 * with a short introduction; the full guide adds every topic.
 */
export function renderHelp(container, { welcome = false, doc = document } = {}) {
  const parts = [];
  if (welcome) {
    const intro = doc.createElement('p');
    intro.className = 'help-intro';
    intro.append(inline('noteroot keeps your notes as plain Markdown files on your own server. Five steps to get going; reopen this guide any time with the **?** button or {?}.', doc));
    parts.push(intro);
  }
  const start = section(doc, 'Quick start', 'help-section help-start');
  start.append(list(doc, 'ol', QUICK_START));
  parts.push(start);
  if (!welcome) {
    const grid = doc.createElement('div');
    grid.className = 'help-grid';
    for (const s of HELP_SECTIONS) {
      const sec = section(doc, s.title);
      sec.id = `help-${s.id}`;
      sec.append(list(doc, 'ul', s.items));
      grid.append(sec);
    }
    parts.push(grid);
  }
  const keys = section(doc, 'Keyboard shortcuts', 'help-section help-keys');
  keys.append(shortcutsTable(doc));
  parts.push(keys);
  container.replaceChildren(...parts);
}

/** True when keystrokes belong to a text field (or Vim's input). */
export function isTypingTarget(target) {
  if (!target) return false;
  const tag = target.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || Boolean(target.isContentEditable);
}

/**
 * Map a keydown to an app action: 'save' | 'search' | 'help' | 'edit' |
 * 'new-note', or null. Single-key shortcuts never fire while typing.
 */
export function shortcutFor(e, { typing = false } = {}) {
  const key = String(e.key || '');
  const lower = key.toLowerCase();
  const mod = e.ctrlKey || e.metaKey;
  if (mod && !e.altKey && !e.shiftKey && lower === 's') return 'save';
  if (mod && !e.altKey && !e.shiftKey && lower === 'k') return 'search';
  if (typing || mod || e.altKey) return null;
  if (key === '?') return 'help';
  if (key === '/') return 'search';
  if (lower === 'e') return 'edit';
  if (lower === 'n') return 'new-note';
  return null;
}
