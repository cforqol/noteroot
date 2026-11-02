// noteroot service worker: offline shell for static assets.
// Static files are cache-first (they change only on deploy); API and
// note content stay network-first so data is never stale.

const CACHE = "noteroot-v1";
const STATIC_ASSETS = [
  "/",
  "/index.html",
  "/app.js",
  "/style.css",
  "/manifest.json",
  "/icon.svg",
  "/lib/prefs.js",
  "/lib/help.js",
  "/lib/frontmatter.js",
  "/lib/tree.js",
  "/lib/upload.js",
  "/lib/dialog.js",
  "/lib/notes-api.js",
  "/lib/templates.js",
  "/lib/vim-editor.js",
  "/lib/plugins.js",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(STATIC_ASSETS)).then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))),
    ).then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const { request } = event;
  // Only handle GET; never intercept API, note or resource requests.
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  // API and dynamic content: network-first, fall back to cache if offline.
  if (url.pathname.startsWith("/n/") || url.pathname.startsWith("/api/")) {
    event.respondWith(
      fetch(request).catch(() => caches.match(request)),
    );
    return;
  }
  // Static shell: cache-first, refresh in background on miss.
  event.respondWith(
    caches.match(request).then((cached) => {
      const fetched = fetch(request).then((response) => {
        if (response.ok) {
          const clone = response.clone();
          caches.open(CACHE).then((cache) => cache.put(request, clone));
        }
        return response;
      }).catch(() => cached);
      return cached || fetched;
    }),
  );
});
