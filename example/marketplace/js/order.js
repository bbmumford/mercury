/* ============================================================
   Orders — Order creation, tracking, and completion

   Consumes the raw shapes returned by the Mercury bridge:
     order = {
       id, listingId, seller, buyer,
       status, variant?, quantity?,
       price, currency, decimals?,
       ts | createdAt | placedAt,
       fundedAt?, acceptedAt?, fulfilledAt?,
       completedAt?, disputedAt?, refundedAt?,
       escrowId?
     }
   Listing title, seller name, and timeline are all derived on the
   client from fresh WASM bridge calls (cached via helpers.js).
   ============================================================ */

(function () {
  'use strict';

  var H = window.MercuryHelpers;
  var toast = window.MercuryUI.toast;
  var el    = window.MercuryUI.el;
  var delay = window.MercuryUI.delay;

  function app() { return document.getElementById('app'); }
  function esc(s) { return H.esc(s); }

  // ---- Status config ----

  var statusMeta = {
    pending:   { label: 'Pending',   css: 'status-pending'   },
    funded:    { label: 'Funded',    css: 'status-funded'    },
    accepted:  { label: 'Accepted',  css: 'status-accepted'  },
    fulfilled: { label: 'Fulfilled', css: 'status-fulfilled' },
    completed: { label: 'Completed', css: 'status-completed' },
    disputed:  { label: 'Disputed',  css: 'status-disputed'  },
    refunded:  { label: 'Refunded',  css: 'status-refunded'  },
    cancelled: { label: 'Cancelled', css: 'status-cancelled' },
  };

  function statusBadge(status) {
    var key = (status || '').toLowerCase();
    var m = statusMeta[key] || { label: status || 'Unknown', css: '' };
    return '<span class="status ' + m.css + '">' + esc(m.label) + '</span>';
  }

  function orderPrice(order) {
    if (!order) return '';
    if (order.priceDisplay) return order.priceDisplay;
    return H.formatPrice(order.price, order.currency || '', order.decimals);
  }

  // ---- Fee helpers ----

  function parsePriceAmount(str) {
    if (str == null) return 0;
    if (typeof str === 'number') return str;
    var m = String(str).match(/([\d.]+)/);
    return m ? parseFloat(m[1]) : 0;
  }

  function feeBreakdown(amount, currency) {
    var maintainer = amount * 0.01;
    var witness    = amount * 0.005;
    return {
      item:       amount.toFixed(6),
      maintainer: maintainer.toFixed(6),
      witness:    witness.toFixed(6),
      total:      (amount + maintainer + witness).toFixed(6),
      currency:   currency || '',
    };
  }

  function feeTable(fees) {
    var cur = fees.currency ? ' ' + fees.currency : '';
    return '<div class="fee-breakdown">' +
      '<div class="fee-row"><span class="fee-label">Item price</span><span class="fee-value">' + fees.item + cur + '</span></div>' +
      '<div class="fee-row"><span class="fee-label">Maintainer fee (1%)</span><span class="fee-value">' + fees.maintainer + cur + '</span></div>' +
      '<div class="fee-row"><span class="fee-label">Witness fee (0.5%)</span><span class="fee-value">' + fees.witness + cur + '</span></div>' +
      '<div class="fee-row total"><span class="fee-label">Total</span><span class="fee-value">' + fees.total + cur + '</span></div>' +
    '</div>';
  }

  function formatFileSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / 1048576).toFixed(1) + ' MB';
  }

  // ---- Inline spinner ----

  function spinner(size) {
    var s = document.createElement('div');
    s.className = 'loading-spinner';
    if (size) { s.style.width = size + 'px'; s.style.height = size + 'px'; }
    return s;
  }

  function spinnerBlock() {
    var wrap = el('div', { className: 'flex flex-center', style: 'padding:48px;' });
    wrap.appendChild(spinner(32));
    return wrap;
  }

  // ============================================================
  //  Order List
  // ============================================================

  async function renderOrders() {
    var root = app();
    root.innerHTML = '';

    root.insertAdjacentHTML('beforeend',
      '<div class="page-header">' +
        '<h1 class="page-title">Orders</h1>' +
        '<p class="page-subtitle">Track your purchases and sales.</p>' +
      '</div>'
    );

    var tabDefs = [
      { key: 'active',    label: 'Active' },
      { key: 'completed', label: 'Completed' },
      { key: 'all',       label: 'All' },
    ];
    var activeTab = 'active';

    var tabBar = el('div', { className: 'tabs', id: 'order-tabs' });
    root.appendChild(tabBar);

    var listWrap = el('div', { id: 'orders-list' });
    root.appendChild(listWrap);

    function buildTabs() {
      tabBar.innerHTML = '';
      tabDefs.forEach(function (td) {
        var btn = el('button', {
          className: 'tab' + (td.key === activeTab ? ' active' : ''),
          textContent: td.label,
        });
        btn.addEventListener('click', function () {
          activeTab = td.key;
          buildTabs();
          loadOrders();
        });
        tabBar.appendChild(btn);
      });
    }

    async function loadOrders() {
      listWrap.innerHTML = '';
      listWrap.appendChild(spinnerBlock());

      var orders;
      try {
        orders = (await window.Mercury.getOrders()) || [];
      } catch (err) {
        listWrap.innerHTML = '';
        toast('Failed to load orders: ' + err.message, 'error');
        return;
      }

      var filtered = orders;
      if (activeTab === 'active') {
        filtered = orders.filter(function (o) {
          var s = (o.status || '').toLowerCase();
          return s !== 'completed' && s !== 'cancelled' && s !== 'refunded';
        });
      } else if (activeTab === 'completed') {
        filtered = orders.filter(function (o) { return (o.status || '').toLowerCase() === 'completed'; });
      }

      listWrap.innerHTML = '';

      if (filtered.length === 0) {
        listWrap.innerHTML =
          '<div class="empty-state">' +
            '<div class="empty-state-icon">&#128230;</div>' +
            '<h3 class="empty-state-title">No orders</h3>' +
            '<p class="empty-state-desc">' +
              (activeTab === 'all'
                ? 'You haven\'t placed or received any orders yet.'
                : 'No ' + activeTab + ' orders.') +
            '</p>' +
            '<a href="#/browse" class="btn btn-primary">Browse Listings</a>' +
          '</div>';
        return;
      }

      filtered.forEach(function (order) {
        var timeline = H.orderTimeline(order);
        var lastStep = null;
        for (var i = timeline.length - 1; i >= 0; i--) {
          if (timeline[i].ts) { lastStep = timeline[i]; break; }
        }
        var lastTimeDisplay = lastStep ? H.formatRelativeTime(lastStep.ts) : '';

        var card = el('div', { className: 'card card-hover', style: 'margin-bottom:12px;cursor:pointer;' });
        card.innerHTML =
          '<div class="card-header">' +
            '<div style="min-width:0;">' +
              '<div class="card-title truncate" data-role="listing-title">' + esc(H.shortHex(order.listingId, 6, 4)) + '</div>' +
              '<div class="text-muted" style="font-size:0.8rem;margin-top:2px;">' +
                '<span data-role="seller-name">' + esc(H.shortHex(order.seller)) + '</span>' +
                (lastTimeDisplay ? ' &middot; ' + esc(lastTimeDisplay) : '') +
              '</div>' +
            '</div>' +
            '<div style="flex-shrink:0;margin-left:12px;">' + statusBadge(order.status) + '</div>' +
          '</div>' +
          '<div class="flex" style="align-items:center;justify-content:space-between;">' +
            '<span class="text-accent" style="font-weight:700;">' + esc(orderPrice(order)) + '</span>' +
            '<span class="mono text-muted" style="font-size:0.75rem;">' + esc(H.shortHex(order.id, 6, 4)) + '</span>' +
          '</div>';

        card.addEventListener('click', function () {
          renderOrderDetail(order.id);
        });
        listWrap.appendChild(card);

        // Hydrate listing title and seller name.
        H.getListing(order.listingId).then(function (l) {
          var t = card.querySelector('[data-role="listing-title"]');
          if (t && l && l.title) t.textContent = l.title;
        });
        H.getProfile(order.seller).then(function (p) {
          var n = card.querySelector('[data-role="seller-name"]');
          if (n) n.textContent = H.displayName(order.seller, p);
        });
      });
    }

    buildTabs();
    await loadOrders();
  }

  // ============================================================
  //  Checkout — multi-step
  // ============================================================

  async function showCheckout(listingId, options) {
    var root = app();
    root.innerHTML = '';
    root.appendChild(spinnerBlock());

    var listing;
    try {
      listing = await window.Mercury.getListing(listingId);
    } catch (err) {
      toast('Failed to load listing: ' + err.message, 'error');
      return;
    }

    if (!listing) {
      toast('Listing not found', 'error');
      return;
    }

    root.innerHTML = '';

    var variants = listing.variants || [];
    var variant  = (options && options.variant)  ? options.variant  : (variants.length ? variants[0] : null);
    var quantity = (options && options.quantity) ? options.quantity : 1;
    var step = 1;

    // Native currency of the listing is the default payment option.
    var nativeChain = {
      id:    listing.currency || 'native',
      label: listing.currency || 'Native',
      icon:  H.chainIcon(listing.currency),
      color: H.chainColor(listing.currency),
    };
    var selectedChain = nativeChain.id;
    var chains = [nativeChain];

    // Resolve seller profile for the summary block.
    var sellerProfile = await H.getProfile(listing.seller);
    var sellerDisplay = H.displayName(listing.seller, sellerProfile);

    var stockValue = null;
    if (listing.customFields && typeof listing.customFields.stock !== 'undefined') {
      stockValue = Number(listing.customFields.stock);
    }
    var qtyMaxHard = (stockValue && stockValue > 0) ? stockValue : null;

    var priceStr = listing.priceDisplay || H.formatPrice(listing.price, listing.currency || '', listing.decimals);

    var wrap = el('div', { style: 'max-width:640px;margin:0 auto;' });
    root.appendChild(wrap);

    function render() {
      wrap.innerHTML = '';

      var prog = el('div', { style: 'display:flex;gap:8px;margin-bottom:28px;' });
      for (var s = 1; s <= 4; s++) {
        prog.appendChild(el('div', {
          style: 'flex:1;height:4px;border-radius:2px;transition:background 0.3s;background:' +
            (s <= step ? 'var(--accent)' : 'var(--border)') + ';',
        }));
      }
      wrap.appendChild(prog);

      var labels = ['', 'Review Item', 'Select Payment', 'Fee Breakdown', 'Placing Order'];
      wrap.appendChild(el('p', {
        className: 'text-muted',
        textContent: 'Step ' + step + ' of 4 \u2014 ' + labels[step],
        style: 'font-size:0.8rem;margin-bottom:16px;',
      }));

      if (step === 1) renderStep1();
      else if (step === 2) renderStep2();
      else if (step === 3) renderStep3();
      else renderStep4();
    }

    function renderStep1() {
      var descPreview = listing.description
        ? (listing.description.length > 140 ? listing.description.substring(0, 140) + '...' : listing.description)
        : '';

      var imgHtml = (listing.images && listing.images.length)
        ? '<img src="' + esc(H.ipfsUrl(listing.images[0])) + '" style="width:88px;height:88px;border-radius:var(--radius);object-fit:cover;flex-shrink:0;">'
        : '<div style="width:88px;height:88px;background:var(--bg-tertiary);border-radius:var(--radius);display:flex;align-items:center;justify-content:center;font-size:2rem;color:var(--text-muted);flex-shrink:0;">&#128247;</div>';

      var card = el('div', { className: 'card' });
      card.innerHTML =
        '<h2 style="font-size:1.15rem;font-weight:700;margin-bottom:16px;">Review Item</h2>' +
        '<div style="display:flex;gap:16px;align-items:start;">' +
          imgHtml +
          '<div style="min-width:0;">' +
            '<div style="font-weight:600;">' + esc(listing.title) + '</div>' +
            '<div class="text-muted" style="font-size:0.85rem;margin-top:4px;">' + esc(descPreview) + '</div>' +
            '<div class="text-accent" style="font-weight:700;margin-top:8px;">' + esc(priceStr) + '</div>' +
          '</div>' +
        '</div>';

      if (variants.length > 0) {
        var vGroup = el('div', { className: 'form-group', style: 'margin-top:16px;' });
        vGroup.innerHTML = '<label class="form-label">Variant</label>';
        var vRow = el('div', { className: 'listing-variant-select' });
        variants.forEach(function (v) {
          var opt = el('button', {
            className: 'variant-option' + (v === variant ? ' selected' : ''),
            textContent: v,
          });
          opt.addEventListener('click', function () { variant = v; render(); });
          vRow.appendChild(opt);
        });
        vGroup.appendChild(vRow);
        card.appendChild(vGroup);
      }

      var qGroup = el('div', { className: 'form-group', style: 'margin-top:12px;' });
      qGroup.innerHTML = '<label class="form-label">Quantity</label>';
      var qCtrl = el('div', { className: 'quantity-control' });
      var qMinus = el('button', { textContent: '\u2212' });
      qMinus.addEventListener('click', function () { if (quantity > 1) { quantity--; render(); } });
      var qVal = el('span', { textContent: String(quantity) });
      var qPlus = el('button', { textContent: '+' });
      qPlus.addEventListener('click', function () {
        if (qtyMaxHard == null || quantity < qtyMaxHard) { quantity++; render(); }
      });
      qCtrl.appendChild(qMinus);
      qCtrl.appendChild(qVal);
      qCtrl.appendChild(qPlus);
      qGroup.appendChild(qCtrl);
      if (qtyMaxHard != null) {
        qGroup.appendChild(el('p', { className: 'form-help', textContent: qtyMaxHard + ' available' }));
      }
      card.appendChild(qGroup);
      wrap.appendChild(card);

      var nav = el('div', { style: 'display:flex;gap:12px;margin-top:20px;' });
      nav.appendChild(el('a', { className: 'btn btn-ghost', href: '#/browse', textContent: 'Cancel' }));
      var next = el('button', { className: 'btn btn-primary', textContent: 'Continue', style: 'margin-left:auto;' });
      next.addEventListener('click', function () { step = 2; render(); });
      nav.appendChild(next);
      wrap.appendChild(nav);
    }

    function renderStep2() {
      var card = el('div', { className: 'card' });
      card.innerHTML = '<h2 style="font-size:1.15rem;font-weight:700;margin-bottom:16px;">Select Payment</h2>';

      var list = el('div', { style: 'display:flex;flex-direction:column;gap:8px;' });
      chains.forEach(function (c) {
        var row = el('div', {
          className: 'wallet-account' + (c.id === selectedChain ? ' active' : ''),
        });
        row.innerHTML =
          '<div class="wallet-account-info">' +
            '<div class="chain-icon" style="background:' + c.color + '22;color:' + c.color + ';">' + esc(c.icon) + '</div>' +
            '<div style="font-weight:600;">' + esc(c.label) + '</div>' +
          '</div>' +
          (c.id === selectedChain ? '<span class="text-accent" style="font-size:1.2rem;">&#10003;</span>' : '');
        row.addEventListener('click', function () { selectedChain = c.id; render(); });
        list.appendChild(row);
      });
      card.appendChild(list);
      wrap.appendChild(card);

      var nav = el('div', { style: 'display:flex;gap:12px;margin-top:20px;' });
      var back = el('button', { className: 'btn btn-ghost', textContent: 'Back' });
      back.addEventListener('click', function () { step = 1; render(); });
      nav.appendChild(back);
      var next = el('button', { className: 'btn btn-primary', textContent: 'Continue', style: 'margin-left:auto;' });
      next.addEventListener('click', function () { step = 3; render(); });
      nav.appendChild(next);
      wrap.appendChild(nav);
    }

    function renderStep3() {
      var amt  = parsePriceAmount(listing.price) * quantity;
      var fees = feeBreakdown(amt, listing.currency || '');

      var card = el('div', { className: 'card' });
      card.innerHTML =
        '<h2 style="font-size:1.15rem;font-weight:700;margin-bottom:16px;">Fee Breakdown</h2>' +
        feeTable(fees) +
        '<p class="text-muted" style="font-size:0.8rem;margin-top:12px;">Funds will be held in escrow until you confirm receipt.</p>';
      wrap.appendChild(card);

      var summary = el('div', { className: 'card', style: 'margin-top:12px;' });
      summary.innerHTML =
        '<h3 style="font-size:0.95rem;font-weight:600;margin-bottom:10px;">Summary</h3>' +
        '<div style="font-size:0.85rem;color:var(--text-secondary);line-height:1.8;">' +
          'Item: ' + esc(listing.title) + '<br>' +
          (variant ? 'Variant: ' + esc(variant) + '<br>' : '') +
          'Qty: ' + quantity + '<br>' +
          'Payment: ' + esc(selectedChain) + '<br>' +
          'Seller: ' + esc(sellerDisplay) +
        '</div>';
      wrap.appendChild(summary);

      var nav = el('div', { style: 'display:flex;gap:12px;margin-top:20px;' });
      var back = el('button', { className: 'btn btn-ghost', textContent: 'Back' });
      back.addEventListener('click', function () { step = 2; render(); });
      nav.appendChild(back);
      var confirm = el('button', { className: 'btn btn-success', textContent: 'Confirm & Fund Escrow', style: 'margin-left:auto;' });
      confirm.addEventListener('click', function () { step = 4; render(); });
      nav.appendChild(confirm);
      wrap.appendChild(nav);
    }

    function renderStep4() {
      var card = el('div', { className: 'card', style: 'text-align:center;padding:48px 24px;' });
      card.appendChild(spinner(40));
      card.appendChild(el('p', {
        className: 'text-muted',
        textContent: 'Creating escrow and locking funds...',
        style: 'margin-top:16px;',
      }));
      wrap.appendChild(card);

      window.Mercury.createOrder(listingId, { variant: variant, quantity: quantity, chain: selectedChain })
        .then(function (order) { return delay(500).then(function () { return order; }); })
        .then(function (order) {
          card.innerHTML =
            '<div style="font-size:2.5rem;color:var(--success);margin-bottom:12px;">&#10003;</div>' +
            '<h2 style="font-size:1.15rem;font-weight:700;margin-bottom:8px;">Order Created</h2>' +
            '<p class="text-muted" style="margin-bottom:4px;">ID: <span class="mono">' + esc(H.shortHex(order.id, 6, 4)) + '</span></p>' +
            '<p class="text-muted" style="margin-bottom:24px;">Funds are locked in escrow. The seller has been notified.</p>' +
            '<div style="display:flex;gap:12px;justify-content:center;">' +
              '<button class="btn btn-primary" id="co-view">View Order</button>' +
              '<a href="#/orders" class="btn btn-ghost">All Orders</a>' +
            '</div>';
          document.getElementById('co-view').addEventListener('click', function () {
            renderOrderDetail(order.id);
          });
          toast('Order placed successfully', 'success');
        })
        .catch(function (err) {
          card.innerHTML =
            '<div style="font-size:2.5rem;color:var(--danger);margin-bottom:12px;">&#10007;</div>' +
            '<h2 style="font-size:1.15rem;font-weight:700;margin-bottom:8px;">Order Failed</h2>' +
            '<p class="text-muted" style="margin-bottom:24px;">' + esc(err && err.message ? err.message : String(err)) + '</p>' +
            '<button class="btn btn-ghost" id="co-retry">Try Again</button>';
          document.getElementById('co-retry').addEventListener('click', function () {
            step = 3; render();
          });
          toast('Order failed: ' + (err && err.message ? err.message : err), 'error');
        });
    }

    render();
  }

  // ============================================================
  //  Order Detail
  // ============================================================

  async function renderOrderDetail(id) {
    var root = app();
    root.innerHTML = '';
    root.appendChild(spinnerBlock());

    var order;
    try {
      order = await window.Mercury.getOrder(id);
    } catch (err) {
      toast('Failed to load order: ' + err.message, 'error');
      return;
    }

    if (!order) {
      root.innerHTML =
        '<div class="empty-state">' +
          '<div class="empty-state-icon">&#10067;</div>' +
          '<h3 class="empty-state-title">Order not found</h3>' +
          '<a href="#/orders" class="btn btn-primary mt-16">Back to Orders</a>' +
        '</div>';
      return;
    }

    // Resolve listing + seller profile in parallel.
    var hydrated = await Promise.all([
      H.getListing(order.listingId),
      H.getProfile(order.seller),
    ]);
    var listing = hydrated[0];
    var profile = hydrated[1];
    var listingTitle = (listing && listing.title) ? listing.title : H.shortHex(order.listingId, 6, 4);
    var sellerDisplay = H.displayName(order.seller, profile);

    root.innerHTML = '';

    root.insertAdjacentHTML('beforeend',
      '<div class="page-header" style="display:flex;align-items:flex-start;justify-content:space-between;flex-wrap:wrap;gap:12px;">' +
        '<div>' +
          '<h1 class="page-title">' + esc(listingTitle) + '</h1>' +
          '<p class="page-subtitle"><a href="#/orders">Orders</a> / <span class="mono">' + esc(H.shortHex(order.id, 6, 4)) + '</span></p>' +
        '</div>' +
        '<div>' + statusBadge(order.status) + '</div>' +
      '</div>'
    );

    var grid = el('div', { className: 'grid-2' });

    // ----- Timeline -----
    var tlCard = el('div', { className: 'card' });
    tlCard.innerHTML = '<h3 class="card-title" style="margin-bottom:16px;">Status Timeline</h3>';

    var tlWrap = el('div', { className: 'order-timeline' });
    var timeline = H.orderTimeline(order);
    timeline.forEach(function (s, idx) {
      var isDone   = s.done;
      var isActive = !isDone && (idx === 0 || timeline[idx - 1].done);
      var cls = isDone ? ' completed' : (isActive ? ' active' : '');
      var stepDiv = el('div', { className: 'timeline-step' + cls });
      stepDiv.innerHTML =
        '<div class="timeline-dot"></div>' +
        '<div class="timeline-content">' +
          '<div class="timeline-title">' + esc(s.step) + '</div>' +
          '<div class="timeline-desc">' + (s.time ? esc(s.time) : 'Pending') + '</div>' +
        '</div>';
      tlWrap.appendChild(stepDiv);
    });
    tlCard.appendChild(tlWrap);
    grid.appendChild(tlCard);

    // ----- Details + actions -----
    var dtCard = el('div', { className: 'card' });
    var escrowDisplay = order.escrowId ? H.shortHex(order.escrowId, 8, 6) : H.shortHex(order.id, 8, 6);

    dtCard.innerHTML =
      '<div class="card-header"><span class="card-title">Details</span></div>' +
      '<div style="display:flex;flex-direction:column;gap:10px;font-size:0.9rem;">' +
        '<div class="flex" style="justify-content:space-between;"><span class="text-muted">Price</span><span class="text-accent" style="font-weight:700;">' + esc(orderPrice(order)) + '</span></div>' +
        '<div class="flex" style="justify-content:space-between;"><span class="text-muted">Seller</span><span>' + esc(sellerDisplay) + '</span></div>' +
        (order.variant ? '<div class="flex" style="justify-content:space-between;"><span class="text-muted">Variant</span><span>' + esc(order.variant) + '</span></div>' : '') +
        '<div class="flex" style="justify-content:space-between;"><span class="text-muted">Escrow</span><span class="mono" style="font-size:0.8rem;color:var(--text-secondary);">' + esc(escrowDisplay) + '</span></div>' +
      '</div>';

    var actions = el('div', { style: 'display:flex;flex-direction:column;gap:8px;margin-top:20px;padding-top:16px;border-top:1px solid var(--border);' });
    var status = (order.status || '').toLowerCase();

    if (status === 'funded' || status === 'accepted' || status === 'fulfilled') {
      var rcptBtn = el('button', { className: 'btn btn-success btn-full', textContent: 'Confirm Receipt' });
      rcptBtn.addEventListener('click', function () { confirmReceipt(order.id); });
      actions.appendChild(rcptBtn);

      var dispBtn = el('button', { className: 'btn btn-outline btn-full', textContent: 'Open Dispute',
        style: 'border-color:var(--danger);color:var(--danger);' });
      dispBtn.addEventListener('click', function () { showDisputeForm(order.id); });
      actions.appendChild(dispBtn);
    }

    if (status === 'completed') {
      actions.appendChild(el('div', {
        className: 'text-success',
        textContent: 'Order complete \u2014 escrow released',
        style: 'text-align:center;font-weight:600;padding:8px;',
      }));
    }

    if (status === 'disputed') {
      actions.appendChild(el('div', {
        className: 'text-danger',
        textContent: 'Dispute in progress',
        style: 'text-align:center;font-weight:600;padding:8px;',
      }));
    }

    actions.appendChild(el('a', {
      className: 'btn btn-ghost btn-full',
      href: '#/messages/' + order.seller,
      textContent: 'Message Seller',
    }));

    dtCard.appendChild(actions);
    grid.appendChild(dtCard);
    root.appendChild(grid);

    root.appendChild(el('a', {
      className: 'btn btn-ghost',
      href: '#/orders',
      textContent: '\u2190 All Orders',
      style: 'margin-top:20px;display:inline-flex;',
    }));
  }

  // ============================================================
  //  Confirm Receipt
  // ============================================================

  async function confirmReceipt(id) {
    var order;
    try {
      order = await window.Mercury.getOrder(id);
    } catch (err) {
      toast('Failed to load order: ' + err.message, 'error');
      return;
    }
    if (!order) return;

    var amt  = parsePriceAmount(order.price);
    var fees = feeBreakdown(amt, order.currency || '');

    var overlay = el('div', { className: 'modal-overlay' });
    var modal   = el('div', { className: 'modal' });

    modal.innerHTML =
      '<div class="modal-header">' +
        '<span class="modal-title">Confirm Receipt</span>' +
        '<button class="modal-close" id="rcpt-close">\u2715</button>' +
      '</div>' +
      '<div class="modal-body">' +
        '<p style="margin-bottom:16px;">Confirming receipt will release escrowed funds to the seller. This cannot be undone.</p>' +
        feeTable(fees) +
      '</div>' +
      '<div class="modal-footer">' +
        '<button class="btn btn-ghost" id="rcpt-cancel">Cancel</button>' +
        '<button class="btn btn-success" id="rcpt-confirm">Release Funds</button>' +
      '</div>';

    overlay.appendChild(modal);
    document.body.appendChild(overlay);

    function close() { overlay.remove(); }
    overlay.addEventListener('click', function (e) { if (e.target === overlay) close(); });
    document.getElementById('rcpt-close').addEventListener('click', close);
    document.getElementById('rcpt-cancel').addEventListener('click', close);

    document.getElementById('rcpt-confirm').addEventListener('click', function () {
      var btn = this;
      btn.disabled = true;
      btn.textContent = 'Releasing...';

      window.Mercury.confirmReceipt(id)
        .then(function () {
          close();
          toast('Receipt confirmed \u2014 funds released to seller.', 'success');
          renderOrderDetail(id);
        })
        .catch(function (err) {
          toast('Failed: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'Release Funds';
        });
    });
  }

  // ============================================================
  //  Dispute Form
  // ============================================================

  function showDisputeForm(id) {
    var overlay = el('div', { className: 'modal-overlay' });
    var modal   = el('div', { className: 'modal' });

    var reasons = [
      'Item not received',
      'Item not as described',
      'Item damaged',
      'Wrong item received',
      'Other',
    ];
    var opts = '';
    reasons.forEach(function (r) { opts += '<option value="' + r + '">' + r + '</option>'; });

    modal.innerHTML =
      '<div class="modal-header">' +
        '<span class="modal-title">Open Dispute</span>' +
        '<button class="modal-close" id="disp-close">\u2715</button>' +
      '</div>' +
      '<div class="modal-body">' +
        '<p class="text-muted mb-16">An arbitrator will review your case. Please be as specific as possible.</p>' +
        '<div class="form-group">' +
          '<label class="form-label">Reason</label>' +
          '<select class="form-select" id="disp-reason">' + opts + '</select>' +
        '</div>' +
        '<div class="form-group">' +
          '<label class="form-label">Description</label>' +
          '<textarea class="form-textarea" id="disp-desc" rows="4" placeholder="Describe the issue in detail..."></textarea>' +
        '</div>' +
        '<div class="form-group">' +
          '<label class="form-label">Evidence (optional)</label>' +
          '<div class="evidence-upload">' +
            '<label class="drop-zone" id="disp-dropzone" style="cursor:pointer;">' +
              '<input type="file" id="disp-file" multiple style="display:none;" accept="image/*,.pdf,.txt">' +
              '<div class="drop-zone-text">Click or drag files here</div>' +
            '</label>' +
            '<div id="disp-files" class="image-preview-grid"></div>' +
          '</div>' +
        '</div>' +
      '</div>' +
      '<div class="modal-footer">' +
        '<button class="btn btn-ghost" id="disp-cancel">Cancel</button>' +
        '<button class="btn btn-danger" id="disp-submit">Submit Dispute</button>' +
      '</div>';

    overlay.appendChild(modal);
    document.body.appendChild(overlay);

    function close() { overlay.remove(); }
    overlay.addEventListener('click', function (e) { if (e.target === overlay) close(); });
    document.getElementById('disp-close').addEventListener('click', close);
    document.getElementById('disp-cancel').addEventListener('click', close);

    var files = [];
    var dropzone  = document.getElementById('disp-dropzone');
    var fileInput = document.getElementById('disp-file');
    var filesList = document.getElementById('disp-files');

    dropzone.addEventListener('click', function () { fileInput.click(); });
    dropzone.addEventListener('dragover', function (e) { e.preventDefault(); dropzone.classList.add('dragover'); });
    dropzone.addEventListener('dragleave', function () { dropzone.classList.remove('dragover'); });
    dropzone.addEventListener('drop', function (e) {
      e.preventDefault();
      dropzone.classList.remove('dragover');
      addFiles(e.dataTransfer.files);
    });
    fileInput.addEventListener('change', function () { addFiles(fileInput.files); });

    function addFiles(list) {
      for (var i = 0; i < list.length; i++) files.push(list[i]);
      renderFiles();
    }

    function renderFiles() {
      filesList.innerHTML = '';
      files.forEach(function (f, idx) {
        var item = el('div', { className: 'evidence-item' });
        item.innerHTML =
          '<span style="font-size:1.1rem;">&#128196;</span>' +
          '<span style="flex:1;font-size:0.85rem;" class="truncate">' + esc(f.name) + '</span>' +
          '<span class="text-muted" style="font-size:0.75rem;">' + formatFileSize(f.size) + '</span>';
        var rm = el('button', { className: 'btn btn-sm btn-ghost', textContent: '\u00d7',
          style: 'color:var(--danger);font-size:1rem;padding:2px 6px;' });
        rm.addEventListener('click', function () { files.splice(idx, 1); renderFiles(); });
        item.appendChild(rm);
        filesList.appendChild(item);
      });
    }

    document.getElementById('disp-submit').addEventListener('click', function () {
      var reason = document.getElementById('disp-reason').value;
      var desc   = document.getElementById('disp-desc').value.trim();
      if (!desc) { toast('Please provide a description', 'warning'); return; }

      var btn = this;
      btn.disabled = true;
      btn.textContent = 'Submitting...';

      window.Mercury.fileDispute(id, reason + ': ' + desc)
        .then(function () {
          close();
          toast('Dispute filed. An arbitrator will review your case.', 'info');
          renderOrderDetail(id);
        })
        .catch(function (err) {
          toast('Failed: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'Submit Dispute';
        });
    });
  }

  // ---- Expose (both namespaces for compatibility) ----

  var api = {
    renderOrders: renderOrders,
    showCheckout: showCheckout,
    renderOrderDetail: renderOrderDetail,
    confirmReceipt: confirmReceipt,
    showDisputeForm: showDisputeForm,
    fileDispute: showDisputeForm,
  };

  window.Orders = api;
  window.MercuryOrder = api;

})();
