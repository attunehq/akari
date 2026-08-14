#!/usr/bin/env bash
# Per-boot reconciliation for akari's Cloud Agent environment: bring PostgreSQL
# online and wait until it accepts connections, then return. Dependency install
# and code generation belong in install.sh; the long-running server lives in the
# terminals config. Safe to run repeatedly.
set -euo pipefail

PGVER="$(pg_lsclusters -h | awk 'NR==1 {print $1}')"
status="$(pg_lsclusters -h | awk 'NR==1 {print $4}')"
if [ "$status" != "online" ]; then
  sudo pg_ctlcluster "$PGVER" main start
fi

for _ in $(seq 1 60); do
  pg_isready -h localhost -p 5432 >/dev/null 2>&1 && break
  sleep 1
done
pg_isready -h localhost -p 5432

echo "postgres online (cluster $PGVER main on port 5432)"
