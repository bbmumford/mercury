/* ============================================================
   Store — Seller's view of their own listings.

   Consumes the real Mercury bridge API:
     Mercury.getMyListings()   -> listing[]
     Mercury.publishListing(l) -> listing
     Mercury.removeListing(id) -> void
   No demo fallbacks. Shows the empty state from the spec when the
   local identity has no published listings.
   ============================================================ */

(function () {
  'use strict';

  var H     = window.MercuryHelpers;
  var toast = window.MercuryUI.toast;

  function app() { return document.getElementById('app'); }
  function esc(s) { return H.esc(s); }

  function listingPrice(l) {
    if (!l) return '';
    if (l.priceDisplay) return l.priceDisplay;
    return H.formatPrice(l.price, l.currency || '', l.decimals);
  }

  function listingImageHtml(l) {
    if (l && l.images && l.images.length) {
      return '<img class="listing-card-img" src="' + esc(H.ipfsUrl(l.images[0])) + '" alt="" loading="lazy">';
    }
    return '<div class="listing-card-img listing-card-img-placeholder">\ud83d\udcf7</div>';
  }

  async function renderMyStore() {
    var root = app();
    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">My Store</h1>' +
        '<p class="page-subtitle">Listings you have published to the network.</p>' +
      '</div>' +
      '<div id="my-store-listings" class="listing-grid">' +
        '<div class="skeleton" style="height:260px;"></div>'.repeat(3) +
      '</div>';

    var listings;
    try {
      if (typeof window.Mercury.getMyListings === 'function') {
        listings = (await window.Mercury.getMyListings()) || [];
      } else {
        listings = [];
      }
    } catch (err) {
      toast('Failed to load your listings: ' + err.message, 'error');
      return;
    }

    var container = document.getElementById('my-store-listings');
    if (!container) return;

    if (listings.length === 0) {
      container.innerHTML =
        '<div class="empty-state" style="grid-column:1/-1;">' +
          '<div class="empty-state-icon">\ud83c\udfea</div>' +
          '<h3 class="empty-state-title">No listings yet</h3>' +
          '<p class="empty-state-desc">You have no published listings. Create one to open your store.</p>' +
        '</div>';
      return;
    }

    container.innerHTML = listings.map(function (l) {
      return (
        '<div class="listing-card" data-listing-id="' + esc(l.id) + '">' +
          listingImageHtml(l) +
          '<div class="listing-card-body">' +
            '<div class="listing-card-title">' + esc(l.title) + '</div>' +
            '<div class="listing-card-price">' + esc(listingPrice(l)) + '</div>' +
          '</div>' +
        '</div>'
      );
    }).join('');

    container.querySelectorAll('.listing-card').forEach(function (card) {
      card.addEventListener('click', function () {
        if (window.MercuryBrowse && window.MercuryBrowse.renderListing) {
          window.MercuryBrowse.renderListing(this.dataset.listingId);
        }
      });
    });
  }

  window.MercuryStore = {
    renderMyStore: renderMyStore,
  };

})();
