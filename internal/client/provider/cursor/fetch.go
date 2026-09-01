package cursor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// DefaultBaseURL is cursor.com. It is a field on Fetcher so tests can serve the
// feed from an httptest server.
const DefaultBaseURL = "https://cursor.com"

const (
	// pageSize matches what Cursor's own dashboard requests.
	pageSize = 1000
	// maxPages bounds one collection so a paging bug cannot loop forever. At the
	// page size above this is 200k events, far past any real account's history.
	maxPages = 200
	// pageTimeout bounds one request. http.DefaultClient has no timeout, so without
	// this a stalled connection would hang the whole collection with nothing to
	// interrupt it but the caller's context, which a walk of up to maxPages requests
	// should not depend on for liveness.
	pageTimeout = 60 * time.Second
)

// defaultHTTP is the client a Fetcher uses when none is injected. Tests inject the
// httptest server's client instead.
var defaultHTTP = &http.Client{Timeout: pageTimeout}

// Event is one billing event, normalized for upload. It is deliberately narrower
// than the feed's row: akari stores what its ledger has columns for, and drops the
// vendor's plan-accounting fields (request cost, chargeability, headlessness, the
// subscription product) rather than warehousing data nothing reads.
type Event struct {
	EventKey       string
	ConversationID string
	Model          string
	Input          int
	Output         int
	CacheWrite     int
	CacheRead      int
	CostUSD        float64
	// CostKnown separates a reported zero from an unreported cost. Cursor omits
	// tokenUsage entirely on some metered rows, and a zero stored as if it were
	// reported would read as a genuinely free request.
	CostKnown  bool
	OccurredAt time.Time
}

// Fetcher pages Cursor's dashboard usage feed.
type Fetcher struct {
	BaseURL string
	HTTP    *http.Client
}

// Fetch returns every usage event at or after since, oldest first.
//
// A zero since collects the account's whole history, which is what a first run
// does. Later runs pass the server's watermark, so the window is small and
// deliberately overlaps the newest stored event by an instant; the server dedups on
// the event key, so re-sending that boundary event is free and losing it would not
// be.
//
// Paging is verified rather than trusted. The feed reports how many events match
// the query, and pages can repeat rows at their boundaries, so a short or empty
// page must prove the walk finished before any of it is returned. An unfinished
// walk is an error, not a partial result: publishing a truncated window would
// silently understate an account's spend, and a retry costs one more request.
func (f *Fetcher) Fetch(ctx context.Context, s Session, since time.Time) ([]Event, error) {
	var pages [][]Event
	expected, dropped := -1, 0
	complete := false
	for page := 1; page <= maxPages; page++ {
		got, err := f.fetchPage(ctx, s, page, since)
		if err != nil {
			return nil, err
		}
		if got.Total != nil {
			if expected >= 0 && expected != *got.Total {
				return nil, fmt.Errorf("cursor usage feed changed size mid-walk: %d then %d", expected, *got.Total)
			}
			expected = *got.Total
		}
		if got.Rows == 0 {
			complete = true
			break
		}
		pages = append(pages, got.Events)
		dropped += got.Rows - len(got.Events)
		if got.Rows < pageSize {
			complete = true
			break
		}
	}
	if !complete {
		return nil, fmt.Errorf("cursor usage feed did not terminate within %d pages", maxPages)
	}

	var events []Event
	for _, p := range pages {
		events = append(events, p...)
	}
	// The reported total counts every row the query matched, undatable ones
	// included, so the count this walk should end on is the total less what it
	// dropped. Comparing against the raw total instead would read a dropped row as a
	// short walk and reject a complete one.
	if expected >= 0 {
		want := expected - dropped
		if len(events) < want {
			return nil, fmt.Errorf("cursor usage feed reported %d events but returned %d", want, len(events))
		}
		if len(events) > want {
			var err error
			if events, err = dropBoundaryRepeats(pages, want); err != nil {
				return nil, err
			}
		}
	}
	return withEventKeys(events), nil
}

// dropBoundaryRepeats removes exactly the surplus the feed's own count proves is
// duplication, choosing rows that repeat across adjacent page boundaries.
//
// The endpoint exposes no event id, so a repeat can only be recognized as an
// identical row in the overlap between one page's tail and the next page's head.
// Two genuinely identical events elsewhere in the walk stay distinct, because the
// reported total counts both. If the surplus cannot be accounted for this way the
// walk is rejected rather than trimmed arbitrarily.
func dropBoundaryRepeats(pages [][]Event, want int) ([]Event, error) {
	if len(pages) == 0 {
		return nil, nil
	}
	total := 0
	for _, p := range pages {
		total += len(p)
	}
	remaining := total - want
	out := append([]Event(nil), pages[0]...)
	for i := 1; i < len(pages); i++ {
		// The drop is capped at the surplus, which is what keeps a real charge. An
		// overlap wider than the surplus is not a miscount: identical rows are
		// indistinguishable, so a boundary where two genuinely identical events happen
		// to meet looks exactly like a repeat. The feed's own count is the authority on
		// how many of them are real, so dropping more than the surplus would delete a
		// charge the vendor billed. Rejecting a wider overlap outright is worse still:
		// it fails every collection for that account until the rows age out of the
		// window. What the surplus cannot explain at all is still caught below.
		drop := min(boundaryOverlap(pages[i-1], pages[i]), remaining)
		out = append(out, pages[i][drop:]...)
		remaining -= drop
	}
	if remaining != 0 || len(out) != want {
		return nil, fmt.Errorf("cursor usage feed returned %d events for an expected %d, and the surplus is not page overlap", total, want)
	}
	return out, nil
}

// boundaryOverlap returns the longest suffix of prev that equals the same-length
// prefix of next. Rows compare by fingerprint, the same identity the stored event
// key is built on, so "the same row" means the same thing here and in the server's
// deduplication.
func boundaryOverlap(prev, next []Event) int {
	limit := min(len(prev), len(next))
	for n := limit; n > 0; n-- {
		if equalRuns(prev[len(prev)-n:], next[:n]) {
			return n
		}
	}
	return 0
}

func equalRuns(a, b []Event) bool {
	for i := range a {
		if a[i].EventKey != b[i].EventKey {
			return false
		}
	}
	return true
}

// feedPage is one decoded page. Rows is the raw row count the feed returned, which
// drives paging; Events is the datable subset, which is what akari keeps.
type feedPage struct {
	Total  *int
	Rows   int
	Events []Event
}

func (f *Fetcher) fetchPage(ctx context.Context, s Session, page int, since time.Time) (feedPage, error) {
	body := map[string]any{"page": page, "pageSize": pageSize}
	if !since.IsZero() {
		body["startDate"] = strconv.FormatInt(since.UnixMilli(), 10)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return feedPage{}, err
	}
	base := f.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/dashboard/get-filtered-usage-events", bytes.NewReader(payload))
	if err != nil {
		return feedPage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", s.CookieHeader)
	// Cursor enforces CSRF on the dashboard POST endpoints, so the request must
	// declare the origin it would have come from in a browser.
	req.Header.Set("Origin", base)

	httpc := f.HTTP
	if httpc == nil {
		httpc = defaultHTTP
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return feedPage{}, fmt.Errorf("fetch cursor usage page %d: %w", page, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return feedPage{}, fmt.Errorf("read cursor usage page %d: %w", page, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return feedPage{}, ErrNoSession
	default:
		return feedPage{}, fmt.Errorf("cursor usage page %d: HTTP %d", page, resp.StatusCode)
	}

	// An empty window answers "{}": no count and no array. A terminal page of a
	// non-empty window keeps the count and omits the array. Decoding into a struct
	// with a pointer count distinguishes both from a page that reported zero events
	// for a window it had already said was non-empty.
	var decoded struct {
		Total  *int        `json:"totalUsageEventsCount"`
		Events []feedEvent `json:"usageEventsDisplay"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return feedPage{}, fmt.Errorf("decode cursor usage page %d: %w", page, err)
	}
	return feedPage{Total: decoded.Total, Rows: len(decoded.Events), Events: normalize(decoded.Events)}, nil
}

// feedEvent is one row of the feed, in the shape Cursor sends it. It is comparable
// so page-boundary repeats can be recognized by value; that is the only reason the
// token counts are not folded into a struct here.
//
// Cursor serializes several of these numbers as strings, so each one is decoded
// leniently (see lenientInt64 and lenientFloat).
type feedEvent struct {
	Timestamp      lenientInt64 `json:"timestamp"`
	Model          string       `json:"model"`
	ConversationID string       `json:"conversationId"`
	TokenUsage     *tokenUsage  `json:"tokenUsage"`
}

type tokenUsage struct {
	Input      lenientInt64  `json:"inputTokens"`
	Output     lenientInt64  `json:"outputTokens"`
	CacheWrite lenientInt64  `json:"cacheWriteTokens"`
	CacheRead  lenientInt64  `json:"cacheReadTokens"`
	TotalCents *lenientFloat `json:"totalCents"`
}

// normalize turns feed rows into upload events, dropping rows with no usable
// timestamp: an undatable event has no place on any time axis, which is the same
// rule the analytics base applies.
//
// The events come back without an event key. Keys are assigned once over the whole
// walk (see withEventKeys), because disambiguating identical rows needs the
// complete ordering, not one page of it.
func normalize(raw []feedEvent) []Event {
	out := make([]Event, 0, len(raw))
	for _, e := range raw {
		ms := int64(e.Timestamp)
		if ms <= 0 {
			continue
		}
		ev := Event{
			ConversationID: e.ConversationID,
			Model:          e.Model,
			OccurredAt:     time.UnixMilli(ms).UTC(),
		}
		if u := e.TokenUsage; u != nil {
			ev.Input = clampInt(u.Input)
			ev.Output = clampInt(u.Output)
			ev.CacheWrite = clampInt(u.CacheWrite)
			ev.CacheRead = clampInt(u.CacheRead)
			if c := u.TotalCents; c != nil && float64(*c) >= 0 {
				ev.CostUSD = float64(*c) / 100
				ev.CostKnown = true
			}
		}
		// The fingerprint lands in EventKey now so the page-boundary comparison and
		// the final key share one computation rather than rehashing every row for
		// every candidate overlap length. withEventKeys appends the ordinal.
		ev.EventKey = eventFingerprint(ev)
		out = append(out, ev)
	}
	return out
}

// withEventKeys stamps each event with its stored identity.
//
// Two rows akari would store identically are distinct events that happen to look
// the same, so a fingerprint alone cannot key them. The ordinal of an identical row
// within the walk breaks the tie, and it is stable across machines and re-fetches
// because the feed returns a window in a fixed order: the same event lands at the
// same ordinal every time, which is exactly what the server's deduplication needs.
// The ordinal is stable across resumed windows, which is what makes the key safe
// to dedup on. It is stable because a fingerprint group cannot be split across two
// walks: eventFingerprint hashes occurred_at, so every member of a group shares one
// instant, and the fetch window is a half-open time range, so it admits all of that
// instant's rows or none of them. The walk is all-or-nothing for the same reason
// (page exhaustion and a short or unexplained surplus both error rather than
// truncate), so nothing else can split a group either. Change either property and
// this numbering stops being a stable identity: a group split across two windows
// would renumber from zero and collide with keys already stored.
func withEventKeys(events []Event) []Event {
	seen := make(map[string]int, len(events))
	for i := range events {
		base := events[i].EventKey
		events[i].EventKey = fmt.Sprintf("%s-%d", base, seen[base])
		seen[base]++
	}
	return events
}

// eventFingerprint hashes exactly the fields akari stores.
//
// The feed carries no event id, so this derived key is what makes a re-fetch and a
// second machine's collection idempotent instead of double-counted. It hashes the
// stored fields rather than the whole row on purpose: two rows akari would store
// identically ARE the same stored event, whatever plan-accounting fields differ
// between them, so keying on the wider row would let a vendor-side change to an
// ignored field re-insert history akari already holds.
func eventFingerprint(e Event) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%t\x00%.6f",
		e.OccurredAt.UnixMilli(), e.ConversationID, e.Model,
		e.Input, e.Output, e.CacheWrite, e.CacheRead, e.CostKnown, e.CostUSD)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func clampInt(v lenientInt64) int {
	if v < 0 {
		return 0
	}
	return int(v)
}

// lenientInt64 accepts a JSON number or a numeric string, which the feed mixes
// even within one object (the timestamp is a string, the token counts are numbers).
type lenientInt64 int64

func (v *lenientInt64) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		parsed, err := n.Int64()
		if err != nil {
			// A fractional count is not a count; treat it as absent rather than
			// truncating silently.
			return nil
		}
		*v = lenientInt64(parsed)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return nil // null or an unexpected shape reads as absent
	}
	parsed, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	*v = lenientInt64(parsed)
	return nil
}

// lenientFloat accepts a JSON number or a numeric string. Unlike lenientInt64 a
// failure to parse must be visible, because the caller distinguishes an absent cost
// from a reported one; the pointer field carries that, so a bad value stays nil.
type lenientFloat float64

func (v *lenientFloat) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		*v = lenientFloat(f)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("cursor cost is neither number nor string")
	}
	parsed, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("cursor cost %q is not a number", s)
	}
	*v = lenientFloat(parsed)
	return nil
}
