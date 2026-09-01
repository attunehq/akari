package upload

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ProviderUsageEvent is one vendor-reported billing event on its way to the
// server. It mirrors the ingest endpoint's JSON exactly; the collector fills it
// from whichever vendor package fetched the event.
type ProviderUsageEvent struct {
	EventKey       string    `json:"event_key"`
	ConversationID string    `json:"conversation_id"`
	Model          string    `json:"model"`
	Input          int       `json:"input_tokens"`
	Output         int       `json:"output_tokens"`
	CacheWrite     int       `json:"cache_write_tokens"`
	CacheRead      int       `json:"cache_read_tokens"`
	CostUSD        float64   `json:"cost_usd"`
	CostKnown      bool      `json:"cost_known"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// providerUsageBatch bounds one request body. It matches the server's own cap, so
// the client splits rather than being rejected.
const providerUsageBatch = 2000

// ProviderUsageWatermark asks where a collection should resume: the newest event
// instant the server already holds for this vendor account, or the zero time when
// it holds none.
//
// The client keeps no cursor of its own, exactly as it keeps none for a transcript
// upload. A fresh checkout, a second machine, and a reinstalled client therefore
// all resume from the same place, and the only durable record of what has been
// collected is the server's.
func (c *Client) ProviderUsageWatermark(ctx context.Context, provider, accountID string) (time.Time, error) {
	q := url.Values{"provider": {provider}, "account_id": {accountID}}
	var out struct {
		LatestEventAt *time.Time `json:"latest_event_at"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/ingest/provider-usage/watermark?"+q.Encode(), nil, &out); err != nil {
		return time.Time{}, err
	}
	if out.LatestEventAt == nil {
		return time.Time{}, nil
	}
	return *out.LatestEventAt, nil
}

// SendProviderUsage uploads collected events and reports how many were new.
//
// Events are sent in fixed-size batches rather than one body, so a first
// collection of an account with years of history does not build a single enormous
// request. Each batch is independently idempotent on its event key, so a failure
// part way through leaves the batches that landed stored and the rest to be
// re-collected on the next run.
func (c *Client) SendProviderUsage(ctx context.Context, provider, accountID string, events []ProviderUsageEvent) (int, error) {
	inserted := 0
	for start := 0; start < len(events); start += providerUsageBatch {
		batch := events[start:min(start+providerUsageBatch, len(events))]
		var out struct {
			Inserted int `json:"inserted"`
		}
		body := map[string]any{
			"provider":   provider,
			"account_id": accountID,
			"events":     batch,
		}
		if err := c.doJSON(ctx, http.MethodPost, "/api/v1/ingest/provider-usage", body, &out); err != nil {
			return inserted, fmt.Errorf("send %s usage events: %w", provider, err)
		}
		inserted += out.Inserted
	}
	return inserted, nil
}
