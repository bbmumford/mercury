# mercury

The SDK and node for a decentralised peer-to-peer marketplace. Sellers publish signed stores of listings, buyers place signed orders paid into escrow, sellers commit to fulfil, and completion, disputes, arbitration, timeout refunds, reviews and reputation all run over gossip with no central server. It composes [minerva](https://github.com/bbmumford/minerva) (the P2P node, identity, CRDTs and messaging) and [plutus](https://github.com/bbmumford/plutus) (wallet, escrow, settlement and witnesses).

## Layout

| Path | What it holds |
|---|---|
| `sdk.go` | `mercury.New(Config)`: one marketplace participant (`Start`, `Stop`, `PublicKey`, `StoreID`) |
| `store`, `order`, `discovery`, `offline` | stores and listings, the order flow, category discovery and reachability announcements, offline seller commitments |
| `arbitration`, `moderation`, `reputation`, `profile` | disputes and rulings, block lists and reports, reviews and reputation, user profiles |
| `runtime/mesh` | a reusable role-based node runtime (blob replicator with proof of storage, message relay, balance and transaction witness, arbitrator, marketplace) |
| `wasm` | the browser build |
| `cmd/mercury-node` | a headless node running infrastructure roles; see [its README](cmd/mercury-node/README.md) |
| `cmd/mercury` | the desktop application (Wails; built with the `wails` build tag) |
| `example/marketplace` | a complete marketplace running in the browser; see [its README](example/marketplace/README.md) |

## Use

```sh
go get github.com/bbmumford/mercury
```

```go
m, err := mercury.New(mercury.Config{
	AppID:         "my-market",
	MinervaConfig: nodeConfig,   // minerva's NodeConfig
	PlutusConfig:  plutusConfig, // plutus's Config
	StoreName:     "Ada's books",
	Categories:    []string{"books"},
})
m.Start(ctx)
defer m.Stop()
```

A nil `StoreOwnerKey` generates a new Ed25519 key pair. mercury's go.mod replaces whisper, plutus, ledger and minerva with sibling directories (`../whisper` and so on), so a build outside a workspace needs them checked out beside it.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the commit and release conventions.

## Consumers

io agent (its mesh build starts `runtime/mesh`).
