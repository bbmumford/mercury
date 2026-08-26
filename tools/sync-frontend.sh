#!/usr/bin/env bash
# Sync the marketplace example UI into the Wails desktop frontend so the
# desktop client and web example share a single source of truth.
#
# Run from the mercury repo root, or via the Makefile target `make sync-desktop`.
# The desktop client uses a different config.json (local relays, embedded WASM
# paths) so we copy a desktop-specific overlay last.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/example/marketplace"
DST="$ROOT/cmd/mercury/frontend"

if [[ ! -d "$SRC" ]]; then
  echo "sync-frontend: missing $SRC" >&2
  exit 1
fi

mkdir -p "$DST/css" "$DST/js"
cp "$SRC/index.html" "$DST/"
cp -r "$SRC/css/." "$DST/css/"
cp -r "$SRC/js/." "$DST/js/"

# Desktop config: WASM is bundled inside the app, no GitHub release fetch.
cat > "$DST/config.json" <<'JSON'
{
  "mercuryWasm": "mercury.wasm",
  "plutusWasm":  "plutus.wasm",
  "relays": [],
  "defaultChain": "polygon",
  "features": {
    "passkey": true,
    "offline": true,
    "search": true,
    "reviews": true,
    "arbitration": true,
    "desktop": true
  }
}
JSON

echo "synced $SRC -> $DST"
