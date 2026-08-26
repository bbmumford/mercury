/* ============================================================
   Reputation — Seller profiles, ratings, and reviews

   Consumes the raw shapes returned by the Mercury bridge:
     profile = { name?, bio?, avatarCid? }
     reputation = {
       score?, totalTrades, positiveRate, averageRating
     }
     review = {
       author (pubkey hex), rating (1-5), text,
       ts | timestamp | date
     }
   Seller name, avatar, and rating are always fetched via the
   bridge. There is no client-side listing aggregation.
   ============================================================ */

(function () {
  'use strict';

  var H = window.MercuryHelpers;
  var toast       = window.MercuryUI.toast;
  var el          = window.MercuryUI.el;
  var renderSkeleton = window.MercuryUI.renderSkeleton;

  function app() { return document.getElementById('app'); }
  function esc(s) { return H.esc(s); }

  // ---- Star Rating Component ----

  function renderStarRating(rating, maxStars) {
    maxStars = maxStars || 5;
    var r = Number(rating) || 0;
    var container = document.createElement('span');
    container.className = 'star-rating-display';

    for (var i = 1; i <= maxStars; i++) {
      var star = document.createElement('span');
      star.className = 'star-svg';
      if (i <= Math.floor(r)) {
        star.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="var(--warning)" stroke="none"><polygon points="12,2 15.09,8.26 22,9.27 17,14.14 18.18,21.02 12,17.77 5.82,21.02 7,14.14 2,9.27 8.91,8.26"/></svg>';
      } else if (i === Math.ceil(r) && r % 1 >= 0.3) {
        star.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24"><defs><linearGradient id="half-' + i + '"><stop offset="50%" stop-color="var(--warning)"/><stop offset="50%" stop-color="var(--bg-tertiary)"/></linearGradient></defs><polygon points="12,2 15.09,8.26 22,9.27 17,14.14 18.18,21.02 12,17.77 5.82,21.02 7,14.14 2,9.27 8.91,8.26" fill="url(#half-' + i + ')" stroke="none"/></svg>';
      } else {
        star.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="var(--bg-tertiary)" stroke="none"><polygon points="12,2 15.09,8.26 22,9.27 17,14.14 18.18,21.02 12,17.77 5.82,21.02 7,14.14 2,9.27 8.91,8.26"/></svg>';
      }
      container.appendChild(star);
    }

    return container;
  }

  function createStarSelector(onChange) {
    var selectedRating = 0;
    var container = document.createElement('div');
    container.className = 'star-selector';

    for (var i = 1; i <= 5; i++) {
      (function (val) {
        var star = document.createElement('button');
        star.type = 'button';
        star.className = 'star-selector-btn';
        star.dataset.value = val;
        star.innerHTML = '<svg width="28" height="28" viewBox="0 0 24 24" fill="var(--bg-tertiary)" stroke="none"><polygon points="12,2 15.09,8.26 22,9.27 17,14.14 18.18,21.02 12,17.77 5.82,21.02 7,14.14 2,9.27 8.91,8.26"/></svg>';

        star.addEventListener('mouseenter', function () {
          highlightStars(container, val);
        });

        star.addEventListener('click', function () {
          selectedRating = val;
          highlightStars(container, val);
          markSelected(container, val);
          if (onChange) onChange(val);
        });

        container.appendChild(star);
      })(i);
    }

    container.addEventListener('mouseleave', function () {
      highlightStars(container, selectedRating);
    });

    container.getSelectedRating = function () { return selectedRating; };

    return container;
  }

  function highlightStars(container, upTo) {
    var btns = container.querySelectorAll('.star-selector-btn');
    btns.forEach(function (btn) {
      var val = parseInt(btn.dataset.value, 10);
      var svg = btn.querySelector('svg polygon');
      if (svg) svg.setAttribute('fill', val <= upTo ? 'var(--warning)' : 'var(--bg-tertiary)');
    });
  }

  function markSelected(container, upTo) {
    var btns = container.querySelectorAll('.star-selector-btn');
    btns.forEach(function (btn) {
      var val = parseInt(btn.dataset.value, 10);
      btn.classList.toggle('selected', val <= upTo);
    });
  }

  // ---- Seller Profile ----

  async function renderSellerProfile(pubkeyHex) {
    var root = app();
    root.innerHTML =
      '<div id="seller-profile-container">' +
        renderSkeleton('100%', '200px') +
        '<div style="margin-top:16px;">' + renderSkeleton('100%', '300px') + '</div>' +
      '</div>';

    var profile, reputation, sellerListings;
    try {
      var results = await Promise.all([
        H.getProfile(pubkeyHex),
        H.getReputation(pubkeyHex),
        (window.Mercury.getSellerListings
          ? window.Mercury.getSellerListings(pubkeyHex)
          : Promise.resolve(null)),
      ]);
      profile        = results[0];
      reputation     = results[1];
      sellerListings = results[2] || [];
    } catch (err) {
      toast('Failed to load seller: ' + err.message, 'error');
      return;
    }

    var container = document.getElementById('seller-profile-container');
    if (!container) return;

    var sellerName = H.displayName(pubkeyHex, profile);
    var avgRating   = (reputation && typeof reputation.averageRating === 'number') ? reputation.averageRating : 0;
    var totalTrades = (reputation && typeof reputation.totalTrades === 'number') ? reputation.totalTrades : 0;
    var positiveRate = (reputation && typeof reputation.positiveRate === 'number')
      ? Math.round(reputation.positiveRate * 100) + '%'
      : '—';

    var avatarHtml = H.avatarHtml(pubkeyHex, profile, 72);

    container.innerHTML =
      '<div class="page-header">' +
        '<p class="page-subtitle"><a href="#/browse" class="text-accent">Browse</a> / Seller Profile</p>' +
      '</div>' +
      '<div class="rep-profile-card">' +
        '<div class="rep-profile-header">' +
          '<div class="rep-profile-avatar">' + avatarHtml + '</div>' +
          '<div class="rep-profile-info">' +
            '<h1 class="rep-profile-name">' + esc(sellerName) + '</h1>' +
            '<div class="rep-profile-pubkey" title="' + esc(pubkeyHex) + '">' +
              '<span style="color:var(--text-muted);font-size:0.75rem;font-family:monospace;">' + esc(H.shortHex(pubkeyHex, 8, 8)) + '</span>' +
            '</div>' +
            (profile && profile.bio ? '<p style="margin-top:8px;color:var(--text-secondary);font-size:0.9rem;">' + esc(profile.bio) + '</p>' : '') +
          '</div>' +
        '</div>' +
        '<div class="rep-stats-row">' +
          '<div class="rep-stat">' +
            '<div class="rep-stat-value">' + avgRating.toFixed(1) + '</div>' +
            '<div class="rep-stat-label">Rating</div>' +
            '<div id="rep-stars-display"></div>' +
          '</div>' +
          '<div class="rep-stat">' +
            '<div class="rep-stat-value">' + totalTrades + '</div>' +
            '<div class="rep-stat-label">Trades</div>' +
          '</div>' +
          '<div class="rep-stat">' +
            '<div class="rep-stat-value">' + sellerListings.length + '</div>' +
            '<div class="rep-stat-label">Listings</div>' +
          '</div>' +
          '<div class="rep-stat">' +
            '<div class="rep-stat-value">' + positiveRate + '</div>' +
            '<div class="rep-stat-label">Positive</div>' +
          '</div>' +
        '</div>' +
        '<div class="rep-profile-actions">' +
          '<button class="btn btn-primary" id="rep-message-seller">Message Seller</button>' +
          '<button class="btn btn-outline" id="rep-view-listings">View Listings (' + sellerListings.length + ')</button>' +
        '</div>' +
      '</div>' +
      '<div class="mt-24" id="rep-reviews-section">' +
        '<div class="loading-spinner" style="margin:24px auto;"></div>' +
      '</div>';

    var starsContainer = document.getElementById('rep-stars-display');
    if (starsContainer) {
      starsContainer.appendChild(renderStarRating(avgRating));
    }

    document.getElementById('rep-message-seller').addEventListener('click', function () {
      if (window.MercuryMessaging && window.MercuryMessaging.renderConversation) {
        window.MercuryMessaging.renderConversation(pubkeyHex);
      }
    });

    document.getElementById('rep-view-listings').addEventListener('click', function () {
      renderSellerListings(pubkeyHex, sellerName, sellerListings);
    });

    renderReviews(pubkeyHex, document.getElementById('rep-reviews-section'));
  }

  function renderSellerListings(pubkeyHex, sellerName, listings) {
    var root = app();
    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">' + esc(sellerName) + '\'s Listings</h1>' +
        '<p class="page-subtitle">' +
          '<a href="#" class="text-accent" id="back-to-profile">Back to profile</a>' +
        '</p>' +
      '</div>' +
      '<div class="listing-grid" id="seller-listings-grid"></div>';

    document.getElementById('back-to-profile').addEventListener('click', function (e) {
      e.preventDefault();
      renderSellerProfile(pubkeyHex);
    });

    var grid = document.getElementById('seller-listings-grid');
    if (!grid) return;

    if (!listings || listings.length === 0) {
      grid.innerHTML =
        '<div class="empty-state">' +
          '<div class="empty-state-icon">&#128230;</div>' +
          '<h3 class="empty-state-title">No listings</h3>' +
          '<p class="empty-state-desc">This seller has no active listings.</p>' +
        '</div>';
      return;
    }

    grid.innerHTML = listings.map(function (l) {
      var imgHtml = (l.images && l.images.length)
        ? '<img class="listing-card-img" src="' + esc(H.ipfsUrl(l.images[0])) + '" alt="" loading="lazy">'
        : '<div class="listing-card-img listing-card-img-placeholder">&#128247;</div>';
      var priceStr = l.priceDisplay || H.formatPrice(l.price, l.currency || '', l.decimals);
      return (
        '<div class="listing-card" data-listing-id="' + esc(l.id) + '">' +
          imgHtml +
          '<div class="listing-card-body">' +
            '<div class="listing-card-title">' + esc(l.title) + '</div>' +
            '<div class="listing-card-price">' + esc(priceStr) + '</div>' +
          '</div>' +
        '</div>'
      );
    }).join('');

    grid.querySelectorAll('.listing-card').forEach(function (card) {
      card.addEventListener('click', function () {
        if (window.MercuryBrowse && window.MercuryBrowse.renderListing) {
          window.MercuryBrowse.renderListing(this.dataset.listingId);
        }
      });
    });
  }

  // ---- Reviews Section ----

  var reviewsCurrentPage = 0;
  var reviewsPerPage = 10;
  var reviewsCurrentSort = 'newest';

  function renderReviews(pubkeyHex, targetContainer) {
    var container = targetContainer || document.getElementById('rep-reviews-section');
    if (!container) return;

    container.innerHTML =
      '<div class="rep-reviews-header">' +
        '<h2 style="font-size:1.1rem;font-weight:600;">Reviews</h2>' +
        '<div class="rep-reviews-sort">' +
          '<button class="filter-chip active" data-sort="newest">Newest</button>' +
          '<button class="filter-chip" data-sort="highest">Highest</button>' +
          '<button class="filter-chip" data-sort="lowest">Lowest</button>' +
        '</div>' +
      '</div>' +
      '<div id="rep-reviews-list">' +
        '<div class="loading-spinner" style="margin:24px auto;"></div>' +
      '</div>';

    container.querySelectorAll('.filter-chip').forEach(function (chip) {
      chip.addEventListener('click', function () {
        container.querySelectorAll('.filter-chip').forEach(function (c) { c.classList.remove('active'); });
        this.classList.add('active');
        reviewsCurrentSort = this.dataset.sort;
        reviewsCurrentPage = 0;
        loadReviews(pubkeyHex, container);
      });
    });

    reviewsCurrentPage = 0;
    reviewsCurrentSort = 'newest';
    loadReviews(pubkeyHex, container);
  }

  async function loadReviews(pubkeyHex, sectionContainer) {
    var listContainer = sectionContainer.querySelector('#rep-reviews-list') || sectionContainer;

    var reviews;
    try {
      reviews = (await window.Mercury.getReviews(pubkeyHex)) || [];
    } catch (err) {
      listContainer.innerHTML = '<p class="text-danger" style="text-align:center;padding:16px;">Failed to load reviews: ' + esc(err.message) + '</p>';
      return;
    }

    if (reviews.length === 0) {
      listContainer.innerHTML =
        '<div class="empty-state" style="padding:32px;">' +
          '<div class="empty-state-icon">&#9733;</div>' +
          '<h3 class="empty-state-title">No reviews yet</h3>' +
          '<p class="empty-state-desc">This seller hasn\'t received any reviews.</p>' +
        '</div>';
      return;
    }

    // Sort.
    var sorted = reviews.slice();
    if (reviewsCurrentSort === 'highest') {
      sorted.sort(function (a, b) { return b.rating - a.rating; });
    } else if (reviewsCurrentSort === 'lowest') {
      sorted.sort(function (a, b) { return a.rating - b.rating; });
    } else {
      sorted.sort(function (a, b) {
        var ta = H.toDate(a.ts || a.timestamp || a.date);
        var tb = H.toDate(b.ts || b.timestamp || b.date);
        return (tb ? tb.getTime() : 0) - (ta ? ta.getTime() : 0);
      });
    }

    var totalPages = Math.ceil(sorted.length / reviewsPerPage);
    var paged = sorted.slice(reviewsCurrentPage * reviewsPerPage, (reviewsCurrentPage + 1) * reviewsPerPage);

    // Resolve author profiles in parallel.
    var authorProfiles = await Promise.all(paged.map(function (r) {
      return H.getProfile(r.author);
    }));

    listContainer.innerHTML = paged.map(function (r, i) {
      var authorProfile = authorProfiles[i];
      var authorName    = H.displayName(r.author, authorProfile);
      var authorAvatar  = H.avatarHtml(r.author, authorProfile, 24);
      var when          = r.ts || r.timestamp || r.date;
      return (
        '<div class="rep-review-card">' +
          '<div class="rep-review-header">' +
            '<div class="rep-review-author">' +
              '<span class="avatar-small">' + authorAvatar + '</span>' +
              '<span style="font-weight:600;">' + esc(authorName) + '</span>' +
            '</div>' +
            '<div class="rep-review-meta">' +
              '<span class="rep-review-stars">' + H.renderStarsFloat(r.rating) + '</span>' +
              '<span class="rep-review-date">' + esc(H.formatRelativeTime(when)) + '</span>' +
            '</div>' +
          '</div>' +
          '<p class="rep-review-text">' + esc(r.text) + '</p>' +
        '</div>'
      );
    }).join('');

    if (totalPages > 1) {
      var paginationHtml = '<div class="rep-pagination">';
      paginationHtml += '<button class="btn btn-ghost btn-sm" id="rep-prev-page"' + (reviewsCurrentPage === 0 ? ' disabled' : '') + '>&larr; Previous</button>';
      paginationHtml += '<span class="rep-page-info">Page ' + (reviewsCurrentPage + 1) + ' of ' + totalPages + '</span>';
      paginationHtml += '<button class="btn btn-ghost btn-sm" id="rep-next-page"' + (reviewsCurrentPage >= totalPages - 1 ? ' disabled' : '') + '>Next &rarr;</button>';
      paginationHtml += '</div>';
      listContainer.innerHTML += paginationHtml;

      var prevBtn = listContainer.querySelector('#rep-prev-page');
      var nextBtn = listContainer.querySelector('#rep-next-page');

      if (prevBtn && reviewsCurrentPage > 0) {
        prevBtn.addEventListener('click', function () {
          reviewsCurrentPage--;
          loadReviews(pubkeyHex, sectionContainer);
        });
      }
      if (nextBtn && reviewsCurrentPage < totalPages - 1) {
        nextBtn.addEventListener('click', function () {
          reviewsCurrentPage++;
          loadReviews(pubkeyHex, sectionContainer);
        });
      }
    }
  }

  // ---- Submit Review ----

  function showSubmitReview(sellerPubkey, orderId) {
    var root = app();
    var selectedRating = 0;

    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">Write a Review</h1>' +
        '<p class="page-subtitle">Share your experience with this seller.</p>' +
      '</div>' +
      '<div class="rep-submit-form">' +
        '<div class="form-group">' +
          '<label class="form-label">Rating</label>' +
          '<div id="rep-star-selector"></div>' +
          '<p class="form-help" id="rep-rating-label">Select a rating</p>' +
        '</div>' +
        '<div class="form-group">' +
          '<label class="form-label">Title</label>' +
          '<input type="text" class="form-input" id="rep-review-title" placeholder="Summarise your experience..." maxlength="100">' +
        '</div>' +
        '<div class="form-group">' +
          '<label class="form-label">Review</label>' +
          '<textarea class="form-input" id="rep-review-body" placeholder="Tell others about your experience with this seller..." rows="5"></textarea>' +
        '</div>' +
        '<p class="form-error hidden" id="rep-review-error"></p>' +
        '<div style="display:flex;gap:8px;margin-top:16px;">' +
          '<button class="btn btn-primary" id="rep-submit-review">Submit Review</button>' +
          '<button class="btn btn-ghost" id="rep-cancel-review">Cancel</button>' +
        '</div>' +
      '</div>';

    var selectorContainer = document.getElementById('rep-star-selector');
    var ratingLabels = ['', 'Poor', 'Fair', 'Good', 'Very Good', 'Excellent'];
    var selector = createStarSelector(function (val) {
      selectedRating = val;
      var label = document.getElementById('rep-rating-label');
      if (label) label.textContent = ratingLabels[val] || '';
    });
    if (selectorContainer) selectorContainer.appendChild(selector);

    document.getElementById('rep-submit-review').addEventListener('click', function () {
      var btn = this;
      var title = document.getElementById('rep-review-title').value.trim();
      var body = document.getElementById('rep-review-body').value.trim();
      var errEl = document.getElementById('rep-review-error');

      if (selectedRating === 0) {
        errEl.textContent = 'Please select a rating.';
        errEl.classList.remove('hidden');
        return;
      }
      if (!body) {
        errEl.textContent = 'Please write a review.';
        errEl.classList.remove('hidden');
        return;
      }

      errEl.classList.add('hidden');
      btn.disabled = true;
      btn.textContent = 'Submitting...';

      var reviewText = title ? title + ' -- ' + body : body;

      window.Mercury.submitReview(sellerPubkey, orderId, selectedRating, reviewText)
        .then(function () {
          toast('Review submitted. Thank you!', 'success');
          renderSellerProfile(sellerPubkey);
        })
        .catch(function (err) {
          toast('Failed to submit review: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'Submit Review';
        });
    });

    document.getElementById('rep-cancel-review').addEventListener('click', function () {
      renderSellerProfile(sellerPubkey);
    });
  }

  // ---- Expose ----

  window.MercuryReputation = {
    renderSellerProfile: renderSellerProfile,
    renderReviews: renderReviews,
    showSubmitReview: showSubmitReview,
    renderStarRating: renderStarRating,
  };

})();
