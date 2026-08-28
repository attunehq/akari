-- Admit the OpenCode session format. The agent column's CHECK is the announce
-- path's hard gate; parser.Agents and the OpenAPI enum grew the same name in
-- this change.

ALTER TABLE sessions DROP CONSTRAINT sessions_agent_check;
ALTER TABLE sessions ADD CONSTRAINT sessions_agent_check
  CHECK (agent IN ('claude', 'codex', 'pi', 'cursor', 'grok', 'opencode'));
