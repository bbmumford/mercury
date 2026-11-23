/**
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 *
 * Mercury JS SDK — browser-side wrapper classes for the Mercury WASM module.
 * Exposes MercuryNode, MercuryStore, MercuryOrders, MercuryMessages, and
 * the initMercury() loader.
 */

/**
 * Initialize the Mercury WASM module.
 * @param {string} wasmUrl - URL to the mercury.wasm binary
 * @returns {Promise<MercuryNode>}
 */
async function initMercury(wasmUrl) {
  if (!wasmUrl) {
    throw new Error('initMercury: wasmUrl is required');
  }

  if (typeof Go === 'undefined') {
    throw new Error('initMercury: Go WASM support (wasm_exec.js) must be loaded first');
  }

  const go = new Go();
  let result;

  if (typeof WebAssembly.instantiateStreaming === 'function') {
    result = await WebAssembly.instantiateStreaming(fetch(wasmUrl), go.importObject);
  } else {
    const resp = await fetch(wasmUrl);
    const bytes = await resp.arrayBuffer();
    result = await WebAssembly.instantiate(bytes, go.importObject);
  }

  // Run the Go main() — this sets up globalThis.Mercury
  go.run(result.instance);

  // Wait for Mercury to be available
  await waitForMercury();

  return MercuryNode;
}

/**
 * Wait for the Mercury global to be set by the WASM module.
 * @param {number} [timeout=5000] - Maximum wait time in ms
 * @returns {Promise<void>}
 */
function waitForMercury(timeout = 5000) {
  return new Promise((resolve, reject) => {
    const start = Date.now();
    const check = () => {
      if (typeof globalThis.Mercury !== 'undefined') {
        resolve();
      } else if (Date.now() - start > timeout) {
        reject(new Error('initMercury: WASM module did not initialize within timeout'));
      } else {
        setTimeout(check, 10);
      }
    };
    check();
  });
}

/**
 * MercuryNode is the top-level entry point for the Mercury marketplace SDK.
 * Manages store, orders, messaging, and reputation subsystems.
 */
class MercuryNode {
  /** @type {Object} */
  #config;
  /** @type {MercuryStore|null} */
  #store = null;
  /** @type {MercuryOrders|null} */
  #orders = null;
  /** @type {MercuryMessages|null} */
  #messages = null;
  /** @type {boolean} */
  #started = false;
  /** @type {number} */
  #nodeHandle = 0;

  /**
   * @param {Object} config
   * @param {string} [config.storeName] - Name for this node's store
   * @param {string[]} [config.categories] - Categories to subscribe to
   */
  constructor(config = {}) {
    this.#config = config;
  }

  /**
   * Start the Mercury node: create store, subscribe to categories.
   * @returns {Promise<void>}
   */
  async start() {
    if (this.#started) return;

    const storeName = this.#config.storeName || 'My Store';
    const storeHandle = await Mercury.createStore(this.#nodeHandle, storeName);

    this.#store = new MercuryStore(storeHandle);
    this.#orders = new MercuryOrders();
    this.#messages = new MercuryMessages();
    this.#started = true;
  }

  /**
   * Stop the Mercury node.
   * @returns {Promise<void>}
   */
  async stop() {
    this.#started = false;
    this.#store = null;
    this.#orders = null;
    this.#messages = null;
  }

  /**
   * Get the store manager for this node.
   * @returns {MercuryStore}
   */
  get store() {
    if (!this.#store) throw new Error('MercuryNode: not started');
    return this.#store;
  }

  /**
   * Get the order manager for this node.
   * @returns {MercuryOrders}
   */
  get orders() {
    if (!this.#orders) throw new Error('MercuryNode: not started');
    return this.#orders;
  }

  /**
   * Get the messaging service for this node.
   * @returns {MercuryMessages}
   */
  get messages() {
    if (!this.#messages) throw new Error('MercuryNode: not started');
    return this.#messages;
  }

  /**
   * Get the reputation score for a seller.
   * @param {string} pubkeyHex - Hex-encoded Ed25519 public key
   * @returns {Promise<Object>}
   */
  async reputation(pubkeyHex) {
    if (!pubkeyHex) throw new Error('MercuryNode.reputation: pubkey is required');
    const raw = await Mercury.getReputation(pubkeyHex);
    return typeof raw === 'string' ? JSON.parse(raw) : raw;
  }
}

/**
 * MercuryStore wraps store listing CRUD operations.
 */
class MercuryStore {
  /** @type {number} */
  #handle;

  /**
   * @param {number} handle - WASM store handle
   */
  constructor(handle) {
    this.#handle = handle;
  }

  /**
   * Add a listing to the store.
   * @param {Object} listing
   * @param {string} [listing.id] - Optional listing ID (auto-generated if omitted)
   * @param {string} listing.title - Listing title
   * @param {string} [listing.description] - Description
   * @param {number} listing.price - Price in smallest currency unit
   * @param {string} [listing.currency] - Currency code (default: "USDC")
   * @param {string} [listing.category] - Category path
   * @param {string[]} [listing.images] - Content-addressed image CIDs
   * @param {boolean} [listing.digital] - Whether this is a digital product
   * @param {string} [listing.digitalCid] - CID of digital content
   * @returns {Promise<string>} Listing ID
   */
  async addListing(listing) {
    if (!listing) throw new Error('MercuryStore.addListing: listing is required');
    return Mercury.addListing(this.#handle, listing);
  }

  /**
   * Get a listing by ID.
   * @param {string} listingID
   * @returns {Promise<Object>}
   */
  async getListing(listingID) {
    if (!listingID) throw new Error('MercuryStore.getListing: listingID is required');
    const storeData = await Mercury.getStore(this.#handle);
    const store = typeof storeData === 'string' ? JSON.parse(storeData) : storeData;
    if (store.listings) {
      const found = store.listings.find(l => l.id === listingID);
      if (found) return found;
    }
    throw new Error(`MercuryStore.getListing: listing ${listingID} not found`);
  }

  /**
   * Remove a listing from the store.
   * @param {string} listingID
   * @returns {Promise<void>}
   */
  async removeListing(listingID) {
    if (!listingID) throw new Error('MercuryStore.removeListing: listingID is required');
    return Mercury.removeListing(this.#handle, listingID);
  }

  /**
   * Get all listings in the store.
   * @returns {Promise<Object[]>}
   */
  async allListings() {
    const storeData = await Mercury.getStore(this.#handle);
    const store = typeof storeData === 'string' ? JSON.parse(storeData) : storeData;
    return store.listings || [];
  }

  /**
   * Publish the store (sign root, update Merkle tree).
   * @returns {Promise<Object>} Store root data
   */
  async publish() {
    const raw = await Mercury.getStore(this.#handle);
    return typeof raw === 'string' ? JSON.parse(raw) : raw;
  }

  /**
   * Get the internal handle.
   * @returns {number}
   */
  get handle() {
    return this.#handle;
  }
}

/**
 * MercuryOrders wraps order lifecycle operations.
 */
class MercuryOrders {
  /**
   * Create a new order.
   * @param {Object} params
   * @param {string} [params.id] - Optional order ID (auto-generated if omitted)
   * @param {string} params.listingId - Listing to purchase
   * @param {number} params.quantity - Quantity
   * @returns {Promise<Object>} Order handle and ID
   */
  async create(params) {
    if (!params) throw new Error('MercuryOrders.create: params is required');
    const handle = await Mercury.createOrder(params);
    return { handle, id: typeof handle === 'number' ? handle : handle };
  }

  /**
   * Accept an order (seller action).
   * @param {number} orderHandle - Order handle from create()
   * @returns {Promise<Object>} Updated order status
   */
  async accept(orderHandle) {
    const raw = await Mercury.acceptOrder(orderHandle);
    return typeof raw === 'string' ? JSON.parse(raw) : raw;
  }

  /**
   * Complete an order (buyer confirms receipt).
   * @param {number} orderHandle - Order handle
   * @returns {Promise<Object>} Updated order status
   */
  async complete(orderHandle) {
    const raw = await Mercury.completeOrder(orderHandle);
    return typeof raw === 'string' ? JSON.parse(raw) : raw;
  }

  /**
   * Dispute an order.
   * @param {number} orderHandle - Order handle
   * @param {string} reason - Dispute reason
   * @param {string} evidence - Evidence data
   * @returns {Promise<Object>} Dispute details
   */
  async dispute(orderHandle, reason, evidence) {
    if (!reason) throw new Error('MercuryOrders.dispute: reason is required');
    const raw = await Mercury.disputeOrder(orderHandle, reason, evidence || '');
    return typeof raw === 'string' ? JSON.parse(raw) : raw;
  }
}

/**
 * MercuryMessages wraps messaging operations.
 */
class MercuryMessages {
  /** @type {Function[]} */
  #handlers = [];

  /**
   * Send a message to a recipient.
   * @param {string} recipientPubKeyHex - Hex-encoded recipient Ed25519 public key
   * @param {string} content - Message content
   * @returns {Promise<string>} Message ID
   */
  async send(recipientPubKeyHex, content) {
    if (!recipientPubKeyHex) throw new Error('MercuryMessages.send: recipient is required');
    if (!content) throw new Error('MercuryMessages.send: content is required');
    return Mercury.sendMessage(recipientPubKeyHex, content);
  }

  /**
   * Get inbox messages for a peer.
   * @param {string} peerID - Hex-encoded peer public key
   * @returns {Promise<Object[]>} Array of messages
   */
  async inbox(peerID) {
    if (!peerID) throw new Error('MercuryMessages.inbox: peerID is required');
    const raw = await Mercury.getMessages(peerID);
    return typeof raw === 'string' ? JSON.parse(raw) : (raw || []);
  }

  /**
   * Register a handler for incoming messages.
   * @param {Function} handler - Called with (message) for each incoming message
   * @returns {Function} Unsubscribe function
   */
  onMessage(handler) {
    if (typeof handler !== 'function') {
      throw new Error('MercuryMessages.onMessage: handler must be a function');
    }
    this.#handlers.push(handler);
    return () => {
      this.#handlers = this.#handlers.filter(h => h !== handler);
    };
  }

  /** @private */
  _dispatch(message) {
    for (const handler of this.#handlers) {
      try {
        handler(message);
      } catch (_) {
        // Swallow handler errors
      }
    }
  }
}

// Export for module systems
if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    initMercury,
    MercuryNode,
    MercuryStore,
    MercuryOrders,
    MercuryMessages,
  };
}
