/**
 * Pure helpers over the server's tree shape:
 *   {name, path, isDir, size, modTime, children?}
 * plus relative-path utilities used by the create/rename/move dialogs.
 */

/**
 * Walk the tree, set `noteCount` (all notes below, recursively) and
 * `folderCount` on every folder, and return the totals for the root.
 */
export function annotateCounts(root) {
  let notes = 0;
  let folders = 0;
  for (const child of root.children || []) {
    if (child.isDir) {
      const sub = annotateCounts(child);
      folders += 1 + sub.folders;
      notes += sub.notes;
    } else {
      notes += 1;
    }
  }
  root.noteCount = notes;
  root.folderCount = folders;
  return { notes, folders };
}

/** "3 notes · 1 folder" */
export function formatTreeStatus({ notes, folders }) {
  const n = `${notes} note${notes === 1 ? '' : 's'}`;
  const f = `${folders} folder${folders === 1 ? '' : 's'}`;
  return `${n} · ${f}`;
}

/** Every folder path in the tree (pre-order). */
export function folderPaths(root) {
  const out = [];
  for (const child of root.children || []) {
    if (child.isDir) {
      out.push(child.path, ...folderPaths(child));
    }
  }
  return out;
}

/** Directory part of a path: "a/b/c.md" -> "a/b", "c.md" -> "". */
export function parentDir(p) {
  const i = p.lastIndexOf('/');
  return i < 0 ? '' : p.slice(0, i);
}

/** Last segment of a path. */
export function baseName(p) {
  return p.slice(p.lastIndexOf('/') + 1);
}

/** Join two relative path parts, tolerating an empty side. */
export function joinPath(dir, name) {
  return dir ? `${dir}/${name}` : name;
}

/** All ancestor folders of a path, outermost first. */
export function ancestors(p) {
  const parts = p.split('/').slice(0, -1);
  return parts.map((_, i) => parts.slice(0, i + 1).join('/'));
}

/** True when `p` equals `dir` or lives anywhere beneath it. */
export function isWithin(p, dir) {
  return p === dir || p.startsWith(`${dir}/`);
}

/**
 * Validate user input for a vault-relative path. Backslashes become
 * slashes, surrounding slashes are trimmed, and empty or traversing
 * input is rejected. `asNote` appends ".md" when missing.
 *
 * @returns {{ok: true, path: string} | {ok: false, error: string}}
 */
export function cleanRelPath(input, { asNote = false } = {}) {
  const raw = String(input ?? '').trim().replace(/\\/g, '/').replace(/^\/+|\/+$/g, '');
  if (raw === '') return { ok: false, error: 'Enter a name.' };
  const segs = raw.split('/');
  if (segs.some((s) => s === '' || s === '.' || s === '..')) {
    return { ok: false, error: 'Name may not contain empty, "." or ".." parts.' };
  }
  if (segs.some((s) => s.startsWith('.'))) {
    return { ok: false, error: 'Names may not start with a dot.' };
  }
  if (segs.length > 5) return { ok: false, error: 'Too deeply nested (max 5 levels).' };
  const path = asNote && !/\.md$/i.test(raw) ? `${raw}.md` : raw;
  return { ok: true, path };
}

/** Starter content for a new note: a title taken from its file name. */
export function newNoteContent(path) {
  const title = baseName(path).replace(/\.md$/i, '');
  return `# ${title}\n\n`;
}

/**
 * Resolve a name typed into a dialog against a base folder: the name
 * itself must be valid, and so must the combined path (depth limit).
 */
export function resolveChild(dir, name, opts = {}) {
  const own = cleanRelPath(name, opts);
  if (!own.ok) return own;
  return cleanRelPath(joinPath(dir, own.path), { asNote: false });
}

/**
 * Destination choices for a folder picker, root first, in tree order:
 * [{value, label, depth}] where value is the folder path ('' = root).
 */
export function folderChoices(root) {
  const out = [{ value: '', label: 'Notebook root', depth: 0 }];
  const walk = (node, depth) => {
    for (const child of node.children || []) {
      if (!child.isDir) continue;
      out.push({ value: child.path, label: child.name, depth });
      walk(child, depth + 1);
    }
  };
  walk(root, 1);
  return out;
}

/** The editable name of a note (without .md) or folder. */
export function displayName(path, isDir) {
  const base = baseName(path);
  return isDir ? base : base.replace(/\.md$/i, '');
}

/**
 * Validate a rename: a new name in the same folder. Slashes are refused
 * (moving is a separate action). `same` is true when nothing changes.
 *
 * @returns {{ok: true, path: string, same: boolean} | {ok: false, error: string}}
 */
export function renameTarget(path, name, isDir) {
  if (/[\\/]/.test(String(name ?? ''))) {
    return { ok: false, error: 'A name cannot contain “/”. Use Move… to change folders.' };
  }
  const r = resolveChild(parentDir(path), name, { asNote: !isDir });
  return r.ok ? { ...r, same: r.path === path } : r;
}

/**
 * Validate a move into `destDir`, optionally inside a new subfolder
 * `newFolder` created there. The item keeps its name.
 *
 * @returns {{ok: true, path: string, dir: string} | {ok: false, error: string}}
 */
export function moveTarget(path, isDir, destDir, newFolder = '') {
  let dir = destDir;
  if (String(newFolder ?? '').trim()) {
    const sub = resolveChild(destDir, newFolder);
    if (!sub.ok) return sub;
    dir = sub.path;
  }
  if (isDir && isWithin(dir, path)) return { ok: false, error: 'A folder cannot be moved into itself.' };
  if (dir === parentDir(path)) return { ok: false, error: 'It is already in that folder. Choose another one.' };
  const to = cleanRelPath(joinPath(dir, baseName(path)));
  return to.ok ? { ok: true, path: to.path, dir } : to;
}
