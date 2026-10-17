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
  tabKey: { key: 'nr.tabKey', choices: ['focus', '2', '4', 'tab'], fallback: 'focus' },
  spellcheck: { key: 'nr.spellcheck', choices: ['on', 'off'], fallback: 'off' },
  wrap: { key: 'nr.wrap', choices: ['on', 'off'], fallback: 'on' },
  treeCounts: { key: 'nr.treeCounts', choices: ['on', 'off'], fallback: 'on' },
  statusBar: { key: 'nr.statusBar', choices: ['on', 'off'], fallback: 'on' },
  readingSpeed: { key: 'nr.readingSpeed', choices: ['150', '200', '250', '300'], fallback: '200' },
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
