/**
 * Import/export helpers: file-type checks, file names, import plan
 * summaries, collision-free paths and the browser download trigger.
 */
import { localDate } from './frontmatter.js';
import { baseName, parentDir, joinPath } from './tree.js';

export function isArchive(name) {
  return /\.(tar\.gz|tgz)$/i.test(String(name ?? ''));
}

export function isMarkdown(name) {
  return /\.(md|markdown)$/i.test(String(name ?? ''));
}

/** "notes-2026-10-17.tar.gz" */
export function exportFileName(slug, date = new Date()) {
  return `${slug}-${localDate(date)}.tar.gz`;
}

/** Counts per import status: {created, updated, skipped, error, total}. */
export function summarizeImport(entries = []) {
  const s = { created: 0, updated: 0, skipped: 0, error: 0, total: entries.length };
  for (const e of entries) if (e.status in s) s[e.status] += 1;
  return s;
}

const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** "2 new notes, 1 overwritten, 3 unchanged, 1 failed" (dry run: future tense). */
export function describeImport(s, { dryRun = false } = {}) {
  const parts = [];
  if (s.created) parts.push(`${plural(s.created, 'new note')}`);
  if (s.updated) parts.push(`${s.updated} ${dryRun ? 'will be overwritten' : 'overwritten'}`);
  if (s.skipped) parts.push(`${s.skipped} unchanged`);
  if (s.error) parts.push(`${s.error} ${dryRun ? 'cannot be imported' : 'failed'}`);
  return parts.length ? parts.join(', ') : 'No notes in this archive';
}

/**
 * First free path for a note: path itself, else "name-2.md", "name-3.md"…
 * `taken(path)` reports whether a path is already used.
 */
export function uniquePath(path, taken) {
  if (!taken(path)) return path;
  const dir = parentDir(path);
  const stem = baseName(path).replace(/\.md$/i, '');
  for (let i = 2; ; i += 1) {
    const candidate = joinPath(dir, `${stem}-${i}.md`);
    if (!taken(candidate)) return candidate;
  }
}

/** Save a Blob as a file through a temporary link. */
export function downloadBlob(blob, filename, doc = document) {
  const url = URL.createObjectURL(blob);
  const a = doc.createElement('a');
  a.href = url;
  a.download = filename;
  a.hidden = true;
  doc.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
