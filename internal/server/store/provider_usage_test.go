package store_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/server/store"
	"github.com/jssblck/akari/internal/server/storetest"
)

// Provider-reported usage is akari's answer to an agent whose transcript records
// no model, no tokens, and no cost. Cursor is that agent: its billing feed is the
// only place those figures exist, and it reports them per account rather than per
// machine or per session.
//
// Two properties carry the whole design, and these tests pin both.
//
// An event that names a session akari has must reach that session's ledger through
// the rebuild, so every session-keyed invariant in docs/data-aggregation.md keeps
// holding. An event that names no session akari has must still count in fleet-scope
// analytics, because it is real subscription spend, and must count in no project or
// machine scope, because it has neither.

// providerEvent builds one fetched event. Callers vary only what a test is about;
// everything else stays fixed so a failure names the field that moved.
func providerEvent(key, conversation, model string, at time.Time, in, out, cr int, cost float64) store.ProviderUsageEvent {
	return store.ProviderUsageEvent{
		EventKey:        key,
		ConversationID:  conversation,
		Model:           model,
		Input:           in,
		Output:          out,
		CacheRead:       cr,
		CostUSD:         cost,
		CostSource:      store.CostSourceProvider,
		ModelNamePublic: false,
		OccurredAt:      at,
	}
}

// TestProviderUsageFoldsIntoSessionLedger pins the attributed half: a Cursor
// session that parsed no usage at all ends up with the vendor's tokens and cost in
// its ledger, in its rollups, and in its daily rollup, and the load-bearing
// rollup-equals-ledger invariant survives the addition.
func TestProviderUsageFoldsIntoSessionLedger(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	user, err := st.Register(ctx, "grace", "h", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	proj, err := st.UpsertProject(ctx, "github.com/ada/engine", "github.com", "ada", "engine", "engine", "remote")
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	// A Cursor transcript projects messages and tool calls but never a usage row,
	// which is exactly why this data has to come from somewhere else.
	const conversation = "1c9000b1-6ffa-406b-8a2c-1992e2912b66"
	msgs := []store.MessageDelta{
		{Ordinal: 0, Role: "user", Content: "refactor the parser"},
		{Ordinal: 1, Role: "assistant", Content: "done"},
	}
	sid := ingestOnly(t, st, user.ID, proj, "cursor", conversation, msgs, nil)

	if _, _, _, _, cost, rows := ledgerTotals(t, st, sid); rows != 0 || cost != 0 {
		t.Fatalf("cursor session before collection: %d ledger rows costing %v, want none", rows, cost)
	}

	at := time.Date(2026, 8, 17, 20, 17, 0, 0, time.UTC)
	res, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "403704963", []store.ProviderUsageEvent{
		providerEvent("evt-a", conversation, "cursor-grok-4.6-high-fast", at, 3264, 812, 540800, 0.2818),
		providerEvent("evt-b", conversation, "cursor-grok-4.6-high-fast", at.Add(time.Minute), 3831, 1971, 626304, 0.3326),
	})
	if err != nil {
		t.Fatalf("record provider usage: %v", err)
	}
	if res.Inserted != 2 {
		t.Fatalf("inserted %d events, want 2", res.Inserted)
	}
	if res.SessionsMarked != 1 {
		t.Fatalf("marked %d sessions for rebuild, want 1", res.SessionsMarked)
	}

	// Nothing has moved yet: the rebuild is the only writer of the ledger, so the
	// events are stored and pending, not counted.
	if _, _, _, _, _, rows := ledgerTotals(t, st, sid); rows != 0 {
		t.Fatalf("ledger has %d rows before the rebuild, want 0: provider usage must not bypass the rebuild", rows)
	}
	assertDue(t, st, sid, true, "after provider usage landed")

	rebuildWith(t, st, sid, store.ProjectionDelta{Messages: msgs})

	in, out, cr, cw, cost, rows := ledgerTotals(t, st, sid)
	if rows != 2 {
		t.Fatalf("ledger has %d rows after the rebuild, want 2", rows)
	}
	if in != 3264+3831 || out != 812+1971 || cr != 540800+626304 || cw != 0 {
		t.Errorf("ledger tokens in=%d out=%d cr=%d cw=%d, want in=%d out=%d cr=%d cw=0",
			in, out, cr, cw, 3264+3831, 812+1971, 540800+626304)
	}
	if math.Abs(cost-(0.2818+0.3326)) > 1e-9 {
		t.Errorf("ledger cost %v, want %v", cost, 0.2818+0.3326)
	}
	assertRollupMatchesLedger(t, st, sid, "after folding provider usage")
	assertDue(t, st, sid, false, "after the rebuild folded the provider usage")

	// The daily rollup is derived in the same transaction, so the Insights money
	// panels see the spend without a second pass.
	var dailyCost float64
	if err := st.Pool.QueryRow(ctx,
		`SELECT coalesce(sum(cost_usd), 0) FROM session_usage_daily WHERE session_id = $1`, sid).
		Scan(&dailyCost); err != nil {
		t.Fatalf("read daily rollup: %v", err)
	}
	if math.Abs(dailyCost-(0.2818+0.3326)) > 1e-9 {
		t.Errorf("daily rollup cost %v, want %v", dailyCost, 0.2818+0.3326)
	}

	// A repeat rebuild must not double count: the fold reads the same stored rows
	// and the ledger is rewritten, not appended to. This is the epoch-rollout path.
	rebuildWith(t, st, sid, store.ProjectionDelta{Messages: msgs})
	if _, _, _, _, cost2, rows2 := ledgerTotals(t, st, sid); rows2 != 2 || math.Abs(cost2-cost) > 1e-9 {
		t.Errorf("repeat rebuild produced %d rows costing %v, want %d costing %v", rows2, cost2, rows, cost)
	}
	assertRollupMatchesLedger(t, st, sid, "after a repeat rebuild")
}

// TestProviderUsageIsIdempotent pins the property every other machine depends on:
// the feed is account-wide, so a second machine collecting the same window, or the
// same machine re-fetching its overlapping resume window, must converge on one
// stored copy rather than double the spend.
func TestProviderUsageIsIdempotent(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	user, err := st.Register(ctx, "ada", "h", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	batch := []store.ProviderUsageEvent{
		providerEvent("evt-a", "conv-1", "claude-opus-5-low", at, 10, 20, 30, 1.5),
		providerEvent("evt-b", "conv-1", "claude-opus-5-low", at.Add(time.Minute), 11, 21, 31, 2.5),
	}

	first, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct", batch)
	if err != nil {
		t.Fatalf("first collection: %v", err)
	}
	if first.Inserted != 2 {
		t.Fatalf("first collection inserted %d, want 2", first.Inserted)
	}

	// The same window again, plus one new event, as an overlapping resume produces.
	second, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct", append(batch,
		providerEvent("evt-c", "conv-1", "claude-opus-5-low", at.Add(2*time.Minute), 12, 22, 32, 3.5)))
	if err != nil {
		t.Fatalf("second collection: %v", err)
	}
	if second.Inserted != 1 {
		t.Errorf("overlapping collection inserted %d, want only the 1 new event", second.Inserted)
	}

	var stored int
	var cost float64
	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*), coalesce(sum(cost_usd), 0) FROM provider_usage_events WHERE user_id = $1`, user.ID).
		Scan(&stored, &cost); err != nil {
		t.Fatalf("count stored events: %v", err)
	}
	if stored != 3 || math.Abs(cost-7.5) > 1e-9 {
		t.Errorf("stored %d events costing %v, want 3 costing 7.5", stored, cost)
	}

	// A repeat within one batch is a collision against the same index, which the
	// batch must resolve before the insert rather than fail on.
	dup, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct2", []store.ProviderUsageEvent{
		providerEvent("evt-x", "conv-2", "gpt-5.2", at, 1, 2, 3, 0.25),
		providerEvent("evt-x", "conv-2", "gpt-5.2", at, 1, 2, 3, 0.25),
	})
	if err != nil {
		t.Fatalf("batch with a repeated key: %v", err)
	}
	if dup.Inserted != 1 {
		t.Errorf("batch with a repeated key inserted %d, want 1", dup.Inserted)
	}
}

// TestProviderUsageResolvesWhenSessionArrivesLater pins the ordering that the
// account grain forces: the vendor bills an account and akari discovers a
// transcript, so either can arrive first, and whichever is second performs the
// join.
func TestProviderUsageResolvesWhenSessionArrivesLater(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	user, err := st.Register(ctx, "anna", "h", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	proj, err := st.UpsertProject(ctx, "github.com/winlock/orbits", "github.com", "winlock", "orbits", "orbits", "remote")
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	const conversation = "b80773cf-83f2-40f2-b71d-8338f93ae58a"
	at := time.Date(2026, 8, 26, 5, 51, 0, 0, time.UTC)
	if _, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct", []store.ProviderUsageEvent{
		providerEvent("evt-a", conversation, "cursor-grok-4.6-high-fast", at, 100, 200, 300, 17.62),
	}); err != nil {
		t.Fatalf("collect before the session exists: %v", err)
	}
	assertUnattachedCount(t, st, user.ID, 1, "before the session was announced")

	// The session shows up afterwards, which is the ordinary case for a machine that
	// collected usage before it synced its transcripts.
	msgs := []store.MessageDelta{{Ordinal: 0, Role: "user", Content: "ship it"}}
	sid := ingestOnly(t, st, user.ID, proj, "cursor", conversation, msgs, nil)
	assertUnattachedCount(t, st, user.ID, 0, "after the session was announced")

	rebuildWith(t, st, sid, store.ProjectionDelta{Messages: msgs})
	if _, _, _, _, cost, rows := ledgerTotals(t, st, sid); rows != 1 || math.Abs(cost-17.62) > 1e-9 {
		t.Errorf("late-announced session has %d ledger rows costing %v, want 1 costing 17.62", rows, cost)
	}
	assertRollupMatchesLedger(t, st, sid, "after a late announce claimed its usage")
}

// TestUnattachedProviderUsageCountsFleetWideOnly pins the unattributed half, which
// is where most of a Cursor account's spend lives: IDE chats, cloud agents, and the
// vendor's own bots write no transcript akari can ingest.
//
// Such spend is real and must appear in the fleet totals, and it belongs to no
// project and no machine, so it must appear in neither of those scopes. The two
// halves together are what make the Overview honest without inventing a project for
// usage that has none.
func TestUnattachedProviderUsageCountsFleetWideOnly(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	user, err := st.Register(ctx, "hopper", "h", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	proj, err := st.UpsertProject(ctx, "github.com/hopper/compiler", "github.com", "hopper", "compiler", "compiler", "remote")
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	at := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	sid := ingestOnly(t, st, user.ID, proj, "claude", "claude-1",
		[]store.MessageDelta{{Ordinal: 0, Role: "user", Content: "hello"}},
		[]usageRow{{Model: "claude-opus-5", In: 100, Out: 50, At: at, Cost: 4, SourceOffset: 0}})
	_ = sid

	// A cloud agent's spend: a conversation akari holds no transcript for.
	if _, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct", []store.ProviderUsageEvent{
		providerEvent("evt-cloud", "bc-5f14d3f7-ed49-5b0d-b22e-db209fd711e1", "cursor-grok-4.6-high", at, 3264, 812, 540800, 6.94),
	}); err != nil {
		t.Fatalf("collect unattached usage: %v", err)
	}

	window := store.AnalyticsFilter{Since: at.Add(-time.Hour), Until: at.Add(time.Hour)}

	fleet, err := st.Analytics(ctx, window)
	if err != nil {
		t.Fatalf("fleet analytics: %v", err)
	}
	if math.Abs(fleet.TotalCost-10.94) > 1e-9 {
		t.Errorf("fleet cost %v, want 10.94 (4 parsed + 6.94 unattached)", fleet.TotalCost)
	}
	assertBreakdown(t, fleet.Models, "cursor-grok-4.6-high", 6.94)
	assertBreakdown(t, fleet.Agents, "cursor", 6.94)
	// The headline is summed from the by-agent split, so the two must agree by
	// construction even with a second arm in the base (see Store.Analytics).
	var agentSum float64
	for _, a := range fleet.Agents {
		agentSum += a.CostUSD
	}
	if math.Abs(agentSum-fleet.TotalCost) > 1e-9 {
		t.Errorf("by-agent split sums to %v but the headline is %v", agentSum, fleet.TotalCost)
	}
	// Unattached usage names no session, so it must not inflate the session count.
	if fleet.Sessions != 1 {
		t.Errorf("fleet session count %d, want 1: unattached usage has no session to count", fleet.Sessions)
	}

	scoped := window
	scoped.ProjectID = proj
	project, err := st.Analytics(ctx, scoped)
	if err != nil {
		t.Fatalf("project analytics: %v", err)
	}
	if math.Abs(project.TotalCost-4) > 1e-9 {
		t.Errorf("project cost %v, want 4: account-wide usage belongs to no project", project.TotalCost)
	}

	machined := window
	machined.Machine = "box"
	byMachine, err := st.Analytics(ctx, machined)
	if err != nil {
		t.Fatalf("machine analytics: %v", err)
	}
	if math.Abs(byMachine.TotalCost-4) > 1e-9 {
		t.Errorf("machine cost %v, want 4: account-wide usage belongs to no machine", byMachine.TotalCost)
	}

	// Insights reads the day-and-model rollup rather than the event ledger, so it is
	// a second base over the same spend. A dollar that counts on the Overview and not
	// here is exactly the drift docs/data-aggregation.md exists to prevent, so the
	// unattached arm has to reach both.
	trended := window
	trended.Bucket = "day"
	ins, err := st.Insights(ctx, trended, store.AllInsightsPanels)
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	if ins.Trends == nil {
		t.Fatal("insights returned no trends for a bucketed filter")
	}
	var scatter float64
	var sawCursorModel bool
	for _, m := range ins.Trends.ModelCost {
		scatter += m.CostUSD
		if m.Model == "cursor-grok-4.6-high" {
			sawCursorModel = true
		}
	}
	if !sawCursorModel {
		t.Error("the model-cost scatter has no cursor-grok-4.6-high row: unattached spend is missing from Insights")
	}
	if math.Abs(scatter-fleet.TotalCost) > 1e-9 {
		t.Errorf("model-cost scatter sums to %v but the overview headline is %v: the two bases disagree", scatter, fleet.TotalCost)
	}
}

// TestAttachedProviderUsageCountsExactlyOnce is the counterpart to the unattached
// test and guards the one way this design could double count: an attached event is
// in the ledger through the rebuild AND in provider_usage_events, so the fleet base
// must read exactly one of the two.
func TestAttachedProviderUsageCountsExactlyOnce(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	user, err := st.Register(ctx, "grace", "h", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	proj, err := st.UpsertProject(ctx, "github.com/ada/engine", "github.com", "ada", "engine", "engine", "remote")
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	const conversation = "6043616f-5686-4624-a6e8-8e09190ce15b"
	at := time.Date(2026, 8, 17, 19, 8, 0, 0, time.UTC)
	msgs := []store.MessageDelta{{Ordinal: 0, Role: "user", Content: "go"}}
	sid := ingestOnly(t, st, user.ID, proj, "cursor", conversation, msgs, nil)
	if _, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct", []store.ProviderUsageEvent{
		providerEvent("evt-a", conversation, "cursor-grok-4.6-high-fast", at, 10, 20, 30, 12.46),
	}); err != nil {
		t.Fatalf("collect: %v", err)
	}
	rebuildWith(t, st, sid, store.ProjectionDelta{Messages: msgs})

	window := store.AnalyticsFilter{Since: at.Add(-time.Hour), Until: at.Add(time.Hour)}
	fleet, err := st.Analytics(ctx, window)
	if err != nil {
		t.Fatalf("fleet analytics: %v", err)
	}
	if math.Abs(fleet.TotalCost-12.46) > 1e-9 {
		t.Errorf("fleet cost %v, want 12.46: an attached event must be counted once, not twice", fleet.TotalCost)
	}
	// It is attached, so unlike the unattached case it DOES carry its session's
	// project and belongs in the project scope.
	scoped := window
	scoped.ProjectID = proj
	project, err := st.Analytics(ctx, scoped)
	if err != nil {
		t.Fatalf("project analytics: %v", err)
	}
	if math.Abs(project.TotalCost-12.46) > 1e-9 {
		t.Errorf("project cost %v, want 12.46: an attached event carries its session's project", project.TotalCost)
	}
}

// TestProviderUsageResolvesAgainstThePathDerivedSourceID pins the shape a real
// Cursor session actually has. Its transcript records no id, so akari derives the
// source id from the file's location and stores a path; the vendor reports the bare
// conversation id, which is that path's last segment. Matching the whole source id
// against it resolves nothing at all, which is what shipping this without the test
// would have done.
func TestProviderUsageResolvesAgainstThePathDerivedSourceID(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	user, err := st.Register(ctx, "grace", "h", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	proj, err := st.UpsertProject(ctx, "github.com/ada/engine", "github.com", "ada", "engine", "engine", "remote")
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	// The two real layouts: a main transcript and a subagent's, both under an
	// encoded project directory. Cursor bills each under its own conversation id.
	const parentConv = "b80773cf-83f2-40f2-b71d-8338f93ae58a"
	const childConv = "add03671-89cb-47be-a2dc-2cce4c8ba383"
	parentSrc := "Users-jess-cursor-worktrees-sorted-7g29/agent-transcripts/" + parentConv + "/" + parentConv
	childSrc := "Users-jess-cursor-worktrees-sorted-7g29/agent-transcripts/" + parentConv + "/subagents/" + childConv

	msgs := []store.MessageDelta{{Ordinal: 0, Role: "user", Content: "go"}}
	parent := ingestOnly(t, st, user.ID, proj, "cursor", parentSrc, msgs, nil)
	child := ingestOnly(t, st, user.ID, proj, "cursor", childSrc, msgs, nil)

	at := time.Date(2026, 8, 26, 5, 51, 0, 0, time.UTC)
	if _, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct", []store.ProviderUsageEvent{
		providerEvent("evt-parent", parentConv, "cursor-grok-4.6-high-fast", at, 10, 20, 30, 17.62),
		providerEvent("evt-child", childConv, "composer-2.5-fast", at, 1, 2, 3, 1.20),
	}); err != nil {
		t.Fatalf("collect: %v", err)
	}

	rebuildWith(t, st, parent, store.ProjectionDelta{Messages: msgs})
	rebuildWith(t, st, child, store.ProjectionDelta{Messages: msgs})

	if _, _, _, _, cost, rows := ledgerTotals(t, st, parent); rows != 1 || math.Abs(cost-17.62) > 1e-9 {
		t.Errorf("parent has %d ledger rows costing %v, want 1 costing 17.62", rows, cost)
	}
	if _, _, _, _, cost, rows := ledgerTotals(t, st, child); rows != 1 || math.Abs(cost-1.20) > 1e-9 {
		t.Errorf("subagent has %d ledger rows costing %v, want 1 costing 1.20", rows, cost)
	}
	assertUnattachedCount(t, st, user.ID, 0, "after both transcripts were announced")
}

// One agent run observed from two worktrees ingests as two sessions naming the same
// conversation. The spend happened once, so it must land on exactly one of them:
// attaching to both would double the fleet total.
func TestOneConversationIngestedTwiceIsCountedOnce(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	user, err := st.Register(ctx, "ada", "h", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	proj, err := st.UpsertProject(ctx, "github.com/ada/engine", "github.com", "ada", "engine", "engine", "remote")
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	const conv = "ba28a390-479f-4d44-93f1-b13b3a3d1d45"
	msgs := []store.MessageDelta{{Ordinal: 0, Role: "user", Content: "go"}}
	first := ingestOnly(t, st, user.ID, proj, "cursor", "worktree-a/agent-transcripts/"+conv+"/"+conv, msgs, nil)
	second := ingestOnly(t, st, user.ID, proj, "cursor", "worktree-b/agent-transcripts/"+conv+"/"+conv, msgs, nil)

	at := time.Date(2026, 8, 15, 2, 28, 0, 0, time.UTC)
	if _, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct", []store.ProviderUsageEvent{
		providerEvent("evt-a", conv, "cursor-grok-4.6-high-fast", at, 10, 20, 30, 9.13),
	}); err != nil {
		t.Fatalf("collect: %v", err)
	}
	rebuildWith(t, st, first, store.ProjectionDelta{Messages: msgs})
	rebuildWith(t, st, second, store.ProjectionDelta{Messages: msgs})

	_, _, _, _, firstCost, firstRows := ledgerTotals(t, st, first)
	_, _, _, _, secondCost, secondRows := ledgerTotals(t, st, second)
	if firstRows+secondRows != 1 {
		t.Errorf("the conversation's one event landed on %d sessions, want exactly 1", firstRows+secondRows)
	}
	if math.Abs(firstCost+secondCost-9.13) > 1e-9 {
		t.Errorf("combined cost %v, want 9.13 counted once", firstCost+secondCost)
	}
	// The oldest session wins, so the choice is the same on every machine and on
	// every re-collection rather than whichever row the planner returned.
	if firstRows != 1 {
		t.Errorf("the event landed on the newer session; the oldest must win for determinism")
	}
}

// TestProviderUsageWatermarkResumes pins the client's resume point: the server is
// the only durable record of what has been collected, so the watermark must name
// the newest stored event and nothing else.
func TestProviderUsageWatermarkResumes(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	user, err := st.Register(ctx, "ada", "h", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	at, err := st.ProviderUsageWatermark(ctx, user.ID, "cursor", "acct")
	if err != nil {
		t.Fatalf("watermark before any collection: %v", err)
	}
	if !at.IsZero() {
		t.Errorf("watermark %v before any collection, want the zero time", at)
	}

	newest := time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)
	if _, err := st.RecordProviderUsage(ctx, user.ID, "cursor", "acct", []store.ProviderUsageEvent{
		providerEvent("evt-old", "conv", "m", newest.Add(-2*time.Hour), 1, 1, 1, 1),
		providerEvent("evt-new", "conv", "m", newest, 1, 1, 1, 1),
	}); err != nil {
		t.Fatalf("collect: %v", err)
	}
	at, err = st.ProviderUsageWatermark(ctx, user.ID, "cursor", "acct")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if !at.Equal(newest) {
		t.Errorf("watermark %v, want the newest stored event %v", at, newest)
	}

	// Accounts are scoped separately, so one account's history never moves another's
	// resume point.
	other, err := st.ProviderUsageWatermark(ctx, user.ID, "cursor", "other-acct")
	if err != nil {
		t.Fatalf("watermark for a second account: %v", err)
	}
	if !other.IsZero() {
		t.Errorf("second account's watermark %v, want the zero time", other)
	}
}

// assertDue reports whether the parse worker would pick the session up. Provider
// usage makes a session due for a reason neither its bytes nor the epoch express,
// so this is what proves the new due arm actually fires.
func assertDue(t *testing.T, st *store.Store, sessionID int64, want bool, when string) {
	t.Helper()
	due, err := st.DueSessions(context.Background(), testEpoch, 100)
	if err != nil {
		t.Fatalf("due sessions %s: %v", when, err)
	}
	got := false
	for _, d := range due {
		if d.ID == sessionID {
			got = true
		}
	}
	if got != want {
		t.Errorf("%s: session %d due = %v, want %v", when, sessionID, got, want)
	}
}

func assertUnattachedCount(t *testing.T, st *store.Store, userID int64, want int, when string) {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM provider_usage_events WHERE user_id = $1 AND session_id IS NULL`, userID).
		Scan(&got); err != nil {
		t.Fatalf("count unattached events %s: %v", when, err)
	}
	if got != want {
		t.Errorf("%s: %d unattached events, want %d", when, got, want)
	}
}

func assertBreakdown(t *testing.T, rows []store.Breakdown, label string, wantCost float64) {
	t.Helper()
	for _, r := range rows {
		if r.Label == label {
			if math.Abs(r.CostUSD-wantCost) > 1e-9 {
				t.Errorf("breakdown %q cost %v, want %v", label, r.CostUSD, wantCost)
			}
			return
		}
	}
	t.Errorf("breakdown has no %q row", label)
}
