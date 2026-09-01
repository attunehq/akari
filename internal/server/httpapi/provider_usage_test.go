package httpapi

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/client/upload"
	"github.com/jssblck/akari/internal/parser"
	"github.com/jssblck/akari/internal/pricing"
)

// These exercise the boundary the collecting client actually crosses: the real
// upload.Client against the real routes, so a mismatch between the two shows up
// here rather than in production against a vendor API no test can call.

func providerEvent(key string, at time.Time, costKnown bool, cost float64) upload.ProviderUsageEvent {
	return upload.ProviderUsageEvent{
		EventKey:       key,
		ConversationID: "1c9000b1-6ffa-406b-8a2c-1992e2912b66",
		Model:          "cursor-grok-4.6-high-fast",
		Input:          3264,
		Output:         812,
		CacheRead:      540800,
		CostUSD:        cost,
		CostKnown:      costKnown,
		OccurredAt:     at,
	}
}

func TestProviderUsageRoundTrip(t *testing.T) {
	t.Parallel()
	srv, st, _ := newTestServerWithReparse(t)
	c, ownerID := ingestClient(t, srv.URL, st)
	ctx := context.Background()

	// A first collection has no resume point.
	at, err := c.ProviderUsageWatermark(ctx, "cursor", "403704963")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if !at.IsZero() {
		t.Errorf("watermark %v before any collection, want the zero time", at)
	}

	newest := time.Date(2026, 8, 17, 20, 17, 0, 0, time.UTC).Truncate(time.Millisecond)
	inserted, err := c.SendProviderUsage(ctx, "cursor", "403704963", []upload.ProviderUsageEvent{
		providerEvent("evt-a", newest.Add(-time.Hour), true, 0.2818),
		providerEvent("evt-b", newest, true, 0.3326),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted %d, want 2", inserted)
	}

	// The watermark now names the newest stored event, which is where the next
	// collection resumes.
	at, err = c.ProviderUsageWatermark(ctx, "cursor", "403704963")
	if err != nil {
		t.Fatalf("watermark after collection: %v", err)
	}
	if !at.Equal(newest) {
		t.Errorf("watermark %v, want %v", at, newest)
	}

	var stored int
	var cost float64
	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*), coalesce(sum(cost_usd), 0) FROM provider_usage_events WHERE user_id = $1`, ownerID).
		Scan(&stored, &cost); err != nil {
		t.Fatalf("read stored events: %v", err)
	}
	if stored != 2 || math.Abs(cost-0.6144) > 1e-9 {
		t.Errorf("stored %d events costing %v, want 2 costing 0.6144", stored, cost)
	}
}

// Disclosure is decided server-side against akari's own catalog. A client that
// claimed a model was publishable would otherwise put an undisclosed identifier on
// a public overview.
func TestProviderUsageDisclosureIsDecidedServerSide(t *testing.T) {
	t.Parallel()
	srv, st, _ := newTestServerWithReparse(t)
	c, ownerID := ingestClient(t, srv.URL, st)
	ctx := context.Background()

	at := time.Date(2026, 8, 17, 20, 17, 0, 0, time.UTC)
	e := providerEvent("evt-a", at, true, 1)
	e.Model = "cursor-grok-4.6-high-fast" // not in the compiled catalog
	if _, err := c.SendProviderUsage(ctx, "cursor", "acct", []upload.ProviderUsageEvent{e}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if pricing.ModelNamePublic(e.Model) {
		t.Fatalf("%s is disclosed in the catalog; pick an undisclosed model for this test", e.Model)
	}
	var public bool
	if err := st.Pool.QueryRow(ctx,
		`SELECT model_name_public FROM provider_usage_events WHERE user_id = $1`, ownerID).Scan(&public); err != nil {
		t.Fatalf("read disclosure: %v", err)
	}
	if public {
		t.Error("an undisclosed model was stored as publishable")
	}
}

// A cost the vendor did not report must stay distinguishable from one it reported
// as zero, which is the whole reason usage_events carries a cost source.
func TestProviderUsageStoresAnUnreportedCostAsUnknown(t *testing.T) {
	t.Parallel()
	srv, st, _ := newTestServerWithReparse(t)
	c, ownerID := ingestClient(t, srv.URL, st)
	ctx := context.Background()

	at := time.Date(2026, 8, 17, 20, 17, 0, 0, time.UTC)
	if _, err := c.SendProviderUsage(ctx, "cursor", "acct", []upload.ProviderUsageEvent{
		providerEvent("unreported", at, false, 99),
		providerEvent("reported-zero", at.Add(time.Minute), true, 0),
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	rows, err := st.Pool.Query(ctx,
		`SELECT event_key, cost_source, cost_usd FROM provider_usage_events
		  WHERE user_id = $1 ORDER BY event_key`, ownerID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	got := map[string]struct {
		source string
		cost   float64
	}{}
	for rows.Next() {
		var key, source string
		var cost float64
		if err := rows.Scan(&key, &source, &cost); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[key] = struct {
			source string
			cost   float64
		}{source, cost}
	}
	if g := got["unreported"]; g.source != "unknown" || g.cost != 0 {
		t.Errorf("unreported cost stored as %s/%v, want unknown/0", g.source, g.cost)
	}
	if g := got["reported-zero"]; g.source != "provider" || g.cost != 0 {
		t.Errorf("reported zero stored as %s/%v, want provider/0", g.source, g.cost)
	}
}

// Every provider name must also be an agent name: that identity is what lets a
// reported conversation id resolve against sessions.source_session_id at all.
func TestProviderNamesAreAgents(t *testing.T) {
	t.Parallel()
	agents := map[string]bool{}
	for _, a := range parser.Agents {
		agents[string(a)] = true
	}
	for name := range validProviders {
		if !agents[name] {
			t.Errorf("provider %q is not a parser.Agents entry, so its events could never resolve to a session", name)
		}
	}
}

// A negative cost has no vendor meaning and would subtract from real spend once it
// reached usage_ledger and, after the fold, a session's own totals. The boundary
// floors it at zero the same way it floors the token counts.
func TestProviderUsageClampsANegativeCost(t *testing.T) {
	t.Parallel()
	srv, st, _ := newTestServerWithReparse(t)
	c, ownerID := ingestClient(t, srv.URL, st)
	ctx := context.Background()

	at := time.Date(2026, 8, 17, 20, 17, 0, 0, time.UTC)
	e := providerEvent("negative", at, true, -12.5)
	e.Input, e.Output, e.CacheRead = -1, -2, -3
	if _, err := c.SendProviderUsage(ctx, "cursor", "acct", []upload.ProviderUsageEvent{e}); err != nil {
		t.Fatalf("send: %v", err)
	}

	var cost float64
	var in, out, cr int
	if err := st.Pool.QueryRow(ctx,
		`SELECT cost_usd, input_tokens, output_tokens, cache_read_tokens
		   FROM provider_usage_events WHERE user_id = $1`, ownerID).Scan(&cost, &in, &out, &cr); err != nil {
		t.Fatalf("read stored event: %v", err)
	}
	if cost != 0 {
		t.Errorf("stored cost %v, want 0: a negative charge would cancel real spend", cost)
	}
	if in != 0 || out != 0 || cr != 0 {
		t.Errorf("stored tokens in=%d out=%d cr=%d, want all 0", in, out, cr)
	}
}
