/* ============================================================
   Browse — Home, categories, listing detail, search

   Consumes the raw shapes returned by the Mercury WASM bridge:
     listing = {
       id, title, description, category,
       seller (pubkey hex),
       price (string — native currency amount),
       currency (chain / token code),
       decimals (optional),
       variants (optional []),
       images (optional [] of CIDs, ipfs:// or https URLs),
       customFields (optional object, may contain "stock")
     }
   Seller name / rating are fetched separately via the bridge;
   USD values are never synthesised client-side.
   ============================================================ */

(function () {
  'use strict';

  var H = window.MercuryHelpers;
  var toast = window.MercuryUI.toast;

  function app() { return document.getElementById('app'); }

  function esc(s) { return H.esc(s); }

  // ---- Price display ----

  function listingPrice(l) {
    if (!l) return '';
    // Prefer a pre-formatted display string if the bridge provides one,
    // else assemble it from amount + currency.
    if (l.priceDisplay) return l.priceDisplay;
    return H.formatPrice(l.price, l.currency || '', l.decimals);
  }

  // ---- Listing image (first image if present) ----

  function listingImageHtml(l) {
    if (l && l.images && l.images.length) {
      var url = H.ipfsUrl(l.images[0]);
      return '<img class="listing-card-img" src="' + esc(url) + '" alt="" loading="lazy">';
    }
    return '<div class="listing-card-img listing-card-img-placeholder">\ud83d\udcf7</div>';
  }

  // ---- Listing Card (async — needs seller profile + reputation) ----

  function listingCardPlaceholder(l) {
    return (
      '<div class="listing-card" data-listing-id="' + esc(l.id) + '">' +
        listingImageHtml(l) +
        '<div class="listing-card-body">' +
          '<div class="listing-card-title">' + esc(l.title) + '</div>' +
          '<div class="listing-card-price">' + esc(listingPrice(l)) + '</div>' +
          '<div class="listing-card-seller" data-seller="' + esc(l.seller) + '">' +
            '<span class="avatar-placeholder"></span>' +
            '<span class="listing-card-seller-name">' + esc(H.shortHex(l.seller)) + '</span>' +
            '<span class="listing-card-rating"></span>' +
          '</div>' +
        '</div>' +
      '</div>'
    );
  }

  async function hydrateListingCard(cardEl, listing) {
    try {
      var results = await Promise.all([
        H.getProfile(listing.seller),
        H.getReputation(listing.seller),
      ]);
      var profile = results[0];
      var rep     = results[1];

      var nameEl = cardEl.querySelector('.listing-card-seller-name');
      if (nameEl) nameEl.textContent = H.displayName(listing.seller, profile);

      var avatarEl = cardEl.querySelector('.avatar-placeholder');
      if (avatarEl) {
        avatarEl.outerHTML = '<span class="avatar-small">' + H.avatarHtml(listing.seller, profile, 24) + '</span>';
      }

      if (rep && typeof rep.averageRating === 'number') {
        var ratingEl = cardEl.querySelector('.listing-card-rating');
        if (ratingEl) {
          var reviews = (typeof rep.totalTrades === 'number') ? rep.totalTrades : 0;
          ratingEl.innerHTML = H.renderStarsFloat(rep.averageRating) + ' (' + reviews + ')';
        }
      }
    } catch (err) {
      console.warn('[browse] hydrateListingCard failed', err);
    }
  }

  function bindListingCards(container) {
    container.querySelectorAll('.listing-card').forEach(function (card) {
      card.addEventListener('click', function (e) {
        // Don't treat clicks on the seller strip as a listing click if
        // we later want to route to seller. For now, any click opens
        // the listing.
        renderListing(this.dataset.listingId);
      });
    });
  }

  function renderListingGrid(container, listings) {
    container.innerHTML = listings.map(listingCardPlaceholder).join('');
    bindListingCards(container);
    // Hydrate seller profile/rating after render.
    var cards = container.querySelectorAll('.listing-card');
    cards.forEach(function (card, i) { hydrateListingCard(card, listings[i]); });
  }

  // ---- Empty states ----

  function emptyBrowseState() {
    return (
      '<div class="empty-state">' +
        '<div class="empty-state-icon">\ud83d\udecd\ufe0f</div>' +
        '<h3 class="empty-state-title">Nothing here yet</h3>' +
        '<p class="empty-state-desc">No listings yet on this network. Be the first to publish a store.</p>' +
        '<a href="#/store" class="btn btn-primary">Open My Store</a>' +
      '</div>'
    );
  }

  // ---- Home ----

  async function renderHome() {
    var root = app();
    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">Welcome to Mercury</h1>' +
        '<p class="page-subtitle">Browse decentralised listings from peers on the network.</p>' +
      '</div>' +
      '<h2 style="font-size:1.1rem;font-weight:600;margin-bottom:12px;">Categories</h2>' +
      '<div id="home-categories" class="category-grid mb-24">' +
        '<div class="skeleton" style="height:100px;"></div>'.repeat(4) +
      '</div>' +
      '<h2 style="font-size:1.1rem;font-weight:600;margin-bottom:12px;">Featured Listings</h2>' +
      '<div id="home-featured" class="listing-grid">' +
        '<div class="skeleton" style="height:260px;"></div>'.repeat(4) +
      '</div>';

    try {
      var results = await Promise.all([
        window.Mercury.getCategories(),
        window.Mercury.getListings(),
      ]);
      var categories = results[0] || [];
      var listings   = results[1] || [];

      var catContainer = document.getElementById('home-categories');
      if (catContainer) {
        if (categories.length === 0) {
          catContainer.innerHTML = '<p class="text-muted" style="grid-column:1/-1;">No categories yet.</p>';
        } else {
          catContainer.innerHTML = categories.map(function (c) {
            return (
              '<div class="category-card" data-cat="' + esc(c.id) + '">' +
                '<div class="category-icon">' + (c.icon || '') + '</div>' +
                '<div class="category-name">' + esc(c.name) + '</div>' +
                (typeof c.count === 'number' ? '<div class="category-count">' + c.count + ' listings</div>' : '') +
              '</div>'
            );
          }).join('');

          catContainer.querySelectorAll('.category-card').forEach(function (card) {
            card.addEventListener('click', function () {
              renderCategory(this.dataset.cat);
            });
          });
        }
      }

      var featContainer = document.getElementById('home-featured');
      if (featContainer) {
        if (listings.length === 0) {
          featContainer.innerHTML = emptyBrowseState();
        } else {
          renderListingGrid(featContainer, listings.slice(0, 4));
        }
      }
    } catch (err) {
      toast('Failed to load home: ' + err.message, 'error');
    }
  }

  // ---- Browse All Categories ----

  async function renderBrowse() {
    var root = app();
    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">Browse</h1>' +
        '<p class="page-subtitle">Explore all categories on the network.</p>' +
      '</div>' +
      '<div id="browse-cats" class="category-grid">' +
        '<div class="skeleton" style="height:100px;"></div>'.repeat(8) +
      '</div>';

    try {
      var categories = (await window.Mercury.getCategories()) || [];
      var container  = document.getElementById('browse-cats');
      if (!container) return;

      if (categories.length === 0) {
        container.innerHTML =
          '<div class="empty-state" style="grid-column:1/-1;">' +
            '<h3 class="empty-state-title">No categories yet</h3>' +
            '<p class="empty-state-desc">No listings yet on this network. Be the first to publish a store.</p>' +
          '</div>';
        return;
      }

      container.innerHTML = categories.map(function (c) {
        return (
          '<div class="category-card" data-cat="' + esc(c.id) + '">' +
            '<div class="category-icon">' + (c.icon || '') + '</div>' +
            '<div class="category-name">' + esc(c.name) + '</div>' +
            (typeof c.count === 'number' ? '<div class="category-count">' + c.count + ' listings</div>' : '') +
          '</div>'
        );
      }).join('');

      container.querySelectorAll('.category-card').forEach(function (card) {
        card.addEventListener('click', function () {
          renderCategory(this.dataset.cat);
        });
      });
    } catch (err) {
      toast('Failed to load categories: ' + err.message, 'error');
    }
  }

  // ---- Category View ----

  async function renderCategory(name) {
    var root = app();
    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title" id="cat-title">Loading...</h1>' +
        '<p class="page-subtitle"><a href="#/browse" class="text-accent">Browse</a> / ' + esc(name) + '</p>' +
      '</div>' +
      '<div class="filter-bar" id="cat-filters"></div>' +
      '<div id="cat-listings" class="listing-grid">' +
        '<div class="skeleton" style="height:260px;"></div>'.repeat(3) +
      '</div>';

    try {
      var results = await Promise.all([
        window.Mercury.getCategories(),
        window.Mercury.getListings(name),
      ]);
      var categories = results[0] || [];
      var listings   = results[1] || [];

      var cat = categories.find(function (c) { return c.id === name; });
      var titleEl = document.getElementById('cat-title');
      if (titleEl) titleEl.textContent = cat ? ((cat.icon || '') + ' ' + cat.name).trim() : name;

      var filtersEl = document.getElementById('cat-filters');
      if (filtersEl) {
        filtersEl.innerHTML =
          '<button class="filter-chip active" data-sort="default">All</button>' +
          '<button class="filter-chip" data-sort="price-low">Price: Low to High</button>' +
          '<button class="filter-chip" data-sort="price-high">Price: High to Low</button>' +
          '<button class="filter-chip" data-sort="rating">Top Rated</button>';

        filtersEl.querySelectorAll('.filter-chip').forEach(function (chip) {
          chip.addEventListener('click', function () {
            filtersEl.querySelectorAll('.filter-chip').forEach(function (c) { c.classList.remove('active'); });
            this.classList.add('active');
          });
        });
      }

      var container = document.getElementById('cat-listings');
      if (!container) return;

      if (listings.length === 0) {
        container.innerHTML =
          '<div class="empty-state">' +
            '<div class="empty-state-icon">\ud83d\udd0d</div>' +
            '<h3 class="empty-state-title">No listings found</h3>' +
            '<p class="empty-state-desc">No peers are currently offering items in this category.</p>' +
          '</div>';
        return;
      }

      renderListingGrid(container, listings);
    } catch (err) {
      toast('Failed to load category: ' + err.message, 'error');
    }
  }

  // ---- Listing Detail ----

  async function renderListing(id) {
    var root = app();
    root.innerHTML =
      '<div id="listing-detail-container">' +
        '<div class="skeleton" style="height:400px;"></div>' +
      '</div>';

    var listing;
    try {
      listing = await window.Mercury.getListing(id);
    } catch (err) {
      toast('Failed to load listing: ' + err.message, 'error');
      return;
    }

    if (!listing) {
      root.innerHTML =
        '<div class="empty-state">' +
          '<div class="empty-state-icon">\u2753</div>' +
          '<h3 class="empty-state-title">Listing not found</h3>' +
          '<p class="empty-state-desc">This listing may have been removed or is no longer available.</p>' +
          '<a href="#/browse" class="btn btn-primary">Browse Listings</a>' +
        '</div>';
      return;
    }

    // Kick off seller profile + reputation fetches in parallel.
    var profilePromise = H.getProfile(listing.seller);
    var repPromise     = H.getReputation(listing.seller);

    // Variant options.
    var variantsHtml = '';
    if (listing.variants && listing.variants.length > 0) {
      variantsHtml =
        '<div style="margin-top:4px;">' +
          '<label class="form-label">Options</label>' +
          '<div class="listing-variant-select">' +
            listing.variants.map(function (v, i) {
              return '<button class="variant-option' + (i === 0 ? ' selected' : '') + '" data-variant="' + esc(v) + '">' + esc(v) + '</button>';
            }).join('') +
          '</div>' +
        '</div>';
    }

    // Images.
    var images = (listing.images && listing.images.length) ? listing.images : [];
    var mainImgHtml = images.length
      ? '<img class="listing-main-image" src="' + esc(H.ipfsUrl(images[0])) + '" alt="">'
      : '<div class="listing-main-image listing-main-image-placeholder">\ud83d\udcf7</div>';
    var thumbsHtml = images.length
      ? images.map(function (c, i) {
          return '<div class="listing-thumb' + (i === 0 ? ' active' : '') + '" data-idx="' + i + '"><img src="' + esc(H.ipfsUrl(c)) + '" alt=""></div>';
        }).join('')
      : '';

    // Stock — only if advertised in customFields.
    var stockValue = null;
    if (listing.customFields && typeof listing.customFields.stock !== 'undefined') {
      stockValue = listing.customFields.stock;
    }
    var stockHtml = (stockValue && Number(stockValue) > 0)
      ? '<span class="text-muted" style="margin-left:12px;font-size:0.85rem;" id="listing-stock-text">' + esc(stockValue) + ' available</span>'
      : '';

    var priceStr = listingPrice(listing);

    var container = document.getElementById('listing-detail-container');
    container.innerHTML =
      '<div class="page-header">' +
        '<p class="page-subtitle"><a href="#/browse" class="text-accent">Browse</a> / <a href="#" class="text-accent cat-link" data-cat="' + esc(listing.category) + '">' + esc(listing.category) + '</a> / ' + esc(listing.title) + '</p>' +
      '</div>' +
      '<div class="listing-detail">' +
        '<div class="listing-detail-images">' +
          mainImgHtml +
          (thumbsHtml ? '<div class="listing-thumbnails">' + thumbsHtml + '</div>' : '') +
        '</div>' +
        '<div class="listing-detail-info">' +
          '<h1 class="listing-detail-title">' + esc(listing.title) + '</h1>' +
          '<div class="listing-detail-price">' + esc(priceStr) + '</div>' +
          '<div class="listing-detail-seller" style="cursor:pointer;" data-seller="' + esc(listing.seller) + '">' +
            '<span class="avatar-small" id="listing-seller-avatar"></span>' +
            '<div>' +
              '<div style="font-weight:600;" id="listing-seller-name">' + esc(H.shortHex(listing.seller)) + '</div>' +
              '<div style="font-size:0.8rem;" id="listing-seller-rating"></div>' +
            '</div>' +
          '</div>' +
          variantsHtml +
          '<div>' +
            '<label class="form-label">Quantity</label>' +
            '<div class="quantity-control">' +
              '<button id="qty-minus">\u2212</button>' +
              '<span id="qty-value">1</span>' +
              '<button id="qty-plus">+</button>' +
            '</div>' +
            stockHtml +
          '</div>' +
          '<div style="display:flex;gap:8px;">' +
            '<button class="btn btn-primary btn-lg" id="listing-checkout">Place Order</button>' +
            '<button class="btn btn-outline btn-lg" id="listing-message-seller">Message Seller</button>' +
          '</div>' +
          '<div class="card mt-16">' +
            '<div class="card-title">Description</div>' +
            '<p style="color:var(--text-secondary);margin-top:8px;font-size:0.9rem;line-height:1.6;">' + esc(listing.description) + '</p>' +
          '</div>' +
        '</div>' +
      '</div>' +
      '<div class="mt-24" id="listing-reviews-section"></div>';

    // Hydrate seller name / avatar / rating.
    profilePromise.then(function (profile) {
      var nameEl = document.getElementById('listing-seller-name');
      if (nameEl) nameEl.textContent = H.displayName(listing.seller, profile);
      var avEl = document.getElementById('listing-seller-avatar');
      if (avEl) avEl.innerHTML = H.avatarHtml(listing.seller, profile, 32);
    });

    repPromise.then(function (rep) {
      var ratingEl = document.getElementById('listing-seller-rating');
      if (!ratingEl) return;
      if (rep && typeof rep.averageRating === 'number') {
        var reviews = (typeof rep.totalTrades === 'number') ? rep.totalTrades : 0;
        ratingEl.innerHTML = H.renderStarsFloat(rep.averageRating) + ' <span class="text-muted">(' + reviews + ' reviews)</span>';
      } else {
        ratingEl.innerHTML = '<span class="text-muted">No reputation yet</span>';
      }
    });

    // Variant selection
    container.querySelectorAll('.variant-option').forEach(function (btn) {
      btn.addEventListener('click', function () {
        container.querySelectorAll('.variant-option').forEach(function (b) { b.classList.remove('selected'); });
        this.classList.add('selected');
      });
    });

    // Quantity control — respect stockValue only if advertised.
    var qty = 1;
    var qtyMaxHard = stockValue && Number(stockValue) > 0 ? Number(stockValue) : null;
    document.getElementById('qty-minus').addEventListener('click', function () {
      if (qty > 1) { qty--; document.getElementById('qty-value').textContent = qty; }
    });
    document.getElementById('qty-plus').addEventListener('click', function () {
      if (qtyMaxHard == null || qty < qtyMaxHard) {
        qty++;
        document.getElementById('qty-value').textContent = qty;
      }
    });

    // Thumbnail clicks swap main image.
    container.querySelectorAll('.listing-thumb').forEach(function (t) {
      t.addEventListener('click', function () {
        container.querySelectorAll('.listing-thumb').forEach(function (tt) { tt.classList.remove('active'); });
        this.classList.add('active');
        var idx = parseInt(this.dataset.idx, 10);
        var main = container.querySelector('.listing-main-image');
        if (main && images[idx]) {
          if (main.tagName === 'IMG') main.src = H.ipfsUrl(images[idx]);
        }
      });
    });

    // Checkout
    document.getElementById('listing-checkout').addEventListener('click', function () {
      var selectedVariant = container.querySelector('.variant-option.selected');
      if (window.MercuryOrder && window.MercuryOrder.showCheckout) {
        window.MercuryOrder.showCheckout(listing.id, {
          variant: selectedVariant ? selectedVariant.dataset.variant : null,
          quantity: qty,
        });
      }
    });

    // Message seller
    document.getElementById('listing-message-seller').addEventListener('click', function () {
      if (window.MercuryMessaging && window.MercuryMessaging.renderConversation) {
        window.MercuryMessaging.renderConversation(listing.seller);
      }
    });

    // Seller profile link
    container.querySelector('.listing-detail-seller').addEventListener('click', function () {
      if (window.MercuryReputation && window.MercuryReputation.renderSellerProfile) {
        window.MercuryReputation.renderSellerProfile(this.dataset.seller);
      }
    });

    // Category breadcrumb
    var catLink = container.querySelector('.cat-link');
    if (catLink) {
      catLink.addEventListener('click', function (e) {
        e.preventDefault();
        renderCategory(this.dataset.cat);
      });
    }

    // Reviews section
    if (window.MercuryReputation && window.MercuryReputation.renderReviews) {
      window.MercuryReputation.renderReviews(listing.seller, document.getElementById('listing-reviews-section'));
    }
  }

  // ---- Search ----

  async function renderSearch(query) {
    var root = app();
    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">Search Results</h1>' +
        '<p class="page-subtitle">Results for: "' + esc(query) + '"</p>' +
      '</div>' +
      '<div id="search-results" class="listing-grid">' +
        '<div class="skeleton" style="height:260px;"></div>'.repeat(3) +
      '</div>';

    try {
      var listings = (await window.Mercury.searchListings(query)) || [];
      var container = document.getElementById('search-results');
      if (!container) return;

      if (listings.length === 0) {
        container.innerHTML =
          '<div class="empty-state">' +
            '<div class="empty-state-icon">\ud83d\udd0d</div>' +
            '<h3 class="empty-state-title">No results</h3>' +
            '<p class="empty-state-desc">No listings matched "' + esc(query) + '". Try different keywords.</p>' +
          '</div>';
        return;
      }

      renderListingGrid(container, listings);
    } catch (err) {
      toast('Search failed: ' + err.message, 'error');
    }
  }

  // ---- Expose ----

  window.MercuryBrowse = {
    renderHome:     renderHome,
    renderBrowse:   renderBrowse,
    renderCategory: renderCategory,
    renderListing:  renderListing,
    renderSearch:   renderSearch,
  };

})();
