/* ============================================================
   Mercury WASM Bootstrap & UI Utilities

   Responsibilities:
     1. Load mercury.wasm and plutus.wasm via the Go WASM runtime
        (wasm_exec.js). The Go modules install window.Mercury and
        window.Plutus themselves via js.Global().Set(...). This file
        does NOT define, stub, or emulate any of those functions.
     2. Expose Mercury.init() / Mercury.ready — the only JS-side
        additions on the Mercury object. init() kicks off the WASM
        load; ready is a Promise that resolves when both modules
        have registered their globals.
     3. Load runtime configuration from ./config.json (relay
        bootstrap addresses, default chain, etc.). No defaults that
        fabricate sample data.
     4. Expose window.MercuryUI: toast, loading overlay, modal,
        DOM helpers. UI concerns only — no business logic.

   There is deliberately no demo data, no fallback state, and no
   mock async. If the WASM modules fail to load, the UI surfaces an
   error and calls to Mercury.* / Plutus.* will fail naturally.
   ============================================================ */

(function () {
  'use strict';

  // ------------------------------------------------------------
  // Configuration
  // ------------------------------------------------------------

  var defaultConfig = {
    mercuryWasm: 'mercury.wasm',
    plutusWasm:  'plutus.wasm',
    // Populated from ./config.json if present. No hardcoded
    // bootstrap peers or sample chain identifiers here.
    relays:      [],
    defaultChain: '',
    features:    {}
  };

  var config = null;

  function loadConfig() {
    return fetch('./config.json', { cache: 'no-cache' })
      .then(function (res) {
        if (!res.ok) {
          // Missing config.json is acceptable — run with defaults.
          if (res.status === 404) return {};
          throw new Error('config.json: HTTP ' + res.status);
        }
        return res.json();
      })
      .catch(function (err) {
        // A missing file also lands here in some browsers (file://).
        // A malformed file should not silently swallow; re-throw.
        if (err && err.name === 'SyntaxError') throw err;
        return {};
      })
      .then(function (override) {
        config = Object.assign({}, defaultConfig, override || {});
        // Expose the resolved config read-only.
        Object.freeze(config);
        window.MercuryConfig = config;
        return config;
      });
  }

  // ------------------------------------------------------------
  // WASM loading
  // ------------------------------------------------------------

  // Instantiate a Go WASM module. Returns a Promise that resolves
  // once the module's main() has started and has had a chance to
  // register its globals.
  function runGoWasm(url, label) {
    if (typeof Go !== 'function') {
      return Promise.reject(new Error(
        label + ': wasm_exec.js did not define global Go runtime'
      ));
    }

    var go = new Go();
    var fetchPromise;
    if (typeof WebAssembly.instantiateStreaming === 'function') {
      fetchPromise = WebAssembly.instantiateStreaming(
        fetch(url, { cache: 'no-cache' }),
        go.importObject
      ).catch(function (streamErr) {
        // Some servers (incl. GitHub Pages) may not send the correct
        // MIME type for .wasm. Fall back to ArrayBuffer instantiation.
        return fetch(url, { cache: 'no-cache' })
          .then(function (res) {
            if (!res.ok) {
              throw new Error(label + ': fetch ' + url + ' -> HTTP ' + res.status);
            }
            return res.arrayBuffer();
          })
          .then(function (buf) { return WebAssembly.instantiate(buf, go.importObject); });
      });
    } else {
      fetchPromise = fetch(url, { cache: 'no-cache' })
        .then(function (res) {
          if (!res.ok) {
            throw new Error(label + ': fetch ' + url + ' -> HTTP ' + res.status);
          }
          return res.arrayBuffer();
        })
        .then(function (buf) { return WebAssembly.instantiate(buf, go.importObject); });
    }

    return fetchPromise.then(function (result) {
      // Kick off the Go runtime. Do not await go.run — it blocks
      // for the lifetime of the module (select {}).
      go.run(result.instance).catch(function (err) {
        console.error('[' + label + '] runtime error:', err);
      });
      // Yield so main() can register its js.Global().Set(...) bindings
      // before we resolve. Two microtask-level yields are sufficient
      // for the Go scheduler to reach its select{}.
      return new Promise(function (resolve) {
        setTimeout(resolve, 0);
      });
    });
  }

  // Poll for a global becoming present. Rejects on timeout. Used
  // because Go's registration happens inside main() after go.run()
  // returns its initial task to the event loop.
  function waitForGlobal(name, timeoutMs) {
    var deadline = Date.now() + (timeoutMs || 5000);
    return new Promise(function (resolve, reject) {
      (function poll() {
        if (window[name] && typeof window[name] === 'object') {
          return resolve(window[name]);
        }
        if (Date.now() > deadline) {
          return reject(new Error(
            'WASM did not register window.' + name + ' within timeout'
          ));
        }
        // requestAnimationFrame if available, otherwise a short setTimeout.
        if (typeof requestAnimationFrame === 'function') {
          requestAnimationFrame(poll);
        } else {
          setTimeout(poll, 16);
        }
      })();
    });
  }

  // ------------------------------------------------------------
  // UI utilities
  // ------------------------------------------------------------

  function toast(message, type) {
    type = type || 'info';
    var container = document.getElementById('toast-container');
    if (!container) {
      // Fallback: create the container lazily so toasts can be
      // emitted even before the main layout renders.
      container = document.createElement('div');
      container.id = 'toast-container';
      document.body.appendChild(container);
    }
    var node = document.createElement('div');
    node.className = 'toast ' + type;
    node.textContent = message;
    container.appendChild(node);
    setTimeout(function () {
      node.classList.add('removing');
      setTimeout(function () { node.remove(); }, 300);
    }, 3500);
  }

  function showLoading(text) {
    var overlay = document.getElementById('loading-overlay');
    if (!overlay) return;
    overlay.classList.remove('hidden');
    var lt = overlay.querySelector('.loading-text');
    if (lt && text) lt.textContent = text;
  }

  function hideLoading() {
    var overlay = document.getElementById('loading-overlay');
    if (overlay) overlay.classList.add('hidden');
  }

  function renderStars(rating, max) {
    max = max || 5;
    var rounded = Math.round(Number(rating) || 0);
    var html = '<span class="stars">';
    for (var i = 1; i <= max; i++) {
      html += '<span class="star ' + (i <= rounded ? 'filled' : '') + '">\u2605</span>';
    }
    html += '</span>';
    return html;
  }

  function renderSkeleton(width, height) {
    return '<div class="skeleton" style="width:' + width + ';height:' + height + ';"></div>';
  }

  function el(tag, attrs, children) {
    var node = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (k) {
        var v = attrs[k];
        if (k === 'className')       node.className = v;
        else if (k === 'textContent') node.textContent = v;
        else if (k === 'innerHTML')   node.innerHTML = v;
        else if (k === 'style' && typeof v === 'string') node.setAttribute('style', v);
        else if (k.indexOf('on') === 0 && k.length > 2 && typeof v === 'function') {
          node.addEventListener(k.substring(2).toLowerCase(), v);
        } else {
          node.setAttribute(k, v);
        }
      });
    }
    if (children != null) {
      if (typeof children === 'string') {
        node.innerHTML = children;
      } else if (Array.isArray(children)) {
        children.forEach(function (c) {
          if (c == null) return;
          if (typeof c === 'string') node.appendChild(document.createTextNode(c));
          else node.appendChild(c);
        });
      } else {
        node.appendChild(children);
      }
    }
    return node;
  }

  function onReady(fn) {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', fn);
    } else {
      fn();
    }
  }

  function qs(sel, root)  { return (root || document).querySelector(sel); }
  function qsa(sel, root) {
    return Array.prototype.slice.call((root || document).querySelectorAll(sel));
  }

  // Simple modal helper. Expects an element with the given id in
  // the DOM; toggles the `hidden` class.
  function showModal(id) {
    var m = document.getElementById(id);
    if (m) m.classList.remove('hidden');
  }
  function hideModal(id) {
    var m = document.getElementById(id);
    if (m) m.classList.add('hidden');
  }

  // A delay helper. Retained because several UI flows legitimately
  // want a short pause for animation transitions. This is NOT used
  // to fake async responses from the WASM bridge.
  function delay(ms) {
    return new Promise(function (resolve) { setTimeout(resolve, ms); });
  }

  // ------------------------------------------------------------
  // Bootstrap
  // ------------------------------------------------------------

  var readyResolve, readyReject;
  var readyPromise = new Promise(function (resolve, reject) {
    readyResolve = resolve;
    readyReject  = reject;
  });

  var initPromise = null;

  function init() {
    if (initPromise) return initPromise;

    initPromise = loadConfig()
      .then(function (cfg) {
        // Load Mercury + Plutus in parallel. Each WASM module
        // blocks on select{} inside its main(), so they cannot be
        // chained serially via go.run().
        return Promise.all([
          runGoWasm(cfg.mercuryWasm, 'mercury.wasm'),
          runGoWasm(cfg.plutusWasm,  'plutus.wasm')
        ]);
      })
      .then(function () {
        return Promise.all([
          waitForGlobal('Mercury', 10000),
          waitForGlobal('Plutus',  10000)
        ]);
      })
      .then(function () {
        // Re-attach init/ready to the Mercury object installed by
        // the WASM module. The Go side only sets business functions.
        if (window.Mercury && typeof window.Mercury === 'object') {
          window.Mercury.init   = init;
          window.Mercury.ready  = readyPromise;
          window.Mercury.config = config;
        }
        readyResolve();
        console.log('[Mercury] WASM runtime ready');
      })
      .catch(function (err) {
        console.error('[Mercury] bootstrap failed:', err);
        readyReject(err);
        // Surface in the UI.
        toast('Mercury runtime failed to load: ' + (err && err.message ? err.message : err), 'error');
        var overlay = document.getElementById('loading-overlay');
        if (overlay) {
          var lt = overlay.querySelector('.loading-text');
          if (lt) lt.textContent = 'Runtime failed to load. Check console for details.';
        }
        throw err;
      });

    return initPromise;
  }

  // ------------------------------------------------------------
  // Exports
  // ------------------------------------------------------------

  // Install a minimal pre-boot shim on window.Mercury so that
  // callers loaded before WASM is ready can still reach init() and
  // ready. The Go module will overwrite this object's function
  // properties (but NOT the object identity, since we copy across
  // init/ready/config after load).
  //
  // Any business-logic call made before WASM registers will hit an
  // `undefined is not a function` error — we deliberately do NOT
  // stub those to preserve the "no mock logic" constraint.
  if (!window.Mercury) {
    window.Mercury = {
      init:  init,
      ready: readyPromise
    };
  } else {
    window.Mercury.init  = init;
    window.Mercury.ready = readyPromise;
  }

  window.MercuryUI = {
    toast:          toast,
    showLoading:    showLoading,
    hideLoading:    hideLoading,
    renderStars:    renderStars,
    renderSkeleton: renderSkeleton,
    el:             el,
    qs:             qs,
    qsa:            qsa,
    onReady:        onReady,
    showModal:      showModal,
    hideModal:      hideModal,
    delay:          delay
  };

})();
