package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// The live session page renders several representations of one transcript side by
// side: the audit header's cost-bearing rows, the windowed transcript with its tools
// and attachments, and the whole-session shape (the outline rail's bounded-column rows
// plus the tool metadata the flow ribbon colors ticks by). The parse worker replaces a
// session's whole projection in one commit, so any two of these read in separate
// transactions can straddle a rebuild and disagree: old window rows beside a new
// outline, ticks colored by another projection's tools, a work-item total that no
// longer matches the row costs beside it. These reads bundle everything one response
// renders into a single repeatable-read snapshot, so a response is always one
// projection or the other, never a mix.

// SessionShape is the whole-session picture the outline rail and the flow ribbon both
// derive from. Its three parts are read together so a tick is never colored by another
// projection's tools, and they travel together for the same reason.
type SessionShape struct {
	Outline []Message
	Tools   []ToolCallView
	// DupIDs counts tool-call ids appearing on more than one row (a replayed
	// transcript). It summarizes the same tool_calls rows the page renders, so it must
	// not straddle a rebuild against them.
	DupIDs int
}

// SessionSnapshot is everything the live session body renders from one MVCC snapshot.
type SessionSnapshot struct {
	Audit SessionAudit
	Page  TranscriptPage
	// Shape is nil when the read skipped it: a quiet append tick changed no turns, so
	// the fragment carries no shape swap. One nullable pointer rather than three fields
	// that happen to be empty together is what stops a consumer from merging "nothing
	// changed" over a good outline: to use the shape you have to unwrap it first.
	Shape *SessionShape
}

// SessionSnapshotByID loads the full session view: the audit bundle, the transcript's
// tail window, and the whole-session shape. A missing session returns ErrNotFound.
func (s *Store) SessionSnapshotByID(ctx context.Context, sessionID int64) (SessionSnapshot, error) {
	var snap SessionSnapshot
	err := s.snapshotTx(ctx, func(tx pgx.Tx) error {
		var err error
		if snap.Audit, err = s.sessionAudit(ctx, tx, sessionID); err != nil {
			return err
		}
		if snap.Page, err = s.transcriptTail(ctx, tx, sessionID, nil); err != nil {
			return err
		}
		return s.fillShape(ctx, tx, sessionID, &snap)
	})
	if err != nil {
		return SessionSnapshot{}, err
	}
	return snap, nil
}

// SessionAppendByID loads the live append: the audit bundle (the fragment refreshes
// the instruments out-of-band on every tick) and the rows past `after`, plus the
// whole-session shape only when rows actually landed. A quiet tick (raw bytes ahead of
// the rebuild) changes no turns, so it skips both the shape read and the swap it would
// feed. A missing session returns ErrNotFound.
func (s *Store) SessionAppendByID(ctx context.Context, sessionID int64, after int) (SessionSnapshot, error) {
	var snap SessionSnapshot
	err := s.snapshotTx(ctx, func(tx pgx.Tx) error {
		var err error
		if snap.Audit, err = s.sessionAudit(ctx, tx, sessionID); err != nil {
			return err
		}
		if snap.Page, err = s.transcriptAfter(ctx, tx, sessionID, after); err != nil {
			return err
		}
		if len(snap.Page.Msgs) == 0 {
			return nil
		}
		return s.fillShape(ctx, tx, sessionID, &snap)
	})
	if err != nil {
		return SessionSnapshot{}, err
	}
	return snap, nil
}

// fillShape loads the whole-session shape inside the snapshot's transaction: the
// outline read's bounded-column rows and the full tool metadata. The outline and the
// ribbon derive one picture from both, so the two reads must share the snapshot with
// each other (a tick colored by another projection's tools points at the wrong turn)
// as well as with the window beside them.
func (s *Store) fillShape(ctx context.Context, tx pgx.Tx, sessionID int64, snap *SessionSnapshot) error {
	outline, tools, err := s.wholeSessionShape(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	dupIDs, err := s.duplicateCallUIDCount(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	snap.Shape = &SessionShape{Outline: outline, Tools: tools, DupIDs: dupIDs}
	return nil
}

// wholeSessionShape reads the outline rail's bounded-column rows and the full tool
// metadata for one session, inside the caller's transaction, so both surfaces derive
// from the same projection: the authenticated session page (via fillShape) and the
// public session page share this read rather than each carrying their own copy of the
// two queries.
func (s *Store) wholeSessionShape(ctx context.Context, tx pgx.Tx, sessionID int64) ([]Message, []ToolCallView, error) {
	outline, err := s.scanMessages(ctx, tx, sessionID, messagesOutlineQuery, sessionID)
	if err != nil {
		return nil, nil, err
	}
	tools, err := s.scanToolCalls(ctx, tx, toolCallsQuery, sessionID)
	if err != nil {
		return nil, nil, err
	}
	return outline, tools, nil
}
