-- Provider-reported usage: billing events fetched from a coding agent vendor's
-- account API rather than parsed from a session transcript.
--
-- Cursor is the first and only provider. Its transcripts carry no model, no token
-- counts, and no cost (see internal/parser/cursor.go), so a Cursor session priced
-- at zero and reported no model anywhere on the dashboard. Its account API
-- (POST cursor.com/api/dashboard/get-filtered-usage-events) reports exactly those
-- figures per billing request, and stamps each one with the conversation it served.
-- That conversation id is the same identifier the CLI names its transcript
-- directory with, so it joins sessions.source_session_id directly.
--
-- Two things follow from the account being the grain the vendor reports at, and
-- they shape every column here.
--
-- First, a fetched event may arrive before the session it belongs to. The
-- conversation id is therefore stored verbatim and session_id is resolved
-- separately, both when events land and when a session is announced.
--
-- Second, most of an account's usage belongs to no akari session at all. Cursor
-- bills IDE composer chats, cloud/background agents, and its Grok bot through the
-- same feed, and none of those write a transcript akari can ingest. Those rows keep
-- session_id NULL. They are real subscription spend, so they count in fleet-scope
-- analytics through the usage_ledger view below, and they are absent from every
-- project, session, and machine scope because they genuinely have none.

CREATE TABLE provider_usage_events (
  id              BIGSERIAL PRIMARY KEY,
  user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider        TEXT NOT NULL CHECK (provider IN ('cursor')),
  -- The vendor-side account the event billed to, so one akari user may collect
  -- from two Cursor accounts without their synthetic event keys colliding.
  account_id      TEXT NOT NULL,
  -- The feed exposes no event id, so the client derives a stable one from the
  -- event's own fields (see internal/client/provider/cursor). It is what makes a
  -- re-fetch, or a second machine collecting the same account-wide feed,
  -- idempotent rather than double-counted.
  event_key       TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  session_id      BIGINT REFERENCES sessions(id) ON DELETE SET NULL,
  model           TEXT NOT NULL,
  input_tokens       INT NOT NULL DEFAULT 0,
  output_tokens      INT NOT NULL DEFAULT 0,
  cache_write_tokens INT NOT NULL DEFAULT 0,
  cache_read_tokens  INT NOT NULL DEFAULT 0,
  -- The vendor reports this cost, so it carries cost_source 'provider' into the
  -- ledger and never re-prices through the rate table. Same three-value domain as
  -- usage_events.cost_source, and the same fail-closed rule: an unknown cost is
  -- zero, and a zero amount alone no longer implies an unknown price.
  cost_usd        DOUBLE PRECISION NOT NULL DEFAULT 0,
  cost_source     TEXT NOT NULL DEFAULT 'provider',
  model_name_public BOOLEAN NOT NULL DEFAULT FALSE,
  occurred_at     TIMESTAMPTZ NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT provider_usage_cost_source_ck
    CHECK (cost_source IN ('unknown', 'rate_table', 'provider')),
  CONSTRAINT provider_usage_unknown_cost_ck
    CHECK (cost_source <> 'unknown' OR cost_usd = 0),
  UNIQUE (user_id, provider, account_id, event_key)
);

-- The rebuild folds a session's provider rows into its ledger, so it reads them by
-- session on every rebuild of a Cursor session.
CREATE INDEX idx_provider_usage_session ON provider_usage_events (session_id)
  WHERE session_id IS NOT NULL;

-- Resolution looks up unattached rows by the conversation a newly announced
-- session names. Only unattached rows are ever scanned, so the index carries only
-- those.
CREATE INDEX idx_provider_usage_unresolved
  ON provider_usage_events (user_id, provider, conversation_id)
  WHERE session_id IS NULL;

-- A Cursor session's source id is a path, not the bare conversation id: the
-- transcript records no id of its own, so the client derives one from the file's
-- location (resolve.sourceIDFromName). The conversation id is that path's last
-- segment, for a main transcript
-- (<project>/agent-transcripts/<id>/<id>) and for a subagent alike
-- (<project>/agent-transcripts/<parent>/subagents/<child>), which is the same fact
-- the client already relies on to find a session's cwd sidecar by basename. Cursor
-- bills subagents under their own conversation id, so the last segment is the right
-- key for both.
--
-- This index serves that join in both directions: an arriving event looking for its
-- session, and an announced session claiming its waiting events.
CREATE INDEX idx_sessions_cursor_conversation
  ON sessions (user_id, agent, (regexp_replace(source_session_id, '^.*/', '')))
  WHERE agent = 'cursor';

-- The fleet-scope analytics arm reads the unattached rows over a time window.
CREATE INDEX idx_provider_usage_unattached_time
  ON provider_usage_events (occurred_at)
  WHERE session_id IS NULL;

-- The client asks where to resume from rather than persisting a cursor of its own,
-- matching how it resumes a transcript upload. This serves that watermark read.
CREATE INDEX idx_provider_usage_watermark
  ON provider_usage_events (user_id, provider, account_id, occurred_at DESC);

-- A session whose provider usage changed is behind its projection for a reason the
-- byte length and the epoch cannot express: its raw bytes and the parser both sat
-- still while the ledger it should fold gained rows. Name that third reason rather
-- than overloading one of the first two.
ALTER TABLE session_raw ADD COLUMN usage_dirty BOOLEAN NOT NULL DEFAULT FALSE;

-- The due scan runs one arm per reason, each terminating at its own LIMIT on its
-- own partial index (see store.DueSessions). This is the usage_dirty arm's index;
-- a corpus with no pending provider usage keeps it empty.
CREATE INDEX idx_session_raw_usage_dirty ON session_raw (session_id)
  WHERE usage_dirty AND parse_retry_at IS NULL;

-- usage_ledger is the event-grain analytics base: every transcript-derived usage
-- row, plus the provider rows that belong to no session. It carries the scope
-- columns (project, user, agent, machine) itself, so a scoped read needs no join
-- and AnalyticsFilter narrows both arms identically (see clauseOn).
--
-- Provider rows WITH a session are deliberately absent: the rebuild already folded
-- them into usage_events for that session, so including them here would count the
-- same spend twice. Between a fetch and the rebuild it triggers, such a row is
-- counted nowhere; that is the ordinary rebuild-on-dirty lag, and it closes on the
-- next drain.
--
-- The unattached arm's project_id and machine are NULL by construction, not by
-- omission. An equality filter never matches NULL, so those rows fall out of every
-- project-scoped and machine-scoped read on their own, which is the correct answer:
-- account-wide subscription spend has no project and no machine.
CREATE VIEW usage_ledger AS
  SELECT ue.session_id,
         ue.model,
         ue.input_tokens,
         ue.output_tokens,
         ue.cache_write_tokens,
         ue.cache_read_tokens,
         ue.reasoning_tokens,
         ue.cost_usd,
         ue.cost_source,
         ue.model_name_public,
         ue.occurred_at,
         s.project_id,
         s.user_id,
         s.agent,
         s.machine
    FROM usage_events ue
    JOIN sessions s ON s.id = ue.session_id
   UNION ALL
  SELECT NULL::bigint,
         p.model,
         p.input_tokens,
         p.output_tokens,
         p.cache_write_tokens,
         p.cache_read_tokens,
         0,
         p.cost_usd,
         p.cost_source,
         p.model_name_public,
         p.occurred_at,
         NULL::bigint,
         p.user_id,
         p.provider,
         NULL::text
    FROM provider_usage_events p
   WHERE p.session_id IS NULL;

-- usage_daily is the day-and-model-grain counterpart, over session_usage_daily
-- rather than the ledger, for the Insights money panels. The same union rule and
-- the same NULL-scope reasoning apply; the unattached arm aggregates on read
-- because it has no per-session rollup to sit in, and its volume is one row per
-- billing request rather than one per token-bearing line.
--
-- Keeping Insights on the same union as the Overview is the point: a dollar that
-- counts in one and not the other is exactly the drift docs/data-aggregation.md
-- exists to prevent.
CREATE VIEW usage_daily AS
  SELECT sud.session_id,
         sud.day,
         sud.model,
         sud.input_tokens,
         sud.output_tokens,
         sud.cache_read_tokens,
         sud.cache_write_tokens,
         sud.cost_usd,
         s.project_id,
         s.user_id,
         s.agent,
         s.machine,
         s.relationship_type,
         -- The cost-of-quality trend gates each session's outcome on its signals
         -- being current, and reads the flag off the view because no sessions row is
         -- in scope (see signalsCurrentOn).
         s.signals_stale
    FROM session_usage_daily sud
    JOIN sessions s ON s.id = sud.session_id
   UNION ALL
  SELECT NULL::bigint,
         (p.occurred_at AT TIME ZONE 'UTC')::date,
         p.model,
         sum(p.input_tokens)::bigint,
         sum(p.output_tokens)::bigint,
         sum(p.cache_read_tokens)::bigint,
         sum(p.cache_write_tokens)::bigint,
         sum(p.cost_usd),
         NULL::bigint,
         p.user_id,
         p.provider,
         NULL::text,
         -- Account-wide usage is nobody's delegated subagent, so it counts in the
         -- subagent-share denominator and never in its numerator.
         ''::text,
         -- Fail closed: an unattached row has no session and so no signal, and the
         -- outcome join finds nothing on its NULL session id either way.
         true
    FROM provider_usage_events p
   WHERE p.session_id IS NULL
   GROUP BY 2, 3, p.user_id, p.provider;
