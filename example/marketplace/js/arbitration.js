/* ============================================================
   Arbitration — Dispute filing, evidence, and resolution

   Consumes the raw shapes returned by the Mercury bridge:
     dispute = {
       id, orderId, seller (pubkey), buyer (pubkey),
       status, reason, filedAt | ts,
       arbitrator: { pubkey, fee, responseTime, specialisation },
       timeline: [ { event, ts, actor? } ],
       evidence: [ { id, submitter, description, hash, ts, type } ],
       ruling: null | { outcome, rationale, buyerPercent, sellerPercent, ts }
     }
     arbitrator = {
       id | pubkey, specialisation, fee, responseTime,
       cases, rating
     }
   No demo data fallbacks. Order titles and seller / actor names
   are resolved lazily through the Mercury helpers.
   ============================================================ */

(function () {
  'use strict';

  var H = window.MercuryHelpers;
  var toast          = window.MercuryUI.toast;
  var el             = window.MercuryUI.el;
  var renderStars    = window.MercuryUI.renderStars;
  var renderSkeleton = window.MercuryUI.renderSkeleton;

  function app() { return document.getElementById('app'); }
  function esc(s) { return H.esc(s); }

  // ---- Status Badge ----

  var statusLabels = {
    filed:    'Filed',
    evidence: 'Evidence Collection',
    review:   'Under Review',
    ruled:    'Ruled',
    closed:   'Closed',
  };

  var statusColors = {
    filed:    'warning',
    evidence: 'accent',
    review:   'warning',
    ruled:    'success',
    closed:   'muted',
  };

  function statusBadge(status) {
    var key = (status || '').toLowerCase();
    var label = statusLabels[key] || status || 'Unknown';
    var color = statusColors[key] || 'accent';
    var colorMap = {
      warning: 'background:var(--warning-soft);color:var(--warning);',
      accent:  'background:var(--accent-soft);color:var(--accent);',
      success: 'background:var(--success-soft);color:var(--success);',
      muted:   'background:var(--bg-tertiary);color:var(--text-muted);',
      danger:  'background:var(--danger-soft);color:var(--danger);',
    };
    return '<span class="arb-status-badge" style="' + (colorMap[color] || colorMap.accent) + '">' + esc(label) + '</span>';
  }

  // ---- Arbitrator name resolver ----

  async function arbitratorDisplay(arb) {
    if (!arb) return 'Unknown';
    if (arb.name) return arb.name;
    if (arb.pubkey) {
      var p = await H.getProfile(arb.pubkey);
      return H.displayName(arb.pubkey, p);
    }
    return arb.id || 'Unknown';
  }

  // ---- Disputes List ----

  async function renderDisputes() {
    var root = app();
    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">Disputes</h1>' +
        '<p class="page-subtitle">File and track dispute resolutions for orders.</p>' +
        '<div class="page-actions">' +
          '<button class="btn btn-outline" id="arb-view-arbitrators">Browse Arbitrators</button>' +
        '</div>' +
      '</div>' +
      '<div class="arb-filter-bar">' +
        '<button class="filter-chip active" data-filter="all">All</button>' +
        '<button class="filter-chip" data-filter="active">Active</button>' +
        '<button class="filter-chip" data-filter="resolved">Resolved</button>' +
      '</div>' +
      '<div id="arb-disputes-list">' +
        renderSkeleton('100%', '100px') +
        renderSkeleton('100%', '100px') +
      '</div>';

    document.getElementById('arb-view-arbitrators').addEventListener('click', renderArbitratorList);

    var disputes = [];
    try {
      if (window.Mercury.getDisputes) {
        disputes = (await window.Mercury.getDisputes()) || [];
      }
    } catch (err) {
      toast('Failed to load disputes: ' + err.message, 'error');
    }

    function applyFilter(f) {
      if (f === 'active') {
        return disputes.filter(function (d) {
          var s = (d.status || '').toLowerCase();
          return s === 'filed' || s === 'evidence' || s === 'review';
        });
      }
      if (f === 'resolved') {
        return disputes.filter(function (d) {
          var s = (d.status || '').toLowerCase();
          return s === 'ruled' || s === 'closed';
        });
      }
      return disputes;
    }

    root.querySelectorAll('.filter-chip').forEach(function (chip) {
      chip.addEventListener('click', function () {
        root.querySelectorAll('.filter-chip').forEach(function (c) { c.classList.remove('active'); });
        this.classList.add('active');
        renderDisputesList(applyFilter(this.dataset.filter));
      });
    });

    renderDisputesList(disputes);
  }

  function renderDisputesList(disputes) {
    var container = document.getElementById('arb-disputes-list');
    if (!container) return;

    if (!disputes || disputes.length === 0) {
      container.innerHTML =
        '<div class="empty-state">' +
          '<div class="empty-state-icon">&#9878;</div>' +
          '<h3 class="empty-state-title">No disputes</h3>' +
          '<p class="empty-state-desc">You have no dispute cases. We hope it stays that way!</p>' +
        '</div>';
      return;
    }

    container.innerHTML = disputes.map(function (d) {
      var reason = d.reason || '';
      var reasonShort = reason.length > 120 ? reason.substring(0, 120) + '...' : reason;
      var evCount = (d.evidence && d.evidence.length) || 0;
      var filed = d.filedAt || d.ts;
      return (
        '<div class="arb-dispute-card" data-case="' + esc(d.id) + '">' +
          '<div class="arb-dispute-top">' +
            '<div class="arb-dispute-title-row">' +
              '<h3 class="arb-dispute-title" data-role="order-title">' + esc(H.shortHex(d.orderId, 6, 4)) + '</h3>' +
              statusBadge(d.status) +
            '</div>' +
            '<div class="arb-dispute-meta">' +
              '<span>Case ' + esc(H.shortHex(d.id, 6, 4)) + '</span>' +
              '<span>&middot;</span>' +
              '<span>Order ' + esc(H.shortHex(d.orderId, 6, 4)) + '</span>' +
              '<span>&middot;</span>' +
              '<span>Filed ' + esc(H.formatRelativeTime(filed)) + '</span>' +
            '</div>' +
          '</div>' +
          '<div class="arb-dispute-body">' +
            '<p class="arb-dispute-reason">' + esc(reasonShort) + '</p>' +
            '<div class="arb-dispute-footer">' +
              '<span class="arb-dispute-arbitrator" data-role="arbitrator">Arbitrator: …</span>' +
              '<span class="arb-dispute-evidence">' + evCount + ' piece(s) of evidence</span>' +
            '</div>' +
          '</div>' +
        '</div>'
      );
    }).join('');

    // Hydrate order titles & arbitrator names.
    var cards = container.querySelectorAll('.arb-dispute-card');
    cards.forEach(function (card, i) {
      var d = disputes[i];
      H.getListing(d.orderId).then(function (l) {
        // Only useful if the order id happens to equal a listing id; skip.
      });
      if (d.orderId) {
        // Try to resolve via getOrder → getListing title.
        if (window.Mercury.getOrder) {
          window.Mercury.getOrder(d.orderId).then(function (order) {
            if (!order) return;
            return H.getListing(order.listingId);
          }).then(function (l) {
            if (l && l.title) {
              var t = card.querySelector('[data-role="order-title"]');
              if (t) t.textContent = l.title;
            }
          }).catch(function () {});
        }
      }
      arbitratorDisplay(d.arbitrator).then(function (name) {
        var s = card.querySelector('[data-role="arbitrator"]');
        if (s) s.textContent = 'Arbitrator: ' + name;
      });
    });

    container.querySelectorAll('.arb-dispute-card').forEach(function (card) {
      card.addEventListener('click', function () {
        renderDisputeDetail(this.dataset.case);
      });
    });
  }

  // ---- Dispute Detail ----

  async function renderDisputeDetail(caseId) {
    var root = app();
    root.innerHTML = '<div style="padding:48px;text-align:center;"><div class="loading-spinner"></div></div>';

    var dispute;
    try {
      dispute = window.Mercury.getDispute ? await window.Mercury.getDispute(caseId) : null;
    } catch (err) {
      toast('Failed to load case: ' + err.message, 'error');
      return;
    }

    if (!dispute) {
      root.innerHTML =
        '<div class="empty-state">' +
          '<div class="empty-state-icon">&#10067;</div>' +
          '<h3 class="empty-state-title">Case not found</h3>' +
          '<p class="empty-state-desc">This dispute case could not be found.</p>' +
          '<button class="btn btn-primary" id="arb-back-list">Back to Disputes</button>' +
        '</div>';
      document.getElementById('arb-back-list').addEventListener('click', renderDisputes);
      return;
    }

    // Resolve order title + seller display.
    var orderTitle = H.shortHex(dispute.orderId, 6, 4);
    var sellerDisplay = H.shortHex(dispute.seller, 6, 4);
    try {
      if (window.Mercury.getOrder) {
        var order = await window.Mercury.getOrder(dispute.orderId);
        if (order && order.listingId) {
          var l = await H.getListing(order.listingId);
          if (l && l.title) orderTitle = l.title;
        }
      }
    } catch (_) {}
    if (dispute.seller) {
      var sp = await H.getProfile(dispute.seller);
      sellerDisplay = H.displayName(dispute.seller, sp);
    }

    var arbName = await arbitratorDisplay(dispute.arbitrator);
    var evidence = dispute.evidence || [];
    var timeline = dispute.timeline || [];

    root.innerHTML =
      '<div class="page-header">' +
        '<p class="page-subtitle">' +
          '<a href="#" class="text-accent" id="arb-back-link">Disputes</a> / Case ' + esc(H.shortHex(dispute.id, 6, 4)) +
        '</p>' +
        '<div style="display:flex;align-items:center;gap:12px;margin-top:8px;">' +
          '<h1 class="page-title" style="margin:0;">' + esc(orderTitle) + '</h1>' +
          statusBadge(dispute.status) +
        '</div>' +
      '</div>' +
      '<div class="arb-detail-grid">' +
        '<div class="arb-detail-main">' +
          '<div class="card">' +
            '<div class="card-title">Dispute Reason</div>' +
            '<p style="color:var(--text-secondary);margin-top:8px;font-size:0.9rem;line-height:1.6;">' + esc(dispute.reason || '') + '</p>' +
          '</div>' +
          '<div class="card mt-16">' +
            '<div class="card-title">Case Timeline</div>' +
            '<div class="arb-timeline" id="arb-timeline"></div>' +
          '</div>' +
          '<div class="card mt-16">' +
            '<div class="card-title" style="display:flex;justify-content:space-between;align-items:center;">' +
              '<span>Evidence (' + evidence.length + ')</span>' +
              ((dispute.status || '').toLowerCase() === 'evidence'
                ? '<button class="btn btn-primary btn-sm" id="arb-add-evidence">+ Add Evidence</button>'
                : '') +
            '</div>' +
            '<div id="arb-evidence-list"></div>' +
          '</div>' +
          (dispute.ruling
            ? '<div class="card mt-16" id="arb-ruling-card"></div>'
            : '') +
        '</div>' +
        '<div class="arb-detail-sidebar">' +
          '<div class="card">' +
            '<div class="card-title">Case Information</div>' +
            '<div class="arb-info-row"><span class="arb-info-label">Case ID</span><span class="arb-info-value mono">' + esc(H.shortHex(dispute.id, 6, 4)) + '</span></div>' +
            '<div class="arb-info-row"><span class="arb-info-label">Order</span><span class="arb-info-value mono">' + esc(H.shortHex(dispute.orderId, 6, 4)) + '</span></div>' +
            '<div class="arb-info-row"><span class="arb-info-label">Filed</span><span class="arb-info-value">' + esc(H.formatDateTime(dispute.filedAt || dispute.ts)) + '</span></div>' +
            '<div class="arb-info-row"><span class="arb-info-label">Seller</span><span class="arb-info-value">' + esc(sellerDisplay) + '</span></div>' +
          '</div>' +
          '<div class="card mt-16">' +
            '<div class="card-title">Arbitrator</div>' +
            '<div class="arb-arbitrator-card-mini">' +
              '<div class="arb-arb-name">' + esc(arbName) + '</div>' +
              '<div class="arb-arb-meta">' +
                '<span>Fee: ' + esc((dispute.arbitrator && dispute.arbitrator.fee) || '—') + '</span>' +
                '<span>&middot;</span>' +
                '<span>Response: ' + esc((dispute.arbitrator && dispute.arbitrator.responseTime) || '—') + '</span>' +
              '</div>' +
              '<div class="arb-arb-spec">' + esc((dispute.arbitrator && dispute.arbitrator.specialisation) || '') + '</div>' +
            '</div>' +
          '</div>' +
        '</div>' +
      '</div>';

    document.getElementById('arb-back-link').addEventListener('click', function (e) {
      e.preventDefault();
      renderDisputes();
    });

    renderTimeline(timeline);
    renderEvidenceList(evidence);
    if (dispute.ruling) renderRuling(dispute.ruling);

    var addEvidenceBtn = document.getElementById('arb-add-evidence');
    if (addEvidenceBtn) {
      addEvidenceBtn.addEventListener('click', function () {
        showEvidenceUpload(caseId, dispute);
      });
    }
  }

  function renderTimeline(timeline) {
    var container = document.getElementById('arb-timeline');
    if (!container) return;

    if (!timeline.length) {
      container.innerHTML = '<p class="text-muted" style="font-size:0.85rem;margin-top:8px;">No timeline events yet.</p>';
      return;
    }

    container.innerHTML = timeline.map(function (t, i) {
      var isLast = i === timeline.length - 1;
      var when = t.ts || t.date;
      return (
        '<div class="arb-timeline-item' + (isLast ? ' arb-timeline-current' : '') + '">' +
          '<div class="arb-timeline-dot"></div>' +
          '<div class="arb-timeline-content">' +
            '<div class="arb-timeline-event">' + esc(t.event) + '</div>' +
            '<div class="arb-timeline-meta">' +
              '<span>' + esc(H.formatDateTime(when)) + '</span>' +
              (t.actor ? '<span>&middot;</span><span>' + esc(t.actor) + '</span>' : '') +
            '</div>' +
          '</div>' +
        '</div>'
      );
    }).join('');
  }

  function renderEvidenceList(evidence) {
    var container = document.getElementById('arb-evidence-list');
    if (!container) return;

    if (!evidence || evidence.length === 0) {
      container.innerHTML =
        '<p style="color:var(--text-muted);font-size:0.85rem;margin-top:12px;">No evidence submitted yet.</p>';
      return;
    }

    container.innerHTML = evidence.map(function (ev) {
      var typeIcon = ev.type === 'image' ? '&#128247;' : '&#128196;';
      var when = ev.ts || ev.date;
      return (
        '<div class="arb-evidence-item">' +
          '<div class="arb-evidence-icon">' + typeIcon + '</div>' +
          '<div class="arb-evidence-info">' +
            '<div class="arb-evidence-desc">' + esc(ev.description || '') + '</div>' +
            '<div class="arb-evidence-meta">' +
              '<span class="arb-evidence-hash" title="' + esc(ev.hash || '') + '">' + esc(H.shortHex(ev.hash, 8, 6)) + '</span>' +
              '<span>&middot;</span>' +
              '<span>' + esc(H.formatRelativeTime(when)) + '</span>' +
              '<span>&middot;</span>' +
              '<span>by ' + esc(H.shortHex(ev.submitter, 4, 4)) + '</span>' +
            '</div>' +
          '</div>' +
        '</div>'
      );
    }).join('');
  }

  function renderRuling(ruling) {
    var container = document.getElementById('arb-ruling-card');
    if (!container) return;

    var buyerPct  = Number(ruling.buyerPercent) || 0;
    var sellerPct = Number(ruling.sellerPercent) || 0;
    var outcomeColor = buyerPct >= 100 ? 'var(--success)' : sellerPct >= 100 ? 'var(--danger)' : 'var(--warning)';

    container.innerHTML =
      '<div class="card-title">Ruling</div>' +
      '<div class="arb-ruling">' +
        '<div class="arb-ruling-outcome" style="color:' + outcomeColor + ';">' + esc(ruling.outcome || '') + '</div>' +
        '<div class="arb-ruling-date">Issued ' + esc(H.formatDateTime(ruling.ts || ruling.date)) + '</div>' +
        '<div class="arb-ruling-rationale">' +
          '<p style="color:var(--text-secondary);font-size:0.9rem;line-height:1.6;margin-top:8px;">' + esc(ruling.rationale || '') + '</p>' +
        '</div>' +
        '<div class="arb-ruling-split">' +
          '<div class="arb-split-bar">' +
            '<div class="arb-split-buyer" style="width:' + buyerPct + '%;"></div>' +
            '<div class="arb-split-seller" style="width:' + sellerPct + '%;"></div>' +
          '</div>' +
          '<div class="arb-split-labels">' +
            '<span style="color:var(--success);">Buyer: ' + buyerPct + '%</span>' +
            '<span style="color:var(--danger);">Seller: ' + sellerPct + '%</span>' +
          '</div>' +
        '</div>' +
      '</div>';
  }

  // ---- Evidence Upload ----

  function showEvidenceUpload(caseId, dispute) {
    var root = app();

    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">Submit Evidence</h1>' +
        '<p class="page-subtitle">Case ' + esc(H.shortHex(caseId, 6, 4)) + '</p>' +
      '</div>' +
      '<div class="arb-evidence-form">' +
        '<div class="card">' +
          '<div class="card-title">Upload Evidence</div>' +
          '<div class="arb-upload-zone" id="arb-upload-zone">' +
            '<div class="arb-upload-icon">&#128206;</div>' +
            '<p>Drag files here or click to browse</p>' +
            '<p style="font-size:0.75rem;color:var(--text-muted);">Images, PDFs, or documents. Max 10MB per file.</p>' +
            '<input type="file" id="arb-file-input" style="display:none;" multiple accept="image/*,.pdf,.doc,.docx,.txt">' +
          '</div>' +
          '<div id="arb-selected-files" style="margin-top:12px;"></div>' +
          '<div class="form-group mt-16">' +
            '<label class="form-label">Description</label>' +
            '<textarea class="form-input" id="arb-evidence-desc" placeholder="Describe this evidence and how it supports your case..." rows="3"></textarea>' +
          '</div>' +
          '<p class="form-error hidden" id="arb-evidence-error"></p>' +
          '<div style="display:flex;gap:8px;margin-top:16px;">' +
            '<button class="btn btn-primary" id="arb-submit-evidence">Submit Evidence</button>' +
            '<button class="btn btn-ghost" id="arb-cancel-evidence">Cancel</button>' +
          '</div>' +
        '</div>' +
      '</div>';

    var uploadZone = document.getElementById('arb-upload-zone');
    var fileInput = document.getElementById('arb-file-input');
    var selectedFiles = [];

    uploadZone.addEventListener('click', function () { fileInput.click(); });
    uploadZone.addEventListener('dragover', function (e) { e.preventDefault(); uploadZone.classList.add('arb-upload-dragover'); });
    uploadZone.addEventListener('dragleave', function () { uploadZone.classList.remove('arb-upload-dragover'); });
    uploadZone.addEventListener('drop', function (e) {
      e.preventDefault();
      uploadZone.classList.remove('arb-upload-dragover');
      addFiles(Array.prototype.slice.call(e.dataTransfer.files));
    });
    fileInput.addEventListener('change', function () {
      addFiles(Array.prototype.slice.call(this.files));
    });

    function addFiles(files) {
      files.forEach(function (f) {
        if (f.size > 10 * 1024 * 1024) {
          toast(f.name + ' exceeds 10MB limit', 'error');
          return;
        }
        selectedFiles.push(f);
      });
      renderSelectedFiles();
    }

    function renderSelectedFiles() {
      var container = document.getElementById('arb-selected-files');
      if (!container) return;
      if (selectedFiles.length === 0) {
        container.innerHTML = '';
        return;
      }
      container.innerHTML = selectedFiles.map(function (f, i) {
        var sizeKB = (f.size / 1024).toFixed(1);
        return (
          '<div class="arb-file-item">' +
            '<span class="arb-file-name">' + esc(f.name) + '</span>' +
            '<span class="arb-file-size">' + sizeKB + ' KB</span>' +
            '<button class="btn btn-ghost btn-sm arb-file-remove" data-index="' + i + '">&times;</button>' +
          '</div>'
        );
      }).join('');
      container.querySelectorAll('.arb-file-remove').forEach(function (btn) {
        btn.addEventListener('click', function () {
          selectedFiles.splice(parseInt(this.dataset.index, 10), 1);
          renderSelectedFiles();
        });
      });
    }

    document.getElementById('arb-submit-evidence').addEventListener('click', function () {
      var btn = this;
      var desc = document.getElementById('arb-evidence-desc').value.trim();
      var errEl = document.getElementById('arb-evidence-error');

      if (!desc) {
        errEl.textContent = 'Please describe this evidence.';
        errEl.classList.remove('hidden');
        return;
      }

      errEl.classList.add('hidden');
      btn.disabled = true;
      btn.textContent = 'Submitting...';

      var submit = window.Mercury.submitEvidence
        ? window.Mercury.submitEvidence(caseId, { description: desc, files: selectedFiles })
        : Promise.reject(new Error('submitEvidence not available'));

      submit.then(function () {
        toast('Evidence submitted successfully', 'success');
        renderDisputeDetail(caseId);
      }).catch(function (err) {
        toast('Failed to submit evidence: ' + err.message, 'error');
        btn.disabled = false;
        btn.textContent = 'Submit Evidence';
      });
    });

    document.getElementById('arb-cancel-evidence').addEventListener('click', function () {
      renderDisputeDetail(caseId);
    });
  }

  // ---- Arbitrator List ----

  async function renderArbitratorList() {
    var root = app();
    var currentSort = 'rating';

    root.innerHTML =
      '<div class="page-header">' +
        '<h1 class="page-title">Arbitrators</h1>' +
        '<p class="page-subtitle">Decentralised dispute resolvers available on the network.</p>' +
        '<p class="page-subtitle">' +
          '<a href="#" class="text-accent" id="arb-back-disputes">Back to Disputes</a>' +
        '</p>' +
      '</div>' +
      '<div class="arb-filter-bar">' +
        '<button class="filter-chip" data-sort="fee">Sort by Fee</button>' +
        '<button class="filter-chip" data-sort="response">Sort by Response Time</button>' +
        '<button class="filter-chip active" data-sort="rating">Sort by Rating</button>' +
      '</div>' +
      '<div id="arb-arbitrator-grid" class="arb-arbitrator-grid">' +
        renderSkeleton('100%', '140px') +
        renderSkeleton('100%', '140px') +
      '</div>';

    document.getElementById('arb-back-disputes').addEventListener('click', function (e) {
      e.preventDefault();
      renderDisputes();
    });

    var arbitrators = [];
    try {
      if (window.Mercury.getArbitrators) {
        arbitrators = (await window.Mercury.getArbitrators()) || [];
      }
    } catch (err) {
      toast('Failed to load arbitrators: ' + err.message, 'error');
    }

    function sortAndRender(sortBy) {
      var sorted = arbitrators.slice();
      if (sortBy === 'fee') {
        sorted.sort(function (a, b) { return parseFloat(a.fee) - parseFloat(b.fee); });
      } else if (sortBy === 'response') {
        sorted.sort(function (a, b) { return parseInt(a.responseTime, 10) - parseInt(b.responseTime, 10); });
      } else {
        sorted.sort(function (a, b) { return (b.rating || 0) - (a.rating || 0); });
      }
      renderArbitrators(sorted);
    }

    root.querySelectorAll('.filter-chip').forEach(function (chip) {
      chip.addEventListener('click', function () {
        root.querySelectorAll('.filter-chip').forEach(function (c) { c.classList.remove('active'); });
        this.classList.add('active');
        currentSort = this.dataset.sort;
        sortAndRender(currentSort);
      });
    });

    sortAndRender(currentSort);
  }

  function renderArbitrators(arbitrators) {
    var container = document.getElementById('arb-arbitrator-grid');
    if (!container) return;

    if (!arbitrators.length) {
      container.innerHTML =
        '<div class="empty-state" style="grid-column:1/-1;">' +
          '<div class="empty-state-icon">&#9878;</div>' +
          '<h3 class="empty-state-title">No arbitrators</h3>' +
          '<p class="empty-state-desc">No arbitrators are announcing on this network.</p>' +
        '</div>';
      return;
    }

    container.innerHTML = arbitrators.map(function (arb) {
      var stars = renderStars(arb.rating);
      var key = arb.pubkey || arb.id || '';
      var label = arb.name ? arb.name : H.shortHex(key);
      return (
        '<div class="arb-arbitrator-card">' +
          '<div class="arb-arbitrator-header">' +
            '<div class="arb-arbitrator-avatar">&#9878;</div>' +
            '<div>' +
              '<h3 class="arb-arbitrator-name">' + esc(label) + '</h3>' +
              '<div class="arb-arbitrator-spec">' + esc(arb.specialisation || '') + '</div>' +
            '</div>' +
          '</div>' +
          '<div class="arb-arbitrator-stats">' +
            '<div class="arb-arb-stat">' +
              '<span class="arb-arb-stat-value">' + esc(arb.fee || '—') + '</span>' +
              '<span class="arb-arb-stat-label">Fee</span>' +
            '</div>' +
            '<div class="arb-arb-stat">' +
              '<span class="arb-arb-stat-value">' + esc(arb.responseTime || '—') + '</span>' +
              '<span class="arb-arb-stat-label">Response</span>' +
            '</div>' +
            '<div class="arb-arb-stat">' +
              '<span class="arb-arb-stat-value">' + (arb.cases || 0) + '</span>' +
              '<span class="arb-arb-stat-label">Cases</span>' +
            '</div>' +
            '<div class="arb-arb-stat">' +
              '<span class="arb-arb-stat-value">' + stars + '</span>' +
              '<span class="arb-arb-stat-label">Rating</span>' +
            '</div>' +
          '</div>' +
        '</div>'
      );
    }).join('');
  }

  // ---- Expose ----

  window.MercuryArbitration = {
    renderDisputes: renderDisputes,
    renderDisputeDetail: renderDisputeDetail,
    showEvidenceUpload: showEvidenceUpload,
    renderArbitratorList: renderArbitratorList,
  };

})();
