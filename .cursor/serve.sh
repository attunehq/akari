#!/usr/bin/env bash
# Runs the akari dev server for the Cloud Agent environment as a visible,
# restartable terminal process. The server applies its embedded migrations on
# boot. `go generate` compiles the gitignored templ error pages first (absent on
# a fresh checkout), matching the .eph server service. Once the server is
# healthy, dev-seed creates the demo accounts (password "akari-dev") in the
# background; it is best-effort and idempotent.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/.." && pwd)"
cd "$repo"

# shellcheck source=/dev/null
. "$repo/.cursor/env.sh"

go generate ./...

(
  for _ in $(seq 1 90); do
    if curl -sf "$AKARI_URL/healthz" >/dev/null 2>&1; then
      go run ./cmd/akari-server dev-seed || true
      break
    fi
    sleep 2
  done
) &

exec go run ./cmd/akari-server
