package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AssignSessionProjectResult is the session's project after a successful pin.
type AssignSessionProjectResult struct {
	SessionID         int64
	ProjectID         int64
	PreviousProjectID int64
	Pinned            bool
}

// AssignSessionProject pins sessionID onto projectID. The session must currently
// live in an orphaned project, or already be pinned (a later re-home). The pin
// survives a projection rebuild (rebuild never writes project_id) and a later
// client announce that still classifies the session as local: keepAttributionTx
// holds the assigned project and announceIntoProjectTx will not overwrite a
// pinned row.
func (s *Store) AssignSessionProject(ctx context.Context, sessionID, projectID int64) (AssignSessionProjectResult, error) {
	var out AssignSessionProjectResult
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var p AnnounceParams
		err := tx.QueryRow(ctx,
			`SELECT user_id, agent, source_session_id
			   FROM sessions WHERE id = $1`, sessionID).
			Scan(&p.UserID, &p.Agent, &p.SourceSessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read session %d identity for project assign: %w", sessionID, err)
		}
		if err := lockAnnounceIdentityTx(ctx, tx, p); err != nil {
			return err
		}

		var currentProject int64
		var kind string
		var pinned bool
		err = tx.QueryRow(ctx,
			`SELECT s.project_id, pr.kind, s.project_pinned
			   FROM sessions s JOIN projects pr ON pr.id = s.project_id
			  WHERE s.id = $1`, sessionID).
			Scan(&currentProject, &kind, &pinned)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read session %d project for assign: %w", sessionID, err)
		}
		if kind != "orphaned" && !pinned {
			return ErrNotOrphaned
		}

		var targetExists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1)`, projectID).
			Scan(&targetExists); err != nil {
			return fmt.Errorf("check target project %d: %w", projectID, err)
		}
		if !targetExists {
			return ErrNotFound
		}

		if _, err := tx.Exec(ctx,
			`UPDATE sessions
			    SET project_id = $2, project_pinned = TRUE, updated_at = now()
			  WHERE id = $1`, sessionID, projectID); err != nil {
			return fmt.Errorf("pin session %d to project %d: %w", sessionID, projectID, err)
		}
		out = AssignSessionProjectResult{
			SessionID: sessionID, ProjectID: projectID,
			PreviousProjectID: currentProject, Pinned: true,
		}
		return nil
	})
	return out, err
}
