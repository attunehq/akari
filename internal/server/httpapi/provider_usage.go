package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/jssblck/akari/internal/pricing"
	"github.com/jssblck/akari/internal/server/store"
)

// maxProviderUsageBatch bounds one collection request. The client pages the
// vendor feed and posts a page at a time, so this caps a single body rather than
// a whole collection; a first sync of a busy account simply sends more requests.
const maxProviderUsageBatch = 2000

// validProviders is the set of vendors akari collects account usage from. Each
// name is also a parser.Agents entry, because that identity is what lets a
// reported conversation id resolve against sessions.source_session_id (see
// store.RecordProviderUsage). The store's CHECK constraint is the hard gate;
// this is the request-shaped one, kept from drifting by
// TestProviderNamesAreAgents.
var validProviders = map[string]bool{"cursor": true}

type providerUsageEventDTO struct {
	EventKey       string  `json:"event_key"`
	ConversationID string  `json:"conversation_id"`
	Model          string  `json:"model"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	CacheWriteToks int     `json:"cache_write_tokens"`
	CacheReadToks  int     `json:"cache_read_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	// CostKnown says the vendor reported this cost. False stores the event as
	// unpriced rather than as a reported zero, which is the distinction
	// usage_events.cost_source exists to keep (migration 0059).
	CostKnown  bool      `json:"cost_known"`
	OccurredAt time.Time `json:"occurred_at"`
}

type providerUsageRequest struct {
	Provider  string                  `json:"provider"`
	AccountID string                  `json:"account_id"`
	Events    []providerUsageEventDTO `json:"events"`
}

type providerUsageResponse struct {
	Inserted       int `json:"inserted"`
	SessionsMarked int `json:"sessions_marked"`
}

type providerUsageWatermarkResponse struct {
	// LatestEventAt is the newest event instant already stored for this account, or
	// null when none is. The client resumes its fetch from here instead of keeping a
	// cursor of its own, so a fresh checkout, a second machine, and a reinstalled
	// client all resume from the same place.
	LatestEventAt *time.Time `json:"latest_event_at"`
}

// handleProviderUsage records a page of vendor-reported usage events.
//
// The events are account-wide, not machine-scoped: the same feed is visible from
// every machine signed in to the vendor account, so this endpoint is idempotent on
// the client-derived event key and two machines collecting concurrently converge on
// one stored copy rather than double counting.
func (s *Server) handleProviderUsage(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var req providerUsageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.AccountID = strings.TrimSpace(req.AccountID)
	if !validProviders[req.Provider] {
		writeError(w, http.StatusBadRequest, "provider must be one of: cursor")
		return
	}
	if req.AccountID == "" {
		writeError(w, http.StatusBadRequest, "account_id is required")
		return
	}
	if len(req.Events) > maxProviderUsageBatch {
		writeError(w, http.StatusRequestEntityTooLarge, "too many events in one request")
		return
	}

	events := make([]store.ProviderUsageEvent, 0, len(req.Events))
	for _, e := range req.Events {
		if strings.TrimSpace(e.EventKey) == "" {
			writeError(w, http.StatusBadRequest, "every event needs an event_key")
			return
		}
		if e.OccurredAt.IsZero() {
			writeError(w, http.StatusBadRequest, "every event needs an occurred_at")
			return
		}
		// An unreported cost is stored as unknown at zero, never as a reported zero.
		// The store's CHECK enforces the same pairing, so this is the boundary that
		// keeps a malformed client from writing a priced-looking unknown.
		//
		// Cost floors at zero for the same reason the token counts do: it flows through
		// usage_ledger into the Overview headline and, once folded, into a session's
		// own totals, where a negative would silently cancel real spend. No vendor
		// reports a negative charge, so clamping here cannot lose a real figure.
		cost, source := max(e.CostUSD, 0), store.CostSourceProvider
		if !e.CostKnown {
			cost, source = 0, store.CostSourceUnknown
		}
		events = append(events, store.ProviderUsageEvent{
			EventKey:       e.EventKey,
			ConversationID: e.ConversationID,
			Model:          e.Model,
			Input:          max(e.InputTokens, 0),
			Output:         max(e.OutputTokens, 0),
			CacheWrite:     max(e.CacheWriteToks, 0),
			CacheRead:      max(e.CacheReadToks, 0),
			CostUSD:        cost,
			CostSource:     source,
			// Disclosure is decided server-side against the compiled catalog, never
			// taken from the client: a model identifier reaches a public overview only
			// when akari itself has approved it (see pricing.ModelNamePublic).
			ModelNamePublic: pricing.ModelNamePublic(e.Model),
			OccurredAt:      e.OccurredAt.UTC(),
		})
	}

	res, err := s.Store.RecordProviderUsage(r.Context(), p.UserID, req.Provider, req.AccountID, events)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not record provider usage")
		return
	}
	// Newly attached usage leaves sessions due for a rebuild, and the rebuild is what
	// moves it into the ledger and the rollups. Wake the worker exactly as an
	// appended chunk does, so a collection's spend shows up on the next drain rather
	// than waiting out the worker's idle tick.
	if res.SessionsMarked > 0 {
		s.worker.Wake()
	}
	writeJSON(w, http.StatusOK, providerUsageResponse{
		Inserted:       res.Inserted,
		SessionsMarked: res.SessionsMarked,
	})
}

// handleProviderUsageWatermark reports where a collection should resume.
func (s *Server) handleProviderUsageWatermark(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	if !validProviders[provider] {
		writeError(w, http.StatusBadRequest, "provider must be one of: cursor")
		return
	}
	if accountID == "" {
		writeError(w, http.StatusBadRequest, "account_id is required")
		return
	}
	at, err := s.Store.ProviderUsageWatermark(r.Context(), p.UserID, provider, accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read provider usage watermark")
		return
	}
	var out providerUsageWatermarkResponse
	if !at.IsZero() {
		utc := at.UTC()
		out.LatestEventAt = &utc
	}
	writeJSON(w, http.StatusOK, out)
}
