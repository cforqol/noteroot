/**
 * Per-browser UI preferences (localStorage): theme, tree placement,
 * user-assigned folder icons/colors, collapsed-folder state, the general
 * settings registry, status-bar fields, first-run help and drafts.
 *
 * Storage is injectable and every access is guarded: quota errors,
 * privacy mode and corrupt JSON degrade to defaults, never throw.
 */

export const THEMES = ['auto', 'light', 'dark'];
export const PLACEMENTS = ['left', 'right', 'hidden'];

const K_THEME = 'nr.theme';
const K_PLACE = 'nr.treePlacement';
const K_LAST = 'nr.treeLast';
const K_DECO = 'nr.folderDeco';
const K_COLLAPSED = 'nr.treeCollapsed';

function defaultStorage() {
  try {
    return globalThis.localStorage;
  } catch {
    return null;
  }
}

function readJSON(key, storage) {
  try {
    const raw = storage?.getItem(key);
    const v = raw ? JSON.parse(raw) : null;
    return v && typeof v === 'object' && !Array.isArray(v) ? v : {};
  } catch {
    return {};
  }
}

function writeRaw(key, value, storage) {
  try {
    storage?.setItem(key, value);
  } catch {
    /* ignore: preferences are best-effort */
  }
}

function writeJSON(key, value, storage) {
  writeRaw(key, JSON.stringify(value), storage);
}

function readChoice(key, allowed, fallback, storage) {
  try {
    const v = storage?.getItem(key);
    return allowed.includes(v) ? v : fallback;
  } catch {
    return fallback;
  }
}

// --- theme -------------------------------------------------------------

export function getTheme(storage = defaultStorage()) {
  return readChoice(K_THEME, THEMES, 'auto', storage);
}

/** Persist a theme; unknown values fall back to "auto". Returns it. */
export function setTheme(theme, storage = defaultStorage()) {
  const t = THEMES.includes(theme) ? theme : 'auto';
  writeRaw(K_THEME, t, storage);
  return t;
}

/** auto -> light -> dark -> auto */
export function nextTheme(theme) {
  return THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length] ?? 'auto';
}

/**
 * Apply an explicit light/dark choice to the document root. "auto"
 * removes the override so the OS preference (or a theme plugin) rules.
 */
export function applyTheme(theme, doc = document) {
  const root = doc.documentElement;
  if (theme === 'light' || theme === 'dark') {
    root.dataset.theme = theme;
  } else {
    delete root.dataset.theme;
  }
  root.dataset.userTheme = theme;
  return theme;
}

// --- tree placement ----------------------------------------------------

export function getPlacement(storage = defaultStorage()) {
  return readChoice(K_PLACE, PLACEMENTS, 'left', storage);
}

export function setPlacement(placement, storage = defaultStorage()) {
  const p = PLACEMENTS.includes(placement) ? placement : 'left';
  writeRaw(K_PLACE, p, storage);
  if (p !== 'hidden') writeRaw(K_LAST, p, storage);
  return p;
}

/** Hide the tree, or bring it back where it last was. Returns the new placement. */
export function togglePlacement(storage = defaultStorage()) {
  if (getPlacement(storage) !== 'hidden') return setPlacement('hidden', storage);
  return setPlacement(readChoice(K_LAST, ['left', 'right'], 'left', storage), storage);
}

export function applyPlacement(placement, doc = document) {
  doc.body.dataset.tree = PLACEMENTS.includes(placement) ? placement : 'left';
}

// --- folder decorations ------------------------------------------------

const COLOR = /^#[0-9a-fA-F]{6}$/;
const MAX_EMOJI = 16;

function cleanDeco({ emoji = '', color = '' } = {}) {
  return {
    emoji: String(emoji).trim().slice(0, MAX_EMOJI),
    color: COLOR.test(color) ? color.toLowerCase() : '',
  };
}

/** {folderPath: {emoji, color}} for one notebook. */
export function getDecorations(slug, storage = defaultStorage()) {
  const all = readJSON(K_DECO, storage);
  return all[slug] && typeof all[slug] === 'object' ? all[slug] : {};
}

/** Set (or, when both parts are empty, clear) a folder's icon and color. */
export function setDecoration(slug, folder, deco, storage = defaultStorage()) {
  const all = readJSON(K_DECO, storage);
  const mine = { ...(all[slug] || {}) };
  const d = cleanDeco(deco);
  if (d.emoji || d.color) mine[folder] = d;
  else delete mine[folder];
  all[slug] = mine;
  writeJSON(K_DECO, all, storage);
  return d;
}

/** Re-key `map` so entries at or under `from` move under `to`. */
function rekey(map, from, to) {
  const out = {};
  for (const [k, v] of Object.entries(map)) {
    if (k === from) out[to] = v;
    else if (k.startsWith(`${from}/`)) out[to + k.slice(from.length)] = v;
    else out[k] = v;
  }
  return out;
}

function drop(map, folder) {
  const out = {};
  for (const [k, v] of Object.entries(map)) {
    if (k !== folder && !k.startsWith(`${folder}/`)) out[k] = v;
  }
  return out;
}

/** A folder was renamed or moved: carry its decorations (and children's) along. */
export function moveFolderPrefs(slug, from, to, storage = defaultStorage()) {
  const deco = readJSON(K_DECO, storage);
  if (deco[slug]) deco[slug] = rekey(deco[slug], from, to);
  writeJSON(K_DECO, deco, storage);
  const col = readJSON(K_COLLAPSED, storage);
  if (Array.isArray(col[slug])) {
    col[slug] = Object.keys(rekey(Object.fromEntries(col[slug].map((p) => [p, 1])), from, to));
  }
  writeJSON(K_COLLAPSED, col, storage);
}

/** A folder was deleted: forget its decorations and collapsed state. */
export function removeFolderPrefs(slug, folder, storage = defaultStorage()) {
  const deco = readJSON(K_DECO, storage);
  if (deco[slug]) deco[slug] = drop(deco[slug], folder);
  writeJSON(K_DECO, deco, storage);
  const col = readJSON(K_COLLAPSED, storage);
  if (Array.isArray(col[slug])) {
    col[slug] = col[slug].filter((p) => p !== folder && !p.startsWith(`${folder}/`));
  }
  writeJSON(K_COLLAPSED, col, storage);
}

// --- collapsed folders -------------------------------------------------

/** Set of collapsed folder paths for a notebook (default: all expanded). */
export function getCollapsed(slug, storage = defaultStorage()) {
  const list = readJSON(K_COLLAPSED, storage)[slug];
  return new Set(Array.isArray(list) ? list.filter((p) => typeof p === 'string') : []);
}

export function setCollapsed(slug, folder, collapsed, storage = defaultStorage()) {
  const all = readJSON(K_COLLAPSED, storage);
  const set = new Set(Array.isArray(all[slug]) ? all[slug] : []);
  if (collapsed) set.add(folder);
  else set.delete(folder);
  all[slug] = [...set];
  writeJSON(K_COLLAPSED, all, storage);
  return set;
}

/** Replace the collapsed set wholesale (expand all / collapse all). */
export function replaceCollapsed(slug, folders, storage = defaultStorage()) {
  const all = readJSON(K_COLLAPSED, storage);
  all[slug] = [...folders];
  writeJSON(K_COLLAPSED, all, storage);
  return new Set(all[slug]);
}

// --- homepage ----------------------------------------------------------

const K_HOMEPAGE = 'nr.homepage';

/**
 * The note path to open when nothing else is (boot, notebook switch,
 * closing a note). Per notebook; empty string means "no homepage — show
 * the empty state".
 */
export function getHomepage(slug, storage = defaultStorage()) {
  const all = readJSON(K_HOMEPAGE, storage);
  const p = all[slug];
  return typeof p === 'string' ? p : '';
}

/** Set (or, with an empty path, clear) a notebook's homepage. */
export function setHomepage(slug, path, storage = defaultStorage()) {
  const all = readJSON(K_HOMEPAGE, storage);
  const clean = String(path || '').trim();
  if (clean) all[slug] = clean;
  else delete all[slug];
  writeJSON(K_HOMEPAGE, all, storage);
  return clean;
}

// --- general settings --------------------------------------------------

/**
 * Settings registry: name -> storage key, allowed values, default.
 * Values are strings (switches use "on"/"off") so the settings form can
 * bind any control generically through `data-setting` attributes.
 */
export const SETTINGS = {
  fontSize: { key: 'nr.fontSize', choices: ['small', 'medium', 'large'], fallback: 'medium' },
  readingWidth: { key: 'nr.readingWidth', choices: ['narrow', 'normal', 'wide'], fallback: 'normal' },
  autosave: { key: 'nr.autosave', choices: ['1000', '2000', '5000', '10000', '0'], fallback: '2000' },
  tabKey: { key: 'nr.tabKey', choices: ['focus', '2', '4', 'tab'], fallback: '4' },
  spellcheck: { key: 'nr.spellcheck', choices: ['on', 'off'], fallback: 'off' },
  wrap: { key: 'nr.wrap', choices: ['on', 'off'], fallback: 'on' },
  treeCounts: { key: 'nr.treeCounts', choices: ['on', 'off'], fallback: 'on' },
  statusBar: { key: 'nr.statusBar', choices: ['on', 'off'], fallback: 'on' },
  readingSpeed: { key: 'nr.readingSpeed', choices: ['150', '200', '250', '300'], fallback: '200' },
  splitEditSide: { key: 'nr.splitEditSide', choices: ['left', 'right'], fallback: 'left' },
  linkTarget: { key: 'nr.linkTarget', choices: ['new-tab', 'same-tab'], fallback: 'new-tab' },
  readPane: { key: 'nr.readPane', choices: ['on', 'off'], fallback: 'on' },
  readPaneSide: { key: 'nr.readPaneSide', choices: ['right', 'left'], fallback: 'right' },
  dailyNotesFolder: { key: 'nr.dailyNotesFolder', choices: [], fallback: 'daily' },
  dailyNotesFormat: { key: 'nr.dailyNotesFormat', choices: ['YYYY-MM-DD', 'YYYY/MM/DD', 'DD-MM-YYYY'], fallback: 'YYYY-MM-DD' },
  dailyNotesTemplate: { key: 'nr.dailyNotesTemplate', choices: [], fallback: '' },
};

/** Current value of a registered setting (its default when unset or invalid). */
export function getSetting(name, storage = defaultStorage()) {
  const s = SETTINGS[name];
  return readChoice(s.key, s.choices, s.fallback, storage);
}

/** Persist a registered setting; invalid values store the default. Returns it. */
export function setSetting(name, value, storage = defaultStorage()) {
  const s = SETTINGS[name];
  const v = s.choices.includes(String(value)) ? String(value) : s.fallback;
  writeRaw(s.key, v, storage);
  return v;
}

/** {name: value} for every registered setting. */
export function getSettings(storage = defaultStorage()) {
  return Object.fromEntries(Object.keys(SETTINGS).map((n) => [n, getSetting(n, storage)]));
}

// --- status-bar fields -------------------------------------------------

export const STATUS_FIELDS = ['position', 'words', 'selected', 'chars', 'paragraphs', 'reading', 'size'];
const K_STATUS = 'nr.statusFields';

/** {field: shown} for every status-bar field (all shown by default). */
export function getStatusFields(storage = defaultStorage()) {
  const saved = readJSON(K_STATUS, storage);
  return Object.fromEntries(STATUS_FIELDS.map((f) => [f, saved[f] !== false]));
}

export function setStatusField(field, shown, storage = defaultStorage()) {
  if (!STATUS_FIELDS.includes(field)) return getStatusFields(storage);
  const saved = readJSON(K_STATUS, storage);
  saved[field] = Boolean(shown);
  writeJSON(K_STATUS, saved, storage);
  return getStatusFields(storage);
}

// --- first-run help ----------------------------------------------------

const K_HELP = 'nr.helpSeen';

export function isHelpSeen(storage = defaultStorage()) {
  return readChoice(K_HELP, ['1'], '', storage) === '1';
}

export function markHelpSeen(storage = defaultStorage()) {
  writeRaw(K_HELP, '1', storage);
}

// --- reset and drafts --------------------------------------------------

function removeKey(key, storage) {
  try {
    storage?.removeItem(key);
  } catch {
    /* ignore */
  }
}

function storageKeys(storage) {
  try {
    const out = [];
    for (let i = 0; i < storage.length; i += 1) out.push(storage.key(i));
    return out;
  } catch {
    return [];
  }
}

/**
 * Forget appearance, editor, tree and status-bar preferences. Folder
 * icons, collapsed folders, drafts and the help flag are kept.
 */
export function resetPreferences(storage = defaultStorage()) {
  const keys = [K_THEME, K_PLACE, K_LAST, K_STATUS, 'nr.editorPref', ...Object.values(SETTINGS).map((s) => s.key)];
  for (const k of keys) removeKey(k, storage);
}

const PAD_PREFIX = 'nr.pad.';

/** Unsaved editor drafts kept in this browser: [{key, slug, path, savedAt}]. */
export function listDrafts(storage = defaultStorage()) {
  const out = [];
  for (const key of storageKeys(storage)) {
    if (!key?.startsWith(PAD_PREFIX)) continue;
    const rest = key.slice(PAD_PREFIX.length);
    const dot = rest.indexOf('.');
    let savedAt = 0;
    try {
      savedAt = Number(JSON.parse(storage.getItem(key))?.savedAt) || 0;
    } catch {
      /* corrupt draft: still listed so it can be cleared */
    }
    out.push(dot < 0
      ? { key, slug: rest, path: '', savedAt }
      : { key, slug: rest.slice(0, dot), path: rest.slice(dot + 1), savedAt });
  }
  return out;
}

/** Delete every draft; returns how many there were. */
export function clearDrafts(storage = defaultStorage()) {
  const drafts = listDrafts(storage);
  for (const d of drafts) removeKey(d.key, storage);
  return drafts.length;
}

// --- bookmarks (pinned notes) ------------------------------------------

const K_TAGS_COLLAPSED = 'nr.tagsCollapsed';

/** Whether the sidebar Tags section is folded away (open by default). */
export function getTagsCollapsed(storage = defaultStorage()) {
  try {
    return storage?.getItem(K_TAGS_COLLAPSED) === '1';
  } catch {
    return false;
  }
}

export function setTagsCollapsed(collapsed, storage = defaultStorage()) {
  writeRaw(K_TAGS_COLLAPSED, collapsed ? '1' : '0', storage);
  return Boolean(collapsed);
}

const K_VIMRC = 'nr.vimrc';

/** The user's vimrc text (plain text, empty = none). */
export function getVimrc(storage = defaultStorage()) {
  try {
    return storage?.getItem(K_VIMRC) ?? '';
  } catch {
    return '';
  }
}

/** Save the vimrc; blank text removes it. Returns what is stored. */
export function setVimrc(text, storage = defaultStorage()) {
  const t = String(text ?? '');
  try {
    if (t.trim() === '') storage?.removeItem(K_VIMRC);
    else storage?.setItem(K_VIMRC, t);
  } catch {
    /* preferences are best-effort */
  }
  return t.trim() === '' ? '' : t;
}

// --- keybindings -------------------------------------------------------

const K_KEYBINDINGS = 'nr.keybindings';

/**
 * Default keybinding for every action.
 *
 * Each entry is {key, ctrl?, shift?, alt?}. `key` is the KeyboardEvent.key
 * string (case-insensitive for letters). ctrl/shift/alt default to false.
 * `null` means the action is unbound by default (can still be bound by user).
 */
export const DEFAULT_KEYBINDINGS = {
  'save':          { key: 's', ctrl: true },
  'search':        { key: '/' },
  'search-mod':    { key: 'k', ctrl: true },
  'help':          { key: '?' },
  'edit':          { key: 'e' },
  'new-note':      { key: 'n' },
  'next-note':     { key: 'j' },
  'prev-note':     { key: 'k' },
  'close-note':    { key: 'Escape' },
  'toggle-split':  { key: 'p', ctrl: true },
  'new-folder':    { key: 'n', ctrl: true, shift: true },
  'trash':         { key: 't', ctrl: true, shift: true },
  'settings':      { key: ',', ctrl: true },
  'focus-sidebar': { key: 'b', ctrl: true },
  'toggle-sidebar':{ key: '\\' },
};

/** Human-readable label for every action (shown in the keybindings table). */
export const KEYBINDING_LABELS = {
  'save':           'Save note',
  'search':         'Focus search',
  'search-mod':     'Focus search (⌘/Ctrl)',
  'help':           'Open guide',
  'edit':           'Edit open note',
  'new-note':       'New note',
  'next-note':      'Next note (reader)',
  'prev-note':      'Previous note (reader)',
  'close-note':     'Close / back',
  'toggle-split':   'Toggle split pane',
  'new-folder':     'New folder',
  'trash':          'Open trash',
  'settings':       'Open settings',
  'focus-sidebar':  'Focus sidebar',
  'toggle-sidebar': 'Toggle sidebar',
};

/**
 * Keybinding descriptor: always {key, ctrl, shift, alt} with boolean mods.
 * `null` (explicit unbind) passes through; a malformed value returns
 * `undefined` so callers can reject it without persisting.
 * `key` matches KeyboardEvent.key; letters compare case-insensitively.
 */
function parseBinding(v) {
  if (typeof v !== 'object' || Array.isArray(v)) return undefined;
  if (!v.key || typeof v.key !== 'string') return undefined;
  return {
    key: v.key,
    ctrl: Boolean(v.ctrl),
    shift: Boolean(v.shift),
    alt: Boolean(v.alt),
  };
}

function normalizeBinding(v) {
  return {
    key: String(v.key),
    ctrl: Boolean(v.ctrl),
    shift: Boolean(v.shift),
    alt: Boolean(v.alt),
  };
}

/**
 * Return the full keybinding map: defaults merged with user overrides.
 * Values are {key, ctrl, shift, alt} or null (unbound).
 */
export function getKeybindings(storage = defaultStorage()) {
  const raw = readJSON(K_KEYBINDINGS, storage);
  const out = {};
  for (const action of Object.keys(DEFAULT_KEYBINDINGS)) {
    if (Object.prototype.hasOwnProperty.call(raw, action)) {
      const parsed = raw[action] === null ? null : parseBinding(raw[action]);
      out[action] = parsed === undefined ? normalizeBinding(DEFAULT_KEYBINDINGS[action]) : parsed;
    } else {
      out[action] = normalizeBinding(DEFAULT_KEYBINDINGS[action]);
    }
  }
  return out;
}

/**
 * Persist one action's binding. Pass `null` to unbind. A malformed binding
 * is rejected (returns null, nothing stored). Returns the binding stored.
 */
export function setKeybinding(action, binding, storage = defaultStorage()) {
  if (!Object.prototype.hasOwnProperty.call(DEFAULT_KEYBINDINGS, action)) return null;
  const parsed = binding === null ? null : parseBinding(binding);
  if (parsed === undefined) return null;
  const all = readJSON(K_KEYBINDINGS, storage);
  all[action] = parsed;
  writeJSON(K_KEYBINDINGS, all, storage);
  return parsed;
}

/** Delete overrides for all or one action; pass no action to reset everything. */
export function resetKeybindings(action, storage = defaultStorage()) {
  if (action !== undefined) {
    const all = readJSON(K_KEYBINDINGS, storage);
    delete all[action];
    writeJSON(K_KEYBINDINGS, all, storage);
  } else {
    try { storage?.removeItem(K_KEYBINDINGS); } catch { /* best-effort */ }
  }
}

/**
 * Match a KeyboardEvent against a binding descriptor.
 * Returns true when every field matches.
 */
export function matchesBinding(e, binding) {
  if (!binding) return false;
  const eventKey = e.key || '';
  const bindKey = binding.key;
  const keyMatch = bindKey === 'Escape'
    ? eventKey === 'Escape'
    : eventKey.toLowerCase() === bindKey.toLowerCase();
  return (
    keyMatch &&
    Boolean(e.ctrlKey || e.metaKey) === Boolean(binding.ctrl) &&
    Boolean(e.shiftKey) === Boolean(binding.shift) &&
    Boolean(e.altKey) === Boolean(binding.alt)
  );
}

// --- templates -----------------------------------------------------------

const K_TEMPLATES = 'nr.templates';

/**
 * Templates are stored per notebook as {name: {content, variables?, isDefault?}}.
 * Variables are custom key/value pairs for {{variable}} substitution.
 */
export function getTemplates(slug, storage = defaultStorage()) {
  const all = readJSON(K_TEMPLATES, storage);
  return all[slug] && typeof all[slug] === 'object' ? all[slug] : {};
}

/** Get a single template by name. Returns {content, variables?, isDefault?} or null. */
export function getTemplate(slug, name, storage = defaultStorage()) {
  const templates = getTemplates(slug, storage);
  return templates[name] || null;
}

/** Save or update a template. Returns the stored template object. */
export function saveTemplate(slug, name, { content = '', variables = {}, isDefault = false } = {}, storage = defaultStorage()) {
  const all = readJSON(K_TEMPLATES, storage);
  const mine = { ...(all[slug] || {}) };
  const template = { content: String(content), variables: variables || {}, isDefault: Boolean(isDefault) };
  mine[name] = template;
  all[slug] = mine;
  writeJSON(K_TEMPLATES, all, storage);
  return template;
}

/** Delete a template by name. Returns true if it existed. */
export function deleteTemplate(slug, name, storage = defaultStorage()) {
  const all = readJSON(K_TEMPLATES, storage);
  if (!all[slug] || !all[slug][name]) return false;
  delete all[slug][name];
  writeJSON(K_TEMPLATES, all, storage);
  return true;
}

/** Get the default template for a notebook (if any). */
export function getDefaultTemplate(slug, storage = defaultStorage()) {
  const templates = getTemplates(slug, storage);
  for (const [name, t] of Object.entries(templates)) {
    if (t.isDefault) return { name, ...t };
  }
  return null;
}

/** Set a template as the default (clears previous default). */
export function setDefaultTemplate(slug, name, storage = defaultStorage()) {
  const all = readJSON(K_TEMPLATES, storage);
  const mine = all[slug] || {};
  for (const t of Object.values(mine)) t.isDefault = false;
  if (mine[name]) mine[name].isDefault = true;
  all[slug] = mine;
  writeJSON(K_TEMPLATES, all, storage);
}

const K_BOOKMARKS = 'nr.bookmarks';

/** Array of pinned note paths for a notebook (up to 20). */
export function getBookmarks(slug, storage = defaultStorage()) {
  const all = readJSON(K_BOOKMARKS, storage);
  const list = all[slug];
  return Array.isArray(list) ? list.filter((p) => typeof p === 'string') : [];
}

/** Add a note path to the bookmarks list. Returns the new list. */
export function addBookmark(slug, path, storage = defaultStorage()) {
  const all = readJSON(K_BOOKMARKS, storage);
  const set = new Set(Array.isArray(all[slug]) ? all[slug] : []);
  set.delete(path); // move to front
  const list = [path, ...set].slice(0, 20);
  all[slug] = list;
  writeJSON(K_BOOKMARKS, all, storage);
  return list;
}

/** Remove a note path from the bookmarks list. Returns the new list. */
export function removeBookmark(slug, path, storage = defaultStorage()) {
  const all = readJSON(K_BOOKMARKS, storage);
  const list = Array.isArray(all[slug]) ? all[slug].filter((p) => p !== path) : [];
  all[slug] = list;
  writeJSON(K_BOOKMARKS, all, storage);
  return list;
}

/** Toggle a bookmark. Returns {bookmarked, list}. */
export function toggleBookmark(slug, path, storage = defaultStorage()) {
  const current = getBookmarks(slug, storage);
  if (current.includes(path)) {
    return { bookmarked: false, list: removeBookmark(slug, path, storage) };
  }
  return { bookmarked: true, list: addBookmark(slug, path, storage) };
}

// --- preferences export/import ----------------------------------------

/**
 * Every preference key that travels between browsers. Drafts (nr.pad.*)
 * and anything not on this list stay local: an import can only ever
 * write these keys.
 */
const PREF_KEYS = [
  K_THEME, K_PLACE, K_LAST, K_DECO, K_COLLAPSED, K_HOMEPAGE, K_STATUS, K_HELP,
  K_KEYBINDINGS, K_TEMPLATES,
  'nr.editorPref', 'nr.vimMode', 'nr.bookmarks', 'nr.vimrc',
  ...Object.values(SETTINGS).map((s) => s.key),
];

/**
 * A JSON snapshot of this browser's preferences. Values are the parsed
 * JSON where the key stores JSON and the raw string otherwise.
 */
export function exportPreferences(storage = defaultStorage()) {
  const prefs = {};
  for (const k of PREF_KEYS) {
    const raw = storage?.getItem(k);
    if (raw == null || raw === '') continue;
    try {
      prefs[k] = JSON.parse(raw);
    } catch {
      prefs[k] = raw;
    }
  }
  return { app: 'noteroot', kind: 'preferences', version: 1, prefs };
}

/**
 * Apply a snapshot from exportPreferences(). Only allowlisted keys are
 * written; unknown or corrupt entries are counted as skipped and never
 * touch storage. Returns {imported, skipped, error}.
 */
export function importPreferences(data, storage = defaultStorage()) {
  const fail = (error) => ({ imported: 0, skipped: 0, error });
  if (!data || typeof data !== 'object' || Array.isArray(data)) {
    return fail('That file is not a noteroot preferences export.');
  }
  if (data.app !== 'noteroot' || data.kind !== 'preferences') {
    return fail('That file is not a noteroot preferences export.');
  }
  if (data.version !== 1) {
    return fail('Unsupported preferences file version.');
  }
  const prefs = data.prefs;
  if (!prefs || typeof prefs !== 'object' || Array.isArray(prefs)) {
    return fail('The preferences file has no preferences.');
  }
  let imported = 0;
  let skipped = 0;
  for (const [k, v] of Object.entries(prefs)) {
    if (!PREF_KEYS.includes(k) || v === undefined || v === null) {
      skipped += 1;
      continue;
    }
    try {
      storage?.setItem(k, typeof v === 'string' ? v : JSON.stringify(v));
      imported += 1;
    } catch {
      skipped += 1;
    }
  }
  return { imported, skipped, error: null };
}
