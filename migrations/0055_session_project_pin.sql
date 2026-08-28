-- A manual assignment of an orphaned session onto a project. project_id is the
-- association the lists and analytics already group by; project_pinned is the
-- bit that keeps a later client announce (still classified orphaned, because
-- the worktree is gone) from moving it back to a local folder, and that a
-- projection rebuild never writes. Once set, only another assignment changes
-- the project.

ALTER TABLE sessions
  ADD COLUMN project_pinned BOOLEAN NOT NULL DEFAULT FALSE;
