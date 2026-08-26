/* ============================================================
   Messaging — Peer-to-peer encrypted conversations

   Consumes the raw shapes returned by the Mercury bridge:
     conversation = {
       peerId,
       messages?: []     // optional preview / fully loaded
     }
     message = {
       from (pubkey hex, or 'me' for outbound),
       text, ts | timestamp
     }
   Peer display names are resolved via Mercury.getProfile(pubkey),
   falling back to shortHex. Unread counts are derived client-side
   from message timestamps vs. a localStorage lastRead marker.
   ============================================================ */

(function () {
  'use strict';

  var H = window.MercuryHelpers;
  var toast          = window.MercuryUI.toast;
  var el             = window.MercuryUI.el;
  var renderSkeleton = window.MercuryUI.renderSkeleton;

  function app() { return document.getElementById('app'); }
  function esc(s) { return H.esc(s); }

  // ---- Utilities ----

  function latestMessage(messages) {
    if (!messages || !messages.length) return null;
    return messages[messages.length - 1];
  }

  function buildDerivedConversationView(conv) {
    // Derive display fields client-side.
    var last  = latestMessage(conv.messages);
    var ts    = last ? (last.ts || last.time || last.timestamp) : null;
    var rel   = ts ? H.formatRelativeTime(ts) : '';
    var prev  = last ? last.text : '';
    var unread = H.computeUnread(conv.peerId, conv.messages);
    return {
      peerId:      conv.peerId,
      lastMessage: prev,
      lastTime:    rel,
      unread:      unread,
    };
  }

  // ---- Conversation List (Messages Inbox) ----

  async function renderMessages() {
    var root = app();
    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">Messages</h1>' +
        '<p class="page-subtitle">End-to-end encrypted conversations with peers.</p>' +
        '<div class="page-actions">' +
          '<button class="btn btn-primary" id="msg-new">' +
            '<span style="margin-right:4px;">+</span> New Message' +
          '</button>' +
        '</div>' +
      '</div>' +
      '<div class="messaging-layout">' +
        '<div class="msg-sidebar" id="msg-sidebar">' +
          '<div class="msg-sidebar-header">' +
            '<input type="text" class="msg-search" id="msg-search-input" placeholder="Search conversations..." autocomplete="off">' +
          '</div>' +
          '<div class="msg-conversation-list" id="msg-conv-list">' +
            renderSkeleton('100%', '64px') +
            renderSkeleton('100%', '64px') +
            renderSkeleton('100%', '64px') +
          '</div>' +
        '</div>' +
        '<div class="msg-main" id="msg-main">' +
          '<div class="msg-empty-state">' +
            '<div style="font-size:2.5rem;margin-bottom:12px;opacity:0.4;">&#128172;</div>' +
            '<h3 style="color:var(--text-secondary);font-weight:600;">Select a conversation</h3>' +
            '<p style="color:var(--text-muted);font-size:0.85rem;margin-top:4px;">Choose a conversation from the sidebar or start a new one.</p>' +
          '</div>' +
        '</div>' +
      '</div>';

    document.getElementById('msg-new').addEventListener('click', showNewMessageDialog);

    var searchInput = document.getElementById('msg-search-input');
    if (searchInput) {
      searchInput.addEventListener('input', function () {
        var q = this.value.toLowerCase();
        var items = document.querySelectorAll('.msg-conv-item');
        items.forEach(function (item) {
          var name = (item.dataset.peerName || '').toLowerCase();
          var preview = (item.dataset.preview || '').toLowerCase();
          item.style.display = (name.indexOf(q) >= 0 || preview.indexOf(q) >= 0) ? '' : 'none';
        });
      });
    }

    try {
      var conversations = (await window.Mercury.getConversations()) || [];
      await renderConversationList(conversations);
      updateNavUnreadBadge();
    } catch (err) {
      toast('Failed to load conversations: ' + err.message, 'error');
    }
  }

  async function renderConversationList(conversations) {
    var container = document.getElementById('msg-conv-list');
    if (!container) return;

    if (!conversations || conversations.length === 0) {
      container.innerHTML =
        '<div class="empty-state" style="padding:24px;">' +
          '<div class="empty-state-icon">\ud83d\udcec</div>' +
          '<h3 class="empty-state-title">No conversations</h3>' +
          '<p class="empty-state-desc">No conversations. Reach out to a seller from a listing page.</p>' +
        '</div>';
      return;
    }

    // Derive display fields and resolve profile names in parallel.
    var derived = conversations.map(buildDerivedConversationView);
    var profiles = await Promise.all(conversations.map(function (c) {
      return H.getProfile(c.peerId);
    }));

    container.innerHTML = derived.map(function (c, i) {
      var profile = profiles[i];
      var name    = H.displayName(c.peerId, profile);
      var avatar  = H.avatarHtml(c.peerId, profile, 36);
      var unreadHtml = c.unread > 0
        ? '<span class="msg-unread-badge">' + c.unread + '</span>'
        : '';
      return (
        '<div class="msg-conv-item" data-peer="' + esc(c.peerId) + '" data-peer-name="' + esc(name) + '" data-preview="' + esc(c.lastMessage) + '">' +
          '<div class="msg-conv-avatar">' + avatar + '</div>' +
          '<div class="msg-conv-info">' +
            '<div class="msg-conv-top">' +
              '<span class="msg-conv-name">' + esc(name) + '</span>' +
              '<span class="msg-conv-time">' + esc(c.lastTime) + '</span>' +
            '</div>' +
            '<div class="msg-conv-preview">' + esc(c.lastMessage) + '</div>' +
          '</div>' +
          unreadHtml +
        '</div>'
      );
    }).join('');

    container.querySelectorAll('.msg-conv-item').forEach(function (item) {
      item.addEventListener('click', function () {
        container.querySelectorAll('.msg-conv-item').forEach(function (i) { i.classList.remove('active'); });
        this.classList.add('active');
        renderConversation(this.dataset.peer);
      });
    });
  }

  // ---- Active Conversation View ----

  async function renderConversation(peerId) {
    var main = document.getElementById('msg-main');
    if (!main) {
      var root = app();
      root.innerHTML =
        '<div class="messaging-layout messaging-layout-full">' +
          '<div class="msg-main" id="msg-main">' +
            '<div class="msg-loading"><div class="loading-spinner"></div></div>' +
          '</div>' +
        '</div>';
      main = document.getElementById('msg-main');
    }

    main.innerHTML = '<div class="msg-loading"><div class="loading-spinner"></div></div>';

    var conv;
    try {
      conv = await window.Mercury.getConversation(peerId);
    } catch (err) {
      toast('Failed to load conversation: ' + err.message, 'error');
      return;
    }

    if (!conv) conv = { peerId: peerId, messages: [] };

    var profile = await H.getProfile(peerId);
    var peerName = H.displayName(peerId, profile);

    main.innerHTML =
      '<div class="msg-header">' +
        '<div class="msg-header-left">' +
          '<button class="btn btn-ghost msg-back-btn" id="msg-back">&larr;</button>' +
          '<div class="msg-header-avatar">' + H.avatarHtml(peerId, profile, 36) + '</div>' +
          '<div>' +
            '<div class="msg-header-name">' + esc(peerName) + '</div>' +
            '<div class="msg-header-status">' +
              '<span class="msg-encrypted-badge">' +
                '<span style="margin-right:4px;">&#128274;</span> Encrypted' +
              '</span>' +
            '</div>' +
          '</div>' +
        '</div>' +
        '<div class="msg-header-right">' +
          '<button class="btn btn-ghost btn-sm" id="msg-view-profile" title="View profile">&#128100;</button>' +
        '</div>' +
      '</div>' +
      '<div class="msg-messages" id="msg-messages-scroll">' +
        buildMessagesHtml(conv.messages) +
      '</div>' +
      '<div class="msg-input-bar">' +
        '<input type="text" class="msg-input" id="msg-input-field" placeholder="Type a message..." autocomplete="off">' +
        '<button class="btn btn-primary msg-send-btn" id="msg-send-btn">Send</button>' +
      '</div>';

    // Mark all messages as read.
    H.setLastRead(peerId, Date.now());

    var scroll = document.getElementById('msg-messages-scroll');
    if (scroll) scroll.scrollTop = scroll.scrollHeight;

    document.getElementById('msg-back').addEventListener('click', function () {
      renderMessages();
    });

    document.getElementById('msg-view-profile').addEventListener('click', function () {
      if (window.MercuryReputation && window.MercuryReputation.renderSellerProfile) {
        window.MercuryReputation.renderSellerProfile(peerId);
      }
    });

    document.getElementById('msg-send-btn').addEventListener('click', function () {
      doSend(peerId);
    });

    document.getElementById('msg-input-field').addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        doSend(peerId);
      }
    });

    var inputField = document.getElementById('msg-input-field');
    if (inputField) inputField.focus();

    var sidebarItem = document.querySelector('.msg-conv-item[data-peer="' + peerId + '"]');
    if (sidebarItem) {
      document.querySelectorAll('.msg-conv-item').forEach(function (i) { i.classList.remove('active'); });
      sidebarItem.classList.add('active');
      var badge = sidebarItem.querySelector('.msg-unread-badge');
      if (badge) badge.remove();
    }

    updateNavUnreadBadge();
  }

  function buildMessagesHtml(messages) {
    if (!messages || messages.length === 0) {
      return (
        '<div style="display:flex;align-items:center;justify-content:center;height:100%;color:var(--text-muted);font-size:0.85rem;">' +
          'No messages yet. Say hello!' +
        '</div>'
      );
    }

    return messages.map(function (m) {
      var isMe = m.from === 'me';
      var align = isMe ? 'msg-bubble-right' : 'msg-bubble-left';
      var ts = m.ts || m.time || m.timestamp;
      var rel = H.formatRelativeTime(ts);
      return (
        '<div class="msg-bubble-row ' + align + '">' +
          '<div class="msg-bubble ' + (isMe ? 'msg-bubble-mine' : 'msg-bubble-theirs') + '">' +
            '<div class="msg-bubble-text">' + esc(m.text) + '</div>' +
            '<div class="msg-bubble-time">' + esc(rel) + '</div>' +
          '</div>' +
        '</div>'
      );
    }).join('');
  }

  // ---- Send Message ----

  function sendMessage(peerId, text) {
    if (!text || !text.trim()) return;

    var scrollContainer = document.getElementById('msg-messages-scroll');

    if (scrollContainer) {
      var emptyState = scrollContainer.querySelector('div[style*="justify-content:center"]');
      if (emptyState) scrollContainer.innerHTML = '';

      var row = document.createElement('div');
      row.className = 'msg-bubble-row msg-bubble-right';
      row.innerHTML =
        '<div class="msg-bubble msg-bubble-mine msg-bubble-sending">' +
          '<div class="msg-bubble-text">' + esc(text) + '</div>' +
          '<div class="msg-bubble-time">Sending...</div>' +
        '</div>';
      scrollContainer.appendChild(row);
      scrollContainer.scrollTop = scrollContainer.scrollHeight;
    }

    window.Mercury.sendMessage(peerId, text).then(function (msg) {
      var sending = scrollContainer ? scrollContainer.querySelector('.msg-bubble-sending') : null;
      if (sending) {
        sending.classList.remove('msg-bubble-sending');
        var timeEl = sending.querySelector('.msg-bubble-time');
        var ts = msg && (msg.ts || msg.time || msg.timestamp);
        if (timeEl) timeEl.textContent = ts ? H.formatRelativeTime(ts) : 'now';
      }

      var sidebarItem = document.querySelector('.msg-conv-item[data-peer="' + peerId + '"]');
      if (sidebarItem) {
        var preview = sidebarItem.querySelector('.msg-conv-preview');
        if (preview) preview.textContent = text;
        var time = sidebarItem.querySelector('.msg-conv-time');
        if (time) time.textContent = 'now';
      }
    }).catch(function (err) {
      toast('Failed to send message: ' + err.message, 'error');
      var sending = scrollContainer ? scrollContainer.querySelector('.msg-bubble-sending') : null;
      if (sending) {
        sending.classList.add('msg-bubble-failed');
        sending.classList.remove('msg-bubble-sending');
        var timeEl = sending.querySelector('.msg-bubble-time');
        if (timeEl) timeEl.textContent = 'Failed to send';
      }
    });
  }

  function doSend(peerId) {
    var input = document.getElementById('msg-input-field');
    if (!input) return;
    var text = input.value.trim();
    if (!text) return;
    input.value = '';
    sendMessage(peerId, text);
    input.focus();
  }

  // ---- Incoming Message Handler ----

  async function onNewMessage(message) {
    var peerId = message.from;
    var activeConv = document.querySelector('.msg-conv-item.active');
    var isViewingConv = activeConv && activeConv.dataset.peer === peerId;

    var ts = message.ts || message.time || message.timestamp;
    var rel = H.formatRelativeTime(ts);

    if (isViewingConv) {
      var scrollContainer = document.getElementById('msg-messages-scroll');
      if (scrollContainer) {
        var row = document.createElement('div');
        row.className = 'msg-bubble-row msg-bubble-left';
        row.innerHTML =
          '<div class="msg-bubble msg-bubble-theirs">' +
            '<div class="msg-bubble-text">' + esc(message.text) + '</div>' +
            '<div class="msg-bubble-time">' + esc(rel || 'now') + '</div>' +
          '</div>';
        scrollContainer.appendChild(row);
        scrollContainer.scrollTop = scrollContainer.scrollHeight;
      }
      // Viewing the conversation — mark as read immediately.
      H.setLastRead(peerId, Date.now());
    } else {
      var profile  = await H.getProfile(peerId);
      var peerName = H.displayName(peerId, profile);
      toast(peerName + ': ' + (message.text.length > 60 ? message.text.substring(0, 60) + '...' : message.text), 'info');
    }

    var sidebarItem = document.querySelector('.msg-conv-item[data-peer="' + peerId + '"]');
    if (sidebarItem) {
      var preview = sidebarItem.querySelector('.msg-conv-preview');
      if (preview) preview.textContent = message.text;
      var time = sidebarItem.querySelector('.msg-conv-time');
      if (time) time.textContent = rel || 'now';

      if (!isViewingConv) {
        var badge = sidebarItem.querySelector('.msg-unread-badge');
        if (!badge) {
          badge = document.createElement('span');
          badge.className = 'msg-unread-badge';
          badge.textContent = '1';
          sidebarItem.appendChild(badge);
        } else {
          badge.textContent = parseInt(badge.textContent, 10) + 1;
        }
      }
    }

    updateNavUnreadBadge();
  }

  function updateNavUnreadBadge() {
    var badges = document.querySelectorAll('.msg-unread-badge');
    var total = 0;
    badges.forEach(function (b) { total += parseInt(b.textContent, 10) || 0; });
    var navBadge = document.getElementById('unread-badge');
    if (navBadge) {
      if (total > 0) {
        navBadge.textContent = total;
        navBadge.classList.remove('hidden');
      } else {
        navBadge.classList.add('hidden');
      }
    }
  }

  // ---- New Message Dialog ----

  async function showNewMessageDialog() {
    var main = document.getElementById('msg-main');
    if (!main) return;

    main.innerHTML =
      '<div class="msg-new-dialog">' +
        '<h2 style="font-size:1.1rem;font-weight:600;margin-bottom:16px;">New Conversation</h2>' +
        '<div class="form-group">' +
          '<label class="form-label">Recipient</label>' +
          '<input type="text" class="form-input" id="msg-new-peer" placeholder="Peer pubkey (hex)..." autocomplete="off">' +
        '</div>' +
        '<div id="msg-peer-suggestions" style="margin-bottom:16px;"></div>' +
        '<div class="form-group">' +
          '<label class="form-label">Message</label>' +
          '<textarea class="form-input" id="msg-new-text" placeholder="Write your first message..." rows="3"></textarea>' +
        '</div>' +
        '<div style="display:flex;gap:8px;">' +
          '<button class="btn btn-primary" id="msg-new-send">Start Conversation</button>' +
          '<button class="btn btn-ghost" id="msg-new-cancel">Cancel</button>' +
        '</div>' +
      '</div>';

    // Load online peer suggestions.
    try {
      var peers = (await window.Mercury.getPeers()) || [];
      var online = peers.filter(function (p) { return p.online; });
      var suggestionsEl = document.getElementById('msg-peer-suggestions');
      if (suggestionsEl && online.length > 0) {
        var profiles = await Promise.all(online.map(function (p) { return H.getProfile(p.id); }));
        suggestionsEl.innerHTML =
          '<p style="font-size:0.8rem;color:var(--text-muted);margin-bottom:8px;">Online peers:</p>' +
          '<div class="msg-peer-chips">' +
            online.map(function (p, i) {
              var name = H.displayName(p.id, profiles[i]);
              return '<button class="msg-peer-chip" data-peer="' + esc(p.id) + '" data-name="' + esc(name) + '">' +
                '<span class="msg-peer-online"></span>' + esc(name) +
              '</button>';
            }).join('') +
          '</div>';

        suggestionsEl.querySelectorAll('.msg-peer-chip').forEach(function (chip) {
          chip.addEventListener('click', function () {
            document.getElementById('msg-new-peer').value = this.dataset.peer;
            suggestionsEl.querySelectorAll('.msg-peer-chip').forEach(function (c) { c.classList.remove('selected'); });
            this.classList.add('selected');
          });
        });
      }
    } catch (err) {
      console.warn('[messaging] peer suggestions failed', err);
    }

    document.getElementById('msg-new-send').addEventListener('click', function () {
      var peerInput = document.getElementById('msg-new-peer');
      var textInput = document.getElementById('msg-new-text');
      var peerId = peerInput.value.trim();
      var text = textInput.value.trim();

      if (!peerId) {
        toast('Please enter a recipient', 'error');
        peerInput.focus();
        return;
      }
      if (!text) {
        toast('Please enter a message', 'error');
        textInput.focus();
        return;
      }

      window.Mercury.sendMessage(peerId, text).then(async function () {
        toast('Conversation started', 'success');
        var conversations = (await window.Mercury.getConversations()) || [];
        await renderConversationList(conversations);
        renderConversation(peerId);
      }).catch(function (err) {
        toast('Failed to start conversation: ' + err.message, 'error');
      });
    });

    document.getElementById('msg-new-cancel').addEventListener('click', function () {
      main.innerHTML =
        '<div class="msg-empty-state">' +
          '<div style="font-size:2.5rem;margin-bottom:12px;opacity:0.4;">&#128172;</div>' +
          '<h3 style="color:var(--text-secondary);font-weight:600;">Select a conversation</h3>' +
          '<p style="color:var(--text-muted);font-size:0.85rem;margin-top:4px;">Choose a conversation from the sidebar or start a new one.</p>' +
        '</div>';
    });
  }

  // ---- Expose ----

  window.MercuryMessaging = {
    renderMessages: renderMessages,
    renderConversation: renderConversation,
    sendMessage: sendMessage,
    onNewMessage: onNewMessage,
  };

})();
