/* ============================================================
   App — SPA router, navigation, WASM init, global search
   ============================================================ */

(function () {
  'use strict';

  var toast = window.MercuryUI.toast;

  // ---- Route table ----

  var routes = {
    '#/home':     function ()    { window.MercuryBrowse.renderHome(); },
    '#/browse':   function ()    { window.MercuryBrowse.renderBrowse(); },
    '#/store':    function ()    { window.MercuryStore.renderMyStore(); },
    '#/orders':   function ()    { window.MercuryOrder.renderOrders(); },
    '#/wallet':   function ()    { window.MercuryWallet.renderWallet(); },
    '#/messages': function ()    { window.MercuryMessaging.renderMessages(); },
    '#/settings': function ()    { window.Settings.renderSettings(); },
  };

  // ---- Active nav highlighting ----

  function updateNavActive(hash) {
    var navLinks = document.querySelectorAll('.nav-link[data-nav]');
    navLinks.forEach(function (link) {
      var target = '#/' + link.dataset.nav;
      if (hash.indexOf(target) === 0) {
        link.classList.add('active');
      } else {
        link.classList.remove('active');
      }
    });
  }

  // ---- Router ----

  function navigate(hash) {
    if (!hash || hash === '#' || hash === '#/') hash = '#/home';

    // Check auth
    if (!window.Mercury.state.authenticated) {
      return; // Auth screen handles itself
    }

    // Parametric routes
    // #/category/:name
    var catMatch = hash.match(/^#\/category\/(.+)$/);
    if (catMatch) {
      updateNavActive('#/browse');
      window.MercuryBrowse.renderCategory(decodeURIComponent(catMatch[1]));
      return;
    }

    // #/listing/:id
    var listingMatch = hash.match(/^#\/listing\/(.+)$/);
    if (listingMatch) {
      updateNavActive('#/browse');
      window.MercuryBrowse.renderListing(decodeURIComponent(listingMatch[1]));
      return;
    }

    // #/order/:id
    var orderMatch = hash.match(/^#\/order\/(.+)$/);
    if (orderMatch) {
      updateNavActive('#/orders');
      window.MercuryOrder.renderOrderDetail(decodeURIComponent(orderMatch[1]));
      return;
    }

    // #/seller/:pubkey
    var sellerMatch = hash.match(/^#\/seller\/(.+)$/);
    if (sellerMatch) {
      updateNavActive('#/browse');
      window.MercuryReputation.renderSellerProfile(decodeURIComponent(sellerMatch[1]));
      return;
    }

    // #/search?q=...
    var searchMatch = hash.match(/^#\/search\?q=(.+)$/);
    if (searchMatch) {
      updateNavActive('#/browse');
      window.MercuryBrowse.renderSearch(decodeURIComponent(searchMatch[1]));
      return;
    }

    // #/disputes
    if (hash === '#/disputes' && window.MercuryArbitration) {
      updateNavActive('#/orders');
      window.MercuryArbitration.renderDisputes();
      return;
    }

    // Static routes
    var handler = routes[hash];
    if (handler) {
      updateNavActive(hash);
      handler();
    } else {
      // Fallback to home
      updateNavActive('#/home');
      routes['#/home']();
    }
  }

  // ---- Hash change listener ----

  window.addEventListener('hashchange', function () {
    navigate(window.location.hash);
  });

  // ---- Mobile menu ----

  function setupMobileMenu() {
    var toggle = document.getElementById('mobile-menu-toggle');
    var overlay = document.getElementById('mobile-nav');
    if (!toggle || !overlay) return;

    toggle.addEventListener('click', function () {
      overlay.classList.toggle('hidden');
    });

    // Close on link click
    overlay.querySelectorAll('.mobile-nav-link').forEach(function (link) {
      link.addEventListener('click', function () {
        overlay.classList.add('hidden');
      });
    });

    // Logout
    var logoutLink = document.getElementById('mobile-logout');
    if (logoutLink) {
      logoutLink.addEventListener('click', function (e) {
        e.preventDefault();
        overlay.classList.add('hidden');
        if (window.MercuryAuth) window.MercuryAuth.logout();
        else if (window.Auth) window.Auth.logout();
      });
    }
  }

  // ---- Global search ----

  function setupGlobalSearch() {
    var input = document.getElementById('global-search');
    var dropdown = document.getElementById('search-results-dropdown');
    if (!input || !dropdown) return;

    var debounceTimer = null;

    input.addEventListener('input', function () {
      var query = input.value.trim();
      if (query.length < 2) {
        dropdown.classList.add('hidden');
        dropdown.innerHTML = '';
        return;
      }

      clearTimeout(debounceTimer);
      debounceTimer = setTimeout(function () {
        window.Mercury.searchListings(query).then(function (results) {
          if (results.length === 0) {
            dropdown.innerHTML = '<div class="search-dropdown-item" style="color:var(--text-muted);">No results found</div>';
            dropdown.classList.remove('hidden');
            return;
          }

          var H = window.MercuryHelpers;
          dropdown.innerHTML = results.slice(0, 6).map(function (l) {
            var imgHtml;
            if (l.images && l.images.length) {
              imgHtml = '<img src="' + escHtml(H.ipfsUrl(l.images[0])) + '" style="width:36px;height:36px;border-radius:var(--radius);object-fit:cover;flex-shrink:0;" alt="">';
            } else {
              imgHtml = '<div style="width:36px;height:36px;background:var(--bg-tertiary);border-radius:var(--radius);display:flex;align-items:center;justify-content:center;color:var(--text-muted);flex-shrink:0;font-size:0.9rem;">\ud83d\udcf7</div>';
            }
            var price = l.priceDisplay || H.formatPrice(l.price, l.currency || '', l.decimals);
            return (
              '<div class="search-dropdown-item" data-id="' + escHtml(l.id) + '">' +
                imgHtml +
                '<div style="flex:1;min-width:0;">' +
                  '<div style="font-weight:600;font-size:0.85rem;" class="truncate">' + escHtml(l.title) + '</div>' +
                  '<div style="font-size:0.8rem;color:var(--accent);">' + escHtml(price) + '</div>' +
                '</div>' +
              '</div>'
            );
          }).join('');

          if (results.length > 6) {
            dropdown.innerHTML +=
              '<div class="search-dropdown-item" style="justify-content:center;color:var(--accent);font-size:0.85rem;cursor:pointer;" id="search-see-all">' +
                'See all ' + results.length + ' results' +
              '</div>';
            var seeAll = document.getElementById('search-see-all');
            if (seeAll) {
              seeAll.addEventListener('click', function () {
                dropdown.classList.add('hidden');
                input.value = '';
                window.location.hash = '#/search?q=' + encodeURIComponent(query);
              });
            }
          }

          dropdown.classList.remove('hidden');

          // Click handlers
          dropdown.querySelectorAll('.search-dropdown-item[data-id]').forEach(function (item) {
            item.addEventListener('click', function () {
              dropdown.classList.add('hidden');
              input.value = '';
              window.MercuryBrowse.renderListing(this.dataset.id);
            });
          });
        });
      }, 250);
    });

    // Enter key
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') {
        var query = input.value.trim();
        if (query.length >= 2) {
          dropdown.classList.add('hidden');
          input.value = '';
          window.location.hash = '#/search?q=' + encodeURIComponent(query);
        }
      }
    });

    // Close dropdown on outside click
    document.addEventListener('click', function (e) {
      if (!e.target.closest('#nav-search-container')) {
        dropdown.classList.add('hidden');
      }
    });
  }

  function escHtml(s) {
    var d = document.createElement('div');
    d.textContent = s || '';
    return d.innerHTML;
  }

  // ---- Unread badge update ----

  function updateUnreadBadge() {
    var badge = document.getElementById('unread-badge');
    if (!badge) return;
    if (!window.Mercury || typeof window.Mercury.getConversations !== 'function') {
      badge.classList.add('hidden');
      return;
    }
    var H = window.MercuryHelpers;
    Promise.resolve(window.Mercury.getConversations()).then(function (conversations) {
      var total = 0;
      (conversations || []).forEach(function (c) {
        total += H.computeUnread(c.peerId, c.messages);
      });
      if (total > 0) {
        badge.textContent = total;
        badge.classList.remove('hidden');
      } else {
        badge.classList.add('hidden');
      }
    }).catch(function () { badge.classList.add('hidden'); });
  }

  // ---- Initialisation ----

  function init() {
    window.MercuryUI.showLoading('Initialising Mercury...');

    window.Mercury.init()
      .then(function () {
        window.MercuryUI.hideLoading();

        setupMobileMenu();
        setupGlobalSearch();
        updateUnreadBadge();

        // Show auth screen
        if (window.Auth && window.Auth.showAuthScreen) {
          window.Auth.showAuthScreen();
        } else if (window.MercuryAuth && window.MercuryAuth.showLanding) {
          window.MercuryAuth.showLanding();
        }
      })
      .catch(function (err) {
        window.MercuryUI.hideLoading();
        toast('Failed to initialise Mercury: ' + err.message, 'error');
        console.error('[Mercury] Init failed:', err);
      });
  }

  // ---- Expose ----

  window.MercuryApp = {
    navigate: navigate,
    updateUnreadBadge: updateUnreadBadge,
  };

  // ---- Boot ----

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

})();
