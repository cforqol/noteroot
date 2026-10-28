/**
 * Editor status-bar statistics. Pure functions: text in, numbers and
 * display strings out, so the status bar can be unit-tested without a DOM.
 */

const WORDS_PER_MINUTE = 200;
const FRONTMATTER = /^---\r?\n[\s\S]*?\r?\n---[ \t]*(?:\r?\n|$)/;

/** Drop a leading YAML frontmatter block — it is metadata, not prose. */
export function stripFrontmatter(text) {
  return String(text ?? '').replace(FRONTMATTER, '');
}

/** Whitespace-delimited word count. */
export function countWords(text) {
  const m = String(text ?? '').match(/\S+/g);
  return m ? m.length : 0;
}

/**
 * Writer statistics for a note body (frontmatter excluded): words,
 * characters without whitespace, paragraphs (blank-line separated) and
 * estimated reading time in whole minutes at `wpm` words per minute
 * (0 only for an empty body).
 */
export function textStats(text, wpm = WORDS_PER_MINUTE) {
  const body = stripFrontmatter(text);
  const words = countWords(body);
  return {
    words,
    chars: Array.from(body.replace(/\s/g, '')).length,
    paragraphs: body.split(/\n\s*\n/).filter((p) => p.trim() !== '').length,
    readingMinutes: words === 0 ? 0 : Math.max(1, Math.round(words / (Number(wpm) || WORDS_PER_MINUTE))),
  };
}

/** 1-based row and column of a character offset. Offsets are clamped. */
export function cursorPosition(text, offset) {
  const t = String(text ?? '');
  const o = Math.max(0, Math.min(Number(offset) || 0, t.length));
  const before = t.slice(0, o);
  return {
    row: before.split('\n').length,
    col: o - (before.lastIndexOf('\n') + 1) + 1,
  };
}

/** UTF-8 size of a string in bytes. */
export function byteLength(text) {
  return new TextEncoder().encode(String(text ?? '')).length;
}

/** 1536 -> "1.5 KB". Invalid input renders as an em dash. */
export function humanSize(bytes) {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KB', 'MB', 'GB'];
  let v = bytes / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v >= 10 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/**
 * Everything the status bar shows, as strings.
 *
 * @param {object} i
 * @param {string|null} i.text      note source, or null when unknown
 * @param {{start:number,end:number}|null} [i.selection]
 * @param {boolean} [i.cursor]      true when row:col is meaningful
 * @param {number|null} [i.bytes]   file size; defaults to the text size
 * @param {number} [i.wpm]          reading speed for the reading time
 */
export function describeStatus({ text, selection = null, cursor = false, bytes = null, wpm = WORDS_PER_MINUTE }) {
  if (text == null) {
    return { words: '—', selected: '—', position: '—', chars: '—', paragraphs: '—', reading: '—', size: humanSize(bytes ?? NaN) };
  }
  const s = textStats(text, wpm);
  const selStart = selection ? Math.min(selection.start, selection.end) : 0;
  const selEnd = selection ? Math.max(selection.start, selection.end) : 0;
  const selected = selEnd > selStart ? countWords(text.slice(selStart, selEnd)) : 0;
  const pos = cursor ? cursorPosition(text, selection ? selection.end : 0) : null;
  return {
    words: String(s.words),
    selected: String(selected),
    position: pos ? `${pos.row}:${pos.col}` : '—',
    chars: String(s.chars),
    paragraphs: String(s.paragraphs),
    reading: s.readingMinutes === 0 ? '—' : `${s.readingMinutes} min`,
    size: humanSize(bytes ?? byteLength(text)),
  };
}
