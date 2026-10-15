/**
 * Resource upload utilities (stage 5).
 *
 * Pure functions extracted from app.js so they can be unit-tested.
 * Network and DOM concerns stay in app.js; this module only builds
 * URLs and markdown snippets.
 */

/**
 * Build the upload URL for a notebook and filename.
 */
export function uploadURL(slug, filename) {
  return `/n/${encodeURIComponent(slug)}/resources?filename=${encodeURIComponent(filename)}`;
}

/**
 * Build the serve URL for a stored blob.
 */
export function resourceURL(slug, sha) {
  return `/n/${encodeURIComponent(slug)}/resources/${sha}`;
}

/**
 * Return the markdown embed snippet for an uploaded resource.
 * Images become ![alt](url); everything else becomes [name](url).
 */
export function markdownFor(slug, res) {
  const url = resourceURL(slug, res.sha);
  if (res.mime && res.mime.startsWith("image/")) {
    return `![${res.filename}](${url})`;
  }
  return `[${res.filename}](${url})`;
}

/**
 * Determine whether a file should be embedded as an image based on
 * its MIME type or file extension.
 */
export function isImage(mime, filename) {
  if (mime && mime.startsWith("image/")) return true;
  if (!filename) return false;
  return /\.(png|jpe?g|gif|webp|svg|avif|bmp|ico)$/i.test(filename);
}

/**
 * Build the thumbnail URL for an image blob.
 * @param {string} slug notebook slug
 * @param {string} sha blob sha
 * @param {number} [w] max width (default 400)
 * @param {number} [h] max height (default 400)
 */
export function thumbURL(slug, sha, w = 400, h = 400) {
  const base = resourceURL(slug, sha);
  return `${base}/thumb?w=${w}&h=${h}`;
}

/**
 * Build the poster URL for a video blob.
 */
export function posterURL(slug, sha) {
  return `${resourceURL(slug, sha)}/poster`;
}

/**
 * Rewrite resource <img> tags in rendered HTML to use thumbnails.
 * Only images whose src points at /n/{slug}/resources/{sha} (without a
 * transform suffix) are rewritten. Images already pointing at /thumb or
 * /poster are left alone.
 *
 * @param {string} html rendered note HTML
 * @param {string} slug notebook slug
 * @param {object} [opts]
 * @param {number} [opts.maxWidth] thumbnail max width
 * @param {number} [opts.maxHeight] thumbnail max height
 */
export function rewriteImageThumbs(html, slug, opts = {}) {
  const { maxWidth = 800, maxHeight = 800 } = opts;
  const escapedSlug = slug.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(
    `(<img\\b[^>]*\\bsrc=")(/n/${escapedSlug}/resources/([a-f0-9]{64}))(")`,
    "gi",
  );
  return html.replace(re, (_m, pre, _orig, sha, post) => {
    return `${pre}${thumbURL(slug, sha, maxWidth, maxHeight)}${post}`;
  });
}

/**
 * Given rendered HTML, find video resource links and return their sha
 * list so the caller can fetch poster frames if desired.
 */
export function findVideoResources(html, slug) {
  const escapedSlug = slug.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(`/n/${escapedSlug}/resources/([a-f0-9]{64})`, "gi");
  const shas = new Set();
  let m;
  while ((m = re.exec(html)) !== null) {
    shas.add(m[1]);
  }
  return [...shas];
}
