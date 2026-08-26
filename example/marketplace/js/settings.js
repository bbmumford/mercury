/* ============================================================
   settings.js -- Profile, passkey, and identity management
   Tab layout: Profile | Security | Network | About
   ============================================================ */

(function () {
  'use strict';

  var toast = window.MercuryUI.toast;
  var el    = window.MercuryUI.el;
  var delay = window.MercuryUI.delay;

  var activeTab = 'profile';

  // ---- Main entry point ----

  function renderSettings() {
    var app = document.getElementById('app');
    app.innerHTML = '';

    var page = el('div', { className: 'page-container' });

    // Header
    var header = el('div', { className: 'page-header' });
    header.innerHTML = '<h1>Settings</h1>';
    page.appendChild(header);

    // Tab bar
    var tabs = el('div', { className: 'tabs' });
    var tabDefs = [
      { id: 'profile',  label: 'Profile' },
      { id: 'security', label: 'Security' },
      { id: 'network',  label: 'Network' },
      { id: 'about',    label: 'About' }
    ];

    tabDefs.forEach(function (t) {
      var btn = el('button', {
        className: 'tab' + (activeTab === t.id ? ' active' : ''),
        textContent: t.label,
        onClick: function () {
          activeTab = t.id;
          renderSettings();
        }
      });
      tabs.appendChild(btn);
    });

    page.appendChild(tabs);

    // Tab content
    var content = el('div', { className: 'settings-content' });
    page.appendChild(content);

    switch (activeTab) {
      case 'profile':  renderProfileTab(content);  break;
      case 'security': renderSecurityTab(content);  break;
      case 'network':  renderNetworkTab(content);   break;
      case 'about':    renderAboutTab(content);     break;
    }

    app.appendChild(page);
  }

  // ---- Profile Tab ----

  function renderProfileTab(container) {
    var H = window.MercuryHelpers;
    var state = (window.Mercury && window.Mercury.state) || {};
    var user = state.currentUser || {};

    var card = el('div', { className: 'card' });

    var peerId = state.pubkey || user.pubkey || 'unknown';
    var truncated = peerId === 'unknown' ? 'unknown' : H.shortHex(peerId, 8, 6);

    card.innerHTML =
      '<div class="card-header"><h2 class="card-title">Your Profile</h2></div>' +
      '<div style="padding:20px;">' +

        // Avatar display — avatar image if profile has avatarCid, else identicon.
        '<div style="display:flex;align-items:center;gap:16px;margin-bottom:24px;">' +
          '<div style="width:64px;height:64px;border-radius:50%;overflow:hidden;display:flex;align-items:center;justify-content:center;">' +
            H.avatarHtml(peerId === 'unknown' ? '' : peerId, user, 64) +
          '</div>' +
          '<div>' +
            '<div style="font-weight:600;font-size:1.1rem;">' + (user.displayName || 'Anonymous') + '</div>' +
            '<div style="color:var(--text-muted);font-size:0.85rem;display:flex;align-items:center;gap:6px;">' +
              '<code style="background:var(--bg);padding:2px 8px;border-radius:4px;">' + truncated + '</code>' +
              '<button class="btn btn-ghost" id="copy-peer-id" style="padding:2px 8px;font-size:0.75rem;">Copy</button>' +
            '</div>' +
          '</div>' +
        '</div>' +

        // Display name field
        '<div class="form-group">' +
          '<label class="form-label">Display Name</label>' +
          '<input type="text" class="form-input" id="settings-name" value="' + (user.displayName || '') + '" placeholder="How others see you">' +
        '</div>' +

        // Bio field
        '<div class="form-group">' +
          '<label class="form-label">Bio</label>' +
          '<textarea class="form-input" id="settings-bio" rows="3" placeholder="Tell other peers about yourself" style="resize:vertical;">' + (user.bio || '') + '</textarea>' +
        '</div>' +

        '<button class="btn btn-primary" id="save-profile">Save Changes</button>' +
      '</div>';

    container.appendChild(card);

    // Copy peer ID handler
    document.getElementById('copy-peer-id').addEventListener('click', function () {
      navigator.clipboard.writeText(peerId).then(function () {
        toast('Peer ID copied to clipboard', 'success');
      }).catch(function () {
        // Fallback: select from a temp input
        var tmp = document.createElement('input');
        tmp.value = peerId;
        document.body.appendChild(tmp);
        tmp.select();
        document.execCommand('copy');
        document.body.removeChild(tmp);
        toast('Peer ID copied to clipboard', 'success');
      });
    });

    // Save profile handler
    document.getElementById('save-profile').addEventListener('click', function () {
      var btn = this;
      var name = document.getElementById('settings-name').value.trim();
      var bio = document.getElementById('settings-bio').value.trim();

      if (!name) {
        toast('Display name cannot be empty', 'error');
        return;
      }

      btn.disabled = true;
      btn.textContent = 'Saving...';

      window.Mercury.updateProfile({ displayName: name, bio: bio })
        .then(function () {
          toast('Profile updated', 'success');
          // Update nav avatar letter
          var avatar = document.getElementById('nav-avatar');
          if (avatar) avatar.textContent = name.charAt(0).toUpperCase();

          btn.disabled = false;
          btn.textContent = 'Save Changes';
        })
        .catch(function (err) {
          toast('Failed to save: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'Save Changes';
        });
    });
  }

  // ---- Security Tab ----

  function renderSecurityTab(container) {
    // Passkey management card
    var passkeyCard = el('div', { className: 'card', style: 'margin-bottom:20px;' });
    passkeyCard.innerHTML =
      '<div class="card-header">' +
        '<h2 class="card-title">Passkeys</h2>' +
        '<button class="btn btn-primary" id="register-new-passkey" style="font-size:0.85rem;">Register New Passkey</button>' +
      '</div>' +
      '<div style="padding:20px;" id="passkey-list">' +
        '<div class="loading-spinner" style="margin:20px auto;"></div>' +
      '</div>';

    container.appendChild(passkeyCard);

    // Load passkeys
    window.Mercury.getPasskeys().then(function (passkeys) {
      var listEl = document.getElementById('passkey-list');
      if (!passkeys || passkeys.length === 0) {
        listEl.innerHTML =
          '<div style="text-align:center;color:var(--text-muted);padding:20px;">' +
            '<p>No passkeys registered.</p>' +
            '<p style="font-size:0.85rem;margin-top:8px;">Register a passkey to enable quick biometric sign-in.</p>' +
          '</div>';
        return;
      }

      var html = '';
      passkeys.forEach(function (pk) {
        var date = pk.createdAt ? new Date(pk.createdAt).toLocaleDateString() : 'Unknown';
        html +=
          '<div class="passkey-item" style="display:flex;align-items:center;justify-content:space-between;padding:12px 0;border-bottom:1px solid var(--border);">' +
            '<div>' +
              '<div style="font-weight:500;">' + (pk.label || 'Passkey') + '</div>' +
              '<div style="color:var(--text-muted);font-size:0.85rem;">Registered ' + date + '</div>' +
            '</div>' +
            '<button class="btn btn-danger" data-passkey-id="' + pk.id + '" style="font-size:0.8rem;padding:4px 12px;">Remove</button>' +
          '</div>';
      });

      listEl.innerHTML = html;

      // Attach remove handlers
      listEl.querySelectorAll('[data-passkey-id]').forEach(function (btn) {
        btn.addEventListener('click', function () {
          var id = btn.getAttribute('data-passkey-id');
          if (!confirm('Remove this passkey? You will not be able to sign in with it anymore.')) return;

          window.Mercury.removePasskey(id).then(function () {
            toast('Passkey removed', 'success');
            renderSecurityTab(container);
          }).catch(function (err) {
            toast('Failed to remove passkey: ' + err.message, 'error');
          });
        });
      });
    });

    // Register new passkey handler
    document.getElementById('register-new-passkey').addEventListener('click', function () {
      var btn = this;
      btn.disabled = true;
      btn.textContent = 'Registering...';

      window.Mercury.registerPasskey()
        .then(function (pk) {
          toast('Passkey registered: ' + pk.label, 'success');
          // Re-render to show new passkey in list
          container.innerHTML = '';
          renderSecurityTab(container);
        })
        .catch(function (err) {
          toast('Registration failed: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'Register New Passkey';
        });
    });

    // Mnemonic export card
    var mnemonicCard = el('div', { className: 'card', style: 'margin-bottom:20px;' });
    mnemonicCard.innerHTML =
      '<div class="card-header"><h2 class="card-title">Recovery Phrase</h2></div>' +
      '<div style="padding:20px;">' +
        '<p style="color:var(--text-secondary);margin-bottom:16px;">Your 24-word recovery phrase is the master key to your identity. Never share it with anyone.</p>' +
        '<button class="btn btn-secondary" id="show-mnemonic-btn">Show Recovery Words</button>' +
        '<div id="mnemonic-export-area"></div>' +
      '</div>';

    container.appendChild(mnemonicCard);

    document.getElementById('show-mnemonic-btn').addEventListener('click', function () {
      showMnemonicExport();
    });

    // Active sessions card
    var sessionsCard = el('div', { className: 'card' });
    var now = new Date().toLocaleString();
    sessionsCard.innerHTML =
      '<div class="card-header"><h2 class="card-title">Active Sessions</h2></div>' +
      '<div style="padding:20px;">' +
        '<div style="display:flex;align-items:center;justify-content:space-between;padding:12px 0;">' +
          '<div>' +
            '<div style="font-weight:500;display:flex;align-items:center;gap:8px;">' +
              '<span style="width:8px;height:8px;border-radius:50%;background:var(--success);display:inline-block;"></span>' +
              'This device' +
            '</div>' +
            '<div style="color:var(--text-muted);font-size:0.85rem;">Active since ' + now + '</div>' +
          '</div>' +
          '<span style="color:var(--success);font-size:0.8rem;font-weight:600;">Current</span>' +
        '</div>' +
      '</div>';

    container.appendChild(sessionsCard);
  }

  // ---- Mnemonic Export ----

  function showMnemonicExport() {
    var area = document.getElementById('mnemonic-export-area');
    if (!area) return;

    // Show confirmation dialog first
    area.innerHTML =
      '<div style="margin-top:16px;padding:16px;border-radius:var(--radius);background:var(--danger-soft);border:1px solid var(--danger);">' +
        '<div style="font-weight:600;color:var(--danger);margin-bottom:8px;">&#9888; Warning</div>' +
        '<p style="color:var(--text-secondary);margin-bottom:12px;">Anyone with these words can take full control of your identity and funds. Only view them in a private, secure location.</p>' +
        '<div style="display:flex;gap:8px;">' +
          '<button class="btn btn-danger" id="confirm-show-mnemonic">Show Recovery Words</button>' +
          '<button class="btn btn-ghost" id="cancel-show-mnemonic">Cancel</button>' +
        '</div>' +
      '</div>';

    document.getElementById('cancel-show-mnemonic').addEventListener('click', function () {
      area.innerHTML = '';
    });

    document.getElementById('confirm-show-mnemonic').addEventListener('click', function () {
      var words = window.Mercury.getMnemonic();
      if (!words || words.length === 0) {
        toast('No recovery phrase available. Was this identity imported without one?', 'error');
        area.innerHTML = '';
        return;
      }

      var wordsHtml = '';
      words.forEach(function (w, i) {
        wordsHtml += '<span class="mnemonic-word"><span class="word-num">' + (i + 1) + '.</span> ' + w + '</span>';
      });

      area.innerHTML =
        '<div style="margin-top:16px;padding:16px;border-radius:var(--radius);background:var(--bg);border:1px solid var(--border);">' +
          '<div class="mnemonic-grid">' + wordsHtml + '</div>' +
          '<div style="margin-top:12px;display:flex;align-items:center;justify-content:space-between;">' +
            '<span style="color:var(--text-muted);font-size:0.8rem;" id="mnemonic-timer">Auto-hiding in 60s</span>' +
            '<button class="btn btn-ghost" id="hide-mnemonic" style="font-size:0.85rem;">Hide Now</button>' +
          '</div>' +
        '</div>';

      // Auto-hide after 60 seconds
      var remaining = 60;
      var timerEl = document.getElementById('mnemonic-timer');
      var interval = setInterval(function () {
        remaining--;
        if (timerEl) timerEl.textContent = 'Auto-hiding in ' + remaining + 's';
        if (remaining <= 0) {
          clearInterval(interval);
          area.innerHTML = '';
        }
      }, 1000);

      document.getElementById('hide-mnemonic').addEventListener('click', function () {
        clearInterval(interval);
        area.innerHTML = '';
      });
    });
  }

  // ---- Network Tab ----

  function renderNetworkTab(container) {
    // Connected peers card
    var peersCard = el('div', { className: 'card', style: 'margin-bottom:20px;' });
    peersCard.innerHTML =
      '<div class="card-header">' +
        '<h2 class="card-title">Connected Peers</h2>' +
        '<button class="btn btn-ghost" id="refresh-peers" style="font-size:0.85rem;">Refresh</button>' +
      '</div>' +
      '<div style="padding:20px;" id="peers-list">' +
        '<div class="loading-spinner" style="margin:20px auto;"></div>' +
      '</div>';

    container.appendChild(peersCard);

    function loadPeers() {
      var H = window.MercuryHelpers;
      window.Mercury.getPeers().then(async function (peers) {
        var listEl = document.getElementById('peers-list');
        if (!peers || peers.length === 0) {
          listEl.innerHTML = '<p style="color:var(--text-muted);text-align:center;padding:20px;">No peers connected.</p>';
          return;
        }

        // Resolve peer profiles in parallel.
        var profiles = await Promise.all(peers.map(function (p) { return H.getProfile(p.id); }));

        var html = '';
        peers.forEach(function (p, i) {
          var statusColor = p.online ? 'var(--success)' : 'var(--text-muted)';
          var statusText = p.online ? 'Connected' : 'Offline';
          var displayName = H.displayName(p.id, profiles[i]);
          var latency = (p.latency != null) ? p.latency : '—';
          html +=
            '<div style="display:flex;align-items:center;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);">' +
              '<div style="display:flex;align-items:center;gap:10px;">' +
                '<span style="width:8px;height:8px;border-radius:50%;background:' + statusColor + ';display:inline-block;"></span>' +
                '<div>' +
                  '<div style="font-weight:500;">' + H.esc(displayName) + '</div>' +
                  '<div style="color:var(--text-muted);font-size:0.8rem;font-family:monospace;">' + H.esc(H.shortHex(p.id, 8, 6)) + '</div>' +
                '</div>' +
              '</div>' +
              '<div style="text-align:right;">' +
                '<div style="font-size:0.85rem;color:' + statusColor + ';">' + statusText + '</div>' +
                '<div style="font-size:0.8rem;color:var(--text-muted);">RTT: ' + H.esc(latency) + '</div>' +
              '</div>' +
            '</div>';
        });

        listEl.innerHTML = html;
      });
    }

    loadPeers();

    document.getElementById('refresh-peers').addEventListener('click', function () {
      var listEl = document.getElementById('peers-list');
      listEl.innerHTML = '<div class="loading-spinner" style="margin:20px auto;"></div>';
      loadPeers();
    });

    // Announce addresses card
    var addressCard = el('div', { className: 'card', style: 'margin-bottom:20px;' });
    addressCard.innerHTML =
      '<div class="card-header"><h2 class="card-title">Announce Addresses</h2></div>' +
      '<div style="padding:20px;">' +
        '<p style="color:var(--text-secondary);margin-bottom:12px;">Addresses advertised to other peers for direct connections.</p>' +
        '<div class="form-group">' +
          '<textarea class="form-input" id="announce-addresses" rows="3" placeholder="/ip4/0.0.0.0/tcp/4001&#10;/ip6/::/tcp/4001" style="resize:vertical;font-family:monospace;font-size:0.85rem;"></textarea>' +
        '</div>' +
        '<button class="btn btn-secondary" id="save-addresses">Save Addresses</button>' +
      '</div>';

    container.appendChild(addressCard);

    document.getElementById('save-addresses').addEventListener('click', function () {
      toast('Announce addresses updated', 'success');
    });

    // Relay bootstrap card
    var relayCard = el('div', { className: 'card' });
    relayCard.innerHTML =
      '<div class="card-header"><h2 class="card-title">Relay Bootstrap Nodes</h2></div>' +
      '<div style="padding:20px;">' +
        '<p style="color:var(--text-secondary);margin-bottom:12px;">Relay servers used when direct peer connections are not possible (NAT traversal).</p>' +
        '<div class="form-group">' +
          '<textarea class="form-input" id="relay-urls" rows="3" placeholder="https://relay1.mercury.example.com&#10;https://relay2.mercury.example.com" style="resize:vertical;font-family:monospace;font-size:0.85rem;"></textarea>' +
        '</div>' +
        '<button class="btn btn-secondary" id="save-relays">Save Relay URLs</button>' +
      '</div>';

    container.appendChild(relayCard);

    document.getElementById('save-relays').addEventListener('click', function () {
      toast('Relay bootstrap URLs updated', 'success');
    });
  }

  // ---- About Tab ----

  function renderAboutTab(container) {
    var state = (window.Mercury && window.Mercury.state) || {};
    var fingerprint = state.pubkey
      ? String(state.pubkey).replace(/^pk_/, '').replace(/^0x/, '').toUpperCase()
      : 'N/A';

    // Format fingerprint with colons every 4 chars
    if (fingerprint.length > 4) {
      fingerprint = fingerprint.match(/.{1,4}/g).join(':');
    }

    var card = el('div', { className: 'card', style: 'margin-bottom:20px;' });
    card.innerHTML =
      '<div class="card-header"><h2 class="card-title">Protocol Information</h2></div>' +
      '<div style="padding:20px;">' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);">' +
          '<span style="color:var(--text-secondary);">Protocol</span>' +
          '<span style="font-weight:500;">Mercury P2P v0.1.0</span>' +
        '</div>' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);">' +
          '<span style="color:var(--text-secondary);">Transport</span>' +
          '<span style="font-weight:500;">WebRTC + WebSocket</span>' +
        '</div>' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);">' +
          '<span style="color:var(--text-secondary);">Identity Fingerprint</span>' +
          '<code style="font-size:0.8rem;background:var(--bg);padding:2px 8px;border-radius:4px;">' + fingerprint + '</code>' +
        '</div>' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);">' +
          '<span style="color:var(--text-secondary);">Escrow</span>' +
          '<span style="font-weight:500;">Plutus 2-of-3 Multisig</span>' +
        '</div>' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;">' +
          '<span style="color:var(--text-secondary);">Messaging</span>' +
          '<span style="font-weight:500;">End-to-end encrypted (X25519)</span>' +
        '</div>' +
      '</div>';

    container.appendChild(card);

    // Package versions card
    var pkgCard = el('div', { className: 'card', style: 'margin-bottom:20px;' });
    pkgCard.innerHTML =
      '<div class="card-header"><h2 class="card-title">Package Versions</h2></div>' +
      '<div style="padding:20px;">' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);">' +
          '<span style="color:var(--text-secondary);">mercury-wasm</span>' +
          '<span style="font-family:monospace;font-size:0.85rem;">0.1.0-demo</span>' +
        '</div>' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);">' +
          '<span style="color:var(--text-secondary);">plutus-escrow</span>' +
          '<span style="font-family:monospace;font-size:0.85rem;">0.1.0-demo</span>' +
        '</div>' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);">' +
          '<span style="color:var(--text-secondary);">libp2p-webrtc</span>' +
          '<span style="font-family:monospace;font-size:0.85rem;">0.4.0</span>' +
        '</div>' +
        '<div class="about-row" style="display:flex;justify-content:space-between;padding:10px 0;">' +
          '<span style="color:var(--text-secondary);">noise-protocol</span>' +
          '<span style="font-family:monospace;font-size:0.85rem;">0.3.1</span>' +
        '</div>' +
      '</div>';

    container.appendChild(pkgCard);

    // Build info card
    var buildCard = el('div', { className: 'card' });
    buildCard.innerHTML =
      '<div style="padding:20px;text-align:center;color:var(--text-muted);font-size:0.85rem;">' +
        '<p>Mercury Marketplace Demo</p>' +
        '<p style="margin-top:4px;">Built with Go WASM + vanilla JavaScript</p>' +
        '<p style="margin-top:4px;">No tracking. No servers. Your keys, your data.</p>' +
      '</div>';

    container.appendChild(buildCard);
  }

  // ---- Expose ----

  window.Settings = { renderSettings: renderSettings };

})();
