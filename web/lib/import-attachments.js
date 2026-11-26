/**
 * Attachment resolution for plain-markdown note import (#107).
 *
 * A note imported as a bare .md/.txt file (see importMarkdown in
 * app.js) is not a noteroot export archive: any image/file it
 * references is named by a path or URL that was only ever meaningful
 * on the system it came from. These pure functions find those
 * references and classify what app.js needs to do with each one —
 * upload a companion file, fetch an external URL, or leave an
 * already-resolved/unresolvable reference untouched — so the DOM/
 * network glue in app.js stays a thin loop over this module's output.
 */

const MD_LINK_RE = /!?\[[^\]\n]*\]\(\s*([^()\s]+)(?:\s+"[^"]*")?\s*\)/g;
const HTML_IMG_SRC_RE = /<img\b[^>]*\bsrc=["']([^"']+)["'][^>]*>/gi;

/**
 * Classify one reference target.
 *   "resource" — already this notebook's own /n/{slug}/resources/{sha}
 *                link; nothing to do.
 *   "skip"     — data:/mailto:/tel: URI, bare anchor, or a link into a
 *                *different* notebook; never resolvable from import.
 *   "http"     — external http(s) URL; the server can fetch it.
 *   "local"    — a relative path or bare filename; only resolvable if
 *                a same-named file was imported alongside the note.
 *
 * @param {string} target
 * @param {string} [slug] current notebook slug, to recognize the
 *   notebook's own resource links
 */
export function classifyAttachmentRef(target, slug) {
  if (!target) return "skip";
  if (/^(data|mailto|tel):/i.test(target)) return "skip";
  if (target.startsWith("#")) return "skip";
  if (/^https?:\/\//i.test(target)) return "http";
  if (slug) {
    const escaped = slug.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    if (new RegExp(`^/n/${escaped}/resources/[a-f0-9]{64}(?:[/?#].*)?$`).test(target)) {
      return "resource";
    }
  }
  // A link into some other part of the app (another notebook, a
  // different resource, an absolute site path) is not an attachment
  // this import step owns.
  if (target.startsWith("/n/") || target.startsWith("/")) return "skip";
  return "local";
}

/** The filename a local/http target implies, for matching or display. */
export function attachmentRefFilename(target) {
  const clean = String(target).split(/[?#]/)[0];
  const idx = clean.lastIndexOf("/");
  const name = idx === -1 ? clean : clean.slice(idx + 1);
  try {
    return decodeURIComponent(name);
  } catch (_) {
    return name;
  }
}

/**
 * Scan markdown source for attachment-like references (markdown
 * image/link syntax and raw `<img src>`) and return the ones that
 * need resolving during import: external URLs to fetch and local
 * paths/filenames to match against sibling files. Already-resolved
 * resource links and non-attachment links (anchors, other notebooks,
 * data URIs) are left out — there is nothing to do with them.
 *
 * Each distinct target string appears once, in first-seen order.
 *
 * @param {string} markdown
 * @param {string} [slug]
 * @returns {{target:string, kind:"http"|"local", filename:string}[]}
 */
export function extractAttachmentRefs(markdown, slug) {
  const text = String(markdown || "");
  const seen = new Set();
  const refs = [];
  const consider = (target) => {
    if (!target || seen.has(target)) return;
    seen.add(target);
    const kind = classifyAttachmentRef(target, slug);
    if (kind !== "http" && kind !== "local") return;
    refs.push({ target, kind, filename: attachmentRefFilename(target) });
  };
  for (const m of text.matchAll(MD_LINK_RE)) consider(m[1]);
  for (const m of text.matchAll(HTML_IMG_SRC_RE)) consider(m[1]);
  return refs;
}

/**
 * Replace every occurrence of an attachment reference's exact target
 * string with the notebook resource URL it now resolves to. Scoped to
 * the literal target text (not a regex), matching the same
 * split/join approach already used for sha-based replace in app.js's
 * replaceAttachmentInNote.
 */
export function rewriteAttachmentRef(markdown, target, resourceUrl) {
  if (!target) return markdown;
  return String(markdown).split(target).join(resourceUrl);
}
