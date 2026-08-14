# Shared environment for akari's Cursor Cloud Agent development environment.
#
# Sourced by .cursor/install.sh, .cursor/start.sh, .cursor/serve.sh, and (via a
# managed block appended by install.sh) the agent's interactive ~/.bashrc, so
# `go test ./...` and `go run ./cmd/akari-server` work without eph.
#
# The Cloud environment runs Postgres natively (the repo's eph loop runs it in
# Docker for multi-worktree isolation, which this single-stack VM does not need).

export BUN_INSTALL="${BUN_INSTALL:-$HOME/.bun}"
case ":$PATH:" in
  *":$BUN_INSTALL/bin:"*) ;;
  *) export PATH="$BUN_INSTALL/bin:$PATH" ;;
esac

# The dev database the running server uses.
export AKARI_DATABASE_URL="postgres://akari:akari@localhost:5432/akari?sslmode=disable"
# Integration tests create and drop their own database beside this one via the
# postgres maintenance database, so the named database is only a connection target.
export AKARI_TEST_DATABASE_URL="postgres://akari:akari@localhost:5432/akari?sslmode=disable"
# Where the dev server listens and how clients/seeding reach it.
export AKARI_LISTEN="127.0.0.1:8080"
export AKARI_URL="http://localhost:8080"
# Local dev is plain HTTP; do not mark session cookies Secure.
export AKARI_COOKIE_INSECURE=1
