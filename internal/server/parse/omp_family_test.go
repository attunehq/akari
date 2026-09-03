package parse

import (
	"context"
	"testing"

	"github.com/jssblck/akari/internal/server/store"
	"github.com/jssblck/akari/internal/server/storetest"
)

// OMP sessions name their parent on the session header (parentSession), the
// same child-declares-parent direction as OpenCode and Codex, not Grok's
// parent-declares-child claim. OMP's reducer never populates
// Identity.ChildSourceIDs, so a linked OMP session never trips the
// usage-suppression check in store.RebuildSession (that check only fires for a
// session another session's subagent_source_ids claims): every OMP session
// keeps its own usage ledger, exactly like OpenCode.
var (
	ompParentLines = `{"type":"session","version":3,"id":"omp-parent","timestamp":"2026-08-20T09:00:00.000Z","cwd":"/home/grace/app"}
{"type":"message","id":"u1","timestamp":"2026-08-20T09:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"map the auth package"}]}}
{"type":"message","id":"a1","timestamp":"2026-08-20T09:00:05.000Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.6-sol","content":[{"type":"text","text":"mapped"}],"usage":{"input":1500,"output":150,"cacheRead":0,"cacheWrite":0},"stopReason":"stop"}}
`
	// parentSession names the parent by its bare session id, the form
	// ompSourceID passes through unchanged.
	ompChildLinesByID = `{"type":"session","version":3,"id":"omp-child","timestamp":"2026-08-20T09:00:02.000Z","cwd":"/home/grace/app","parentSession":"omp-parent"}
{"type":"message","id":"cu1","timestamp":"2026-08-20T09:00:02.100Z","message":{"role":"user","content":[{"type":"text","text":"map it"}]}}
{"type":"message","id":"ca1","timestamp":"2026-08-20T09:00:03.000Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.6-sol","content":[{"type":"text","text":"auth package mapped"}],"usage":{"input":500,"output":50,"cacheRead":0,"cacheWrite":0},"stopReason":"stop"}}
`
	// parentSession instead names the parent by its session file path. OMP
	// intentionally accepts both; ompSourceID trims the trailing _<id>.jsonl
	// to the same bare id the id form supplies directly, so this must resolve
	// to the same parent as ompChildLinesByID.
	ompChildLinesByPath = `{"type":"session","version":3,"id":"omp-child-path","timestamp":"2026-08-20T09:00:02.000Z","cwd":"/home/grace/app","parentSession":"/home/grace/.omp/agent/sessions/-home-grace-app/2026-08-20T09-00-00-000Z_omp-parent.jsonl"}
{"type":"message","id":"cu2","timestamp":"2026-08-20T09:00:02.100Z","message":{"role":"user","content":[{"type":"text","text":"map it too"}]}}
{"type":"message","id":"ca2","timestamp":"2026-08-20T09:00:03.000Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.6-sol","content":[{"type":"text","text":"auth package mapped again"}],"usage":{"input":500,"output":50,"cacheRead":0,"cacheWrite":0},"stopReason":"stop"}}
`
)

func ompIngest(t *testing.T, st *store.Store, uid, pid int64, src, raw string) int64 {
	t.Helper()
	ctx := context.Background()
	ann, err := st.Announce(ctx, store.AnnounceParams{
		UserID: uid, Agent: "omp", SourceSessionID: src,
		ProjectID: pid, Cwd: "/home/grace/app", Machine: "laptop",
	})
	if err != nil {
		t.Fatalf("announce %q: %v", src, err)
	}
	if _, err := st.AppendChunk(ctx, ann.SessionID, 0, []byte(raw)); err != nil {
		t.Fatalf("append %q: %v", src, err)
	}
	if err := Rebuild(ctx, st, ann.SessionID, "omp"); err != nil {
		t.Fatalf("rebuild %q: %v", src, err)
	}
	return ann.SessionID
}

func ompSeed(t *testing.T, st *store.Store) (uid, pid int64) {
	t.Helper()
	uid = firstUser(t, st)
	pid, err := st.UpsertProject(context.Background(),
		"github.com/jssblck/akari", "github.com", "jssblck", "akari", "akari", "remote")
	if err != nil {
		t.Fatal(err)
	}
	return uid, pid
}

// TestOMPParentLink pins the plain id form of parentSession, in both ingest
// orders. Unlike Grok's parent-declared claim, an OMP parent never aggregates
// its child's usage, so both sessions keep their own ledger after linking.
func TestOMPParentLink(t *testing.T) {
	assertLinked := func(t *testing.T, st *store.Store, parent, child int64) {
		t.Helper()
		cs := readGrokFamilyState(t, st, child)
		if cs.parentID == nil || *cs.parentID != parent || cs.rel != "subagent" || cs.parentSrc != "omp-parent" {
			t.Fatalf("child link = %+v, want parent %d subagent of omp-parent", cs, parent)
		}
		if cs.usageRows != 1 || cs.inputTokens != 500 {
			t.Errorf("child ledger = %d rows, %d input, want its own 500-input usage", cs.usageRows, cs.inputTokens)
		}
		if cs.messages == 0 {
			t.Error("child transcript missing")
		}
		ps := readGrokFamilyState(t, st, parent)
		if ps.parentID != nil || ps.usageRows != 1 || ps.inputTokens != 1500 {
			t.Errorf("parent = %+v, want top-level with its own ledger", ps)
		}
	}

	t.Run("child ingested first", func(t *testing.T) {
		st := storetest.NewStore(t)
		uid, pid := ompSeed(t, st)
		child := ompIngest(t, st, uid, pid, "omp-child", ompChildLinesByID)
		cs := readGrokFamilyState(t, st, child)
		if cs.parentID != nil {
			t.Fatalf("unparented child already linked: %+v", cs)
		}
		if cs.usageRows != 1 || cs.inputTokens != 500 {
			t.Fatalf("unparented child ledger = %+v", cs)
		}
		parent := ompIngest(t, st, uid, pid, "omp-parent", ompParentLines)
		assertLinked(t, st, parent, child)
	})

	t.Run("parent ingested first", func(t *testing.T) {
		st := storetest.NewStore(t)
		uid, pid := ompSeed(t, st)
		parent := ompIngest(t, st, uid, pid, "omp-parent", ompParentLines)
		child := ompIngest(t, st, uid, pid, "omp-child", ompChildLinesByID)
		assertLinked(t, st, parent, child)
	})
}

// TestOMPParentLinkPathForm pins the session-path form of parentSession, the
// dual acceptance that is unique to OMP among the linked agents. The id-form
// test above already exercises both ingest orders and the ledger semantics,
// so this only needs to show the path form resolves to the same parent.
func TestOMPParentLinkPathForm(t *testing.T) {
	st := storetest.NewStore(t)
	uid, pid := ompSeed(t, st)
	parent := ompIngest(t, st, uid, pid, "omp-parent", ompParentLines)
	child := ompIngest(t, st, uid, pid, "omp-child-path", ompChildLinesByPath)

	cs := readGrokFamilyState(t, st, child)
	if cs.parentID == nil || *cs.parentID != parent || cs.rel != "subagent" || cs.parentSrc != "omp-parent" {
		t.Fatalf("path-form child link = %+v, want parent %d subagent of omp-parent", cs, parent)
	}
	if cs.usageRows != 1 || cs.inputTokens != 500 {
		t.Errorf("path-form child ledger = %d rows, %d input, want its own 500-input usage", cs.usageRows, cs.inputTokens)
	}
}
