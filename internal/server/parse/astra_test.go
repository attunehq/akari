package parse

import (
	"math"
	"testing"

	"github.com/jssblck/akari/internal/server/store"
)

func TestCodexAstraProjection(t *testing.T) {
	raw := []byte(`{"type":"turn_context","timestamp":"2026-09-04T10:00:00Z","payload":{"model":"gpt-6-astra"}}
{"type":"response_item","timestamp":"2026-09-04T10:00:01Z","payload":{"role":"assistant","content":[{"type":"output_text","text":"Added the tests."}]}}
{"type":"event_msg","timestamp":"2026-09-04T10:00:02Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":8000,"cached_input_tokens":3000,"cache_write_input_tokens":4000,"output_tokens":2000,"reasoning_output_tokens":500}}}}
`)
	r, err := newSessionReducer("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Feed(raw, 0); err != nil {
		t.Fatal(err)
	}
	d := r.Finish()
	if len(d.Usage) != 1 {
		t.Fatalf("usage rows = %d, want 1", len(d.Usage))
	}
	u := d.Usage[0]
	if u.Model != "gpt-6-astra" || !u.ModelNamePublic || u.CostSource != store.CostSourceRateTable {
		t.Errorf("Astra usage should be public and rate-table priced: %+v", u)
	}
	if u.Input != 1000 || u.Output != 2000 || u.CacheRead != 3000 || u.CacheWrite != 4000 || u.Reasoning != 500 {
		t.Errorf("unexpected token split: %+v", u)
	}
	if math.Abs(u.CostUSD-0.163) > 1e-9 {
		t.Errorf("cost = %v, want 0.163 (reasoning included in output)", u.CostUSD)
	}
}
