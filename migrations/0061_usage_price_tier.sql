-- Record each usage event's price tier.
--
-- Some models charge a higher rate for a whole request once its prompt reaches a
-- size (GPT-5.6 over 272K prompt tokens, Claude Haiku 5.5 over 100K; the cutoff is
-- per model, see internal/pricing). That tier is a fact about one request: two short
-- requests can sum past a cutoff without either belonging to the tier, so a day's
-- summed tokens cannot recover it. The rebuild classifies each event against the
-- pricing table and stores the answer here, and the daily rollup groups by it, so the
-- cache-savings trend can price each tier's cached volume at its own rate while still
-- reading the cheap (session, day, model) rollup instead of grouping the ledger.
--
-- Add-only and safe during a rolling deploy. Each column is NOT NULL with a constant
-- default, which PostgreSQL records in the catalog without rewriting the table, so
-- the ALTERs hold their lock only for the catalog update. Existing rows read FALSE,
-- the short tier, which is exactly how they were priced before. A binary that
-- predates this column names its columns on every insert, so its rebuilds keep
-- working and write FALSE; parse.Epoch moves in the same release, so every session
-- (including any an old binary rebuilds mid-rollout, which stamps the old epoch)
-- stays due until a new binary re-derives it with the real tier. No backfill: the
-- tier needs the Go pricing table, and the epoch rebuild is the one mechanism that
-- re-derives the corpus.
ALTER TABLE usage_events ADD COLUMN long_context BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE session_usage_daily ADD COLUMN long_context BOOLEAN NOT NULL DEFAULT FALSE;

-- The union views gain the column at the end, the only change CREATE OR REPLACE VIEW
-- permits, so every existing column keeps its position and type and a reader that
-- names its columns (every query akari runs) is unaffected. The definitions are
-- otherwise 0060's verbatim; see that migration for why each arm looks as it does.
--
-- Provider rows (the unattached arm, and attached rows once the rebuild folds them)
-- always carry FALSE. A Cursor billing event covers a whole agent run rather than one
-- model request, so its token sums cannot be classified any more than a day's can,
-- and the vendor's reported cost already reflects whatever tier it billed.
CREATE OR REPLACE VIEW usage_ledger AS
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
         s.machine,
         ue.long_context
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
         NULL::text,
         false
    FROM provider_usage_events p
   WHERE p.session_id IS NULL;

CREATE OR REPLACE VIEW usage_daily AS
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
         s.signals_stale,
         sud.long_context
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
         ''::text,
         true,
         false
    FROM provider_usage_events p
   WHERE p.session_id IS NULL
   GROUP BY 2, 3, p.user_id, p.provider;
