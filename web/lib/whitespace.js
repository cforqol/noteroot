/**
 * Plain-text editor "automatic formatting" helpers: trim trailing
 * whitespace, split a line into text/space runs for the show-whitespace
 * overlay, and the soft-tab-aware Backspace range.
 */

/**
 * Strip trailing spaces/tabs from every line. Note two trailing spaces
 * are a Markdown hard line break, so this is opt-in (off by default).
 * @param {string} src
 * @returns {{content: string, changed: boolean}}
 */
export function trimTrailingSpaces(src) {
  const text = String(src ?? '');
  const eol = text.includes('\r\n') ? '\r\n' : '\n';
  const lines = text.split(/\r\n|\n/);
  const out = lines.map((l) => l.replace(/[ \t]+$/, ''));
  return { content: out.join(eol), changed: out.some((l, i) => l !== lines[i]) };
}

/**
 * Split one line into runs of spaces and non-spaces, merging consecutive
 * same-kind characters, for rendering a show-whitespace overlay.
 * @param {string} line
 * @returns {{text: string, space: boolean}[]}
 */
export function splitLineForWhitespace(line) {
  const out = [];
  let cur = '';
  let curSpace = null;
  for (const ch of String(line ?? '')) {
    const isSpace = ch === ' ';
    if (curSpace === null || isSpace === curSpace) {
      cur += ch;
      curSpace = isSpace;
    } else {
      out.push({ text: cur, space: curSpace });
      cur = ch;
      curSpace = isSpace;
    }
  }
  if (cur !== '') out.push({ text: cur, space: curSpace });
  return out;
}

/**
 * The range Backspace should delete when the caret sits right after a
 * soft-tab indent (a run of exactly `width` spaces ending at a column
 * that is a multiple of `width`): the whole indent, not one space.
 * Returns null when the normal single-character Backspace applies.
 * @param {string} value
 * @param {number} caret
 * @param {number} width
 * @returns {{start: number, end: number}|null}
 */
export function softTabBackspaceRange(value, caret, width) {
  if (!Number.isInteger(width) || width < 1 || caret < width) return null;
  const lineStart = value.lastIndexOf('\n', caret - 1) + 1;
  const col = caret - lineStart;
  if (col < width || col % width !== 0) return null;
  const run = value.slice(caret - width, caret);
  if (run !== ' '.repeat(width)) return null;
  return { start: caret - width, end: caret };
}
