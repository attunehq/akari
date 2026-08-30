-- A numeric zero no longer identifies an unpriced model: providers may report a
-- real free turn. Preserve how each cost was obtained, and persist the fail-closed
-- decision that controls whether a model identifier may appear on public overviews.
ALTER TABLE usage_events
  ADD COLUMN cost_source TEXT NOT NULL DEFAULT 'rate_table',
  ADD COLUMN model_name_public BOOLEAN NOT NULL DEFAULT FALSE,
  ADD CONSTRAINT usage_events_cost_source_ck
    CHECK (cost_source IN ('unknown', 'rate_table', 'provider')),
  ADD CONSTRAINT usage_events_unknown_cost_ck
    CHECK (cost_source <> 'unknown' OR cost_usd = 0);

-- Before this migration, every positive cost came from the rate table and zero was
-- the unknown-price sentinel. Epoch 28 rebuilds the exact source and disclosure
-- decision from raw transcripts before public aggregate reads resume.
UPDATE usage_events
   SET cost_source = 'unknown'
 WHERE cost_usd = 0;
