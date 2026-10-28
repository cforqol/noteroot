/**
 * Client-side proxies for server-side external integrations.
 * Credentials never leave the server; these helpers call the server
 * endpoints which proxy out to Karakeep / Slink.
 */

const STATUS_URL = '/api/v1/integrations/status';
const KARAKEEP_URL = '/api/v1/integrations/karakeep/archive';
const SLINK_URL = '/api/v1/integrations/slink/upload';

let _statusPromise = null;

/** Fetch and cache the integration status for the lifetime of the page. */
export async function fetchIntegrationStatus(deps = {}) {
  if (_statusPromise) return _statusPromise;
  const fetchFn = deps.fetchFn || ((...a) => fetch(...a));
  _statusPromise = fetchFn(STATUS_URL).then(r => r.ok ? r.json() : { karakeep: false, slink: false, s3: false });
  return _statusPromise;
}

/** Reset the cached status (useful after config changes or in tests). */
export function resetIntegrationStatus() {
  _statusPromise = null;
}

/**
 * Archive a URL with Karakeep.
 * Returns the bookmark URL on success, throws on error.
 * @param {string} url  the URL to archive
 * @param {object} [deps]  { fetchFn } for testing
 */
export async function karakeepArchive(url, deps = {}) {
  const fetchFn = deps.fetchFn || ((...a) => fetch(...a));
  const res = await fetchFn(KARAKEEP_URL, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url }),
  });
  if (!res.ok) {
    let msg = `archive failed (${res.status})`;
    try { const j = await res.json(); if (j.error) msg = j.error; } catch (_) {}
    throw new Error(msg);
  }
  const { bookmarkUrl } = await res.json();
  return bookmarkUrl;
}

/**
 * Upload a file to Slink.
 * Returns the hosted URL on success, throws on error.
 * @param {File} file
 * @param {object} [deps]  { fetchFn } for testing
 */
export async function slinkUpload(file, deps = {}) {
  const fetchFn = deps.fetchFn || ((...a) => fetch(...a));
  const form = new FormData();
  form.append('file', file);
  const res = await fetchFn(SLINK_URL, { method: 'POST', body: form });
  if (!res.ok) {
    let msg = `slink upload failed (${res.status})`;
    try { const j = await res.json(); if (j.error) msg = j.error; } catch (_) {}
    throw new Error(msg);
  }
  const { url } = await res.json();
  return url;
}

/** True if the plain text looks like an http/https URL (no whitespace). */
export function isHttpUrl(text) {
  if (!text || /\s/.test(text.trim())) return false;
  try {
    const u = new URL(text.trim());
    return u.protocol === 'http:' || u.protocol === 'https:';
  } catch (_) {
    return false;
  }
}
