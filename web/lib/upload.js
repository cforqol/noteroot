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
 * Brackets, backslashes, and parens in the name are backslash-escaped
 * so a hostile or merely odd filename cannot break out of the embed.
 */
export function markdownFor(slug, res) {
  const url = resourceURL(slug, res.sha);
  const label = String(res.filename).replace(/([\\[\]()])/g, '\\$1');
  if (res.mime && res.mime.startsWith("image/")) {
    return `![${label}](${url})`;
  }
  return `[${label}](${url})`;
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

// --- Presigned direct-to-S3 flow (stage 7) ---

/** Presign request URL for a notebook. */
export function presignURL(slug) {
  return `/n/${encodeURIComponent(slug)}/resources/presign`;
}

/** Confirm request URL for a notebook. */
export function confirmURL(slug) {
  return `/n/${encodeURIComponent(slug)}/resources/confirm`;
}

/** Presigned read URL endpoint for a blob. */
export function presignReadURL(slug, sha) {
  return `${resourceURL(slug, sha)}/presign-read`;
}

/**
 * Compute the hex sha256 of an ArrayBuffer/TypedArray/File using
 * WebCrypto. Duck-types .arrayBuffer() (File/Blob) and TypedArrays;
 * jsdom's cross-realm ArrayBuffers are passed through as-is.
 * Throws if WebCrypto is unavailable — callers treat that as "no
 * presign support".
 */
export async function sha256Hex(data) {
  let buf;
  if (typeof data?.arrayBuffer === "function") {
    buf = await data.arrayBuffer();
  } else if (ArrayBuffer.isView(data)) {
    buf = data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength);
  } else {
    buf = data; // ArrayBuffer (possibly cross-realm)
  }
  const digest = await crypto.subtle.digest("SHA-256", buf);
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

/**
 * Try the presigned upload path for a file.
 *
 * Returns one of:
 *   {mode:"exists",  sha}                    blob already in store
 *   {mode:"s3",      sha, result}            uploaded direct to S3 + confirmed
 *   {mode:"direct"}                          no S3; caller should use normal upload
 *   {mode:"error",   error}                  presign request failed (policy etc.)
 *
 * Throws only on unexpected S3 PUT or confirm failure after a presign
 * was issued (caller may report the error; retrying is safe because the
 * blob is content-addressed).
 *
 * @param {string} slug
 * @param {File} file
 * @param {object} [deps] injectables for tests: {fetchFn, putFn}
 */
export async function presignUpload(slug, file, deps = {}) {
  if (typeof crypto === "undefined" || !crypto.subtle) return { mode: "direct" };

  const fetchFn = deps.fetchFn || ((...a) => fetch(...a));
  const putFn = deps.putFn || ((url, init) => fetch(url, init));

  let sha;
  try {
    sha = await sha256Hex(file);
  } catch (_) {
    return { mode: "direct" };
  }

  const presignRes = await fetchFn(presignURL(slug), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      sha,
      size: file.size,
      filename: file.name,
      mime: file.type || "application/octet-stream",
    }),
  });
  if (!presignRes.ok) {
    let msg = `presign failed (${presignRes.status})`;
    try {
      const j = await presignRes.json();
      if (j.error) msg = j.error;
    } catch (_) { /* non-JSON */ }
    return { mode: "error", error: msg };
  }

  const body = await presignRes.json();

  if (body.exists) return { mode: "exists", sha: body.sha };
  if (body.direct) return { mode: "direct" };

  // Direct-to-S3 PUT with the presigned URL.
  const putRes = await putFn(body.uploadURL, {
    method: "PUT",
    headers: { "Content-Type": file.type || "application/octet-stream" },
    body: file,
  });
  if (!putRes.ok) {
    throw new Error(`S3 upload failed (${putRes.status})`);
  }

  // Confirm the journal entry.
  const confRes = await fetchFn(confirmURL(slug), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ entryID: body.entryID, sha: body.sha }),
  });
  if (!confRes.ok) {
    let msg = `confirm failed (${confRes.status})`;
    try {
      const j = await confRes.json();
      if (j.error) msg = j.error;
    } catch (_) { /* non-JSON */ }
    throw new Error(msg);
  }

  return {
    mode: "s3",
    sha: body.sha,
    result: {
      sha: body.sha,
      filename: file.name,
      mime: file.type || "application/octet-stream",
      created: true,
    },
  };
}
