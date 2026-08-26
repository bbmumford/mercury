# mercury-node

Headless Mercury/Plutus node for running marketplace infrastructure roles
(replicator, arbitrator, seller) and, optionally, a witness-mesh
**balance-witness** that services on-chain balance queries for WASM clients.

## Roles

| Role | Flag | Purpose |
|---|---|---|
| `replicator` | `--roles=replicator` | Pins stores and rehosts content for other peers |
| `arbitrator` | `--roles=arbitrator` | Resolves disputes; needs registered Plutus identity |
| `seller` | `--roles=seller` | Advertises a store and services offline commitments |
| `balance-witness` | `--roles=balance-witness` | **NEW.** Serves quorum-verified balance reads |

`--headless` enables replicator, arbitrator, and seller (but NOT
balance-witness — opt in explicitly because it needs RPC URLs).

## Running a balance-witness

A balance-witness listens on the witness-mesh `plutus.<fp>.balance.request`
topic, queries the relevant chain via its configured RPC URL, and replies with
a signed `BalanceAttestation` on `plutus.<fp>.balance.response`. Clients
(mainly WASM wallets) apply a K-of-N quorum (default **2-of-3**) to the
returned attestations.

This exists because WASM clients cannot `net.Dial` chain RPC — the mesh is
their window onto chain state. See
`_OTHER/mercury/witness-balance-query-guide.md` for the full protocol spec.

### Flags

```
--roles=balance-witness          # enable the role
--rpc-polygon=URL                # Polygon JSON-RPC
--rpc-ethereum=URL               # Ethereum JSON-RPC
--rpc-arbitrum=URL               # Arbitrum JSON-RPC
--rpc-bsc=URL                    # BSC JSON-RPC
--esplora-url=URL                # BTC Esplora (default: blockstream.info/api)
```

You do not need to supply all flags — omit an `--rpc-*` to not serve that
chain. BTC support is always registered against the public Blockstream
Esplora if no `--esplora-url` is provided.

### Example

```bash
mercury-node \
  --roles=replicator,balance-witness \
  --rpc-polygon=https://polygon-mainnet.g.alchemy.com/v2/YOUR_KEY \
  --rpc-ethereum=https://eth-mainnet.g.alchemy.com/v2/YOUR_KEY \
  --esplora-url=https://blockstream.info/api \
  --data-dir=./mercury-data
```

### Cost to operate

Copied verbatim from the design guide (`witness-balance-query-guide.md`):

| Resource | Per-month |
|---|---|
| 1 vCPU / 2 GB RAM VPS | $5 |
| Polygon archive RPC (Alchemy free tier: 300M cu/mo) | $0 |
| Ethereum archive RPC (free tier exhausts at ~50k queries/day; Growth) | $49 |
| Blockstream Esplora (public) | $0 |
| Bandwidth | ~free (<1 GB/day) |

A **Polygon + BTC** balance-witness runs free-tier indefinitely. Adding
Ethereum mainnet is ~$49/mo unless you self-host an archive node
(~$200/mo hardware).

The trust model is K-of-N across the mesh — no individual RPC provider is
trusted. Witnesses can freely use public free-tier RPCs.

### Privacy note

v1 does **not** implement query mixing or dummy-address batching. Every
balance-witness that answers a query sees `(chain, address, token)`. Use
short-lived derived addresses (Plutus Layer-B per-order keys) for anything
you want to hide. Main-wallet balance queries are observable.

### Security

Each attestation is Ed25519-signed. Stale snapshots are rejected via
`MaxAgeSeconds`; replay attempts are caught via `request_nonce`. Disagreement
between witnesses is logged and fed to the existing collusion detector.
Witnesses must be registered in the canonical `WitnessRegistry` (signed
entries) for their attestations to be accepted.
