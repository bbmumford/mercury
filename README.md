# mercury

mercury is the SDK and node for a decentralised peer-to-peer marketplace. Sellers publish signed stores of listings, buyers place signed orders paid into escrow, sellers commit to fulfil, and completion, disputes, arbitration, timeout refunds, reviews and reputation all run over gossip with no central server. It composes [minerva](https://github.com/bbmumford/minerva) (the P2P node, identity, CRDTs and messaging) and [plutus](https://github.com/bbmumford/plutus) (wallet, escrow, settlement and witnesses).

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

A nil `StoreOwnerKey` generates a new Ed25519 key pair.

## Dependencies

- `github.com/bbmumford/minerva` (`sdk`, `crdt`, `identity`, `messaging`,
  `persistence/file`): the node, identities, CRDTs and messaging.
- `github.com/bbmumford/plutus` (wallet, adapters, escrow, settlement,
  witnesses, arbitrators, reputation, observability): payment and settlement.
- `github.com/bbmumford/whisper`: the marketplace's gossip topics.
- `github.com/bbmumford/diststore` (`abuse`, `advertise`, `cache`, `crypto`,
  `fetch`, `quota`, `rarest`, `replicate`, `resolve`, `storage`) and
  `github.com/bbmumford/ledger` (`cache`, `blob/distributed/onchain`): the
  blob replicator and its proof of storage in `runtime/mesh`.
- `github.com/ORBTR/aether` (`runtime/mesh`).
- `github.com/wailsapp/wails/v2` (the desktop application),
  `go.opentelemetry.io/otel`, `golang.org/x/crypto` and
  `google.golang.org/protobuf`.
- `go.mod` replaces whisper, plutus, ledger, minerva, aether and diststore
  with sibling checkouts (`../whisper` and so on), so a build outside a
  workspace needs them checked out beside it.

## Used by

Importers in the ORBTR estate:

- `github.com/ORBTR/agent/systempart/internal/meshembed` (`runtime/mesh`),
  built only with the agent's `mesh` build tag.

## Platforms and build

Pure Go, no cgo in mercury itself. `cmd/mercury` builds only with the `wails`
build tag (`wails build`), and `wasm` only for js/wasm; the js/wasm build
also needs minerva's `sdk` to build for js. The module's `go` directive is
1.25.0. Releases are cut by release-please from conventional commits.

## Licence

MIT. Copyright (c) 2026 Brandon Miller-Mumford. The full text is in
[LICENSE](LICENSE).

## Contributing

[AGENTS.md](AGENTS.md) holds the repository's two rules, for people and coding
agents alike: no temporary documents in the repository, and conventional commit
messages that stand on their own. [CLAUDE.md](CLAUDE.md) imports it for Claude
Code; [CONTRIBUTING.md](CONTRIBUTING.md) has the commit and release
conventions.
