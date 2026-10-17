/**
 * Note frontmatter, client side: the browser mirror of
 * internal/frontmatter.
 *
 * A note the UI shows or saves carries a "correct" frontmatter block: a
 * durable `id` (26-char ULID + 2-char checksum, the format the server
 * mints), a `title`, and `created` / `updated` dates. Edits are textual,
 * like the server's WithID: missing keys are added, an invalid `id`
 * value is replaced, and every other byte (formatting, key order,
 * comments, the body) is preserved.
 */

export const ID_RE = /^[0-9A-HJKMNP-TV-Z]{28}$/;

const CROCK = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
const DELIM = '---';
const MAX_BLOCK = 64 * 1024; // bytes, as internal/frontmatter maxBlock

export function isValidId(id) {
  return typeof id === 'string' && ID_RE.test(id);
}

let crcTable = null;

function crc32(ascii) {
  if (!crcTable) {
    crcTable = new Uint32Array(256);
    for (let n = 0; n < 256; n += 1) {
      let c = n;
      for (let k = 0; k < 8; k += 1) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      crcTable[n] = c >>> 0;
    }
  }
  let crc = 0xffffffff;
  for (let i = 0; i < ascii.length; i += 1) {
    crc = crcTable[(crc ^ ascii.charCodeAt(i)) & 0xff] ^ (crc >>> 8);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

/** 48-bit millisecond timestamp + 10 random bytes -> 26 Crockford chars. */
export function encodeULID(ts, rnd) {
  let n = BigInt(ts) & 0xffffffffffffn;
  for (const b of rnd) n = (n << 8n) | BigInt(b & 0xff);
  let out = '';
  for (let i = 0; i < 26; i += 1) {
    out = CROCK[Number(n % 32n)] + out;
    n /= 32n;
  }
  return out;
}

/** CRC32 (IEEE) of the ULID reduced to 10 bits, as 2 Crockford chars. */
export function ulidChecksum(u26) {
  const c = crc32(u26) % 1024;
  return CROCK[Math.floor(c / 32)] + CROCK[c % 32];
}

function randomBytes(n) {
  const out = new Uint8Array(n);
  globalThis.crypto.getRandomValues(out);
  return out;
}

/** A fresh note id, sortable by creation time. */
export function mintId({ now = Date.now(), random = randomBytes } = {}) {
  const u = encodeULID(now, random(10));
  return u + ulidChecksum(u);
}

/** Local calendar date as YYYY-MM-DD. */
export function localDate(d = new Date()) {
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

function utf8Length(s) {
  return new TextEncoder().encode(s).length;
}

/**
 * Split a leading frontmatter block off a note, with the server's rules:
 * the first line is exactly `---`, and a closing `---` line starts within
 * 64 KiB. Without a block, `ok` is false and the whole text is body.
 *
 * @returns {{ok:boolean, head:string, block:string, close:string, body:string, eol:string}}
 *   `head` and `close` are the delimiter lines including their line
 *   breaks, so head + block + close + body === src.
 */
export function splitFrontmatter(src) {
  const s = String(src ?? '');
  const none = { ok: false, head: '', block: '', close: '', body: s, eol: s.includes('\r\n') ? '\r\n' : '\n' };
  if (!s.startsWith(DELIM)) return none;
  const nl = s.indexOf('\n');
  if (nl < 0 || s.slice(0, nl).replace(/\r+$/, '') !== DELIM) return none;
  const head = s.slice(0, nl + 1);
  const rest = s.slice(nl + 1);
  // Code units never exceed UTF-8 bytes, so off > MAX_BLOCK is past the limit.
  for (let off = 0; off < rest.length && off <= MAX_BLOCK;) {
    const end = rest.indexOf('\n', off);
    const line = end < 0 ? rest.slice(off) : rest.slice(off, end);
    if (line.replace(/\r+$/, '') === DELIM) {
      const block = rest.slice(0, off);
      if (utf8Length(block) > MAX_BLOCK) return none;
      const close = end < 0 ? line : rest.slice(off, end + 1);
      return { ok: true, head, block, close, body: end < 0 ? '' : rest.slice(end + 1), eol: head.endsWith('\r\n') ? '\r\n' : '\n' };
    }
    if (end < 0) break;
    off = end + 1;
  }
  return none;
}

// A column-0 `key: value` line (the only lines frontmatter edits touch).
function parseLine(line) {
  const l = line.replace(/\r$/, '');
  if (l === '' || /^[\s#-]/.test(l)) return null;
  const i = l.indexOf(':');
  if (i < 0) return null;
  const raw = l.slice(i + 1).trim();
  let value = raw;
  const q = raw[0];
  if (q === '"' || q === "'") {
    const j = raw.indexOf(q, 1);
    value = j > 0 ? raw.slice(1, j) : raw.slice(1);
  } else {
    const c = raw.search(/(^|\s)#/);
    if (c >= 0) value = raw.slice(0, c).trim();
  }
  return { key: l.slice(0, i).trim(), value };
}

function topLevel(lines) {
  const keys = new Map();
  lines.forEach((line, index) => {
    const p = parseLine(line);
    if (!p || keys.has(p.key)) return;
    // `key:` followed by an indented block or a sequence still has a value.
    const next = lines[index + 1] ?? '';
    const continued = p.value === '' && /^([ \t]|-[ \t]|-\r?$)/.test(next);
    keys.set(p.key, { index, value: p.value, present: p.value !== '' || continued });
  });
  return keys;
}

/** The recognised keys of a note's frontmatter (textual, never throws). */
export function readFrontmatter(src) {
  const sp = splitFrontmatter(src);
  if (!sp.ok) return { ok: false, id: '', title: '', created: '', updated: '' };
  const keys = topLevel(sp.block.split('\n'));
  const v = (k) => (keys.get(k)?.present ? keys.get(k).value : '');
  return { ok: true, id: v('id'), title: v('title'), created: v('created'), updated: v('updated') };
}

/**
 * Title for a note without one: its first ATX heading outside code
 * fences, else its file name.
 */
export function deriveTitle(text, path = '') {
  let fence = '';
  for (const raw of String(text ?? '').split('\n')) {
    const line = raw.replace(/\r$/, '');
    const f = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (f) {
      if (!fence) fence = f[1][0];
      else if (f[1][0] === fence) fence = '';
      continue;
    }
    if (fence) continue;
    const m = /^ {0,3}#[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$/.exec(line);
    if (m && m[1].trim()) return m[1].trim().slice(0, 200);
  }
  const p = String(path ?? '');
  return p.slice(p.lastIndexOf('/') + 1).replace(/\.md$/i, '') || 'Untitled';
}

const RESERVED = /^(?:true|false|yes|no|on|off|null|y|n|~)$/i;

/** A YAML scalar for s: plain when unambiguous, else double-quoted. */
export function yamlString(s) {
  const v = String(s ?? '');
  if (/^[\p{L}][\p{L}\p{N} _.,()&!?'’-]*$/u.test(v) && !v.endsWith(' ') && !RESERVED.test(v)) return v;
  return JSON.stringify(v);
}

/**
 * Give a note correct frontmatter.
 *
 * @param {string} src
 * @param {object} [o]
 * @param {string} [o.path]     note path (title fallback)
 * @param {string} [o.id]       identity to reuse when src has no valid id
 * @param {string} [o.created]  date for a missing `created` (default today)
 * @param {string} [o.updated]  date for a missing `updated` (default created)
 * @param {boolean} [o.touch]   save semantics: set `updated` to o.today
 * @param {string} [o.today]
 * @param {function} [o.mint]   id factory
 * @returns {{content:string, changed:boolean, added:string[], id:string}}
 */
export function normalizeNote(src, o = {}) {
  const s = String(src ?? '');
  const today = o.today || localDate();
  const created = o.created || today;
  const updated = o.touch ? today : (o.updated || created);
  const mint = o.mint || mintId;
  const sp = splitFrontmatter(s);
  const freshId = () => (isValidId(o.id) ? o.id : mint());

  if (!sp.ok) {
    const id = freshId();
    const title = yamlString(deriveTitle(s, o.path));
    const eol = sp.eol;
    let header = [DELIM, `id: ${id}`, `title: ${title}`, `created: ${created}`, `updated: ${updated}`, DELIM].join(eol) + eol;
    if (s !== '' && !s.startsWith('\n') && !s.startsWith('\r\n')) header += eol;
    return { content: header + s, changed: true, added: ['id', 'title', 'created', 'updated'], id };
  }

  const cr = sp.eol === '\r\n' ? '\r' : '';
  // The block is empty or ends in a line break: drop the final empty piece.
  const lines = sp.block.split('\n');
  lines.pop();
  const keys = topLevel(lines);
  const added = [];
  const set = (key, value, { first = false } = {}) => {
    const k = keys.get(key);
    if (k) {
      lines[k.index] = `${key}: ${value}${lines[k.index].endsWith('\r') ? '\r' : ''}`;
    } else if (first) {
      lines.unshift(`${key}: ${value}${cr}`);
    } else {
      lines.push(`${key}: ${value}${cr}`);
    }
    added.push(key);
  };

  let id = keys.get('id')?.value ?? '';
  if (!isValidId(id)) {
    id = freshId();
    set('id', id, { first: true });
    // Re-index: the unshift moved every other line down by one.
    for (const [k, v] of topLevel(lines)) keys.set(k, v);
  }
  if (!keys.get('title')?.present) set('title', yamlString(deriveTitle(sp.body, o.path)));
  if (!keys.get('created')?.present) set('created', created);
  const up = keys.get('updated');
  if (!up?.present || (o.touch && up.value !== today)) set('updated', updated);

  if (!added.length) return { content: s, changed: false, added, id };
  return { content: `${sp.head}${lines.join('\n')}\n${sp.close}${sp.body}`, changed: true, added, id };
}

/**
 * The single edit turning a into b: replace a.slice(start, end) with
 * text. Lets an editor apply a frontmatter fix without moving the
 * caret. Null when a === b.
 */
export function changedRange(a, b) {
  if (a === b) return null;
  let start = 0;
  const max = Math.min(a.length, b.length);
  while (start < max && a[start] === b[start]) start += 1;
  let ea = a.length;
  let eb = b.length;
  while (ea > start && eb > start && a[ea - 1] === b[eb - 1]) {
    ea -= 1;
    eb -= 1;
  }
  return { start, end: ea, text: b.slice(start, eb) };
}
