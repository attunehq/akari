package parse

import (
	"context"
	"testing"

	"github.com/jssblck/akari/internal/server/store"
	"github.com/jssblck/akari/internal/server/storetest"
)

// Two minimal Grok sessions. The parent's transcript spawns a subagent and
// reports the turn's usage, which the Grok CLI aggregates over the child's
// spend; the child's own transcript reports that same spend again as its own
// turn_completed. The child never names its parent, so the parent's
// subagent_spawned claim is the only place the relationship exists.
var (
	grokParentLines = `{"timestamp":1709280001,"method":"session/update","params":{"sessionId":"g-parent","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"map the auth package"},"_meta":{"modelId":"grok-4.6"}}}}
{"timestamp":1709280002,"method":"_x.ai/session/update","params":{"sessionId":"g-parent","update":{"sessionUpdate":"subagent_spawned","subagent_id":"g-child","child_session_id":"g-child","subagent_type":"explore","description":"Map the auth package","model":"grok-4.6"}}}
{"timestamp":1709280010,"method":"session/update","params":{"sessionId":"g-parent","update":{"sessionUpdate":"turn_completed","prompt_id":"p-1","stop_reason":"end_turn","usage":{"modelUsage":{"grok-4.6-build":{"inputTokens":1500,"outputTokens":150,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0}}}}}}
`
	grokChildLines = `{"timestamp":1709280003,"method":"session/update","params":{"sessionId":"g-child","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"child task"},"_meta":{"modelId":"grok-4.6"}}}}
{"timestamp":1709280009,"method":"session/update","params":{"sessionId":"g-child","update":{"sessionUpdate":"turn_completed","prompt_id":"p-1","stop_reason":"end_turn","usage":{"modelUsage":{"grok-4.6-build":{"inputTokens":500,"outputTokens":50,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0}}}}}}
`
)

// grokFamilyState is what the assertions read back for one session.
type grokFamilyState struct {
	parentID    *int64
	rel         string
	parentSrc   string
	usageRows   int
	inputTokens int64
	messages    int
	parserEpoch int
}

func readGrokFamilyState(t *testing.T, st *store.Store, sid int64) grokFamilyState {
	t.Helper()
	var s grokFamilyState
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT s.parent_session_id, s.relationship_type, s.parent_source_id,
		        (SELECT count(*) FROM usage_events u WHERE u.session_id = s.id),
		        s.total_input_tokens, s.message_count, r.parser_epoch
		   FROM sessions s JOIN session_raw r ON r.session_id = s.id
		  WHERE s.id = $1`, sid).
		Scan(&s.parentID, &s.rel, &s.parentSrc, &s.usageRows, &s.inputTokens, &s.messages, &s.parserEpoch); err != nil {
		t.Fatalf("read session %d state: %v", sid, err)
	}
	return s
}

// TestGrokSubagentClaim pins the parent-declared linking and the usage
// suppression it implies, in both ingest orders: a claimed child ends linked
// under its parent with a full transcript but no usage ledger of its own,
// because the parent's turn_completed usage already includes the child's spend.
func TestGrokSubagentClaim(t *testing.T) {
	ingest := func(t *testing.T, st *store.Store, uid, pid int64, src, raw string) int64 {
		t.Helper()
		ctx := context.Background()
		ann, err := st.Announce(ctx, store.AnnounceParams{
			UserID: uid, Agent: "grok", SourceSessionID: src,
			ProjectID: pid, Cwd: "/home/grace/app", Machine: "laptop",
		})
		if err != nil {
			t.Fatalf("announce %q: %v", src, err)
		}
		if _, err := st.AppendChunk(ctx, ann.SessionID, 0, []byte(raw)); err != nil {
			t.Fatalf("append %q: %v", src, err)
		}
		if err := Rebuild(ctx, st, ann.SessionID, "grok"); err != nil {
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

	t.Run("child ingested first", func(t *testing.T) {
		st := storetest.NewStore(t)
		uid, pid := seed(t, st)

		// With no parent in sight the child parses as an ordinary session: its
		// transcript's usage is the only record of that spend.
		child := ingest(t, st, uid, pid, "g-child", grokChildLines)
		cs := readGrokFamilyState(t, st, child)
		if cs.parentID != nil || cs.usageRows != 1 || cs.inputTokens != 500 {
			t.Fatalf("unclaimed child = %+v, want unlinked with its own ledger", cs)
		}

		// The parent's rebuild claims the child: linked, and marked due again so
		// the reparse drops the now double-counted ledger.
		parent := ingest(t, st, uid, pid, "g-parent", grokParentLines)
		cs = readGrokFamilyState(t, st, child)
		if cs.parentID == nil || *cs.parentID != parent || cs.rel != "subagent" || cs.parentSrc != "g-parent" {
			t.Fatalf("claimed child link = %+v, want parent %d", cs, parent)
		}
		if cs.parserEpoch != 0 {
			t.Fatalf("claimed child parser_epoch = %d, want 0 (due for the suppressing reparse)", cs.parserEpoch)
		}

		// The forced reparse (the worker's next drain) keeps the transcript and
		// drops the ledger.
		if err := Rebuild(context.Background(), st, child, "grok"); err != nil {
			t.Fatalf("reparse child: %v", err)
		}
		cs = readGrokFamilyState(t, st, child)
		if cs.usageRows != 0 || cs.inputTokens != 0 {
			t.Errorf("suppressed child ledger = %d rows, %d input tokens, want zero", cs.usageRows, cs.inputTokens)
		}
		if cs.messages == 0 {
			t.Error("suppressed child lost its transcript; only usage should go")
		}
		if cs.parserEpoch != Epoch {
			t.Errorf("reparsed child parser_epoch = %d, want %d", cs.parserEpoch, Epoch)
		}

		// The parent keeps its aggregated ledger and stays top-level.
		ps := readGrokFamilyState(t, st, parent)
		if ps.parentID != nil || ps.usageRows != 1 || ps.inputTokens != 1500 {
			t.Errorf("parent = %+v, want top-level with its aggregate ledger", ps)
		}
	})

	t.Run("parent ingested first", func(t *testing.T) {
		st := storetest.NewStore(t)
		uid, pid := seed(t, st)

		parent := ingest(t, st, uid, pid, "g-parent", grokParentLines)
		child := ingest(t, st, uid, pid, "g-child", grokChildLines)

		// The claim is already visible at the child's first rebuild, so it links
		// itself, never writes a ledger, and needs no second parse: the totals
		// guard leaves its parse stamp current.
		cs := readGrokFamilyState(t, st, child)
		if cs.parentID == nil || *cs.parentID != parent || cs.rel != "subagent" {
			t.Fatalf("child link = %+v, want parent %d", cs, parent)
		}
		if cs.usageRows != 0 || cs.inputTokens != 0 {
			t.Errorf("child ledger = %d rows, %d input tokens, want zero from the first parse", cs.usageRows, cs.inputTokens)
		}
		if cs.messages == 0 {
			t.Error("child transcript missing")
		}
		if cs.parserEpoch != Epoch {
			t.Errorf("child parser_epoch = %d, want %d (no forced reparse)", cs.parserEpoch, Epoch)
		}
	})
}
