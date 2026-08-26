/* ============================================================
   Helpers — shared utilities for consuming the real Mercury /
   Plutus WASM bridge data. Pure, side-effect free, no demo
   fallbacks. Loaded before all page-level module scripts.
   ============================================================ */

(function () {
  'use strict';

  // ------------------------------------------------------------
  // HTML escape (local copy — others also have their own `esc`)
  // ------------------------------------------------------------

  function esc(s) {
    if (s == null) return '';
    var d = document.createElement('div');
    d.textContent = String(s);
    return d.innerHTML;
  }

  // ------------------------------------------------------------
  // shortHex — abbreviate a hex string for display
  //   shortHex("a3f2...d091") -> "a3f2…d091"
  // ------------------------------------------------------------

  function shortHex(hex, head, tail) {
    if (!hex) return '';
    head = head == null ? 4 : head;
    tail = tail == null ? 4 : tail;
    var s = String(hex).replace(/^0x/, '');
    if (s.length <= head + tail + 1) return s;
    return s.substring(0, head) + '\u2026' + s.substring(s.length - tail);
  }

  // ------------------------------------------------------------
  // Relative time formatter. Accepts:
  //   - JS Date
  //   - seconds or milliseconds since epoch (numeric or numeric-ish string)
  //   - ISO 8601 string
  // Returns empty string for falsy / zero timestamps (useful for
  // order timeline steps that haven't happened yet).
  // ------------------------------------------------------------

  function toDate(ts) {
    if (ts == null || ts === '' || ts === 0 || ts === '0') return null;
    if (ts instanceof Date) return isNaN(ts.getTime()) ? null : ts;
    if (typeof ts === 'number') {
      // Treat "small" numbers as seconds since epoch.
      var ms = ts < 1e12 ? ts * 1000 : ts;
      var d = new Date(ms);
      return isNaN(d.getTime()) ? null : d;
    }
    if (typeof ts === 'string') {
      if (/^\d+$/.test(ts)) return toDate(parseInt(ts, 10));
      var d = new Date(ts);
      return isNaN(d.getTime()) ? null : d;
    }
    return null;
  }

  function formatRelativeTime(ts) {
    var d = toDate(ts);
    if (!d) return '';
    var diff = (Date.now() - d.getTime()) / 1000;
    if (diff < 0) diff = 0;
    if (diff < 45) return 'now';
    if (diff < 60 * 60) return Math.round(diff / 60) + 'm';
    if (diff < 60 * 60 * 24) return Math.round(diff / 3600) + 'h';
    if (diff < 60 * 60 * 24 * 7) return Math.round(diff / 86400) + 'd';
    // Older: absolute date, ISO-style.
    var yy = d.getFullYear();
    var mm = String(d.getMonth() + 1).padStart(2, '0');
    var dd = String(d.getDate()).padStart(2, '0');
    return yy + '-' + mm + '-' + dd;
  }

  function formatDateTime(ts) {
    var d = toDate(ts);
    if (!d) return '';
    return d.toLocaleString();
  }

  // ------------------------------------------------------------
  // formatPrice — renders amount + currency code. Never USD.
  //   amount may be a string (raw on-chain integer units) or number.
  //   decimals (optional) shifts the decimal point.
  // ------------------------------------------------------------

  function formatPrice(amount, currency, decimals) {
    if (amount == null || amount === '') return '';
    var n;
    if (typeof amount === 'number') {
      n = amount;
    } else {
      var s = String(amount).replace(/[^0-9eE+\-.]/g, '');
      n = parseFloat(s);
    }
    if (!isFinite(n)) return String(amount) + (currency ? ' ' + currency : '');
    if (decimals && decimals > 0) n = n / Math.pow(10, decimals);

    var text;
    if (Math.abs(n) >= 1) {
      text = n.toFixed(6).replace(/\.?0+$/, '');
    } else {
      text = n.toPrecision(4).replace(/\.?0+$/, '');
    }
    return text + (currency ? ' ' + currency : '');
  }

  // ------------------------------------------------------------
  // Profile cache. Keyed by pubkey hex. `getProfile` always
  // returns a Promise for the actual WASM call result (or null),
  // but subsequent calls return the cached result synchronously
  // wrapped in a Promise.
  // ------------------------------------------------------------

  var profileCache = new Map();
  var profileInflight = new Map();

  function getProfile(pubkey) {
    if (!pubkey) return Promise.resolve(null);
    if (profileCache.has(pubkey)) return Promise.resolve(profileCache.get(pubkey));
    if (profileInflight.has(pubkey)) return profileInflight.get(pubkey);
    if (!window.Mercury || typeof window.Mercury.getProfile !== 'function') {
      return Promise.resolve(null);
    }
    var p = Promise.resolve(window.Mercury.getProfile(pubkey))
      .then(function (profile) {
        profileCache.set(pubkey, profile || null);
        profileInflight.delete(pubkey);
        return profile || null;
      })
      .catch(function (err) {
        profileInflight.delete(pubkey);
        // Cache the null so we don't spam retries in the same session.
        profileCache.set(pubkey, null);
        console.warn('[helpers] getProfile failed for', shortHex(pubkey), err);
        return null;
      });
    profileInflight.set(pubkey, p);
    return p;
  }

  function displayName(pubkey, profile) {
    if (profile && profile.name) return profile.name;
    return shortHex(pubkey);
  }

  // ------------------------------------------------------------
  // Listing cache
  // ------------------------------------------------------------

  var listingCache = new Map();
  var listingInflight = new Map();

  function getListing(id) {
    if (!id) return Promise.resolve(null);
    if (listingCache.has(id)) return Promise.resolve(listingCache.get(id));
    if (listingInflight.has(id)) return listingInflight.get(id);
    if (!window.Mercury || typeof window.Mercury.getListing !== 'function') {
      return Promise.resolve(null);
    }
    var p = Promise.resolve(window.Mercury.getListing(id))
      .then(function (listing) {
        listingCache.set(id, listing || null);
        listingInflight.delete(id);
        return listing || null;
      })
      .catch(function (err) {
        listingInflight.delete(id);
        listingCache.set(id, null);
        console.warn('[helpers] getListing failed for', id, err);
        return null;
      });
    listingInflight.set(id, p);
    return p;
  }

  // Reputation is fetched fresh per view (no cache — score changes).
  function getReputation(pubkey) {
    if (!pubkey || !window.Mercury || typeof window.Mercury.getReputation !== 'function') {
      return Promise.resolve(null);
    }
    return Promise.resolve(window.Mercury.getReputation(pubkey))
      .catch(function (err) {
        console.warn('[helpers] getReputation failed for', shortHex(pubkey), err);
        return null;
      });
  }

  // ------------------------------------------------------------
  // Chain icon table. Purely presentational.
  // ------------------------------------------------------------

  var chainIcons = {
    polygon:  '\u25ce',   // ◎
    ethereum: '\u039e',   // Ξ
    bitcoin:  '\u20bf',   // ₿
    solana:   '\u25ce',   // ◎
    arbitrum: '\u039e',   // Ξ
    ETH:      '\u039e',
    BTC:      '\u20bf',
    SOL:      '\u25ce',
    ARB:      '\u039e',
    POLYGON:  '\u25ce',
  };

  var chainColors = {
    polygon:  '#8247e5',
    ethereum: '#627eea',
    bitcoin:  '#f7931a',
    solana:   '#9945ff',
    arbitrum: '#28a0f0',
    ETH:      '#627eea',
    BTC:      '#f7931a',
    SOL:      '#9945ff',
    ARB:      '#28a0f0',
    POLYGON:  '#8247e5',
  };

  function chainIcon(chainID) {
    if (!chainID) return '?';
    var k = String(chainID);
    return chainIcons[k] || chainIcons[k.toLowerCase()] || '?';
  }

  function chainColor(chainID) {
    if (!chainID) return 'var(--accent)';
    var k = String(chainID);
    return chainColors[k] || chainColors[k.toLowerCase()] || 'var(--accent)';
  }

  // ------------------------------------------------------------
  // IPFS gateway helpers. Allow configuration via
  // Mercury.config.ipfsGateway (trailing slash optional). Falls back
  // to ipfs.io.
  // ------------------------------------------------------------

  function ipfsGateway() {
    var cfg = (window.Mercury && window.Mercury.config) || window.MercuryConfig || {};
    var g = cfg.ipfsGateway || 'https://ipfs.io/ipfs/';
    return g.charAt(g.length - 1) === '/' ? g : g + '/';
  }

  // Validate a content address so we never accept `javascript:`,
  // `data:` or other injection vectors. Accepts:
  //   - CIDv0  (Qm… 46-char base58)
  //   - CIDv1  (b… multibase, alnum)
  //   - path-suffixed variants like "bafy…/thumb.jpg"
  // Anything else → treated as invalid and returns empty.
  var CID_RE = /^(Qm[1-9A-HJ-NP-Za-km-z]{44}|b[a-z2-7]{20,})(\/[\w.\-\/]*)?$/;

  function isSafeCid(s) {
    return typeof s === 'string' && CID_RE.test(s);
  }

  function ipfsUrl(cid) {
    if (!cid) return '';
    // Accept either a raw CID or an already-formed ipfs:// or https URL.
    var s = String(cid).trim();
    // Explicit https[s]://<host>/… only — never allow other schemes
    // (e.g. javascript:, data:, vbscript:) into src= attributes.
    if (/^https?:\/\//i.test(s)) return s;
    if (/^ipfs:\/\//i.test(s)) {
      var stripped = s.replace(/^ipfs:\/\//i, '');
      if (!isSafeCid(stripped)) return '';
      return ipfsGateway() + stripped;
    }
    if (!isSafeCid(s)) return '';
    return ipfsGateway() + s;
  }

  // ------------------------------------------------------------
  // Deterministic identicon — hashes the pubkey to a small grid
  // of coloured cells. Pure SVG string.
  // ------------------------------------------------------------

  function hashString(str) {
    // Simple 32-bit FNV-1a — deterministic, no dependency.
    var h = 0x811c9dc5;
    for (var i = 0; i < str.length; i++) {
      h ^= str.charCodeAt(i);
      h = (h + ((h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24))) >>> 0;
    }
    return h >>> 0;
  }

  function identicon(pubkey, size) {
    size = size || 40;
    var key = String(pubkey || '');
    var h = hashString(key);
    // HSL hue from top bits; keep saturation/lightness fixed-ish.
    var hue = h % 360;
    var fg = 'hsl(' + hue + ', 55%, 48%)';
    var bg = 'hsl(' + hue + ', 30%, 92%)';

    // 5x5 mirrored grid (classic identicon layout).
    var cells = [];
    for (var row = 0; row < 5; row++) {
      for (var col = 0; col < 3; col++) {
        // Re-hash per cell for better distribution.
        var bit = (hashString(key + ':' + row + ':' + col) & 1) === 1;
        cells.push(bit);
      }
    }

    var cell = size / 5;
    var rects = '';
    for (var r = 0; r < 5; r++) {
      for (var c = 0; c < 3; c++) {
        if (!cells[r * 3 + c]) continue;
        var x1 = c * cell;
        var x2 = (4 - c) * cell; // mirror
        rects += '<rect x="' + x1 + '" y="' + (r * cell) + '" width="' + cell + '" height="' + cell + '" fill="' + fg + '"/>';
        if (c !== 2) {
          rects += '<rect x="' + x2 + '" y="' + (r * cell) + '" width="' + cell + '" height="' + cell + '" fill="' + fg + '"/>';
        }
      }
    }

    return (
      '<svg xmlns="http://www.w3.org/2000/svg" width="' + size + '" height="' + size + '" viewBox="0 0 ' + size + ' ' + size + '" role="img" aria-label="identicon">' +
        '<rect width="' + size + '" height="' + size + '" fill="' + bg + '"/>' +
        rects +
      '</svg>'
    );
  }

  // ------------------------------------------------------------
  // Avatar HTML — IPFS avatarCid if present, else identicon.
  // ------------------------------------------------------------

  function avatarHtml(pubkey, profile, size) {
    size = size || 40;
    if (profile && profile.avatarCid) {
      var url = ipfsUrl(profile.avatarCid);
      return '<img class="avatar-img" src="' + esc(url) + '" width="' + size + '" height="' + size + '" alt="" style="width:' + size + 'px;height:' + size + 'px;border-radius:50%;object-fit:cover;">';
    }
    return '<span class="avatar-identicon" style="display:inline-block;width:' + size + 'px;height:' + size + 'px;border-radius:50%;overflow:hidden;vertical-align:middle;">' + identicon(pubkey, size) + '</span>';
  }

  // ------------------------------------------------------------
  // Order timeline reconstruction. Maps the protocol status +
  // timestamps to a list of display steps with done flags.
  //
  // Non-terminal branch statuses (Disputed, Refunded) are added
  // only if their respective timestamp is present.
  // ------------------------------------------------------------

  function orderTimeline(order) {
    if (!order) return [];
    function stamp(name) {
      return order[name];
    }
    var steps = [
      { key: 'placed',    label: 'Order placed',             ts: stamp('ts') || stamp('placedAt') || stamp('createdAt') },
      { key: 'funded',    label: 'Funds locked in escrow',   ts: stamp('fundedAt') },
      { key: 'accepted',  label: 'Accepted by seller',       ts: stamp('acceptedAt') },
      { key: 'fulfilled', label: 'Item delivered',           ts: stamp('fulfilledAt') },
      { key: 'completed', label: 'Receipt confirmed',        ts: stamp('completedAt') },
    ];

    if (stamp('disputedAt')) {
      steps.push({ key: 'disputed', label: 'Dispute filed', ts: stamp('disputedAt') });
    }
    if (stamp('refundedAt')) {
      steps.push({ key: 'refunded', label: 'Refunded', ts: stamp('refundedAt') });
    }

    return steps.map(function (s) {
      return {
        step: s.label,
        key:  s.key,
        time: s.ts ? formatDateTime(s.ts) : '',
        ts:   s.ts || null,
        done: !!toDate(s.ts),
      };
    });
  }

  // ------------------------------------------------------------
  // Unread-tracking for conversations. Stored in localStorage,
  // keyed per peer pubkey.
  // ------------------------------------------------------------

  var LS_READ_PREFIX = 'mercury.msg.lastRead.';

  function getLastRead(peerId) {
    if (!peerId) return 0;
    try {
      var v = window.localStorage.getItem(LS_READ_PREFIX + peerId);
      return v ? parseInt(v, 10) || 0 : 0;
    } catch (_) { return 0; }
  }

  function setLastRead(peerId, tsMillis) {
    if (!peerId) return;
    try {
      window.localStorage.setItem(LS_READ_PREFIX + peerId, String(tsMillis || Date.now()));
    } catch (_) {}
  }

  function computeUnread(peerId, messages) {
    if (!messages || !messages.length) return 0;
    var lastRead = getLastRead(peerId);
    var count = 0;
    for (var i = 0; i < messages.length; i++) {
      var m = messages[i];
      if (m.from === 'me') continue;
      var d = toDate(m.ts || m.time || m.timestamp);
      if (!d) continue;
      if (d.getTime() > lastRead) count++;
    }
    return count;
  }

  // ------------------------------------------------------------
  // renderStarRating — compact 5-star renderer driven by a float.
  // Kept as plain HTML so it composes with existing innerHTML usage.
  // ------------------------------------------------------------

  function renderStarsFloat(rating, max) {
    max = max || 5;
    var r = Number(rating) || 0;
    var html = '<span class="stars">';
    for (var i = 1; i <= max; i++) {
      var filled = i <= Math.round(r);
      html += '<span class="star ' + (filled ? 'filled' : '') + '">\u2605</span>';
    }
    html += '</span>';
    return html;
  }

  // ------------------------------------------------------------
  // Exports
  // ------------------------------------------------------------

  window.MercuryHelpers = {
    esc:                esc,
    escapeHTML:         esc,   // explicit alias
    isSafeCid:          isSafeCid,
    shortHex:           shortHex,
    toDate:             toDate,
    formatRelativeTime: formatRelativeTime,
    formatDateTime:     formatDateTime,
    formatPrice:        formatPrice,
    getProfile:         getProfile,
    getListing:         getListing,
    getReputation:      getReputation,
    displayName:        displayName,
    profileCache:       profileCache,
    listingCache:       listingCache,
    chainIcon:          chainIcon,
    chainColor:         chainColor,
    ipfsGateway:        ipfsGateway,
    ipfsUrl:            ipfsUrl,
    identicon:          identicon,
    avatarHtml:         avatarHtml,
    orderTimeline:      orderTimeline,
    getLastRead:        getLastRead,
    setLastRead:        setLastRead,
    computeUnread:      computeUnread,
    renderStarsFloat:   renderStarsFloat,
  };

})();
