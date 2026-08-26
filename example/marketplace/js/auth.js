/* ============================================================
   auth.js -- Identity management for Mercury marketplace demo
   Account creation, login, session management
   ============================================================ */

(function () {
  'use strict';

  var toast = window.MercuryUI.toast;
  var el    = window.MercuryUI.el;

  // ---- IndexedDB helpers ----

  var DB_NAME = 'mercury_identity';
  var DB_STORE = 'identity';

  function openDB() {
    return new Promise(function (resolve, reject) {
      var req = indexedDB.open(DB_NAME, 1);
      req.onupgradeneeded = function () {
        var db = req.result;
        if (!db.objectStoreNames.contains(DB_STORE)) {
          db.createObjectStore(DB_STORE);
        }
      };
      req.onsuccess = function () { resolve(req.result); };
      req.onerror = function () { reject(req.error); };
    });
  }

  function dbPut(key, value) {
    return openDB().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(DB_STORE, 'readwrite');
        tx.objectStore(DB_STORE).put(value, key);
        tx.oncomplete = function () { resolve(); };
        tx.onerror = function () { reject(tx.error); };
      });
    });
  }

  function dbGet(key) {
    return openDB().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(DB_STORE, 'readonly');
        var req = tx.objectStore(DB_STORE).get(key);
        req.onsuccess = function () { resolve(req.result); };
        req.onerror = function () { reject(req.error); };
      });
    });
  }

  function dbDelete(key) {
    return openDB().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(DB_STORE, 'readwrite');
        tx.objectStore(DB_STORE).delete(key);
        tx.oncomplete = function () { resolve(); };
        tx.onerror = function () { reject(tx.error); };
      });
    });
  }

  // ---- DOM helpers ----

  function authScreen() { return document.getElementById('auth-screen'); }

  function showAuth() {
    var screen = authScreen();
    screen.classList.remove('hidden');
    document.getElementById('main-nav').classList.add('hidden');
  }

  function hideAuth() {
    var screen = authScreen();
    screen.classList.add('hidden');
    document.getElementById('main-nav').classList.remove('hidden');
  }

  // ---- Auth Screen (landing) ----

  function showAuthScreen() {
    showAuth();
    var screen = authScreen();
    screen.innerHTML = '';

    var container = el('div', { className: 'auth-container' });

    var state = (window.Mercury && window.Mercury.state) || {};
    var hasPasskey = !!(state.passkeys && state.passkeys.length > 0);

    var html =
      '<div class="auth-logo">&#9791;</div>' +
      '<h1 class="auth-title">Mercury</h1>' +
      '<p class="auth-subtitle">Decentralised peer-to-peer exchange. Your keys, your identity.</p>' +
      '<div class="auth-actions">' +
        '<button class="btn btn-primary btn-lg btn-full" id="auth-create">Create New Identity</button>' +
        '<button class="btn btn-secondary btn-lg btn-full" id="auth-import">Restore from Mnemonic</button>';

    if (hasPasskey) {
      html += '<button class="btn btn-outline btn-lg btn-full" id="auth-passkey">Login with Passkey</button>';
    }

    html +=
      '</div>';

    container.innerHTML = html;
    screen.appendChild(container);

    document.getElementById('auth-create').addEventListener('click', showCreateAccount);
    document.getElementById('auth-import').addEventListener('click', showImportAccount);

    var passkeyBtn = document.getElementById('auth-passkey');
    if (passkeyBtn) {
      passkeyBtn.addEventListener('click', loginWithPasskey);
    }

    // Also check IndexedDB for a stored identity and show passkey option
    hasStoredIdentity().then(function (stored) {
      if (stored && !hasPasskey) {
        var actions = container.querySelector('.auth-actions');
        if (actions && !document.getElementById('auth-passkey')) {
          var btn = el('button', {
            className: 'btn btn-outline btn-lg btn-full',
            id: 'auth-passkey',
            textContent: 'Login with Passkey'
          });
          actions.appendChild(btn);
          btn.addEventListener('click', loginWithPasskey);
        }
      }
    });
  }

  // ---- Create Account ----

  function showCreateAccount() {
    var screen = authScreen();
    screen.innerHTML = '';

    var words = window.Mercury.generateMnemonic();

    var container = el('div', { className: 'auth-container' });

    // Build mnemonic grid
    var wordsHtml = '';
    words.forEach(function (w, i) {
      wordsHtml += '<span class="mnemonic-word"><span class="word-num">' + (i + 1) + '.</span> ' + w + '</span>';
    });

    container.innerHTML =
      '<h1 class="auth-title">Your Recovery Phrase</h1>' +
      '<p class="auth-subtitle">Write down these 24 words in order. They are the only way to recover your identity.</p>' +
      '<div class="mnemonic-display">' +
        '<div style="font-weight:600;color:var(--warning);margin-bottom:8px;">&#9888; Save these words securely</div>' +
        '<div class="mnemonic-grid">' + wordsHtml + '</div>' +
      '</div>' +
      '<label class="form-checkbox" style="margin:16px 0;display:flex;align-items:center;gap:8px;cursor:pointer;color:var(--text-secondary);">' +
        '<input type="checkbox" id="auth-saved-check">' +
        '<span>I have saved these words in a safe location</span>' +
      '</label>' +
      '<div class="auth-actions">' +
        '<button class="btn btn-primary btn-lg btn-full" id="auth-confirm-create" disabled>I\'ve Saved My Recovery Phrase</button>' +
        '<button class="btn btn-ghost btn-full" id="auth-back-create">Back</button>' +
      '</div>';

    screen.appendChild(container);

    // Checkbox enables the confirm button
    var checkbox = document.getElementById('auth-saved-check');
    var confirmBtn = document.getElementById('auth-confirm-create');

    checkbox.addEventListener('change', function () {
      confirmBtn.disabled = !checkbox.checked;
    });

    confirmBtn.addEventListener('click', function () {
      var btn = this;
      btn.disabled = true;
      btn.textContent = 'Creating identity...';

      window.Mercury.createIdentity(words)
        .then(function (user) {
          // Persist identity reference to IndexedDB
          return dbPut('current', {
            pubkey: user.pubkey,
            displayName: user.displayName,
            createdAt: new Date().toISOString()
          });
        })
        .then(function () {
          toast('Identity created successfully', 'success');
          showPasskeySetup();
        })
        .catch(function (err) {
          toast('Failed to create identity: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'I\'ve Saved My Recovery Phrase';
        });
    });

    document.getElementById('auth-back-create').addEventListener('click', showAuthScreen);
  }

  // ---- Import Account ----

  function showImportAccount() {
    var screen = authScreen();
    screen.innerHTML = '';

    var container = el('div', { className: 'auth-container' });

    container.innerHTML =
      '<h1 class="auth-title">Restore Identity</h1>' +
      '<p class="auth-subtitle">Enter your 24-word recovery phrase to restore your identity.</p>' +
      '<div class="form-group" style="text-align:left;">' +
        '<label class="form-label">Recovery phrase</label>' +
        '<textarea class="form-input mnemonic-input" id="import-mnemonic" placeholder="word1 word2 word3 ... (24 words)" rows="4" style="resize:vertical;"></textarea>' +
        '<p class="form-help" style="color:var(--text-muted);font-size:0.85rem;margin-top:6px;">Separate each word with a space.</p>' +
        '<p class="form-error hidden" id="import-error" style="color:var(--danger);font-size:0.85rem;margin-top:6px;"></p>' +
      '</div>' +
      '<div class="auth-actions">' +
        '<button class="btn btn-primary btn-lg btn-full" id="auth-confirm-import">Restore Identity</button>' +
        '<button class="btn btn-ghost btn-full" id="auth-back-import">Back</button>' +
      '</div>';

    screen.appendChild(container);

    // Live word count feedback
    var textarea = document.getElementById('import-mnemonic');
    var errEl = document.getElementById('import-error');

    textarea.addEventListener('input', function () {
      var count = textarea.value.trim().split(/\s+/).filter(Boolean).length;
      if (count > 0 && count !== 24) {
        errEl.textContent = count + ' of 24 words entered';
        errEl.classList.remove('hidden');
        errEl.style.color = 'var(--text-muted)';
      } else {
        errEl.classList.add('hidden');
      }
    });

    document.getElementById('auth-confirm-import').addEventListener('click', function () {
      var btn = this;
      var input = textarea.value.trim();
      var wordCount = input.split(/\s+/).filter(Boolean).length;

      if (wordCount !== 24) {
        errEl.textContent = 'Expected 24 words, got ' + wordCount + '.';
        errEl.style.color = 'var(--danger)';
        errEl.classList.remove('hidden');
        return;
      }

      errEl.classList.add('hidden');
      btn.disabled = true;
      btn.textContent = 'Restoring...';

      window.Mercury.importIdentity(input)
        .then(function (user) {
          return dbPut('current', {
            pubkey: user.pubkey,
            displayName: user.displayName,
            createdAt: new Date().toISOString()
          });
        })
        .then(function () {
          toast('Identity restored successfully', 'success');
          finishAuth();
        })
        .catch(function (err) {
          errEl.textContent = err.message;
          errEl.style.color = 'var(--danger)';
          errEl.classList.remove('hidden');
          btn.disabled = false;
          btn.textContent = 'Restore Identity';
        });
    });

    document.getElementById('auth-back-import').addEventListener('click', showAuthScreen);
  }

  // ---- Passkey Setup (offered after account creation) ----

  function showPasskeySetup() {
    var screen = authScreen();
    screen.innerHTML = '';

    var container = el('div', { className: 'auth-container' });

    container.innerHTML =
      '<div class="auth-logo" style="font-size:3rem;">&#128274;</div>' +
      '<h1 class="auth-title">Set Up Quick Sign-In</h1>' +
      '<p class="auth-subtitle">Register a passkey (fingerprint, Face ID, or security key) so you can sign in without typing your recovery phrase.</p>' +
      '<div class="auth-actions">' +
        '<button class="btn btn-primary btn-lg btn-full" id="auth-register-passkey">Register Fingerprint / Face ID</button>' +
        '<button class="btn btn-ghost btn-full" id="auth-skip-passkey">Skip for Now</button>' +
      '</div>';

    screen.appendChild(container);

    document.getElementById('auth-register-passkey').addEventListener('click', function () {
      var btn = this;
      btn.disabled = true;
      btn.textContent = 'Registering...';

      window.Mercury.registerPasskey()
        .then(function (pk) {
          toast('Passkey registered: ' + pk.label, 'success');
          finishAuth();
        })
        .catch(function (err) {
          toast('Passkey registration failed: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'Register Fingerprint / Face ID';
        });
    });

    document.getElementById('auth-skip-passkey').addEventListener('click', finishAuth);
  }

  // ---- Login with Passkey ----

  async function loginWithPasskey() {
    var screen = authScreen();
    screen.innerHTML = '';

    var container = el('div', { className: 'auth-container' });

    container.innerHTML =
      '<div class="auth-logo" style="font-size:3rem;">&#128273;</div>' +
      '<h1 class="auth-title">Sign In</h1>' +
      '<p class="auth-subtitle">Authenticate with your registered passkey.</p>' +
      '<div class="auth-actions">' +
        '<div style="display:flex;flex-direction:column;align-items:center;gap:12px;">' +
          '<div class="loading-spinner"></div>' +
          '<p style="color:var(--text-muted);">Waiting for passkey authentication...</p>' +
        '</div>' +
        '<button class="btn btn-ghost btn-full" id="auth-cancel-passkey" style="margin-top:16px;">Cancel</button>' +
      '</div>';

    screen.appendChild(container);

    document.getElementById('auth-cancel-passkey').addEventListener('click', showAuthScreen);

    try {
      await window.Mercury.authenticatePasskey();
      toast('Signed in successfully', 'success');
      finishAuth();
      return true;
    } catch (err) {
      toast('Passkey authentication failed: ' + err.message, 'error');
      showAuthScreen();
      return false;
    }
  }

  // ---- Logout ----

  function logout() {
    window.Mercury.logout();
    toast('Signed out', 'info');
    showAuthScreen();
  }

  // ---- Check for stored identity in IndexedDB ----

  function hasStoredIdentity() {
    return dbGet('current').then(function (val) {
      return !!val;
    }).catch(function () {
      return false;
    });
  }

  // ---- Finish auth and enter app ----

  function finishAuth() {
    hideAuth();
    window.MercuryUI.hideLoading();

    // Update nav avatar. Prefer identicon/avatarCid via helpers; fall back to a letter.
    var avatar = document.getElementById('nav-avatar');
    var state = (window.Mercury && window.Mercury.state) || {};
    var user  = state.currentUser || {};
    var pubkey = state.pubkey || user.pubkey || '';
    if (avatar) {
      if (window.MercuryHelpers && pubkey) {
        avatar.innerHTML = window.MercuryHelpers.avatarHtml(pubkey, user, 24);
      } else if (user.displayName) {
        avatar.textContent = user.displayName.charAt(0).toUpperCase();
      } else {
        avatar.textContent = 'U';
      }
    }

    // Trigger initial route
    if (typeof window.MercuryApp !== 'undefined' && window.MercuryApp.navigate) {
      window.MercuryApp.navigate(window.location.hash || '#/home');
    }
  }

  // ---- Expose ----

  window.Auth = {
    showAuthScreen: showAuthScreen,
    showCreateAccount: showCreateAccount,
    showImportAccount: showImportAccount,
    showPasskeySetup: showPasskeySetup,
    loginWithPasskey: loginWithPasskey,
    logout: logout,
    hasStoredIdentity: hasStoredIdentity
  };

  // Backward-compat alias
  window.MercuryAuth = {
    showLanding: showAuthScreen,
    showCreateAccount: showCreateAccount,
    showImportAccount: showImportAccount,
    showPasskeySetup: showPasskeySetup,
    loginWithPasskey: loginWithPasskey,
    logout: logout
  };

})();
