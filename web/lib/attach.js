/**
 * Attach-menu helpers: markdown for links and internal note links,
 * URL validation and note resolution (stage: attachments).
 *
 * Pure functions so they can be unit-tested; DOM and dialogs stay in
 * app.js. Internal links are wikilinks — [[path]] resolves through the
 * notebook's index (title/path/id/alias), so only same-notebook targets
 * are ever offered.
 */

import { splitFrontmatter } from './frontmatter.js';

/** Schemes a user-inserted link may use. Relative paths have no scheme. */
const ALLOWED_SCHEMES = ['http', 'https', 'mailto'];

/**
 * True when a URL may be inserted as a markdown link: http(s), mailto
 * or a relative path (no scheme). javascript:, data: etc. are rejected.
 */
export function isValidLinkUrl(url) {
  const u = String(url || '').trim();
  if (!u) return false;
  const m = u.match(/^([a-zA-Z][a-zA-Z0-9+.-]*):/);
  if (m) return ALLOWED_SCHEMES.includes(m[1].toLowerCase());
  return true; // relative: /path, ./path, path.md, #anchor
}

/** Percent-encode characters that would break a markdown link target. */
function encodeTarget(url) {
  return String(url).replace(/[ ()]/g, (c) =>
    (c === ' ' ? '%20' : `%${c.charCodeAt(0).toString(16).toUpperCase()}`));
}

/** Escape markdown emphasis/bracket characters inside a link label. */
function escapeLabel(text) {
  return String(text).replace(/([\\[\]()])/g, '\\$1');
}

/**
 * Markdown for a user link: `[label](url)`. Empty label falls back to
 * the URL itself; the target is percent-encoded so spaces and parens
 * cannot break the link.
 */
export function buildLinkMarkdown(url, label) {
  const u = encodeTarget(String(url || '').trim());
  const text = String(label || '').trim() || u;
  return `[${escapeLabel(text)}](${u})`;
}

/**
 * Markdown for an internal note link: `[[path]]`, or `[[path|label]]`
 * when a label differs from the note's file name. Returns null when the
 * path or label cannot be represented (the wikilink parser rejects `]`
 * in targets and at most one `|`).
 */
export function buildNoteLinkMarkdown(path, label = '') {
  const p = String(path || '').trim();
  if (!p || p.includes(']') || p.includes('[') || p.includes('\n')) return null;
  const raw = String(label || '').trim();
  const base = p.endsWith('.md') ? p.slice(0, -3) : p;
  const fileName = base.slice(base.lastIndexOf('/') + 1);
  if (!raw || raw === fileName || raw === p) return `[[${p}]]`;
  if (raw.includes(']') || raw.includes('[') || raw.includes('|') || raw.includes('\n')) return null;
  return `[[${p}|${raw}]]`;
}

/** Every note path (non-directory) under a tree root, sorted. */
export function collectNotePaths(root) {
  const out = [];
  const walk = (node) => {
    for (const c of node?.children || []) {
      if (c.isDir) walk(c);
      else out.push(c.path);
    }
  };
  walk(root);
  return out.sort();
}

/**
 * Resolve what the user typed to a note path from `paths`: exact path,
 * path + .md, or — for a bare name without slashes — the one note with
 * that file name anywhere in the tree. Returns null when nothing
 * matches or several notes share the name.
 */
export function resolveNotePath(paths, query) {
  const q = String(query || '').trim();
  if (!q) return null;
  if (paths.includes(q)) return q;
  if (!q.endsWith('.md') && paths.includes(`${q}.md`)) return `${q}.md`;
  if (!q.includes('/')) {
    const stem = q.endsWith('.md') ? q.slice(0, -3) : q;
    const nameHits = paths.filter((p) => p.slice(0, -3).split('/').pop() === stem);
    if (nameHits.length === 1) return nameHits[0];
  }
  return null;
}

/**
 * Fit an insert into a note without breaking its frontmatter. An insert
 * at [start, end) that touches the leading block moves to the start of
 * the body, on a line of its own (markdown inside the block would break
 * the YAML). Returns the range to replace and the text to put there.
 */
export function bodyInsert(note, start, end, insert) {
  const sp = splitFrontmatter(note);
  const bodyStart = sp.head.length + sp.block.length + sp.close.length;
  const closed = sp.close.endsWith('\n');
  if (!sp.ok || (closed && start >= bodyStart)) return { start, end, text: insert };
  const lead = closed ? '' : sp.eol; // the block ends the file without a line break
  const tail = sp.body && !insert.endsWith('\n') ? sp.eol : '';
  return { start: bodyStart, end: Math.max(end, bodyStart), text: lead + insert + tail };
}

/** Case-insensitive substring filter over note paths for the picker list. */
export function filterNotePaths(paths, query) {
  const q = String(query || '').trim().toLowerCase();
  if (!q) return paths;
  return paths.filter((p) => p.toLowerCase().includes(q));
}

/**
 * The note `delta` places from `path` in the sorted note order (tree
 * pre-order, as `collectNotePaths` returns). Used for j/k navigation in
 * the reader: +1 is the next note, -1 the previous. Returns null at the
 * ends, when `path` is not in `paths`, or for a zero/NaN delta.
 */
export function adjacentNote(paths, path, delta) {
  const step = Number(delta) || 0;
  if (!step || !path) return null;
  const i = paths.indexOf(path);
  if (i < 0) return null;
  const next = i + step;
  return next >= 0 && next < paths.length ? paths[next] : null;
}
