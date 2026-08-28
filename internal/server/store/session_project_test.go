package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jssblck/akari/internal/server/store"
	"github.com/jssblck/akari/internal/server/storetest"
)

func TestAssignSessionProjectPinsOrphanAndSurvivesAnnounceAndRebuild(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	u, err := st.Register(ctx, "grace", "hash", "")
	if err != nil {
		t.Fatal(err)
	}
	remoteID, err := st.UpsertProject(ctx, "github.com/grace/akari", "github.com", "grace", "akari", "akari", "remote")
	if err != nil {
		t.Fatal(err)
	}
	orphanID, err := st.UpsertProject(ctx, "local:laptop:/tmp/akari-wt", "laptop", "", "akari-wt", "akari-wt", "orphaned")
	if err != nil {
		t.Fatal(err)
	}

	ann, err := st.Announce(ctx, store.AnnounceParams{
		UserID: u.ID, Agent: "claude", SourceSessionID: "orphan-wt",
		ProjectID: orphanID, Kind: "orphaned",
		Cwd: "/tmp/akari-wt", Machine: "laptop", GitBranch: "feat",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.AssignSessionProject(ctx, ann.SessionID, remoteID)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if got.SessionID != ann.SessionID || got.ProjectID != remoteID || got.PreviousProjectID != orphanID || !got.Pinned {
		t.Fatalf("assign result = %+v, want session %d pinned to %d from %d", got, ann.SessionID, remoteID, orphanID)
	}

	if _, err := st.AnnounceWithProject(ctx, store.AnnounceParams{
		UserID: u.ID, Agent: "claude", SourceSessionID: "orphan-wt",
		Kind: "orphaned", Machine: "laptop", Cwd: "/tmp/akari-wt", GitBranch: "feat",
	}, store.ProjectParams{
		RemoteKey: "local:laptop:/tmp/akari-wt", Host: "laptop",
		Repo: "akari-wt", DisplayName: "akari-wt", Kind: "orphaned",
	}); err != nil {
		t.Fatalf("re-announce orphaned: %v", err)
	}
	assertPinnedProject(t, st, ann.SessionID, remoteID)

	rebuildWith(t, st, ann.SessionID, store.ProjectionDelta{
		Messages: []store.MessageDelta{{Ordinal: 0, Role: "user", Content: "hello"}},
	})
	assertPinnedProject(t, st, ann.SessionID, remoteID)

	otherRemote, err := st.UpsertProject(ctx, "github.com/grace/other", "github.com", "grace", "other", "other", "remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Announce(ctx, store.AnnounceParams{
		UserID: u.ID, Agent: "claude", SourceSessionID: "orphan-wt",
		ProjectID: otherRemote, Kind: "remote",
		Cwd: "/tmp/akari-wt", Machine: "laptop",
	}); err != nil {
		t.Fatalf("remote re-announce: %v", err)
	}
	assertPinnedProject(t, st, ann.SessionID, remoteID)
}

func TestAssignSessionProjectRejectsUnpinnedRemoteAndStandalone(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	u, err := st.Register(ctx, "ada", "hash", "")
	if err != nil {
		t.Fatal(err)
	}
	remoteID, err := st.UpsertProject(ctx, "github.com/ada/akari", "github.com", "ada", "akari", "akari", "remote")
	if err != nil {
		t.Fatal(err)
	}
	standaloneID, err := st.UpsertProject(ctx, "local:box:/home/ada/scratch", "box", "", "scratch", "scratch", "standalone")
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.UpsertProject(ctx, "github.com/ada/other", "github.com", "ada", "other", "other", "remote")
	if err != nil {
		t.Fatal(err)
	}

	remoteSess, err := st.Announce(ctx, store.AnnounceParams{
		UserID: u.ID, Agent: "codex", SourceSessionID: "resolved",
		ProjectID: remoteID, Kind: "remote", Cwd: "/home/ada/akari", Machine: "box",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AssignSessionProject(ctx, remoteSess.SessionID, target); !errors.Is(err, store.ErrNotOrphaned) {
		t.Fatalf("remote assign err = %v, want ErrNotOrphaned", err)
	}

	standaloneSess, err := st.Announce(ctx, store.AnnounceParams{
		UserID: u.ID, Agent: "codex", SourceSessionID: "live-local",
		ProjectID: standaloneID, Kind: "standalone", Cwd: "/home/ada/scratch", Machine: "box",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AssignSessionProject(ctx, standaloneSess.SessionID, target); !errors.Is(err, store.ErrNotOrphaned) {
		t.Fatalf("standalone assign err = %v, want ErrNotOrphaned", err)
	}
}

func TestAssignSessionProjectRehomesPinnedSession(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	u, err := st.Register(ctx, "anna", "hash", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.UpsertProject(ctx, "github.com/anna/one", "github.com", "anna", "one", "one", "remote")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.UpsertProject(ctx, "github.com/anna/two", "github.com", "anna", "two", "two", "remote")
	if err != nil {
		t.Fatal(err)
	}
	orphanID, err := st.UpsertProject(ctx, "local:rig:/tmp/gone", "rig", "", "gone", "gone", "orphaned")
	if err != nil {
		t.Fatal(err)
	}
	ann, err := st.Announce(ctx, store.AnnounceParams{
		UserID: u.ID, Agent: "pi", SourceSessionID: "rehome",
		ProjectID: orphanID, Kind: "orphaned", Cwd: "/tmp/gone", Machine: "rig",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AssignSessionProject(ctx, ann.SessionID, first); err != nil {
		t.Fatal(err)
	}
	got, err := st.AssignSessionProject(ctx, ann.SessionID, second)
	if err != nil {
		t.Fatalf("re-assign: %v", err)
	}
	if got.ProjectID != second || got.PreviousProjectID != first {
		t.Fatalf("re-assign = %+v, want previous %d current %d", got, first, second)
	}
	assertPinnedProject(t, st, ann.SessionID, second)
}

func TestAssignSessionProjectMissingIDs(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	u, err := st.Register(ctx, "katherine", "hash", "")
	if err != nil {
		t.Fatal(err)
	}
	orphanID, err := st.UpsertProject(ctx, "local:box:/tmp/x", "box", "", "x", "x", "orphaned")
	if err != nil {
		t.Fatal(err)
	}
	ann, err := st.Announce(ctx, store.AnnounceParams{
		UserID: u.ID, Agent: "claude", SourceSessionID: "missing-target",
		ProjectID: orphanID, Kind: "orphaned", Cwd: "/tmp/x", Machine: "box",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.AssignSessionProject(ctx, 999999, orphanID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing session err = %v, want ErrNotFound", err)
	}
	if _, err := st.AssignSessionProject(ctx, ann.SessionID, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project err = %v, want ErrNotFound", err)
	}
}

func assertPinnedProject(t *testing.T, st *store.Store, sessionID, wantProject int64) {
	t.Helper()
	var projectID int64
	var pinned bool
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT project_id, project_pinned FROM sessions WHERE id = $1`, sessionID).
		Scan(&projectID, &pinned); err != nil {
		t.Fatalf("read pinned project: %v", err)
	}
	if projectID != wantProject || !pinned {
		t.Fatalf("session %d project_id=%d pinned=%v, want project %d pinned", sessionID, projectID, pinned, wantProject)
	}
}
