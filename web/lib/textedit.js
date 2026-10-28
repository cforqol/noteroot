/**
 * Plain-text editor helpers.
 */

const lineStartOf = (value, i) => value.lastIndexOf('\n', i - 1) + 1;

/**
 * Tab / Shift+Tab: insert `unit` at a caret, or indent / outdent every
 * line a selection touches. Returns the edit to apply: replace
 * value.slice(start, end) with text, then select [selStart, selEnd].
 */
export function indentEdit(value, selStart, selEnd, unit, outdent = false) {
  const multi = value.slice(selStart, selEnd).includes('\n');
  if (!outdent && !multi) {
    const at = selStart + unit.length;
    return { start: selStart, end: selEnd, text: unit, selStart: at, selEnd: at };
  }
  const start = lineStartOf(value, selStart);
  // A selection that ends right after a line break leaves the next line alone.
  const last = multi && value[selEnd - 1] === '\n' ? selEnd - 1 : selEnd;
  const nl = value.indexOf('\n', last);
  const end = nl < 0 ? value.length : nl;
  const width = unit === '\t' ? 4 : unit.length;
  const strip = new RegExp(`^(\\t| {1,${width}})`);
  let firstDelta = 0;
  let total = 0;
  const text = value.slice(start, end).split('\n').map((line, i) => {
    const delta = outdent ? -((strip.exec(line) || [''])[0].length) : unit.length;
    if (i === 0) firstDelta = delta;
    total += delta;
    return outdent ? line.slice(-delta) : unit + line;
  }).join('\n');
  const a = Math.max(start, selStart + firstDelta);
  return { start, end, text, selStart: a, selEnd: selStart === selEnd ? a : Math.max(a, selEnd + total) };
}
