# Token and cost aggregation: bases and invariants

This is the inventory the audit in issue #41 asked for: every place akari
aggregates token or cost data, which base each reads, and the invariant that keeps
the bases agreeing. It exists because overlapping views that each derive "the same"
number from a different query have nothing forcing them to agree, and #40 was the
bug that shape produced (the overview headline diverging from the rows beneath it
by an order of magnitude).

This is the token/cost instance of a general pattern: any maintained projection of
the same underlying data (a denormalized rollup, a counter, a facet, a cache, a
running hash) can drift from its source unless something forces it equal. This
document is the worked example, the one cluster written out in full.

The rule the codebase follows now:

> When several views present the same underlying datum, build them off one
> canonical aggregate; where two bases must coexist (a cheap rollup for a long
> index, the granular ledger for a chart), pin the invariant that keeps them equal
> with a test, so they reconcile by construction rather than by luck.

## The three bases

Token and cost data is aggregated from exactly three places. A fourth table,
`provider_usage_events`, is an *input* to the first three rather than a base of
its own for anything that has a session; the section below covers the part of it
that has none.

1. **The ledger: `usage_events`.** The granular, per-event record. One row per
   usage event, carrying the four token classes (input,
   output, cache read, cache write), a cost, an `occurred_at`, and the dedup keys
   that make a replayed line idempotent. Summing it is the source of truth.
   One family of sessions writes no ledger by design: a Grok subagent whose
   parent is ingested, because the parent's own transcript reports usage that
   aggregates the child's spend. The child's rebuild suppresses its rows (see
   `store.RebuildSession`), so summing the ledger counts that spend exactly
   once, on the parent, and the subagent cost-share panel attributes Grok
   delegation spend to the parent rather than the child.

2. **The session rollups: `sessions.total_*`.** Per-session running totals
   (`total_input_tokens`, `total_output_tokens`, `total_cache_read_tokens`,
   `total_cache_write_tokens`, `total_cost_usd`, plus `message_count` and the
   generated `total_tokens`). They are written by each session's rebuild so a
   long list or index never has to scan the ledger.

3. **The daily rollup: `session_usage_daily`.** Per (session, UTC day, model)
   sums of the four token classes and cost. Written by the same rebuild
   transaction (`internal/server/store/rollups.go`), it is the base the
   Insights money panels read (fleet mix, economics, cache savings, subagent
   cost share) so a page render never groups the ledger. An undated event folds
   into a NULL day, keeping the undated gap identical to the session rollups'.
   It is one of five insights rollup tables; the other four
   (`session_tool_rollup`, `session_file_churn`, `session_turns`,
   `session_activity_hourly`) carry no token or cost data but follow the same
   discipline: derived in the rebuild transaction, keyed on session_id only,
   pinned to their source rows by `TestRollupsDerivedInRebuild` and to the
   migration backfill by `TestRollupBackfillMatchesDerivations`
   (docs/insights-rollups.md).

## The load-bearing invariant

A session's projection is only ever written by a whole-session rebuild
(`store.RebuildSession`), which computes the ledger and the rollups from one
in-memory fold: usage events dedup in memory, and `sessions.total_*` is summed
from the exact row set the rebuild writes, in the same transaction. So, for
every session:

> `sessions.total_<class> == sum(usage_events.<class>_tokens)` for each of the four
> classes, `sessions.total_cost_usd == sum(usage_events.cost_usd)`, and
> `sessions.message_count == count(messages)`.

This holds by construction, but nothing in the schema enforces it, so it is exactly
the kind of thing that rots. It is pinned directly by
`TestSessionRollupMatchesLedger` (after live ingest and after an epoch rebuild,
across multiple agents, models, cache tokens, duplicate usage, undated usage, and
unknown model rates) and, for the specific Claude duplicate-usage case, by
`TestClaudeDuplicateUsageCountedOnce` in the parse package.

`sessions.model_fallback_count` follows the same construction against the
`model_fallbacks` table: the fold merges the several transcript lines of one
fallback (they share a dedup key) into one row in memory and counts the merged
set. The invariant `sessions.model_fallback_count == count(model_fallbacks)` is
pinned across ingest and rebuild by `TestClaudeModelFallbackMergesAndCounts` in
the parse package. The declined attempt's token counts live on the `model_fallbacks` row
only; they are deliberately NOT folded into `sessions.total_*` or `usage_events`
(whether a declined attempt is billed depends on where in the stream it declined, so
the totals stay a record of served usage).

## The one legitimate gap

The analytics surfaces filter `occurred_at IS NOT NULL`: an undated event has no day
to plot, so counting it in a headline but not in the daily chart would make the total
exceed the sum of the chart (the exact drift #40 fixed). The rollups carry no such
filter; they count every surviving event. So the rollup base and the ledger-analytics
base differ by exactly the undated usage, and by nothing else. In practice that is
zero (every agent parser stamps the turn a usage line belongs to, so a NULL
`occurred_at` is a malformed transcript to fix at ingest, not usage to scatter across
the dashboard), but it is a real difference and is pinned to exactly the undated
amount by `TestUndatedUsageIsTheOnlyRollupAnalyticsGap`.

## Provider usage that has no session

The three bases above are all derived from transcripts, so they can only ever
describe work akari has a session for. Cursor bills for work that leaves no
transcript on any machine akari syncs (cloud agents, IDE chats, the Grok bot):
on the corpus this was built against, that is 94% of the account's Cursor spend.
The client collects the vendor's own usage feed and stores it in
`provider_usage_events`, one row per billing request, keyed
`(user_id, provider, account_id, event_key)` so a re-collection of an overlapping
window is idempotent. Cost comes from the feed's `totalCents` (what the request
cost at token rates), not `chargedCents` (what it deducted from the plan), because
the former is the same kind of number as every other agent's cost in akari and the
latter is a billing artifact of one vendor's subscription.

Every row lands in exactly one of two classes, decided by whether the vendor's
reported `conversation_id` names a session akari already has:

**Attached** rows get a `session_id` and are then read by exactly one thing:
`store.RebuildSession`, which appends them to the same in-memory usage fold the
transcript produces. They become ordinary `usage_events`, `sessions.total_*`, and
`session_usage_daily` rows, so the load-bearing invariant above covers them without
amendment. Nothing else may read them; writing them into the ledger directly would
break the rule that a rebuild is the ledger's only writer. Because attachment can
happen after a session's last rebuild (the vendor feed and the transcript arrive
independently, in either order), attaching or newly claiming rows sets
`session_raw.usage_dirty`, which is a fourth reason a session is due
(`internal/server/store/due.go`) alongside a moved epoch, moved bytes, and a due
retry. The flag means "usage arrived that no rebuild has folded", so it is set
only for rows a write actually stored or moved. Setting it for every session that
merely holds attached usage would re-flag sessions the rebuild already folded, and
since the rebuild clears the flag while the rows stay attached, each collection
would set it again: an unbounded refold of the Cursor corpus, once per pass.

Attachment is also self-healing. Each collection sweeps any still-unattached row
whose session is now visible, because a collection and an announce can each read a
snapshot that predates the other's commit and so miss each other; a later
collection re-reporting the event cannot fix it on its own, since the event dedups
and never re-resolves. The sweep normally moves nothing and so costs no rebuild.

The join is on the conversation id, not on `source_session_id` directly: a Cursor
transcript carries no id of its own, so akari derives one from the file's location
and `source_session_id` is a path whose last segment is the conversation id. The SQL
expression and its Go twin live together in `store.provider_usage.go`
(`sessionConversationID` / `conversationIDOf`) so they cannot drift, and a partial
expression index on `sessions` backs the lookup. One conversation can be ingested
under two project slugs (a worktree moved), so the attachment picks the oldest
matching session deterministically and every later announce claims only rows that
are still unattached, keeping the spend on one session.

**Unattached** rows stay where they are and are read only through two union views:

- `usage_ledger` unions `usage_events` (joined to `sessions` for scope) with the
  unattached provider rows, at event grain. It is what the Overview panel reads.
- `usage_daily` unions `session_usage_daily` with the same provider rows aggregated
  on read to (day, model), at day grain. It is what the Insights money panels read.

Both views union or neither does. Counting account-wide spend in the Overview
headline while the Insights panels beneath it counted only session spend is exactly
the #40 shape this document exists to prevent, at a larger magnitude.

The provider arm supplies NULL `project_id`, NULL `machine`, and NULL `session_id`,
which is what keeps the union honest rather than merely wide:

- Project- and machine-scoped reads filter on those columns, so usage with no
  session drops out of them by construction. It counts fleet-wide and nowhere else,
  which is the truthful answer: akari does not know which project a cloud agent ran
  in.
- `count(DISTINCT session_id)` over the union never inflates, so a by-model or
  by-agent row can legitimately show spend against zero sessions.
- `agent` is the provider name. That is why every provider name must also be a
  `parser.Agents` entry (`TestProviderNamesAreAgents`): the identity is what lets a
  conversation id resolve against `sessions.source_session_id` at all, and what makes
  the by-agent split read as one Cursor column rather than two.
- `usage_daily` additionally supplies `relationship_type = ''` and
  `signals_stale = true` for provider rows. The first keeps account-wide spend in the
  subagent cost-share denominator and out of its numerator; the second is fail-closed,
  since no session signal covers usage with no session.

The class split is pinned by `TestUnattachedProviderUsageCountsFleetWideOnly` and
`TestAttachedProviderUsageCountsExactlyOnce`, and the attach path by
`TestProviderUsageResolvesWhenSessionArrivesLater`,
`TestProviderUsageResolvesAgainstThePathDerivedSourceID`, and
`TestOneConversationIngestedTwiceIsCountedOnce`.

## Whole-day windows on the daily rollup

`session_usage_daily` is day-grained, so the Insights reads over it window in
whole UTC days (`AnalyticsFilter.clauseForRollupDay`). The upper bound is exact:
Insights pins `Until` to a bucket boundary (a UTC midnight) before any rollup
query runs. The lower bound is deliberately wider: a mid-day `Since` (the "now
minus N days" ranges) counts the window's first UTC day in full, where the
ledger scan cut it at the instant. The charts drew that first-day bucket either
way, so the rollup read fills it as a complete bucket instead of a partially
counted one. `TestUsageTrendsWholeDayWindow` pins the behavior so it stays a
decision rather than drifting. The ledger surfaces (`Analytics`, `cacheStats`,
the sparklines) keep their instant-precise windows, which is why they stay on
the ledger.

## Where each view reads, and how it reconciles

| View | Function | Base | Reconciliation |
| --- | --- | --- | --- |
| Overview usage panel (totals, daily grid, by-model, by-agent) | `Store.Analytics(0, …)` | `usage_ledger` (ledger ∪ unattached provider usage) | One base grouped three ways; headline summed from the by-agent split, so `sum(by-model) == sum(by-agent) == headline` by construction (#40). |
| Project usage panel | `Store.Analytics(projectID, …)` | `usage_ledger`, scoped | Same function, scoped to one project. Unattached provider usage carries a NULL `project_id` and so drops out here. The project header shows no rollup figure of its own (`Store.Project` loads identity only), so nothing on the page contradicts the panel. |
| Project sparklines (30d trend on the projects index) | `Store.ProjectSparklines` | ledger | Per-project daily tokens over a trailing window; a trend, not a lifetime total, so it is not expected to equal the index's lifetime columns. It stays off `usage_ledger` because every row it can return is project-scoped anyway. |
| Projects index (tokens, cost columns) | `Store.ListProjects` | rollups | Lifetime per-project totals. Must equal the project usage panel's all-time figure (same datum, two pages). Pinned by `TestProjectsIndexReconcilesWithAnalytics`. |
| Global session list / project session list / subagents | `Store.ListAllSessions` / `Store.ListSessions` / `Store.Subagents` | rollups | Per-session rollups; the `tokens` sort walks the generated `total_tokens` column. |
| Session detail header (Tokens tile, cost) | `Store.SessionDetailByID` | rollups | Per-session rollups. The session page shows no ledger-derived figure beside them, so the invariant alone keeps them honest. |
| Insights fleet mix, model cost, economics, cache savings, subagent cost share | `fleetMixFrom`, `modelCostFrom`, `economicsFrom`, `cacheSavingsTrend`, `subagentTrendsFrom` | `usage_daily` (daily rollup ∪ unattached provider usage) | Per-day-and-model sums the trend grid re-buckets (fleet mix, economics) or the window totals (model cost); pinned to the ledger by `TestRollupsDerivedInRebuild` and windowed in whole days (see above). |

### Why the index stays on rollups

Converging the projects index onto the ledger would mean a `GROUP BY` over
`usage_events` for every project on a list that should be a cheap rollup read. That
is the case issue #41 explicitly carves out: where a view genuinely needs the cheaper
rollup and another needs the ledger, keep both and pin the invariant with a test
rather than forcing one base. That is what `TestProjectsIndexReconcilesWithAnalytics`
does.

## When you add a view

If you add a surface that shows a token or cost figure:

1. Read from the base the table above uses for that cluster. Do not introduce a third
   query that sums the same datum a new way. A fleet-scoped money figure reads a union
   view (`usage_ledger` or `usage_daily`); reading the underlying table instead would
   quietly omit account-wide provider spend from that one surface.
2. If your view genuinely needs the other base (a cheap index that should not scan the
   ledger, say), add or extend a reconciliation test so the two bases are pinned equal,
   the way `TestProjectsIndexReconcilesWithAnalytics` pins the index against the panel.
3. If you add a fifth token class or a new usage column, thread it through the
   rebuild fold (`store.RebuildSession`) and every aggregate query, and extend the
   invariant test. Dropping a class from one side is the regression these tests
   exist to catch.
