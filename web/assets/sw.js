// vpn-ui service worker: caches the panel's STATIC ASSETS so a repeat visit
// boots without re-downloading 9MB of CSS/JS on a slow link.
//
// What it must never do (and used to, until 2.8.1):
//
//   - Serve a cached copy of a data endpoint. The API answers with live
//     sessions, traffic and inbounds; a cached answer is a lie. The previous
//     handler cached EVERY successful same-origin GET, so /panel/api/... and
//     every /panel/setting/... response landed in the cache too.
//   - Answer a request with `undefined`. The fallback was
//     `respondWith(caches.match(req))`, and with no cache entry that resolves
//     to undefined, which fails the fetch mid-flight. On a page whose whole
//     body is behind "data arrived", one failed request is a blank panel.
//   - Cache a page document. A stale copy of the panel HTML can reference an
//     asset fingerprint the running binary no longer serves.
//
// So the rules are narrow now:
//
//   - /assets/... (content-addressed by ?v=<asset fingerprint>): cache-first.
//     A new build changes the URL, so a hit is always the same bytes.
//   - A navigation (opening the panel itself): network-only, with an explicit
//     offline reply when the network is gone. Never a cached document.
//   - Everything else, including every /panel/api, /panel/setting, /panel/xray,
//     /ws and /sub request: left to the browser, untouched.
//
// CACHE_NAME is bumped on a behaviour change so a worker that cached API
// responses self-evicts on its next activation.

const CACHE_NAME = 'vpn-ui-v2';

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
  return CACHABLE_PREFIXES.some(p => url.includes(p));
}

self.addEventListener('install', (event) => {
  // Pre-cache nothing on install: the panel's first visit fetches every
  // asset it needs anyway, and the cache fills in as the operator browses.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  // Evict every cache that is not the current version, so an old worker's
  // cache (a previous asset fingerprint, or the API responses v1 stored)
  // does not linger.
  event.waitUntil(
    caches.keys().then(keys => Promise.all(
      keys.filter(k => k !== CACHE_NAME).map(k => caches.delete(k))
    ))
  );
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
  const request = event.request;

  // Only plain same-origin GETs are in scope. POST/PUT (every write, and the
  // login form) never touches the cache.
  if (request.method !== 'GET') {
    return;
  }
  const url = new URL(request.url);
  if (url.origin !== location.origin) {
    return;
  }

  // The panel document: always the network. An offline operator gets an
  // honest "offline" answer instead of a stale panel that pretends to work.
  if (request.mode === 'navigate') {
    event.respondWith(
      fetch(request).catch(() =>
        new Response(
          'The panel is unreachable right now. Check the server, then reload.',
          { status: 503, statusText: 'Offline', headers: { 'Content-Type': 'text/plain; charset=utf-8' } }
        )
      )
    );
    return;
  }

  // Everything that is not a static asset (the API, settings, xray, the
  // subscription server, websockets) is the network's: no cache read, no
  // cache write. Returning without respondWith lets the browser handle it.
  if (!isCachable(url.pathname)) {
    return;
  }

  // Cache-first for assets: the fingerprint in the URL is what makes this
  // safe, and a hit is always the same bytes the panel would serve.
  event.respondWith(
    caches.match(request).then(cached => {
      if (cached) {
        return cached;
      }
      return fetch(request).then(resp => {
        // Only cache a genuine same-origin success. A 404 HTML error page or
        // an opaque redirect is not an asset, and caching it would paper over
        // the real problem on every later visit.
        if (resp && resp.ok && resp.type === 'basic') {
          const clone = resp.clone();
          caches.open(CACHE_NAME)
            .then(cache => cache.put(request, clone))
            .catch(() => { /* a full or disabled cache must not break the load */ });
        }
        return resp;
      });
      // No `.catch(() => caches.match(...))` here: a miss is a miss. Letting
      // the network error propagate is what lets the page report it.
    })
  );
});
