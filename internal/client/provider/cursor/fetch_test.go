package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The feed these tests stand in for is Cursor's own, recorded from a live account:
// timestamps arrive as strings while token counts arrive as numbers, an empty
// window answers "{}", a terminal page keeps the reported count but omits the array,
// and adjacent pages can repeat rows at their boundary. Each of those is a shape
// that would silently corrupt an account's recorded spend if mishandled, so each has
// a test.

// feedServer serves a canned sequence of page bodies, in order, and records the
// requests it saw.
type feedServer struct {
	pages    []string
	requests []map[string]any
}

func (f *feedServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") == "" {
			t.Error("request carried no Cookie header")
		}
		if r.Header.Get("Origin") == "" {
			t.Error("request carried no Origin header; cursor.com rejects the POST without one")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		f.requests = append(f.requests, body)
		i := len(f.requests) - 1
		if i >= len(f.pages) {
			t.Errorf("server asked for page %d but only %d were canned", i+1, len(f.pages))
			w.Write([]byte(`{}`))
			return
		}
		w.Write([]byte(f.pages[i]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fetchFrom(t *testing.T, srv *httptest.Server, since time.Time) ([]Event, error) {
	t.Helper()
	f := &Fetcher{BaseURL: srv.URL, HTTP: srv.Client()}
	return f.Fetch(context.Background(), Session{CookieHeader: "WorkosCursorSessionToken=x", AccountID: "acct"}, since)
}

// rows renders n synthetic feed rows starting at the given millisecond, in the
// vendor's own serialization: a string timestamp beside numeric token counts.
func rows(startMS int64, n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf(
			`{"timestamp":"%d","model":"cursor-grok-4.6-high","conversationId":"conv-%d","tokenUsage":{"inputTokens":10,"outputTokens":20,"cacheReadTokens":30,"totalCents":250}}`,
			startMS+int64(i), i))
	}
	return out
}

func page(total int, body []string) string {
	return fmt.Sprintf(`{"totalUsageEventsCount":%d,"usageEventsDisplay":[%s]}`, total, strings.Join(body, ","))
}

func TestFetchDecodesTheVendorSerialization(t *testing.T) {
	f := &feedServer{pages: []string{page(1, rows(1788230654170, 1))}}
	events, err := fetchFrom(t, f.start(t), time.Time{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("fetched %d events, want 1", len(events))
	}
	e := events[0]
	if !e.OccurredAt.Equal(time.UnixMilli(1788230654170).UTC()) {
		t.Errorf("occurred at %v, want the string timestamp decoded", e.OccurredAt)
	}
	if e.Input != 10 || e.Output != 20 || e.CacheRead != 30 {
		t.Errorf("tokens in=%d out=%d cr=%d, want 10/20/30", e.Input, e.Output, e.CacheRead)
	}
	// The vendor reports cents; akari stores dollars.
	if e.CostUSD != 2.5 || !e.CostKnown {
		t.Errorf("cost %v known=%v, want 2.5 known", e.CostUSD, e.CostKnown)
	}
	if e.Model != "cursor-grok-4.6-high" || e.ConversationID != "conv-0" {
		t.Errorf("model %q conversation %q, want the reported values", e.Model, e.ConversationID)
	}
}

// A metered row can omit tokenUsage entirely. Storing its cost as a reported zero
// would claim the vendor said the request was free, which it did not.
func TestFetchSeparatesAnUnreportedCostFromAReportedZero(t *testing.T) {
	f := &feedServer{pages: []string{page(2, []string{
		`{"timestamp":"1788230654170","model":"m","conversationId":"c"}`,
		`{"timestamp":"1788230654171","model":"m","conversationId":"c","tokenUsage":{"inputTokens":1,"totalCents":0}}`,
	})}}
	events, err := fetchFrom(t, f.start(t), time.Time{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("fetched %d events, want 2", len(events))
	}
	if events[0].CostKnown {
		t.Error("a row with no tokenUsage reads as a known cost, want unknown")
	}
	if !events[1].CostKnown || events[1].CostUSD != 0 {
		t.Errorf("a reported zero reads as known=%v cost=%v, want known 0", events[1].CostKnown, events[1].CostUSD)
	}
}

// An empty window answers "{}". Reading that as a failure would make a quiet
// account error on every collection.
func TestFetchAcceptsAnEmptyWindow(t *testing.T) {
	f := &feedServer{pages: []string{`{}`}}
	events, err := fetchFrom(t, f.start(t), time.Now())
	if err != nil {
		t.Fatalf("fetch an empty window: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("fetched %d events from an empty window, want 0", len(events))
	}
}

// A full first page followed by a terminal page that keeps the count and omits the
// array is the ordinary end of a multi-page walk.
func TestFetchWalksToATerminalPage(t *testing.T) {
	full := rows(1788000000000, pageSize)
	f := &feedServer{pages: []string{
		page(pageSize, full),
		`{"totalUsageEventsCount":1000}`,
	}}
	events, err := fetchFrom(t, f.start(t), time.Time{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != pageSize {
		t.Fatalf("fetched %d events, want %d", len(events), pageSize)
	}
	if len(f.requests) != 2 {
		t.Fatalf("made %d requests, want 2", len(f.requests))
	}
	if got := f.requests[1]["page"]; got != float64(2) {
		t.Errorf("second request asked for page %v, want 2", got)
	}
}

// Cursor can repeat rows where two pages meet. The reported total is the authority
// on how many are real, so exactly that surplus comes off the boundary.
func TestFetchDropsPageBoundaryRepeats(t *testing.T) {
	first := rows(1788000000000, pageSize)
	// The next page opens by repeating the previous page's last two rows.
	second := append(append([]string{}, first[pageSize-2:]...), rows(1788000009000, 3)...)
	f := &feedServer{pages: []string{
		page(pageSize+3, first),
		page(pageSize+3, second),
	}}
	events, err := fetchFrom(t, f.start(t), time.Time{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != pageSize+3 {
		t.Fatalf("fetched %d events, want the reported %d", len(events), pageSize+3)
	}
	keys := map[string]bool{}
	for _, e := range events {
		if keys[e.EventKey] {
			t.Fatalf("duplicate event key %s survived the walk", e.EventKey)
		}
		keys[e.EventKey] = true
	}
}

// A surplus the page boundaries cannot explain means the walk is not understood, and
// publishing it would silently misstate an account's spend. Fail instead.
func TestFetchRejectsAnUnexplainedSurplus(t *testing.T) {
	f := &feedServer{pages: []string{page(1, rows(1788000000000, 3))}}
	if _, err := fetchFrom(t, f.start(t), time.Time{}); err == nil {
		t.Fatal("fetch accepted more events than the feed reported, want an error")
	}
}

// A short walk is a truncated one. Returning it would understate the account.
func TestFetchRejectsAShortWalk(t *testing.T) {
	f := &feedServer{pages: []string{page(9, rows(1788000000000, 3))}}
	if _, err := fetchFrom(t, f.start(t), time.Time{}); err == nil {
		t.Fatal("fetch accepted fewer events than the feed reported, want an error")
	}
}

// Two identical rows are two distinct billable events. Keying them on their fields
// alone would collapse them into one, losing a real charge.
func TestIdenticalRowsGetDistinctStableKeys(t *testing.T) {
	body := `{"timestamp":"1788230654170","model":"m","conversationId":"c","tokenUsage":{"inputTokens":1,"outputTokens":1,"totalCents":5}}`
	f := &feedServer{pages: []string{page(2, []string{body, body})}}
	events, err := fetchFrom(t, f.start(t), time.Time{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("fetched %d events, want 2", len(events))
	}
	if events[0].EventKey == events[1].EventKey {
		t.Fatal("two identical rows got the same key; one of the two charges would be lost")
	}

	// The key must also be stable: a second machine collecting the same window, or
	// the same machine re-fetching it, has to derive the same keys or the server
	// stores the account's history twice.
	g := &feedServer{pages: []string{page(2, []string{body, body})}}
	again, err := fetchFrom(t, g.start(t), time.Time{})
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	for i := range events {
		if events[i].EventKey != again[i].EventKey {
			t.Errorf("event %d keyed %s then %s; a re-fetch must derive the same key",
				i, events[i].EventKey, again[i].EventKey)
		}
	}
}

// A row akari cannot place on a time axis is dropped, and the reported total counts
// it, so the completeness check has to allow for the drop rather than read it as a
// short walk.
func TestFetchDropsUndatableRowsWithoutFailingTheWalk(t *testing.T) {
	f := &feedServer{pages: []string{page(2, []string{
		`{"timestamp":"0","model":"m","conversationId":"c"}`,
		`{"timestamp":"1788230654170","model":"m","conversationId":"c"}`,
	})}}
	events, err := fetchFrom(t, f.start(t), time.Time{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("fetched %d events, want the 1 datable row", len(events))
	}
}

// The resume window is the whole point of the watermark: a later run must ask for a
// window rather than re-paging the account's history.
func TestFetchWindowsFromTheResumePoint(t *testing.T) {
	since := time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)
	f := &feedServer{pages: []string{`{}`}}
	if _, err := fetchFrom(t, f.start(t), since); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, ok := f.requests[0]["startDate"].(string)
	if !ok {
		t.Fatalf("request carried no startDate: %v", f.requests[0])
	}
	if want := fmt.Sprint(since.UnixMilli()); got != want {
		t.Errorf("startDate %s, want %s", got, want)
	}

	// A first collection has no resume point and must ask for everything.
	g := &feedServer{pages: []string{`{}`}}
	if _, err := fetchFrom(t, g.start(t), time.Time{}); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, present := g.requests[0]["startDate"]; present {
		t.Error("a first collection sent a startDate; it must ask for the whole history")
	}
}

// A rejected credential is an ordinary state (a revoked or superseded session), not
// a failure of this machine, so it must surface as the sentinel the caller skips on.
func TestFetchReportsARejectedSessionAsNoSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	_, err := fetchFrom(t, srv, time.Time{})
	if err == nil || !strings.Contains(err.Error(), ErrNoSession.Error()) {
		t.Errorf("fetch against a 401 returned %v, want ErrNoSession", err)
	}
}

// Two genuinely identical adjacent events can straddle a page boundary. They are
// indistinguishable from a repeat, so the feed's own count is the authority: with a
// correct total there is no surplus, nothing may be dropped, and the walk must be
// accepted. Rejecting it would fail every collection for that account until the rows
// aged out of the window.
func TestFetchKeepsIdenticalEventsAcrossABoundaryWhenTheCountAllowsThem(t *testing.T) {
	first := rows(1788000000000, pageSize)
	// The next page opens with a row identical to the previous page's last, but the
	// feed's count says every row is real.
	second := append(append([]string{}, first[pageSize-1:]...), rows(1788000009000, 1)...)
	f := &feedServer{pages: []string{
		page(pageSize+2, first),
		page(pageSize+2, second),
	}}
	events, err := fetchFrom(t, f.start(t), time.Time{})
	if err != nil {
		t.Fatalf("fetch rejected a walk its own count says is complete: %v", err)
	}
	if len(events) != pageSize+2 {
		t.Fatalf("fetched %d events, want the reported %d", len(events), pageSize+2)
	}
}

// The event key's ordinal must survive a resume, which is the property the whole
// dedup rests on. It survives because identical rows share an instant (the
// fingerprint hashes occurred_at) and the window is a time range, so a resumed walk
// admits a fingerprint group whole or not at all and never renumbers part of one.
func TestIdenticalRowsKeepTheirKeysAcrossAResume(t *testing.T) {
	const body = `{"timestamp":"1788230654170","model":"m","conversationId":"c","tokenUsage":{"inputTokens":1,"outputTokens":1,"totalCents":5}}`
	// A full-history walk sees both identical rows.
	full := &feedServer{pages: []string{page(2, []string{body, body})}}
	first, err := fetchFrom(t, full.start(t), time.Time{})
	if err != nil {
		t.Fatalf("full walk: %v", err)
	}

	// A later collection resumes from a watermark at that same instant. The window
	// still contains the whole group, so the keys come back identical.
	at := time.UnixMilli(1788230654170).UTC()
	resumed := &feedServer{pages: []string{page(2, []string{body, body})}}
	second, err := fetchFrom(t, resumed.start(t), at.Add(-time.Second))
	if err != nil {
		t.Fatalf("resumed walk: %v", err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("walks returned %d and %d events, want 2 each", len(first), len(second))
	}
	for i := range first {
		if first[i].EventKey != second[i].EventKey {
			t.Errorf("event %d keyed %s on a full walk and %s on a resume; a resumed group must keep its ordinals or the server drops or doubles a charge",
				i, first[i].EventKey, second[i].EventKey)
		}
	}
}
