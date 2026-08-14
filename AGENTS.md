# Agent notes for akari

Short orientation for coding agents. The full story lives in
[docs/development.md](docs/development.md) and [DESIGN.md](DESIGN.md); this file
just front-loads the three things that bite if you skip them.

## Build both browser surfaces

The application UI lives in `frontend/` and builds into
`internal/server/frontend/dist/`, which Go embeds in the server binary. The
public homepage and user guide live in `site/` and deploy to GitHub Pages.
The guide Markdown is single-sourced under `docs/user-guide/`.

```sh
make build          # build React, generate templ, then compile Go
make test           # check React, rebuild it, then run Go tests under -race
make frontend-check # OpenAPI contract, Biome, and TypeScript
make site            # build the public homepage and guide
go generate ./...   # regenerate the templated server error pages
```

The production frontend artifact is committed so release cross-compilation and
downstream source builds still require only Go. Run `make frontend` after any
file under `frontend/` and commit the resulting `dist/` changes.
Run `make site` after changing `site/` or `docs/user-guide/`.

## Keep the browser API contract synchronized

OpenAPI is authoritative for the browser API. When a browser endpoint's route,
status, request, or response changes, update its named Go boundary DTO and
`internal/server/httpapi/openapi.json` in the same change, then regenerate
`frontend/src/api.generated.ts`. Do not edit the generated TypeScript by hand.
`make frontend-check` rejects invalid OpenAPI and stale generated types, while
the Go HTTP API contract tests reject route and response-schema drift.

## Integration tests gate on a database

Tests that touch Postgres skip cleanly unless `AKARI_TEST_DATABASE_URL` is set.
A green `go test ./...` with the variable unset has silently skipped the store,
parse, and web integration tests. Under eph the variable is already set, so the
one-liner is:

```sh
eph run go test ./...
```

Without eph, point it at any Postgres whose role may create databases (each test
provisions and drops its own database beside the one the URL names):

```sh
AKARI_TEST_DATABASE_URL=postgres://akari:akari@localhost:5432/akari go test ./...
```

## Running the app locally

`eph up` brings up Postgres plus the server and seeds demo data; sign in as
`grace` (admin) with password `akari-dev`. See
[docs/development.md](docs/development.md) "Worktree-based development with eph"
and "Example data for development".

## Parsing, signals, and the parse epoch

Parsing is rebuild-on-dirty: the ingest path only appends raw bytes, and a
background worker rebuilds a session's whole projection whenever its raw bytes
or the parser have moved (docs/DESIGN.md, "Server-side parsing pipeline").
There is no incremental parse; never write projection rows outside the rebuild.
Per-session signals (outcome, quality score and grade, tool health, prompt
hygiene, context health, observed thinking) live in `session_signals`, graded
off the hot path once a session settles or is declared terminal (a mid-session
verdict would drift).

One constant gates every derived representation: `parse.Epoch`. Bump it in the
same commit as any change to parser or reducer output, a rebuild-derived
column, the signal set or scoring, prompt classification, the pricing table, or
the thinking calibration's stored scalars; the next deploy rebuilds the corpus
in the background. The golden-fixtures test fails by name when you forget.
[docs/signals.md](docs/signals.md) has the full rules, including why a new
signal must default to "unmeasured" and the absolute token scale observed
thinking bands on. Read it before you touch signals, scoring, or pricing.

## Cursor Cloud specific instructions

The Cloud Agent environment (`.cursor/environment.json`) runs Postgres natively
instead of through eph/Docker, so skip the `eph` commands here. `install`
provisions Bun, PostgreSQL, and the `akari` role/database; `start` brings
Postgres online; and the `akari-server` terminal runs the dev server on
<http://localhost:8080> (seeding the demo accounts, password `akari-dev`). The
`AKARI_*` variables are exported into every shell via `~/.bashrc`
(from `.cursor/env.sh`), so the database-backed suites run with a plain:

```sh
go test ./...          # AKARI_TEST_DATABASE_URL is already set
```

Frontend work needs Bun on `PATH` (also set by `.cursor/env.sh`); use the
`make`/`bun` targets above unchanged.
