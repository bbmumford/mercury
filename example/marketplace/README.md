# Mercury Marketplace Example

A complete decentralised marketplace running entirely in the browser via WebAssembly.
No servers. No databases. Pure peer-to-peer commerce.

## Architecture

Mercury is a decentralised commerce protocol built on the Minerva P2P platform.
This example app compiles the Mercury Go package to WASM and runs the full stack
in the browser:

- **Identity** -- BIP39 mnemonic generation, Ed25519 keypairs, WebAuthn passkeys
- **Store Protocol** -- Signed Merkle DAG store roots, CRDT listing state
- **Discovery** -- Gossip-based category channels, local full-text search
- **Escrow** -- Non-custodial chain-native smart contracts (via Plutus SDK)
- **Messaging** -- E2E encrypted buyer/seller chat over Aether streams
- **Reputation** -- CRDT-based scoring bound to identity
- **Arbitration** -- Decentralised dispute resolution with evidence

All P2P networking, payments, and state sync happen via the WASM binary.
The static HTML/CSS/JS is a thin UI layer that calls into the Go runtime.

## Quick Start

1. Build the WASM binary:
   ```bash
   cd _PACKAGES/mercury
   GOOS=js GOARCH=wasm go build -o examples/marketplace/mercury.wasm ./app/wasm/
   cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" examples/marketplace/
   cp app/wasm/mercury.js examples/marketplace/js/
   ```

2. Serve locally:
   ```bash
   cd examples/marketplace
   python3 -m http.server 8080
   ```

3. Open http://localhost:8080

## Deploy to GitHub Pages

1. Push this directory to a GitHub repo
2. Settings -> Pages -> Source: Deploy from branch (main, /docs or root)
3. Access at `https://yourusername.github.io/mercury-marketplace/`

## Directory Structure

```
examples/marketplace/
  index.html           Main SPA entry point
  css/
    style.css          Dark theme marketplace design
  js/
    app.js             Routing, state management, WASM bridge
    auth.js            Account creation, passkey, mnemonic backup
    store.js           Store management, listings, publish
    browse.js          Category browsing, search, listing detail
    order.js           Order flow, escrow, tracking
    wallet.js          Multi-account wallet, balances, send/receive
    messaging.js       Buyer-seller E2E encrypted chat
    reputation.js      Ratings, reviews, seller scores
    arbitration.js     Dispute filing, evidence, rulings
    settings.js        Profile, passkey management, export
  .nojekyll            Tells GitHub Pages to serve as-is
```

## Dependencies

- **Mercury WASM** -- compiled from the Mercury Go package
- **wasm_exec.js** -- Go's WASM support runtime
- **mercury.js** -- JS bridge exposing Go functions to the browser

No npm. No bundler. No framework. Vanilla JS + Web Components.

## Protocol Fee Structure

Every transaction enforces transparent, non-discretionary fees:

| Recipient | Rate | Condition |
|-----------|------|-----------|
| Maintainer | Fixed bps from PRC | Always (on-chain enforced) |
| Replicator | Seller-configured % | Only if replicator served the buyer |
| Arbitrator | Seller-configured % | Only if dispute was filed |

Fees are enforced at settlement by chain-level escrow contracts referencing the
immutable Protocol Registry Contract (PRC). See `maintainer-fees.md` for details.
