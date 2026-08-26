/* ============================================================
   Wallet — Multi-account management, balances, send, history

   Consumes the raw shapes returned by the Plutus WASM bridge:
     account = {
       chain, address, name?, balance, currency?, decimals?,
       identityOnly?     // if true, no signing — view-only identity
     }
     totalBalance = { display }   // no USD conversion on-chain
     transaction = {
       id, type ('sent'|'received'),
       amount, currency?, ts|time, to|from|counterparty, status
     }
   No USD fiat conversion, no oracles — all amounts are in the
   native chain's currency.
   ============================================================ */

(function () {
  'use strict';

  var H = window.MercuryHelpers;
  var toast = window.MercuryUI.toast;
  var el    = window.MercuryUI.el;

  function app() { return document.getElementById('app'); }
  function esc(s) { return H.esc(s); }

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

  // ---- Chain label ----

  var chainLabels = {
    ETH: 'Ethereum',
    SOL: 'Solana',
    BTC: 'Bitcoin',
    ARB: 'Arbitrum',
    POLYGON: 'Polygon',
    polygon: 'Polygon',
    ethereum: 'Ethereum',
    bitcoin: 'Bitcoin',
    solana: 'Solana',
    arbitrum: 'Arbitrum',
  };

  function chainLabel(chain) {
    if (!chain) return 'Unknown';
    return chainLabels[chain] || chainLabels[String(chain).toLowerCase()] || chain;
  }

  function accountName(acct) {
    if (acct.name) return acct.name;
    return chainLabel(acct.chain);
  }

  function accountBalanceDisplay(acct) {
    if (!acct) return '';
    if (acct.balanceDisplay) return acct.balanceDisplay;
    return H.formatPrice(acct.balance, acct.currency || acct.chain || '', acct.decimals);
  }

  function totalDisplay(total) {
    if (!total) return '';
    if (total.display) return total.display;
    if (total.balance != null) return H.formatPrice(total.balance, total.currency || '', total.decimals);
    return '';
  }

  // ============================================================
  //  Main Wallet View
  // ============================================================

  async function renderWallet() {
    var root = app();
    root.innerHTML = '';

    root.insertAdjacentHTML('beforeend',
      '<div class="page-header">' +
        '<h1 class="page-title">Wallet</h1>' +
        '<p class="page-subtitle">Manage your accounts and balances.</p>' +
      '</div>'
    );

    var balSection = el('div', { id: 'wallet-balance-section' });
    balSection.innerHTML = '<div class="skeleton" style="height:140px;border-radius:var(--radius-lg);"></div>';
    root.appendChild(balSection);

    root.insertAdjacentHTML('beforeend',
      '<div class="tabs mt-24" id="wallet-tabs">' +
        '<button class="tab active" data-tab="accounts">Accounts</button>' +
        '<button class="tab" data-tab="history">Transactions</button>' +
      '</div>'
    );

    var tabContent = el('div', { id: 'wallet-tab-content' });
    root.appendChild(tabContent);

    // Total balance card.
    try {
      var total = await window.Plutus.getTotalBalance();
      var sec = document.getElementById('wallet-balance-section');
      if (sec) {
        sec.innerHTML =
          '<div class="wallet-balance-card">' +
            '<div class="wallet-balance-label">Total Portfolio Value</div>' +
            '<div class="wallet-balance-value">' + esc(totalDisplay(total)) + '</div>' +
            '<div class="wallet-actions">' +
              '<button class="btn" id="w-send">Send</button>' +
              '<button class="btn" id="w-receive">Receive</button>' +
              '<button class="btn" id="w-add">+ Account</button>' +
            '</div>' +
          '</div>';

        document.getElementById('w-send').addEventListener('click', showSendFunds);
        document.getElementById('w-receive').addEventListener('click', function () {
          toast('Select an account to view its receive address.', 'info');
        });
        document.getElementById('w-add').addEventListener('click', function () {
          showCreateAccount('ETH');
        });
      }
    } catch (err) {
      toast('Failed to load balance: ' + err.message, 'error');
    }

    document.getElementById('wallet-tabs').addEventListener('click', function (e) {
      var tab = e.target.closest('.tab');
      if (!tab) return;
      this.querySelectorAll('.tab').forEach(function (t) { t.classList.remove('active'); });
      tab.classList.add('active');
      if (tab.dataset.tab === 'accounts') loadAccounts();
      else loadTransactions();
    });

    await loadAccounts();
  }

  // ============================================================
  //  Accounts List
  // ============================================================

  async function loadAccounts() {
    var container = document.getElementById('wallet-tab-content');
    if (!container) return;
    container.innerHTML = '';
    container.appendChild(spinnerBlock());

    var accounts;
    try {
      accounts = (await window.Plutus.getAccounts()) || [];
    } catch (err) {
      container.innerHTML = '';
      toast('Failed to load accounts: ' + err.message, 'error');
      return;
    }

    var c = document.getElementById('wallet-tab-content');
    if (!c) return;
    c.innerHTML = '';

    if (accounts.length === 0) {
      c.innerHTML =
        '<div class="empty-state">' +
          '<div class="empty-state-icon">&#128179;</div>' +
          '<h3 class="empty-state-title">No accounts</h3>' +
          '<p class="empty-state-desc">Create your first account on the chain you want to use.</p>' +
          '<button class="btn btn-primary" id="w-empty-add">+ Create Account</button>' +
        '</div>';
      document.getElementById('w-empty-add').addEventListener('click', function () {
        showCreateAccount('ETH');
      });
      return;
    }

    var list = el('div', { className: 'wallet-account-list' });

    accounts.forEach(function (acct, idx) {
      var color = H.chainColor(acct.chain);
      var icon  = H.chainIcon(acct.chain);
      var addressShort = acct.address ? H.shortHex(acct.address, 6, 4) : '';
      var identityOnlyHtml = acct.identityOnly
        ? '<span class="badge" style="margin-left:6px;background:var(--bg-tertiary);color:var(--text-muted);font-size:0.7rem;">identity-only</span>'
        : '';

      var row = el('div', { className: 'wallet-account' });
      row.innerHTML =
        '<div class="wallet-account-info">' +
          '<div class="chain-icon" style="background:' + color + '22;color:' + color + ';">' + esc(icon) + '</div>' +
          '<div style="min-width:0;">' +
            '<div style="font-weight:600;">' + esc(accountName(acct)) + identityOnlyHtml + '</div>' +
            '<div class="mono text-muted" style="font-size:0.8rem;">' + esc(addressShort) +
              '<button class="btn btn-sm btn-ghost copy-addr" data-addr="' + esc(acct.address || '') + '" style="padding:2px 6px;margin-left:4px;font-size:0.7rem;" title="Copy address">&#128203;</button>' +
            '</div>' +
          '</div>' +
        '</div>' +
        '<div style="text-align:right;">' +
          '<div style="font-weight:600;">' + esc(accountBalanceDisplay(acct)) + '</div>' +
        '</div>';

      row.style.cursor = 'pointer';
      row.addEventListener('click', function (e) {
        if (e.target.classList.contains('copy-addr')) return;
        renderAccountDetail(acct.chain, idx);
      });

      list.appendChild(row);
    });

    c.appendChild(list);

    var addRow = el('div', { style: 'margin-top:12px;text-align:center;' });
    var addBtn = el('button', { className: 'btn btn-outline', textContent: '+ Create Account' });
    addBtn.addEventListener('click', function () { showCreateAccount('ETH'); });
    addRow.appendChild(addBtn);
    c.appendChild(addRow);

    c.querySelectorAll('.copy-addr').forEach(function (btn) {
      btn.addEventListener('click', function (e) {
        e.stopPropagation();
        var addr = this.dataset.addr;
        if (!addr) return;
        if (navigator.clipboard) {
          navigator.clipboard.writeText(addr).then(function () {
            toast('Address copied', 'success');
          });
        } else {
          toast('Address: ' + addr, 'info');
        }
      });
    });
  }

  // ============================================================
  //  Create Account
  // ============================================================

  function showCreateAccount(defaultChain) {
    var overlay = el('div', { className: 'modal-overlay' });
    var modal   = el('div', { className: 'modal' });

    var supported = ['ETH', 'SOL', 'BTC', 'ARB', 'POLYGON'];
    var chainOpts = '';
    supported.forEach(function (c) {
      chainOpts += '<option value="' + c + '"' + (c === defaultChain ? ' selected' : '') + '>' + chainLabel(c) + ' (' + c + ')</option>';
    });

    modal.innerHTML =
      '<div class="modal-header">' +
        '<span class="modal-title">Create Account</span>' +
        '<button class="modal-close" id="ca-close">\u2715</button>' +
      '</div>' +
      '<div class="modal-body">' +
        '<div class="form-group">' +
          '<label class="form-label">Account Name (optional)</label>' +
          '<input type="text" class="form-input" id="ca-name" placeholder="e.g. Trading, Savings">' +
        '</div>' +
        '<div class="form-group">' +
          '<label class="form-label">Chain</label>' +
          '<select class="form-select" id="ca-chain">' + chainOpts + '</select>' +
        '</div>' +
        '<p class="text-muted" style="font-size:0.85rem;">A new BIP-44 key will be derived from your identity. No additional seed phrase is needed.</p>' +
      '</div>' +
      '<div class="modal-footer">' +
        '<button class="btn btn-ghost" id="ca-cancel">Cancel</button>' +
        '<button class="btn btn-primary" id="ca-create">Create</button>' +
      '</div>';

    overlay.appendChild(modal);
    document.body.appendChild(overlay);

    function close() { overlay.remove(); }
    overlay.addEventListener('click', function (e) { if (e.target === overlay) close(); });
    document.getElementById('ca-close').addEventListener('click', close);
    document.getElementById('ca-cancel').addEventListener('click', close);

    document.getElementById('ca-create').addEventListener('click', function () {
      var chain = document.getElementById('ca-chain').value;
      var name  = document.getElementById('ca-name').value.trim();
      var btn = this;
      btn.disabled = true;
      btn.textContent = 'Deriving key...';

      window.Plutus.createAccount(chain, name || undefined)
        .then(function (acct) {
          close();
          var shown = acct.address ? H.shortHex(acct.address, 6, 4) : '(pending)';
          toast(accountName(acct) + ' account created: ' + shown, 'success');
          renderWallet();
        })
        .catch(function (err) {
          toast('Failed: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'Create';
        });
    });
  }

  // ============================================================
  //  Account Detail
  // ============================================================

  async function renderAccountDetail(chainId, accountIndex) {
    var root = app();
    root.innerHTML = '';
    root.appendChild(spinnerBlock());

    var accounts;
    try {
      accounts = (await window.Plutus.getAccounts()) || [];
    } catch (err) {
      toast('Failed to load account: ' + err.message, 'error');
      return;
    }

    var acct = accounts[accountIndex];
    if (!acct) {
      root.innerHTML =
        '<div class="empty-state">' +
          '<div class="empty-state-icon">&#128179;</div>' +
          '<h3 class="empty-state-title">Account not found</h3>' +
          '<a href="#/wallet" class="btn btn-primary mt-16">Back to Wallet</a>' +
        '</div>';
      return;
    }

    root.innerHTML = '';

    var color = H.chainColor(acct.chain);
    var icon  = H.chainIcon(acct.chain);

    root.insertAdjacentHTML('beforeend',
      '<div class="page-header">' +
        '<h1 class="page-title">' + esc(accountName(acct)) + '</h1>' +
        '<p class="page-subtitle"><a href="#/wallet">Wallet</a> / ' + esc(accountName(acct)) + '</p>' +
      '</div>'
    );

    var grid = el('div', { className: 'grid-2' });

    var addrCard = el('div', { className: 'card' });
    addrCard.innerHTML =
      '<div class="card-header">' +
        '<span class="card-title">Account Info</span>' +
        '<div class="chain-icon" style="background:' + color + '22;color:' + color + ';">' + esc(icon) + '</div>' +
      '</div>' +
      '<div style="width:140px;height:140px;background:var(--bg-tertiary);border:1px solid var(--border);border-radius:var(--radius);display:flex;align-items:center;justify-content:center;margin:0 auto 16px;color:var(--text-muted);font-size:0.8rem;">QR Code</div>' +
      '<div style="text-align:center;">' +
        '<div class="mono" style="font-size:0.85rem;word-break:break-all;padding:8px;background:var(--bg);border-radius:var(--radius);border:1px solid var(--border);">' + esc(acct.address || '') + '</div>' +
        '<button class="btn btn-sm btn-outline mt-8" id="ad-copy">Copy Address</button>' +
      '</div>' +
      '<div style="text-align:center;margin-top:20px;padding-top:16px;border-top:1px solid var(--border);">' +
        '<div class="text-muted" style="font-size:0.8rem;">Balance (via witness attestation)</div>' +
        '<div style="font-size:1.5rem;font-weight:700;margin-top:4px;">' + esc(accountBalanceDisplay(acct)) + '</div>' +
      '</div>';
    grid.appendChild(addrCard);

    var txCard = el('div', { className: 'card' });
    txCard.innerHTML = '<h3 class="card-title" style="margin-bottom:16px;">Recent Transactions</h3>';
    var txList = el('div', { id: 'ad-txns' });
    txList.appendChild(spinnerBlock());
    txCard.appendChild(txList);
    grid.appendChild(txCard);

    root.appendChild(grid);

    root.appendChild(el('a', {
      className: 'btn btn-ghost',
      href: '#/wallet',
      textContent: '\u2190 Back to Wallet',
      style: 'margin-top:20px;display:inline-flex;',
    }));

    document.getElementById('ad-copy').addEventListener('click', function () {
      if (!acct.address) return;
      if (navigator.clipboard) {
        navigator.clipboard.writeText(acct.address).then(function () {
          toast('Address copied', 'success');
        });
      } else {
        toast('Address: ' + acct.address, 'info');
      }
    });

    try {
      var txns = (await window.Plutus.getTransactions()) || [];
      var container = document.getElementById('ad-txns');
      if (!container) return;
      container.innerHTML = '';

      if (txns.length === 0) {
        container.innerHTML = '<p class="text-muted" style="text-align:center;padding:24px;">No transactions yet.</p>';
        return;
      }

      txns.slice(0, 5).forEach(function (tx) {
        var typeColor = tx.type === 'sent' ? 'var(--danger)' : 'var(--success)';
        var counterparty = tx.to || tx.from || tx.counterparty || '';
        var row = el('div', {
          style: 'display:flex;align-items:center;justify-content:space-between;padding:10px 0;border-bottom:1px solid var(--border);',
        });
        row.innerHTML =
          '<div>' +
            '<div style="font-weight:600;font-size:0.85rem;color:' + typeColor + ';">' + esc(H.formatPrice(tx.amount, tx.currency || '', tx.decimals)) + '</div>' +
            '<div class="text-muted" style="font-size:0.75rem;">' + esc(H.shortHex(counterparty, 6, 4)) + '</div>' +
          '</div>' +
          '<div style="text-align:right;">' +
            '<div class="text-muted" style="font-size:0.75rem;">' + esc(H.formatRelativeTime(tx.ts || tx.time)) + '</div>' +
            '<span class="status ' + (tx.status === 'confirmed' ? 'status-completed' : 'status-pending') + '">' +
              esc((tx.status || '').charAt(0).toUpperCase() + (tx.status || '').slice(1)) +
            '</span>' +
          '</div>';
        container.appendChild(row);
      });
    } catch (err) {
      var container2 = document.getElementById('ad-txns');
      if (container2) container2.innerHTML = '<p class="text-danger" style="text-align:center;padding:24px;">Failed to load: ' + esc(err.message) + '</p>';
    }
  }

  // ============================================================
  //  Fee Breakdown (utility renderer)
  // ============================================================

  function renderFeeBreakdown(amount, options) {
    options = options || {};
    var maintainerPct = options.maintainerPct || 0.01;
    var witnessPct    = options.witnessPct    || 0.005;
    var servicePct    = options.servicePct    || 0;
    var currency      = options.currency      || '';
    var curSuffix     = currency ? ' ' + currency : '';

    var maintainer = amount * maintainerPct;
    var witness    = amount * witnessPct;
    var service    = amount * servicePct;
    var total      = amount + maintainer + witness + service;

    var html =
      '<div class="fee-breakdown">' +
        '<div class="fee-row"><span class="fee-label">Item amount</span><span class="fee-value">' + amount.toFixed(6) + curSuffix + '</span></div>' +
        '<div class="fee-row"><span class="fee-label">Maintainer (' + (maintainerPct * 100).toFixed(1) + '%)</span><span class="fee-value">' + maintainer.toFixed(6) + curSuffix + '</span></div>' +
        '<div class="fee-row"><span class="fee-label">Witness (' + (witnessPct * 100).toFixed(1) + '%)</span><span class="fee-value">' + witness.toFixed(6) + curSuffix + '</span></div>';

    if (servicePct > 0) {
      html += '<div class="fee-row"><span class="fee-label">Service (' + (servicePct * 100).toFixed(1) + '%)</span><span class="fee-value">' + service.toFixed(6) + curSuffix + '</span></div>';
    }

    html +=
        '<div class="fee-row total"><span class="fee-label">Total</span><span class="fee-value">' + total.toFixed(6) + curSuffix + '</span></div>' +
      '</div>';

    return html;
  }

  // ============================================================
  //  Send Funds Modal
  // ============================================================

  function showSendFunds() {
    var overlay = el('div', { className: 'modal-overlay' });
    var modal   = el('div', { className: 'modal' });

    modal.innerHTML =
      '<div class="modal-header">' +
        '<span class="modal-title">Send Funds</span>' +
        '<button class="modal-close" id="sf-close">\u2715</button>' +
      '</div>' +
      '<div class="modal-body">' +
        '<div class="form-group">' +
          '<label class="form-label">Recipient address or peer ID</label>' +
          '<input type="text" class="form-input" id="sf-to" placeholder="0x... or peer_...">' +
        '</div>' +
        '<div class="form-row">' +
          '<div class="form-group">' +
            '<label class="form-label">Amount</label>' +
            '<input type="text" class="form-input" id="sf-amount" placeholder="0.01">' +
          '</div>' +
          '<div class="form-group">' +
            '<label class="form-label">Chain</label>' +
            '<select class="form-select" id="sf-chain">' +
              '<option value="ETH">ETH</option>' +
              '<option value="SOL">SOL</option>' +
              '<option value="BTC">BTC</option>' +
              '<option value="ARB">ARB</option>' +
              '<option value="POLYGON">POLYGON</option>' +
            '</select>' +
          '</div>' +
        '</div>' +
      '</div>' +
      '<div class="modal-footer">' +
        '<button class="btn btn-ghost" id="sf-cancel">Cancel</button>' +
        '<button class="btn btn-primary" id="sf-confirm">Send</button>' +
      '</div>';

    overlay.appendChild(modal);
    document.body.appendChild(overlay);

    function close() { overlay.remove(); }
    overlay.addEventListener('click', function (e) { if (e.target === overlay) close(); });
    document.getElementById('sf-close').addEventListener('click', close);
    document.getElementById('sf-cancel').addEventListener('click', close);

    document.getElementById('sf-confirm').addEventListener('click', function () {
      var to     = document.getElementById('sf-to').value.trim();
      var amount = document.getElementById('sf-amount').value.trim();
      var chain  = document.getElementById('sf-chain').value;

      if (!to || !amount) { toast('Please fill in all fields', 'warning'); return; }

      var btn = this;
      btn.disabled = true;
      btn.textContent = 'Sending...';

      window.Plutus.sendFunds(to, amount, chain)
        .then(function (tx) {
          close();
          toast('Transaction ' + H.shortHex(tx.id, 6, 4) + ' submitted', 'success');
          renderWallet();
        })
        .catch(function (err) {
          toast('Failed: ' + err.message, 'error');
          btn.disabled = false;
          btn.textContent = 'Send';
        });
    });
  }

  // ============================================================
  //  Transaction History
  // ============================================================

  async function loadTransactions() {
    var container = document.getElementById('wallet-tab-content');
    if (!container) return;
    container.innerHTML = '';
    container.appendChild(spinnerBlock());

    var txns;
    try {
      txns = (await window.Plutus.getTransactions()) || [];
    } catch (err) {
      container.innerHTML = '<p class="text-danger" style="text-align:center;padding:24px;">Failed to load: ' + esc(err.message) + '</p>';
      return;
    }

    var c = document.getElementById('wallet-tab-content');
    if (!c) return;
    c.innerHTML = '';

    if (txns.length === 0) {
      c.innerHTML =
        '<div class="empty-state">' +
          '<div class="empty-state-icon">&#128220;</div>' +
          '<h3 class="empty-state-title">No transactions</h3>' +
          '<p class="empty-state-desc">Your transaction history will appear here.</p>' +
        '</div>';
      return;
    }

    var html = '<div class="table-container"><table class="table"><thead><tr>' +
      '<th>ID</th><th>Type</th><th>Amount</th><th>To / From</th><th>Time</th><th>Status</th>' +
      '</tr></thead><tbody>';

    txns.forEach(function (tx) {
      var typeClass = tx.type === 'sent' ? 'text-danger' : 'text-success';
      var statusCls = tx.status === 'confirmed' ? 'status-completed' : 'status-pending';
      var counterparty = tx.to || tx.from || tx.counterparty || '';
      var typeLabel = (tx.type || '').charAt(0).toUpperCase() + (tx.type || '').slice(1);
      var statusLabel = (tx.status || '').charAt(0).toUpperCase() + (tx.status || '').slice(1);
      html +=
        '<tr>' +
          '<td class="mono" style="font-size:0.8rem;">' + esc(H.shortHex(tx.id, 6, 4)) + '</td>' +
          '<td class="' + typeClass + '" style="font-weight:600;">' + esc(typeLabel) + '</td>' +
          '<td style="font-weight:600;">' + esc(H.formatPrice(tx.amount, tx.currency || '', tx.decimals)) + '</td>' +
          '<td class="text-muted">' + esc(H.shortHex(counterparty, 6, 4)) + '</td>' +
          '<td class="text-muted" style="font-size:0.85rem;">' + esc(H.formatRelativeTime(tx.ts || tx.time)) + '</td>' +
          '<td><span class="status ' + statusCls + '">' + esc(statusLabel) + '</span></td>' +
        '</tr>';
    });

    html += '</tbody></table></div>';
    c.innerHTML = html;
  }

  // ---- Expose ----

  var api = {
    renderWallet: renderWallet,
    showCreateAccount: showCreateAccount,
    renderAccountDetail: renderAccountDetail,
    renderFeeBreakdown: renderFeeBreakdown,
    showSendFunds: showSendFunds,
    renderTransactionHistory: loadTransactions,
  };

  window.Wallet = api;
  window.MercuryWallet = api;

})();
