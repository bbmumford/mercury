# Changelog

## [0.1.0](https://github.com/bbmumford/mercury/compare/v0.0.1...v0.1.0) (2026-11-28)


### ⚠ BREAKING CHANGES

* SettleDispute refuses a ruling that lacks valid reputation signatures; an arbitrator signs them with SignDisputeReputation after SignRuling.

### Features

* a chain marked untrustworthy gets no signature from this node's witnesses until an operator clears the mark ([8cd6929](https://github.com/bbmumford/mercury/commit/8cd6929b830b2f8f334a0244ef416b989f1b063a))
* a dispute settles only with the arbitrator's signatures over both parties' reputation records, which are then stored ([6e8f8ab](https://github.com/bbmumford/mercury/commit/6e8f8ab682f61e160dc37e8f5425d657dc0cc087))


### Bug Fixes

* a listing field that does not decode is refused, so a listing is never served with a gossiped field at its zero value ([88c499b](https://github.com/bbmumford/mercury/commit/88c499b3e50ed73cda855618aac41e2b12bd98d8))
* a settlement claims its order around the escrow call ([5b68de9](https://github.com/bbmumford/mercury/commit/5b68de96dfd3cfdc878a05ab544fc615e3182041))
* dependencies follow plutus past the reported vulnerabilities: OpenTelemetry v1.47.0 and the modules it needs ([c4b85c0](https://github.com/bbmumford/mercury/commit/c4b85c069975e0be9dee295e7cd57e09ca9de169))
* go.mod requires aether v0.0.118, the release minerva requires, so the module still builds read-only ([5689309](https://github.com/bbmumford/mercury/commit/56893096f38101125ca1d8ab799b66d45c728210))
* go.mod requires ledger v0.0.21 and diststore v0.1.0, the versions the module builds against, so it builds read-only ([c3d597a](https://github.com/bbmumford/mercury/commit/c3d597a95e3b4040c1fc7d9548ece09300afd9a4))
* go.mod requires minerva v0.1.0, plutus v0.1.0, ledger v0.1.0, whisper v0.0.20 and diststore v0.1.1, with no local replaces ([edb5548](https://github.com/bbmumford/mercury/commit/edb55489ff665f1bab7a342ea7888bf2cc0415e5))
* go.mod requires whisper v0.0.19, so the module builds read-only ([43599aa](https://github.com/bbmumford/mercury/commit/43599aa577492d24937dcbf0118300135549e301))
* **proto:** order.proto field names are lowerCamelCase ([7b461c9](https://github.com/bbmumford/mercury/commit/7b461c9cbdc8b2ff35906cd433298d7ebb79b59a))
* **proto:** store.proto field names are lowerCamelCase ([61495ba](https://github.com/bbmumford/mercury/commit/61495bab5d0f421c9dfb2452793a9f7f7b8b076a))
* Replicate reports a pin it could not announce and takes it back, so the same root can be replicated again ([0f4506c](https://github.com/bbmumford/mercury/commit/0f4506cd7b5fc1cb3c0cd0384a092ae18a4f03f8))
* SettleDispute executes only a ruling signed by the dispute's assigned arbitrator ([e257918](https://github.com/bbmumford/mercury/commit/e2579187d50d85112b734dfd83612ba3a1a8b7b6))
* the profile and review state stores answer whisper's StateStore as the required whisper names it ([ff83034](https://github.com/bbmumford/mercury/commit/ff83034a6dcf20d9a7300900d2f1b13615f13c73))
* the profile and review state stores summarise the records they hold, filter Delta on the time a record was stored here, and admit a peer's record only under the rule Set and Submit apply ([dae0f6e](https://github.com/bbmumford/mercury/commit/dae0f6ea3e45e9bf23de8983372c681e01b9e22a))
