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
