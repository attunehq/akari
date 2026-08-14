#!/usr/bin/env bash
# Idempotent bootstrap for akari's Cursor Cloud Agent environment.
#
# Installs the toolchains the repo needs beyond the base image (Bun and a native
# PostgreSQL), provisions the akari role/database, then refreshes application
# dependencies and generated state. Runs after the repository is checked out; it
# must terminate and be safe to run repeatedly (including against a cached VM or a
# build snapshot). Long-running services live in start.sh / the terminals config,
# never here.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/.." && pwd)"
cd "$repo"

# --- Bun: the frontend build + OpenAPI codegen toolchain ---
if [ ! -x "$HOME/.bun/bin/bun" ]; then
  curl -fsSL https://bun.sh/install | bash
fi
export BUN_INSTALL="$HOME/.bun"
export PATH="$BUN_INSTALL/bin:$PATH"

# --- PostgreSQL: installed natively (the eph loop uses Docker; this VM does not) ---
if ! command -v pg_ctlcluster >/dev/null 2>&1; then
  sudo apt-get update
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql postgresql-contrib
fi

PGVER="$(pg_lsclusters -h | awk 'NR==1 {print $1}')"
# Start the cluster so we can provision the role/db (idempotent: already-online is fine).
sudo pg_ctlcluster "$PGVER" main start || true
for _ in $(seq 1 30); do
  pg_isready -h localhost -p 5432 >/dev/null 2>&1 && break
  sleep 1
done
pg_isready -h localhost -p 5432 >/dev/null 2>&1

# Create the akari login role (CREATEDB so the integration suite can provision
# per-test databases) and the akari database, both only if absent.
sudo -u postgres psql -v ON_ERROR_STOP=1 <<'SQL'
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'akari') THEN
    CREATE ROLE akari LOGIN CREATEDB PASSWORD 'akari';
  END IF;
END $$;
SQL
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname = 'akari'" | grep -q 1; then
  sudo -u postgres createdb -O akari akari
fi

# --- Make the shared env vars available to future interactive shells ---
marker="# >>> akari cloud env >>>"
if ! grep -qF "$marker" "$HOME/.bashrc" 2>/dev/null; then
  {
    echo ""
    echo "$marker"
    echo "[ -f \"$repo/.cursor/env.sh\" ] && . \"$repo/.cursor/env.sh\""
    echo "# <<< akari cloud env <<<"
  } >> "$HOME/.bashrc"
fi

# --- Application dependencies and generated state ---
# shellcheck source=/dev/null
. "$repo/.cursor/env.sh"

( cd frontend && bun install --frozen-lockfile )
( cd site && npm ci )

# Compile the gitignored templ error pages, then warm the Go build cache.
go generate ./...
go build ./...

# Rebuild the committed embedded React artifact so the server serves the current UI.
( cd frontend && bun run build )

echo "akari install complete"
