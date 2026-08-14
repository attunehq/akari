-- Admit the Cursor CLI and Grok CLI session formats. The agent column's CHECK
-- is the announce path's hard gate; parser.Agents and the OpenAPI enum grew the
-- same two names in the same change.

ALTER TABLE sessions DROP CONSTRAINT sessions_agent_check;
ALTER TABLE sessions ADD CONSTRAINT sessions_agent_check
  CHECK (agent IN ('claude', 'codex', 'pi', 'cursor', 'grok'));

-- Grok parenthood is declared by the parent, not the child: a subagent's own
-- directory carries no parent reference, but the parent's updates.jsonl logs
-- subagent_spawned with the child's session id. The rebuild stores the parsed
-- set here, and link-up/adoption treat a claim exactly like a child-declared
-- parent_source_id. A parent that claims children also aggregates their token
-- spend into its own turn_completed usage, so a claimed session's rebuild
-- writes no usage ledger of its own (see RebuildSession).
ALTER TABLE sessions
  ADD COLUMN subagent_source_ids TEXT[] NOT NULL DEFAULT '{}';

-- The child-side probes (a rebuilt session looking for a parent that claims it,
-- and the usage-suppression check) match with @>, which this serves; rows with
-- an empty array cost the index nothing.
CREATE INDEX idx_sessions_subagent_source_ids
    ON sessions USING gin (subagent_source_ids);
