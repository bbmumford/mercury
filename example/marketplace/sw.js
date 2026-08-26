/* ============================================================
   Mercury Marketplace — Service Worker

   Strategy:
     - Static shell (index.html, css/*, js/*): cache-first with
       background refresh. Survives offline.
     - WASM payload (mercury.wasm, wasm_exec.js): network-first so
       new builds propagate; falls back to cache when offline.
     - Operator config (config.json): network-first so runtime
       config changes (e.g. IPFS gateway) take effect on next load.
     - Everything else (IPFS gateway fetches, API calls): passthrough.

   Bump CACHE_VERSION on every deploy to invalidate old caches.
   ============================================================ */

'use strict';

const CACHE_VERSION = 'mercury-mkt-v1.0.0-2026-04-15';
const STATIC_CACHE  = CACHE_VERSION + '-static';
const RUNTIME_CACHE = CACHE_VERSION + '-runtime';

// Static assets pre-cached on install. Paths are relative to the
// service worker's scope so the whole example works when served
// from a subdirectory (e.g. GitHub Pages).
const STATIC_ASSETS = [
  './',
  './index.html',
  './manifest.json',
  './css/style.css',
  './js/mercury.js',
  './js/helpers.js',
  './js/auth.js',
  './js/store.js',
  './js/browse.js',
  './js/order.js',
  './js/wallet.js',
  './js/messaging.js',
  './js/reputation.js',
  './js/arbitration.js',
  './js/settings.js',
  './js/app.js',
  './wasm_exec.js',
];

// Network-first paths. Suffix match against request URL pathname.
const NETWORK_FIRST_SUFFIXES = [
  '/config.json',
  '/mercury.wasm',
  '.wasm',
];

// ---- Install: pre-cache the shell ----

self.addEventListener('install', function (event) {
  event.waitUntil(
    caches.open(STATIC_CACHE).then(function (cache) {
      // addAll is atomic — if any asset fails, install fails and
      // the new SW is discarded. We tolerate individual 404s by
      // adding each entry independently.
      return Promise.all(
        STATIC_ASSETS.map(function (url) {
          return cache.add(new Request(url, { cache: 'reload' })).catch(function (err) {
            console.warn('[sw] pre-cache failed for', url, err);
          });
        })
      );
    }).then(function () {
      // Activate immediately on first install so the page is
      // controlled without a reload.
      return self.skipWaiting();
    })
  );
});

// ---- Activate: delete stale caches ----

self.addEventListener('activate', function (event) {
  event.waitUntil(
    caches.keys().then(function (keys) {
      return Promise.all(
        keys.map(function (k) {
          if (k !== STATIC_CACHE && k !== RUNTIME_CACHE) {
            return caches.delete(k);
          }
        })
      );
    }).then(function () {
      return self.clients.claim();
    })
  );
});

// ---- Fetch helpers ----

function isNetworkFirst(url) {
  var p = url.pathname || '';
  for (var i = 0; i < NETWORK_FIRST_SUFFIXES.length; i++) {
    if (p.endsWith(NETWORK_FIRST_SUFFIXES[i])) return true;
  }
  return false;
}

function cacheFirst(event) {
  return caches.match(event.request).then(function (cached) {
    if (cached) {
      // Background refresh — swallow errors, we already have a hit.
      event.waitUntil((async function () {
        try {
          var fresh = await fetch(event.request);
          if (fresh && fresh.ok) {
            var cache = await caches.open(STATIC_CACHE);
            await cache.put(event.request, fresh.clone());
          }
        } catch (_) { /* offline; ignore */ }
      })());
      return cached;
    }
    return fetch(event.request).then(function (resp) {
      if (resp && resp.ok) {
        var clone = resp.clone();
        caches.open(STATIC_CACHE).then(function (cache) {
          cache.put(event.request, clone);
        });
      }
      return resp;
    });
  });
}

function networkFirst(event) {
  return fetch(event.request).then(function (resp) {
    if (resp && resp.ok) {
      var clone = resp.clone();
      caches.open(RUNTIME_CACHE).then(function (cache) {
        cache.put(event.request, clone);
      });
    }
    return resp;
  }).catch(function () {
    return caches.match(event.request);
  });
}

// ---- Fetch handler ----

self.addEventListener('fetch', function (event) {
  var req = event.request;

  // Only intercept GETs — everything else goes to network.
  if (req.method !== 'GET') return;

  var url;
  try { url = new URL(req.url); } catch (_) { return; }

  // Skip cross-origin (IPFS gateways, WebRTC signaling, etc.).
  if (url.origin !== self.location.origin) return;

  if (isNetworkFirst(url)) {
    event.respondWith(networkFirst(event));
    return;
  }

  // Everything else served by this origin is treated as part of
  // the app shell → cache-first with background refresh.
  event.respondWith(cacheFirst(event));
});

// ---- Optional message handler for skipWaiting from client ----

self.addEventListener('message', function (event) {
  if (event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
});
