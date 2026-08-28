package parse

import (
	"context"
	"testing"

	"github.com/jssblck/akari/internal/server/store"
	"github.com/jssblck/akari/internal/server/storetest"
)

// OpenCode subagents name their parent on the session header (parent_id), the
// same direction as Codex. Each session keeps its own usage ledger: the parent
// does not fold the child's spend into its turn totals.
var (
	opencodeParentLines = `{"type":"session","id":"oc-parent","directory":"/home/grace/app","title":"map auth","slug":"mighty-lagoon","agent":"build","time_created":1709294400000,"time_updated":1709294500000}
{"type":"message","id":"msg_u","time_created":1709294401000,"data":{"role":"user","time":{"created":1709294401000}},"parts":[{"id":"prt_u","time_created":1709294401100,"data":{"type":"text","text":"map the auth package"}}]}
{"type":"message","id":"msg_a","time_created":1709294402000,"data":{"role":"assistant","modelID":"grok-4.6","time":{"created":1709294402000,"completed":1709294403000},"tokens":{"input":1500,"output":150,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_t","time_created":1709294402100,"data":{"type":"tool","tool":"task","callID":"c1","metadata":{"sessionId":"oc-child"},"state":{"status":"completed","input":{"description":"Map auth","subagent_type":"explore"},"output":"done"}}},{"id":"prt_x","time_created":1709294402200,"data":{"type":"text","text":"mapped"}}]}
`
	opencodeChildLines = `{"type":"session","id":"oc-child","directory":"/home/grace/app","parent_id":"oc-parent","title":"Map auth (@explore subagent)","slug":"playful-eagle","agent":"explore","time_created":1709294402100,"time_updated":1709294404000}
{"type":"message","id":"msg_cu","time_created":1709294402200,"data":{"role":"user","time":{"created":1709294402200},"agent":"explore"},"parts":[{"id":"prt_cu","time_created":1709294402210,"data":{"type":"text","text":"map it"}}]}
{"type":"message","id":"msg_ca","time_created":1709294402300,"data":{"role":"assistant","modelID":"grok-4.6","time":{"created":1709294402300,"completed":1709294404000},"tokens":{"input":500,"output":50,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_cx","time_created":1709294402400,"data":{"type":"text","text":"auth package mapped"}}]}
`
)

func TestOpenCodeSubagentParentLink(t *testing.T) {
	ingest := func(t *testing.T, st *store.Store, uid, pid int64, src, raw string) int64 {
		t.Helper()
		ctx := context.Background()
		ann, err := st.Announce(ctx, store.AnnounceParams{
			UserID: uid, Agent: "opencode", SourceSessionID: src,
			ProjectID: pid, Cwd: "/home/grace/app", Machine: "laptop",
		})
		if err != nil {
			t.Fatalf("announce %q: %v", src, err)
		}
		if _, err := st.AppendChunk(ctx, ann.SessionID, 0, []byte(raw)); err != nil {
			t.Fatalf("append %q: %v", src, err)
		}
		if err := Rebuild(ctx, st, ann.SessionID, "opencode"); err != nil {
			t.Fatalf("rebuild %q: %v", src, err)
		}
		return ann.SessionID
	}
	seed := func(t *testing.T, st *store.Store) (uid, pid int64) {
		t.Helper()
		uid = firstUser(t, st)
		pid, err := st.UpsertProject(context.Background(),
			"github.com/jssblck/akari", "github.com", "jssblck", "akari", "akari", "remote")
		if err != nil {
			t.Fatal(err)
		}
		return uid, pid
	}
	assertLinked := func(t *testing.T, st *store.Store, parent, child int64) {
		t.Helper()
		cs := readGrokFamilyState(t, st, child)
		if cs.parentID == nil || *cs.parentID != parent || cs.rel != "subagent" || cs.parentSrc != "oc-parent" {
			t.Fatalf("child link = %+v, want parent %d subagent of oc-parent", cs, parent)
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
		uid, pid := seed(t, st)
		child := ingest(t, st, uid, pid, "oc-child", opencodeChildLines)
		cs := readGrokFamilyState(t, st, child)
		if cs.parentID != nil {
			t.Fatalf("unparented child already linked: %+v", cs)
		}
		if cs.usageRows != 1 || cs.inputTokens != 500 {
			t.Fatalf("unparented child ledger = %+v", cs)
		}
		parent := ingest(t, st, uid, pid, "oc-parent", opencodeParentLines)
		assertLinked(t, st, parent, child)
	})

	t.Run("parent ingested first", func(t *testing.T) {
		st := storetest.NewStore(t)
		uid, pid := seed(t, st)
		parent := ingest(t, st, uid, pid, "oc-parent", opencodeParentLines)
		child := ingest(t, st, uid, pid, "oc-child", opencodeChildLines)
		assertLinked(t, st, parent, child)
	})
}
