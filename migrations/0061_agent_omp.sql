-- Admit OMP as a native session format. parser.Agents owns the application
-- enum; this widens the database gate to the same set without changing any
-- existing projection.
ALTER TABLE sessions DROP CONSTRAINT sessions_agent_check;
ALTER TABLE sessions ADD CONSTRAINT sessions_agent_check
  CHECK (agent IN ('claude', 'codex', 'pi', 'omp', 'cursor', 'grok', 'opencode'));
