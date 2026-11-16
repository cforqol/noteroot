/**
 * Auto-format GFM pipe tables on save: pad every cell to its column's
 * widest cell (header or data), normalize the delimiter row's dashes
 * and alignment markers, and add a leading/trailing pipe on every row.
 * Lines inside a fenced code block are left untouched — a `|` there is
 * not a table.
 */

const FENCE_RE = /^\s*(`{3,}|~{3,})/;

/**
 * Split one table row into trimmed cell strings, honoring a leading
 * and/or trailing pipe (either is optional) and `\|` as a literal pipe
 * inside a cell rather than a separator.
 */
function splitTableRow(line) {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1);
  const cells = [];
  let cur = '';
  for (let i = 0; i < s.length; i += 1) {
    const c = s[i];
    if (c === '\\' && i + 1 < s.length) {
      cur += c + s[i + 1];
      i += 1;
    } else if (c === '|') {
      cells.push(cur.trim());
      cur = '';
    } else {
      cur += c;
    }
  }
  cells.push(cur.trim());
  return cells;
}

function isDelimiterRow(line) {
  if (!line.includes('-')) return false;
  const cells = splitTableRow(line);
  return cells.length > 0 && cells.every((c) => /^:?-+:?$/.test(c));
}

/** 'left' | 'right' | 'center' | null (no explicit alignment) from a delimiter cell. */
function alignmentOf(cell) {
  const left = cell.startsWith(':');
  const right = cell.endsWith(':');
  if (left && right) return 'center';
  if (right) return 'right';
  if (left) return 'left';
  return null;
}

function padCell(text, width, align) {
  const pad = Math.max(0, width - text.length);
  if (align === 'right') return ' '.repeat(pad) + text;
  if (align === 'center') {
    const left = Math.floor(pad / 2);
    return ' '.repeat(left) + text + ' '.repeat(pad - left);
  }
  return text + ' '.repeat(pad);
}

function renderRow(cells, widths, aligns) {
  return `| ${cells.map((c, i) => padCell(c, widths[i], aligns[i])).join(' | ')} |`;
}

function renderDelimiter(widths, aligns) {
  const cells = widths.map((w, i) => {
    const a = aligns[i];
    if (a === 'center') return `:${'-'.repeat(Math.max(1, w - 2))}:`;
    if (a === 'left') return `:${'-'.repeat(Math.max(1, w - 1))}`;
    if (a === 'right') return `${'-'.repeat(Math.max(1, w - 1))}:`;
    return '-'.repeat(w);
  });
  return `| ${cells.join(' | ')} |`;
}

/** Pad/truncate a row to exactly ncols cells, same as CommonMark's table rule. */
function fitRow(cells, ncols) {
  const r = cells.slice(0, ncols);
  while (r.length < ncols) r.push('');
  return r;
}

const MIN_COL_WIDTH = 3; // the shortest valid delimiter cell is "---"

/**
 * @param {string} src
 * @returns {{content: string, changed: boolean}}
 */
export function formatPipeTables(src) {
  const text = String(src ?? '');
  const eol = text.includes('\r\n') ? '\r\n' : '\n';
  const lines = text.split(/\r\n|\n/);
  const out = [];
  let inFence = false;
  let changed = false;
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];
    if (FENCE_RE.test(line)) {
      inFence = !inFence;
      out.push(line);
      i += 1;
      continue;
    }
    const header = !inFence && line.includes('|') ? splitTableRow(line) : null;
    const delimLine = i + 1 < lines.length ? lines[i + 1] : '';
    if (
      header && header.length > 1 && !inFence && isDelimiterRow(delimLine)
      && splitTableRow(delimLine).length === header.length
    ) {
      const ncols = header.length;
      const aligns = splitTableRow(delimLine).map(alignmentOf);
      const dataLines = [];
      let j = i + 2;
      while (j < lines.length && lines[j].trim() !== '' && lines[j].includes('|') && !FENCE_RE.test(lines[j])) {
        dataLines.push(lines[j]);
        j += 1;
      }
      const dataRows = dataLines.map((l) => fitRow(splitTableRow(l), ncols));

      const widths = new Array(ncols).fill(MIN_COL_WIDTH);
      for (const row of [header, ...dataRows]) {
        for (let c = 0; c < ncols; c += 1) widths[c] = Math.max(widths[c], row[c].length);
      }

      const newHeader = renderRow(header, widths, aligns);
      const newDelim = renderDelimiter(widths, aligns);
      const newData = dataRows.map((row) => renderRow(row, widths, aligns));

      out.push(newHeader, newDelim, ...newData);
      if (newHeader !== line || newDelim !== delimLine || newData.some((l, idx) => l !== dataLines[idx])) {
        changed = true;
      }
      i = j;
      continue;
    }
    out.push(line);
    i += 1;
  }

  return { content: out.join(eol), changed };
}
