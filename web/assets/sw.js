// vpn-ui service worker: caches the panel's static assets so the login page
// and the CSS/JS bootstrap survive a slow or flaky connection. The panel
// itself stays online (the API is not cached: a stale response is worse
// than a failed one, and the operator's "active sessions" / "traffic" views
// must read live data).
//
// The cache is versioned by the asset fingerprint the panel stamps on every
// asset URL (?v=...). A deploy that changes the fingerprint busts the cache
// without a hard refresh: the worker sees a new URL and fetches it, and the
// old version is evicted on the next cache update.
//
// Strategy:
//   - Network-first for the page (the index): a cached copy of a stale panel
//     is a lie the operator does not want; a fresh fetch is what they asked
//     for when they opened the app.
//   - Cache-first for static assets (CSS/JS/fonts/images under /assets/):
//     they are content-hashed by the asset fingerprint, so a "cached" asset
//     is the same asset the panel just served, and the cache is what keeps
//     the page fast on the second visit.

const CACHE_NAME = 'vpn-ui-v1';

// The asset prefixes that are safe to cache-first: every URL the panel
// stamps with ?v=<fingerprint> is under one of these.
const CACHABLE_PREFIXES = [
  '/assets/css/',
  '/assets/js/',
  '/assets/vue/',
  '/assets/axios/',
  '/assets/ant-design-vue/',
  '/assets/img/',
  '/assets/jspdf/',
  '/assets/qrcode/',
  '/assets/qrious/',
  '/assets/codemirror/',
  '/assets/sockjs/',
  '/assets/terminal/',
];

function isCachable(url) {
  return CACHABLE_PREFIXES.some(p => url.startsWith(p));
}

self.addEventListener('install', (event) => {
  // Pre-cache nothing on install: the panel's first visit fetches every
  // asset it needs anyway, and the cache fills in as the operator browses.
  // A pre-cache of a stale fingerprint would evict the live one on a
  // version bump.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  // Evict every cache that is not the current version, so an old worker's
  // cache (a previous asset fingerprint) does not linger.
  event.waitUntil(
    caches.keys().then(keys => Promise.all(
      keys.filter(k => k !== CACHE_NAME).map(k => caches.delete(k))
    ))
  );
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  // Only same-origin GETs are in scope: a cross-origin asset (a CDN, a
  // font from a third party) is the browser's to manage, and POST/PUT
  // requests (the panel's API calls) must never be served from cache.
  if (url.origin !== location.origin || event.request.method !== 'GET') {
    return;
  }
  if (isCachable(url.pathname)) {
    // Cache-first: the asset is content-hashed by the fingerprint, so a
    // cache hit is the same bytes the panel would serve. A miss fetches
    // and fills the cache for the next visit.
    event.respondWith(
      caches.match(event.request).then(cached => {
        if (cached) {
          return cached;
        }
        return fetch(event.request).then(resp => {
          // Only cache a successful response: a 404 or 500 is not an asset,
          // and serving it from cache would paper over the real problem.
          if (resp.ok) {
            const clone = resp.clone();
            caches.open(CACHE_NAME).then(cache => cache.put(event.request, clone));
          }
          return resp;
        });
      })
    );
    return;
  }
  // The page itself (the index, the login, /panel/): network-first. A
  // cached page is a stale panel, and the operator wants the fresh one.
  // Fall back to the cache only when the network is unreachable (airplane
  // mode, a dead connection), so the panel still opens, just with the
  // last-known HTML.
  event.respondWith(
    fetch(event.request).then(resp => {
      if (resp.ok) {
        const clone = resp.clone();
        caches.open(CACHE_NAME).then(cache => cache.put(event.request, clone));
      }
      return resp;
    }).catch(() => {
      return caches.match(event.request);
    })
  );
});
