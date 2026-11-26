package pricing

import (
	"math"
	"testing"
	"time"
)

// anytime is a fixed instant to price single-window models at, where the exact time
// is irrelevant because the model has one rate for all time.
var anytime = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func TestRateAtDatedSnapshotsAndAliases(t *testing.T) {
	// Each model is exercised through its dated release ID and its alias; both
	// must resolve to the same rate via date-snapshot normalization.
	cases := []struct {
		model         string
		input, output float64
	}{
		// Legacy Opus (4.0/4.1) at $15/$75. 4.0's dated ID has no minor number.
		{"claude-opus-4-20250514", 15, 75},
		{"claude-opus-4-0", 15, 75},
		{"claude-opus-4-1-20250805", 15, 75},
		{"claude-opus-4-1", 15, 75},
		// Current Opus (4.5+) at $5/$25, which Opus 5 holds.
		{"claude-opus-4-5-20251101", 5, 25},
		{"claude-opus-4-6", 5, 25},
		{"claude-opus-4-7", 5, 25},
		{"claude-opus-4-8", 5, 25},
		{"claude-opus-5", 5, 25},
		{"claude-opus-5-5", 4, 20},
		// Sonnet at $3/$15 from 3.5 through 4.6, and Sonnet 5 at $2/$10.
		{"claude-sonnet-5", 2, 10},
		{"claude-sonnet-5-5", 2, 10},
		{"claude-sonnet-4-20250514", 3, 15},
		{"claude-sonnet-4-0", 3, 15},
		{"claude-sonnet-4-5-20250929", 3, 15},
		{"claude-sonnet-4-6", 3, 15},
		{"claude-3-7-sonnet-20250219", 3, 15},
		{"claude-3-5-sonnet-20241022", 3, 15},
		// Haiku.
		{"claude-haiku-5-5", 0.10, 0.50},
		{"claude-haiku-4-5-20251001", 1, 5},
		{"claude-3-5-haiku-20241022", 0.80, 4},
	}
	for _, c := range cases {
		r, ok := RateAt(c.model, anytime)
		if !ok || r.Input != c.input || r.Output != c.output {
			t.Errorf("%s rate = %+v (ok=%v), want input %v / output %v", c.model, r, ok, c.input, c.output)
		}
	}
}

func TestGPT56SolDatedWindows(t *testing.T) {
	// GPT-5.6 Sol launched at $5/$30 and dropped to $4/$20 on 2026-08-21. The window
	// selects on the event time, and the boundary is inclusive of the later window
	// (From is its first instant).
	cases := []struct {
		name                       string
		at                         time.Time
		input, output, write, read float64
	}{
		{"launch price", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 5, 30, 6.25, 0.50},
		{"launch last instant", time.Date(2026, 8, 20, 23, 59, 59, 0, time.UTC), 5, 30, 6.25, 0.50},
		{"reprice boundary", gpt56SolReprice, 4, 20, 5, 0.40},
		{"reprice later", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 4, 20, 5, 0.40},
		// An undated event (zero time) selects the earliest window, the same choice
		// parse-time pricing makes for a row with no OccurredAt.
		{"undated selects launch", time.Time{}, 5, 30, 6.25, 0.50},
	}
	for _, c := range cases {
		r, ok := RateAt("gpt-5.6-sol", c.at)
		if !ok {
			t.Errorf("%s: gpt-5.6-sol should be priced", c.name)
			continue
		}
		if r.Input != c.input || r.Output != c.output || r.CacheWrite != c.write || r.CacheRead != c.read {
			t.Errorf("%s: rate = %+v, want %v/%v write %v read %v", c.name, r, c.input, c.output, c.write, c.read)
		}
	}
}

func TestTableWindowsSorted(t *testing.T) {
	// Every model's windows must be From-ascending with a zero-From first window, the
	// shape windowAt relies on: it seeds from the first window and keeps the last whose
	// From is at or before the event time.
	for _, catalog := range []map[string][]DatedRate{table, undisclosedRates} {
		for model, rates := range catalog {
			if len(rates) == 0 {
				t.Errorf("%s: no rate windows", model)
				continue
			}
			if !rates[0].From.IsZero() {
				t.Errorf("%s: first window From = %v, want the zero value (in effect from the beginning)", model, rates[0].From)
			}
			for i := 1; i < len(rates); i++ {
				if !rates[i].From.After(rates[i-1].From) {
					t.Errorf("%s: window %d From %v is not after window %d From %v", model, i, rates[i].From, i-1, rates[i-1].From)
				}
			}
		}
	}
}

func TestDatedWindowsStartAtUTCMidnight(t *testing.T) {
	// Every non-zero window boundary must fall on UTC midnight. The aggregate cache-savings
	// paths (store/analytics_cache.go) price per UTC day, relying on a whole UTC day sitting
	// inside one rate window so the day-bucketed recompute matches the exact per-row fold it
	// reconciles against. A boundary at any other instant (a midday reprice) would split a day
	// across two windows and make the two disagree. This pins that invariant at the table so a
	// future dated rate cannot silently break it; a genuine midday change would need the
	// aggregate paths reworked to bucket on the exact window first.
	for _, catalog := range []map[string][]DatedRate{table, undisclosedRates} {
		for model, rates := range catalog {
			for i, dr := range rates {
				if dr.From.IsZero() {
					continue // the open first window is in effect from the beginning, no boundary
				}
				if dr.From.Location() != time.UTC {
					t.Errorf("%s window %d From %v is not in UTC", model, i, dr.From)
				}
				if !dr.From.Equal(dr.From.Truncate(24 * time.Hour)) {
					t.Errorf("%s window %d From %v is not on a UTC-midnight boundary", model, i, dr.From)
				}
			}
		}
	}
}

func TestRateAtFableAndMythos(t *testing.T) {
	// Every Fable and Mythos model prices at $10/$50 with a $12.50 cache write.
	// Only the cache read differs: 5 and the preview take the standard 0.1x, and
	// 5.1 takes the 0.025x rate that no other Anthropic model uses.
	cases := []struct {
		model string
		read  float64
	}{
		{"claude-fable-5", 1.00},
		{"claude-mythos-5", 1.00},
		{"claude-mythos-preview", 1.00},
		{"claude-fable-5-1", 0.25},
		{"claude-mythos-5-1", 0.25},
	}
	for _, c := range cases {
		r, ok := RateAt(c.model, anytime)
		if !ok || r.Input != 10 || r.Output != 50 || r.CacheWrite != 12.50 || r.CacheRead != c.read {
			t.Errorf("%s rate = %+v (ok=%v), want 10/50 with write 12.50 and read %v", c.model, r, ok, c.read)
		}
	}
}

func TestRateAtGPT(t *testing.T) {
	cases := []struct {
		model         string
		input, output float64
	}{
		{"gpt-5.5", 5, 30},
		{"gpt-5.5-pro", 30, 180},
		{"gpt-5.4", 2.50, 15},
		{"gpt-5.4-mini", 0.75, 4.50},
		{"gpt-5.4-nano", 0.20, 1.25},
		{"gpt-5.4-pro", 30, 180},
		{"gpt-5.3-codex", 1.75, 14},
		{"gpt-5.3-codex-spark", 1.75, 14},
		// GPT-5.2 and GPT-5.1, including their dated base snapshots.
		{"gpt-5.2", 1.75, 14},
		{"gpt-5.2-2025-12-11", 1.75, 14},
		{"gpt-5.2-codex", 1.75, 14},
		{"gpt-5.2-pro", 21, 168},
		{"gpt-5.2-pro-2025-12-11", 21, 168},
		{"gpt-5.1", 1.25, 10},
		{"gpt-5.1-2025-11-13", 1.25, 10},
		{"gpt-5.1-codex", 1.25, 10},
		{"gpt-5.1-codex-max", 1.25, 10},
		{"gpt-5.1-codex-mini", 0.25, 2},
		// Prior generation, including a dated base snapshot that normalizes to gpt-5.
		{"gpt-5", 1.25, 10},
		{"gpt-5-2025-08-07", 1.25, 10},
		{"gpt-5-codex", 1.25, 10},
		{"gpt-5-mini", 0.25, 2},
		{"gpt-5-nano", 0.05, 0.40},
	}
	for _, c := range cases {
		r, ok := RateAt(c.model, anytime)
		if !ok || r.Input != c.input || r.Output != c.output {
			t.Errorf("%s rate = %+v (ok=%v), want input %v / output %v", c.model, r, ok, c.input, c.output)
		}
	}
}

func TestGPT6AstraCost(t *testing.T) {
	for _, tt := range []struct {
		model string
		want  float64
	}{
		{"gpt-6-astra", 0.163},
		{" GPT-6-ASTRA ", 0.163},
		{"gpt-6-astra-2026-09-04", 0.163},
		{"openai/gpt-6-astra", 0.188},
		{"openai-codex/gpt-6-astra", 0.188},
	} {
		t.Run(tt.model, func(t *testing.T) {
			// Codex includes reasoning in output; qualified providers report it separately.
			got, known := Cost(tt.model, anytime, 1000, 2000, 4000, 3000, 500)
			if !known || math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("cost = %v (known=%v), want %v", got, known, tt.want)
			}
			if !ModelNamePublic(tt.model) {
				t.Error("released Astra model should be public")
			}
			if got := CacheSavings(tt.model, anytime, false, 3000, 4000); math.Abs(got-0.017) > 1e-9 {
				t.Errorf("cache savings = %v, want 0.017", got)
			}
		})
	}
}

func TestNewModelRates(t *testing.T) {
	for _, tt := range []struct {
		model                      string
		input, output, write, read float64
	}{
		{"claude-opus-5-5", 4, 20, 5, 0.20},
		{"anthropic/claude-opus-5-5", 4, 20, 5, 0.20},
		{"gpt-6-sol", 2, 10, 2.50, 0.20},
		{"openai-codex/gpt-6-sol", 2, 10, 2.50, 0.20},
		{"openai/gpt-6-sol", 2, 10, 2.50, 0.20},
		{"gpt-6-luna", 0.10, 0.50, 0.125, 0.01},
		{"openai-codex/gpt-6-luna", 0.10, 0.50, 0.125, 0.01},
		{"openai/gpt-6-luna", 0.10, 0.50, 0.125, 0.01},
		{"grok-4.7", 2, 6, 0, 0.50},
		{"xai/grok-4.7", 2, 6, 0, 0.50},
		{"claude-sonnet-5-5", 2, 10, 2.50, 0.10},
		{"anthropic/claude-sonnet-5-5", 2, 10, 2.50, 0.10},
		{"gpt-6.1-sol", 2, 10, 2.50, 0.10},
		{"openai-codex/gpt-6.1-sol", 2, 10, 2.50, 0.10},
		{"openai/gpt-6.1-sol", 2, 10, 2.50, 0.10},
		{"claude-fable-5-2", 8, 40, 10, 0.20},
		{"claude-mythos-5-2", 8, 40, 10, 0.20},
		{"anthropic/claude-fable-5-2", 8, 40, 10, 0.20},
		{"claude-haiku-5-5", 0.10, 0.50, 0.125, 0.01},
		{"anthropic/claude-haiku-5-5", 0.10, 0.50, 0.125, 0.01},
		{"gpt-6.1-terra", 1, 5, 1.25, 0.05},
		{"openai-codex/gpt-6.1-terra", 1, 5, 1.25, 0.05},
		{"openai/gpt-6.1-terra", 1, 5, 1.25, 0.05},
		{"gpt-6.2-astra", 8, 40, 10, 0.40},
		{"openai-codex/gpt-6.2-astra", 8, 40, 10, 0.40},
		{"openai/gpt-6.2-astra", 8, 40, 10, 0.40},
		{"gpt-6.2-sol", 2, 10, 2.50, 0.10},
		{"openai-codex/gpt-6.2-sol", 2, 10, 2.50, 0.10},
		{"openai/gpt-6.2-sol", 2, 10, 2.50, 0.10},
		// Pre-5.6 OpenAI models bill no cache write, so write stays zero.
		{"gpt-5.2", 1.75, 14, 0, 0.175},
		{"gpt-5.2-codex", 1.75, 14, 0, 0.175},
		{"openai-codex/gpt-5.2-codex", 1.75, 14, 0, 0.175},
		{"gpt-5.2-pro", 21, 168, 0, 0},
		{"gpt-5.1", 1.25, 10, 0, 0.125},
		{"gpt-5.1-codex", 1.25, 10, 0, 0.125},
		{"gpt-5.1-codex-max", 1.25, 10, 0, 0.125},
		{"openai-codex/gpt-5.1-codex-max", 1.25, 10, 0, 0.125},
		{"gpt-5.1-codex-mini", 0.25, 2, 0, 0.025},
		{"grok-build-0.1", 1, 2, 0, 0.20},
		{"xai/grok-build-0.1", 1, 2, 0, 0.20},
		{"grok-4.3", 1.25, 2.50, 0, 0.20},
		{"xai/grok-4.3", 1.25, 2.50, 0, 0.20},
		{"grok-4.20-0309-reasoning", 1.25, 2.50, 0, 0.20},
		{"grok-4.20-0309-non-reasoning", 1.25, 2.50, 0, 0.20},
		{"grok-4.20-multi-agent-0309", 1.25, 2.50, 0, 0.20},
		{"zai/glm-5.1", 1.40, 4.40, 0, 0.26},
		{"zai/glm-5", 1, 3.20, 0, 0.20},
		{"zai/glm-4.7", 0.60, 2.20, 0, 0.11},
		{"zai/glm-4.7-flashx", 0.07, 0.40, 0, 0.01},
		{"zai/glm-4.6", 0.60, 2.20, 0, 0.11},
		{"zai/glm-4.5", 0.60, 2.20, 0, 0.11},
		{"zai/glm-4.5-x", 2.20, 8.90, 0, 0.45},
		{"zai/glm-4.5-air", 0.20, 1.10, 0, 0.03},
		{"zai/glm-4.5-airx", 1.10, 4.50, 0, 0.22},
	} {
		t.Run(tt.model, func(t *testing.T) {
			r, ok := RateAt(tt.model, anytime)
			if !ok || r.Input != tt.input || r.Output != tt.output || r.CacheWrite != tt.write || r.CacheRead != tt.read {
				t.Errorf("rate = %+v (ok=%v), want %v/%v write %v read %v", r, ok, tt.input, tt.output, tt.write, tt.read)
			}
			if !ModelNamePublic(tt.model) {
				t.Error("released model should be public")
			}
		})
	}
	for _, tt := range []struct {
		model string
		want  float64
	}{
		{"claude-opus-5-5", 0.0596},
		{"gpt-6-sol", 0.0301},
		{"gpt-6-luna", 0.001505},
		{"grok-4.7", 0.0155},
		{"claude-sonnet-5-5", 0.0298},
		{"gpt-6.1-sol", 0.0298},
		{"claude-fable-5-2", 0.1186},
		{"claude-haiku-5-5", 0.001505},
		{"gpt-6.1-terra", 0.0149},
		{"gpt-6.2-astra", 0.1192},
		{"gpt-6.2-sol", 0.0298},
		{"gpt-5.2-codex", 0.030275},
		{"gpt-5.1-codex", 0.021625},
		{"gpt-5.1-codex-mini", 0.004325},
		{"grok-build-0.1", 0.0056},
		{"grok-4.3", 0.00685},
		{"zai/glm-5", 0.008},
	} {
		got, known := Cost(tt.model, anytime, 1000, 2000, 3000, 3000, 0)
		if !known || math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("%s cost = %v (known=%v), want %v", tt.model, got, known, tt.want)
		}
	}
	// Direct OpenAI providers can report reasoning separately; Codex includes it
	// in output and must not add a second reasoning charge.
	if got, known := Cost("openai/gpt-6-sol", anytime, 1000, 2000, 3000, 3000, 500); !known || math.Abs(got-0.0351) > 1e-9 {
		t.Errorf("qualified GPT-6 Sol cost = %v (known=%v), want 0.0351", got, known)
	}
	if got, known := Cost("openai/gpt-6.1-sol", anytime, 1000, 2000, 3000, 3000, 500); !known || math.Abs(got-0.0348) > 1e-9 {
		t.Errorf("qualified GPT-6.1 Sol cost = %v (known=%v), want 0.0348", got, known)
	}
	if got, known := Cost("openai/gpt-6.2-astra", anytime, 1000, 2000, 3000, 3000, 500); !known || math.Abs(got-0.1392) > 1e-9 {
		t.Errorf("qualified GPT-6.2 Astra cost = %v (known=%v), want 0.1392", got, known)
	}
}

func TestProviderSpecificRates(t *testing.T) {
	cases := []struct {
		model                 string
		input, output, cached float64
	}{
		{"deepseek/deepseek-v4-flash", 0.14, 0.28, 0.0028},
		{"opencode/deepseek-v4-flash", 0.14, 0.28, 0.028},
		{"opencode-go/deepseek-v4-flash", 0.22, 0.66, 0.007},
		{"openrouter/deepseek/deepseek-v4-flash", 0.0868, 0.1736, 0.01736},
		{"openai/gpt-5.6-sol", 5, 30, 0.50},
		{"opencode/gpt-5.6-sol", 2, 10, 0.20},
	}
	for _, c := range cases {
		r, ok := RateAt(c.model, anytime)
		if !ok || r.Input != c.input || r.Output != c.output || r.Reasoning != c.output || r.CacheRead != c.cached {
			t.Errorf("%s rate = %+v (ok=%v), want %v/%v cached %v", c.model, r, ok, c.input, c.output, c.cached)
		}
	}
}

func TestGLM53FlashPromotion(t *testing.T) {
	for _, model := range []string{
		"zai/glm-5.3-flash",
		"openrouter/z-ai/glm-5.3-flash",
		"opencode-go/glm-5.3-flash",
	} {
		promo, ok := RateAt(model, time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC))
		if !ok || promo.Input != 0.075 || promo.Output != 0.25 || promo.CacheRead != 0.015 {
			t.Errorf("%s promo rate = %+v (ok=%v)", model, promo, ok)
		}
		sticker, ok := RateAt(model, glm53FlashSticker)
		if !ok || sticker.Input != 0.15 || sticker.Output != 0.50 || sticker.CacheRead != 0.03 {
			t.Errorf("%s sticker rate = %+v (ok=%v)", model, sticker, ok)
		}
	}
}

func TestGLM53FlashX(t *testing.T) {
	for _, model := range []string{"zai/glm-5.3-flashx", "openrouter/z-ai/glm-5.3-flashx"} {
		r, ok := RateAt(model, anytime)
		if !ok || r.Input != 0.37 || r.Output != 1.25 || r.CacheRead != 0.075 || r.CacheWrite != 0 {
			t.Errorf("%s rate = %+v (ok=%v), want $0.37/$1.25 cached $0.075", model, r, ok)
		}
		if r.Reasoning != r.Output {
			t.Errorf("%s reasoning rate = %v, want output rate %v", model, r.Reasoning, r.Output)
		}
	}
	// Exact matching: FlashX must not inherit Flash's cheaper rate or vice versa.
	flash, _ := RateAt("zai/glm-5.3-flash", glm53FlashSticker)
	if flash.Input != 0.15 {
		t.Errorf("zai/glm-5.3-flash input = %v after adding FlashX, want 0.15", flash.Input)
	}
}

func TestDirectProviderFallback(t *testing.T) {
	r, ok := RateAt("anthropic/claude-opus-4-8", anytime)
	if !ok || r.Input != 5 || r.Output != 25 {
		t.Errorf("direct Anthropic rate = %+v (ok=%v)", r, ok)
	}
	if r.Reasoning != r.Output {
		t.Errorf("provider-qualified Anthropic reasoning rate = %v, want output rate %v", r.Reasoning, r.Output)
	}
	r, ok = RateAt("openai-codex/gpt-5.3-codex-spark", anytime)
	if !ok || r.Input != 1.75 || r.Output != 14 {
		t.Errorf("Codex provider rate = %+v (ok=%v)", r, ok)
	}
	if _, ok := RateAt("unlisted-router/claude-opus-4-8", anytime); ok {
		t.Error("an unlisted router must not inherit the direct Anthropic rate")
	}

	// OpenAI's own API (OpenCode and pi record it as openai/<model>) prices every
	// model at its list rate, which the unqualified keys already hold. Reasoning
	// fills from output, as on every qualified route.
	for _, c := range []struct {
		model                 string
		input, output, cached float64
	}{
		{"openai/gpt-5.5", 5, 30, 0.50},
		{"openai/gpt-5.4-mini", 0.75, 4.50, 0.075},
		{"openai/gpt-5.2", 1.75, 14, 0.175},
		{"openai/gpt-5.1-codex", 1.25, 10, 0.125},
		{"openai/gpt-5-2025-08-07", 1.25, 10, 0.125},
	} {
		r, ok := RateAt(c.model, anytime)
		if !ok || r.Input != c.input || r.Output != c.output || r.CacheRead != c.cached || r.Reasoning != c.output {
			t.Errorf("RateAt(%q) = %+v (ok=%v), want %v/%v cached %v reasoning %v", c.model, r, ok, c.input, c.output, c.cached, c.output)
		}
	}
	// The fallback carries the long-context tier: a 300K-token prompt on
	// openai/gpt-5.5 pays 2x input and 1.5x output, reasoning at the long output rate.
	if got, ok := Cost("openai/gpt-5.5", anytime, 300_000, 1_000, 0, 0, 1_000); !ok || math.Abs(got-(3+0.045+0.045)) > 1e-9 {
		t.Errorf("long openai/gpt-5.5 cost = %v (ok=%v), want 3.09", got, ok)
	}
	if !ModelNamePublic("openai/gpt-5.5") {
		t.Error("openai/gpt-5.5 is a released model and should be public")
	}
	// Unlisted models stay unknown on the direct route too, and routers that resell
	// OpenAI models never inherit its list price.
	for _, model := range []string{"openai/gpt-5.7", "openai/gpt-6.1-luna", "opencode/gpt-5.5", "openrouter/openai/gpt-5.5"} {
		if r, ok := RateAt(model, anytime); ok {
			t.Errorf("%q priced as %+v; expected unknown", model, r)
		}
	}
}

func TestGPT56DatedWindows(t *testing.T) {
	cases := []struct {
		name                       string
		model                      string
		at                         time.Time
		input, output, write, read float64
	}{
		{"sol before reprice", "gpt-5.6-sol", gpt56SolReprice.Add(-time.Nanosecond), 5, 30, 6.25, 0.50},
		{"sol reprice boundary", "gpt-5.6-sol", gpt56SolReprice, 4, 20, 5, 0.40},
		{"sol alias before reprice", "gpt-5.6", gpt56SolReprice.Add(-time.Nanosecond), 5, 30, 6.25, 0.50},
		{"sol alias reprice boundary", "gpt-5.6", gpt56SolReprice, 4, 20, 5, 0.40},
		{"terra before reprice", "gpt-5.6-terra", gpt56LunaTerraReprice.Add(-time.Nanosecond), 2.50, 15, 3.125, 0.25},
		{"terra reprice boundary", "gpt-5.6-terra", gpt56LunaTerraReprice, 2, 12, 2.50, 0.20},
		{"luna before reprice", "gpt-5.6-luna", gpt56LunaTerraReprice.Add(-time.Nanosecond), 1, 6, 1.25, 0.10},
		{"luna reprice boundary", "gpt-5.6-luna", gpt56LunaTerraReprice, 0.20, 1.20, 0.25, 0.02},
		{"undated sol keeps launch price", "gpt-5.6-sol", time.Time{}, 5, 30, 6.25, 0.50},
		{"undated terra keeps launch price", "gpt-5.6-terra", time.Time{}, 2.50, 15, 3.125, 0.25},
		{"undated luna keeps launch price", "gpt-5.6-luna", time.Time{}, 1, 6, 1.25, 0.10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := RateAt(c.model, c.at)
			if !ok {
				t.Fatalf("%s should be priced", c.model)
			}
			if r.Input != c.input || r.Output != c.output || r.CacheWrite != c.write || r.CacheRead != c.read {
				t.Errorf("rate = %+v, want %v/%v write %v read %v", r, c.input, c.output, c.write, c.read)
			}
		})
	}
}

func TestDateSnapshotNormalization(t *testing.T) {
	// Both date formats strip to the same canonical key; a non-date suffix (a
	// variant) is left intact so it is matched (or not) on its own.
	if r, ok := RateAt("claude-opus-4-8-20260115", anytime); !ok || r.Input != 5 {
		t.Errorf("Anthropic dated form not normalized: %+v (ok=%v)", r, ok)
	}
	if r, ok := RateAt("gpt-5-2025-08-07", anytime); !ok || r.Input != 1.25 {
		t.Errorf("OpenAI dated form not normalized: %+v (ok=%v)", r, ok)
	}
	// A variant suffix is not a date and must not be stripped: gpt-5.4-mini stays
	// gpt-5.4-mini (its own rate), and an unlisted variant stays unknown rather
	// than collapsing onto the base model.
	if r, ok := RateAt("gpt-5.4-mini", anytime); !ok || r.Input != 0.75 {
		t.Errorf("variant suffix wrongly altered: %+v (ok=%v)", r, ok)
	}
}

func TestUnlistedModelsAreUnknown(t *testing.T) {
	// Plausible future or sibling models we have deliberately NOT priced. Each
	// must report unknown rather than inherit a neighbor's rate. Because matching
	// is exact, this now covers same-version
	// variants too (gpt-5.4-turbo no longer collapses onto gpt-5.4), which prefix
	// matching could not guard.
	for _, model := range []string{
		"claude-opus-4-9", "claude-opus-5-0", "claude-opus-5-1",
		"claude-sonnet-4-7",
		"claude-haiku-4-9", "claude-haiku-5",
		"claude-fable-6", "claude-mythos-6",
		"gpt-5.7", "gpt-6", "gpt-7",
		"gpt-6-astra-pro", "gpt-6-astra-secret-eap",
		"gpt-6.1", "gpt-6.1-luna", "gpt-6.1-astra", // 6.1 shipped only Sol and Terra
		"gpt-6.2", "gpt-6.2-luna", "gpt-6.2-terra", // 6.2 shipped only Astra and Sol
		"gpt-6.2-astra-law", "gpt-6-cyber", // specialized models stay unpriced
		"opencode/gpt-6-astra", "openrouter/openai/gpt-6-astra",
		"gpt-5.4-turbo", "gpt-5.5-ultra", // same-version variants we never priced
		"gpt-5.6-mini", "gpt-5.6-nano", // GPT-5.6's real tiers are sol/terra/luna, not mini/nano
		"gpt-5.1-mini", "gpt-5.2-codex-mini", "gpt-5.2-codex-max", // tiers 5.1 and 5.2 never shipped
		"gpt-5.1-chat-latest", "gpt-5.2-chat-latest", // ChatGPT aliases, not Codex models
		"grok-build", "grok-build-0.2", "openrouter/x-ai/grok-build-0.1",
		"grok-4.7-latest", "grok-4.20", "grok-4.20-reasoning", // moving aliases and dateless 4.20 names
		"zai/glm-4.7-flash", "zai/glm-4.5-flash", "zai/glm-5.4", // free tiers, and a model with no published price
		"openrouter/z-ai/glm-5.1", "opencode/glm-5", // routers do not inherit Z.AI's direct rate
	} {
		if r, ok := RateAt(model, anytime); ok {
			t.Errorf("unlisted model %q priced as %+v; expected unknown", model, r)
		}
	}
}

func TestRateAtUnknown(t *testing.T) {
	if _, ok := RateAt("some-future-model", anytime); ok {
		t.Error("unknown model should not be priced")
	}
	if _, ok := RateAt("", anytime); ok {
		t.Error("empty model should not be priced")
	}
	// A bare date-like string normalizes to empty and must not panic or match.
	if _, ok := RateAt("20250514", anytime); ok {
		t.Error("bare token should not be priced")
	}
}

func TestModelNamePublicIsExactAndFailClosed(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-opus-4-8", true},
		{"claude-opus-4-8-20260115", true},
		{"anthropic/claude-opus-4-8", true},
		{"claude-mythos-preview", false},
		{"gpt-5.6-secret-eap", false},
		{"gpt-6", false},
		{"gpt-6-astra-secret-eap", false},
		{"openrouter/openai/gpt-6-astra", false},
		{"<synthetic>", false},
		{"", false},
	}
	for _, tt := range cases {
		if got := ModelNamePublic(tt.model); got != tt.want {
			t.Errorf("ModelNamePublic(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestCost(t *testing.T) {
	// 1M input + 1M output on Sonnet 4.0 (dated ID) at 3 + 15 per million.
	cost, known := Cost("claude-sonnet-4-20250514", anytime, 1_000_000, 1_000_000, 0, 0, 0)
	if !known || math.Abs(cost-18.0) > 1e-9 {
		t.Errorf("cost = %v, want 18", cost)
	}

	// All four token classes contribute.
	cost, known = Cost("claude-sonnet-4-5", anytime, 1_000_000, 0, 1_000_000, 1_000_000, 0)
	want := 3.0 + 3.75 + 0.30
	if !known || math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", cost, want)
	}

	if got, known := Cost("mystery-model", anytime, 100, 100, 0, 0, 0); known || got != 0 {
		t.Errorf("unknown model cost = %v (known=%v), want 0 and false", got, known)
	}

	// OpenCode records reasoning apart from output and bills both at the model's
	// output rate. One million of each on the Zen Grok route costs $12 total.
	if got, known := Cost("opencode/grok-4.6", anytime, 0, 1_000_000, 0, 0, 1_000_000); !known || got != 12 {
		t.Errorf("OpenCode output plus reasoning cost = %v, want 12", got)
	}
}

func TestCostSelectsDatedWindow(t *testing.T) {
	// The same 100k input + 100k output on GPT-5.6 Sol prices at the launch rate before
	// the 2026-08-21 reprice and at the reduced rate from the boundary on. The prompt
	// volume stays under the long-context threshold so the test pins the dated window,
	// not the tier (TestCostLongContextOnlyAfterReprice covers their interaction).
	launch, _ := Cost("gpt-5.6-sol", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), 100_000, 100_000, 0, 0, 0)
	if math.Abs(launch-3.50) > 1e-9 {
		t.Errorf("launch cost = %v, want 3.50 (0.5 + 3)", launch)
	}
	reduced, _ := Cost("gpt-5.6-sol", gpt56SolReprice, 100_000, 100_000, 0, 0, 0)
	if math.Abs(reduced-2.40) > 1e-9 {
		t.Errorf("reduced cost = %v, want 2.40 (0.4 + 2)", reduced)
	}
}

func TestCostIsArchitectureStable(t *testing.T) {
	got, _ := Cost("claude-sonnet-4-20250514", anytime, 1500, 120, 0, 6000, 0)
	if got != 0.0081 {
		t.Errorf("cost = %.18f, want exact rounded value %.18f", got, 0.0081)
	}
}

func TestCacheSavings(t *testing.T) {
	// Opus 4.8: Input 5, CacheRead 0.50, CacheWrite 6.25 per million.
	// A cache read saves the full input-minus-read gap; a cache write costs the
	// write-minus-input premium (negative saving), the price paid to make reads cheap.
	cases := []struct {
		name        string
		model       string
		read, write int64
		wantSaving  float64
	}{
		// 1M read alone: 1 * (5 - 0.50) = 4.50.
		{"read only", "claude-opus-4-8", 1_000_000, 0, 4.50},
		// 1M write alone: 1 * (5 - 6.25) = -1.25, caching costs more than it saved.
		{"write only is negative", "claude-opus-4-8", 0, 1_000_000, -1.25},
		// Both: 4.50 - 1.25 = 3.25.
		{"read and write net", "claude-opus-4-8", 1_000_000, 1_000_000, 3.25},
		// OpenAI bills no separate cache write (CacheWrite rate is 0), so a read saves
		// the input-minus-read gap and the parser reports no write tokens to charge.
		{"openai read", "gpt-5.5", 1_000_000, 0, 4.50},
		{"unknown model", "secret-model", 1_000_000, 0, 0},
	}
	for _, c := range cases {
		saving := CacheSavings(c.model, anytime, false, c.read, c.write)
		if math.Abs(saving-c.wantSaving) > 1e-9 {
			t.Errorf("%s: saving = %v, want %v", c.name, saving, c.wantSaving)
		}
	}
}

func TestCacheSavingsSelectsDatedWindow(t *testing.T) {
	// GPT-5.6 Sol at launch (Input 5, CacheRead 0.50): 1M read saves 1 * (5 - 0.50) = 4.50.
	// After the reprice (Input 4, CacheRead 0.40): 1M read saves 1 * (4 - 0.40) = 3.60.
	launch := CacheSavings("gpt-5.6-sol", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), false, 1_000_000, 0)
	if math.Abs(launch-4.50) > 1e-9 {
		t.Errorf("launch saving = %v, want 4.50", launch)
	}
	reduced := CacheSavings("gpt-5.6-sol", gpt56SolReprice, false, 1_000_000, 0)
	if math.Abs(reduced-3.60) > 1e-9 {
		t.Errorf("reduced saving = %v, want 3.60", reduced)
	}
}

func TestIsLongContext(t *testing.T) {
	// Each model carries its own cutoff, and each cutoff is tested exactly at the
	// line and one token past it. Both published boundaries are strict ("over"):
	// GPT-5.6 at 272,000 and Haiku 5.5 at 100,000 stay short, one more token prices
	// the whole request long. Every prompt-side class counts toward the volume;
	// output never does (Cost passes it separately and IsLongContext never sees it).
	gpt := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name               string
		model              string
		at                 time.Time
		input, read, write int64
		want               bool
	}{
		{"gpt nothing", "gpt-5.6-sol", gpt, 0, 0, 0, false},
		{"gpt input at the line", "gpt-5.6-sol", gpt, 272_000, 0, 0, false},
		{"gpt input one over", "gpt-5.6-sol", gpt, 272_001, 0, 0, true},
		{"gpt split classes at the line", "gpt-5.6-terra", gpt, 100_000, 100_000, 72_000, false},
		{"gpt split classes one over", "gpt-5.6-terra", gpt, 100_000, 100_000, 72_001, true},
		{"gpt cache reads alone cross it", "gpt-5.6-luna", gpt, 0, 272_001, 0, true},
		{"gpt cache writes alone cross it", "gpt-5.6", gpt, 0, 0, 272_001, true},
		{"gpt qualified route shares the tier", "openai/gpt-5.6-sol", gpt, 272_001, 0, 0, true},
		{"gpt launch window has no tier", "gpt-5.6-sol", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), 300_000, 0, 0, false},
		{"gpt opencode route publishes no tier", "opencode/gpt-5.6-sol", gpt, 300_000, 0, 0, false},
		{"gpt-6 at the line", "gpt-6.2-sol", gpt, 272_000, 0, 0, false},
		{"gpt-6 one over", "gpt-6.2-sol", gpt, 272_001, 0, 0, true},
		{"gpt-6 tier from launch", "gpt-6-astra", time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), 272_001, 0, 0, true},
		{"gpt-5.4 one over", "gpt-5.4", gpt, 200_000, 72_001, 0, true},
		{"gpt-5.4-mini publishes no tier", "gpt-5.4-mini", gpt, 1_000_000, 0, 0, false},
		{"haiku at the line", "claude-haiku-5-5", gpt, 100_000, 0, 0, false},
		{"haiku one over", "claude-haiku-5-5", gpt, 100_001, 0, 0, true},
		{"haiku cache counts toward the prompt", "claude-haiku-5-5", gpt, 1, 90_000, 10_000, true},
		{"haiku dated snapshot normalizes", "claude-haiku-5-5-20261101", gpt, 100_001, 0, 0, true},
		{"gpt cutoff does not leak to haiku's neighbor", "claude-haiku-4-5", gpt, 300_000, 0, 0, false},
		{"untiered model", "claude-opus-4-8", gpt, 1_000_000, 0, 0, false},
		{"unknown model", "not-a-model", gpt, 1_000_000, 0, 0, false},
	}
	for _, c := range cases {
		if got := IsLongContext(c.model, c.at, c.input, c.read, c.write); got != c.want {
			t.Errorf("%s: IsLongContext(%s, %d, %d, %d) = %v, want %v", c.name, c.model, c.input, c.read, c.write, got, c.want)
		}
	}
}

func TestLongContextInclusiveBoundary(t *testing.T) {
	// A provider that tiers "at or above" n (xAI words Grok 4.7's tier as 200K or
	// more) writes MinPrompt: n directly, so n itself is long and n-1 short. over(n)
	// is the strict form and lands one token later.
	inclusive := DatedRate{Long: &LongContext{MinPrompt: 200_000}}
	if inclusive.longContext(199_999, 0, 0) {
		t.Error("inclusive tier: 199,999 should price short")
	}
	if !inclusive.longContext(200_000, 0, 0) {
		t.Error("inclusive tier: 200,000 should price long")
	}
	strict := DatedRate{Long: over(200_000, Rate{})}
	if strict.longContext(200_000, 0, 0) {
		t.Error("strict tier: 200,000 should price short")
	}
	if !strict.longContext(200_001, 0, 0) {
		t.Error("strict tier: 200,001 should price long")
	}
	if (DatedRate{}).longContext(10_000_000, 0, 0) {
		t.Error("a window without a tier is never long")
	}
}

func TestTableLongContextTiers(t *testing.T) {
	// Every tier the table declares must be a real tier: a positive cutoff and a
	// nonzero rate. A zero cutoff would price every request long, and an empty rate
	// would price a long request at zero.
	for _, catalog := range []map[string][]DatedRate{table, undisclosedRates} {
		for model, rates := range catalog {
			for i, w := range rates {
				if w.Long == nil {
					continue
				}
				if w.Long.MinPrompt <= 0 {
					t.Errorf("%s window %d: long-context cutoff %d, want positive", model, i, w.Long.MinPrompt)
				}
				if w.Long.Rate == (Rate{}) {
					t.Errorf("%s window %d: long-context tier has no rate", model, i)
				}
			}
		}
	}
}

func TestCostHaikuLongContext(t *testing.T) {
	// Claude Haiku 5.5: up to 100,000 prompt tokens $0.10/$0.50 (write $0.125, read
	// $0.01); over it $0.50/$2.50 (write $0.625, read $0.05) for the whole request.
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	short, ok := Cost("claude-haiku-5-5", at, 50_000, 2_000, 10_000, 40_000, 0)
	if !ok {
		t.Fatal("claude-haiku-5-5 should be priced")
	}
	if want := 50_000/1e6*0.10 + 2_000/1e6*0.50 + 10_000/1e6*0.125 + 40_000/1e6*0.01; math.Abs(short-want) > 1e-9 {
		t.Errorf("100,000-prompt cost = %v, want short-tier %v", short, want)
	}
	long, _ := Cost("claude-haiku-5-5", at, 50_001, 2_000, 10_000, 40_000, 0)
	if want := 50_001/1e6*0.50 + 2_000/1e6*2.50 + 10_000/1e6*0.625 + 40_000/1e6*0.05; math.Abs(long-want) > 1e-9 {
		t.Errorf("100,001-prompt cost = %v, want long-tier %v", long, want)
	}
	if got := CacheSavings("claude-haiku-5-5", at, true, 1_000_000, 0); math.Abs(got-0.45) > 1e-9 {
		t.Errorf("long read saving = %v, want 0.45 (0.50 - 0.05)", got)
	}
}

func TestCostLongContextBoundary(t *testing.T) {
	// GPT-5.6 Sol after the 2026-08-21 reprice: short $4/$20 write $5 read $0.40,
	// long $8/$30 write $10 read $0.80, applied to the FULL request. 272,000 total
	// prompt tokens prices short; 272,001 prices long. The cost doubles on one token.
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	short, ok := Cost("gpt-5.6-sol", at, 272_000, 1_000, 0, 0, 0)
	if !ok {
		t.Fatal("gpt-5.6-sol should be priced")
	}
	if want := 272_000/1e6*4 + 1_000/1e6*20; math.Abs(short-want) > 1e-9 {
		t.Errorf("272000-prompt cost = %v, want short-tier %v", short, want)
	}
	long, _ := Cost("gpt-5.6-sol", at, 272_001, 1_000, 0, 0, 0)
	if want := 272_001/1e6*8 + 1_000/1e6*30; math.Abs(long-want) > 1e-9 {
		t.Errorf("272001-prompt cost = %v, want long-tier %v", long, want)
	}

	// Every token class takes the tier rate, not just uncached input: cache reads
	// and cache writes cross the line too and price at the long cached/write rates.
	mixed, _ := Cost("gpt-5.6-sol", at, 100_000, 1_000, 72_001, 100_000, 0)
	if want := 100_000/1e6*8 + 1_000/1e6*30 + 72_001/1e6*10 + 100_000/1e6*0.80; math.Abs(mixed-want) > 1e-9 {
		t.Errorf("mixed long-tier cost = %v, want %v (long input/output/write/read)", mixed, want)
	}
	// The same split one token shorter stays entirely short.
	mixedShort, _ := Cost("gpt-5.6-sol", at, 100_000, 1_000, 72_000, 100_000, 0)
	if want := 100_000/1e6*4 + 1_000/1e6*20 + 72_000/1e6*5 + 100_000/1e6*0.40; math.Abs(mixedShort-want) > 1e-9 {
		t.Errorf("mixed short-tier cost = %v, want %v (short input/output/write/read)", mixedShort, want)
	}
}

func TestCostLongContextAllGPT56Models(t *testing.T) {
	// Sol, the gpt-5.6 alias, Terra, and Luna each carry the published long rates
	// (input, cached input, cache write, output per million) in the current window.
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		model                      string
		input, read, write, output float64
	}{
		{"gpt-5.6-sol", 8, 0.80, 10, 30},
		{"gpt-5.6", 8, 0.80, 10, 30},
		{"gpt-5.6-terra", 4, 0.40, 5, 18},
		{"gpt-5.6-luna", 0.40, 0.04, 0.50, 1.80},
		{"openai/gpt-5.6-sol", 8, 0.80, 10, 30},
		{"openai/gpt-5.6", 8, 0.80, 10, 30},
		{"openai/gpt-5.6-terra", 4, 0.40, 5, 18},
		{"openai/gpt-5.6-luna", 0.40, 0.04, 0.50, 1.80},
	}
	for _, c := range cases {
		// 300k prompt tokens: 100k uncached, 120k cached reads, 80k writes, plus
		// 10k output, all over the line.
		got, ok := Cost(c.model, at, 100_000, 10_000, 80_000, 120_000, 0)
		if !ok {
			t.Errorf("%s should be priced", c.model)
			continue
		}
		want := 100_000/1e6*c.input + 120_000/1e6*c.read + 80_000/1e6*c.write + 10_000/1e6*c.output
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s long-tier cost = %v, want %v", c.model, got, want)
		}
	}
}

func TestCostLongContextOpenAIFromLaunch(t *testing.T) {
	// GPT-6.x, GPT-5.5, GPT-5.4, and the two -pro tiers price a prompt over 272K at
	// 2x input and cache rates and 1.5x output for the whole request, from launch.
	// Rates per million: input, cached read, 30-minute cache write, output. The
	// GPT-5.x models bill no cache write and the -pro tiers no cached read.
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		model                      string
		input, read, write, output float64
	}{
		{"gpt-6.2-astra", 16, 0.80, 20, 60},
		{"gpt-6.2-sol", 4, 0.20, 5, 15},
		{"gpt-6.1-terra", 2, 0.10, 2.50, 7.50},
		{"gpt-6.1-sol", 4, 0.20, 5, 15},
		{"gpt-6-astra", 20, 2, 25, 75},
		{"gpt-6-sol", 4, 0.40, 5, 15},
		{"gpt-6-luna", 0.20, 0.02, 0.25, 0.75},
		{"openai/gpt-6.2-astra", 16, 0.80, 20, 60},
		{"openai/gpt-6-luna", 0.20, 0.02, 0.25, 0.75},
		{"gpt-5.5", 10, 1, 0, 45},
		{"gpt-5.4", 5, 0.50, 0, 22.50},
		{"gpt-5.5-pro", 60, 0, 0, 270},
		{"gpt-5.4-pro", 60, 0, 0, 270},
	}
	for _, c := range cases {
		// 300k prompt tokens over the line, split across the classes each model bills.
		input, read, write := 300_000, 0, 0
		if c.read > 0 {
			input, read = 100_000, 200_000
		}
		if c.write > 0 {
			input, write = 50_000, 50_000
		}
		got, ok := Cost(c.model, at, input, 10_000, write, read, 0)
		if !ok {
			t.Errorf("%s should be priced", c.model)
			continue
		}
		want := float64(input)/1e6*c.input + float64(read)/1e6*c.read + float64(write)/1e6*c.write + 10_000/1e6*c.output
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s long-tier cost = %v, want %v", c.model, got, want)
		}
	}
}

func TestCostLongContextQualifiedReasoning(t *testing.T) {
	// The qualified openai/ routes bill reasoning at the output rate, so the long
	// tier prices reasoning at the LONG output rate too.
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got, ok := Cost("openai/gpt-5.6-sol", at, 300_000, 0, 0, 0, 10_000)
	if !ok {
		t.Fatal("openai/gpt-5.6-sol should be priced")
	}
	if want := 300_000/1e6*8 + 10_000/1e6*30; math.Abs(got-want) > 1e-9 {
		t.Errorf("long-tier reasoning cost = %v, want %v (reasoning at long output 30)", got, want)
	}
}

func TestCostLongContextOnlyAfterReprice(t *testing.T) {
	// OpenAI introduced the tier with the 2026 rate reductions. A long request in
	// the launch window prices at the launch short rate (5/30 write 6.25 read
	// 0.50): the launch windows carry no Long rate.
	launch := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	got, ok := Cost("gpt-5.6-sol", launch, 300_000, 1_000, 0, 0, 0)
	if !ok {
		t.Fatal("gpt-5.6-sol should be priced")
	}
	if want := 300_000/1e6*5 + 1_000/1e6*30; math.Abs(got-want) > 1e-9 {
		t.Errorf("launch-window long-prompt cost = %v, want launch short rate %v", got, want)
	}
	// Terra and Luna likewise keep their pre-reprice rates before 2026-07-30.
	got, _ = Cost("gpt-5.6-terra", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), 300_000, 0, 0, 0, 0)
	if want := 300_000 / 1e6 * 2.50; math.Abs(got-want) > 1e-9 {
		t.Errorf("terra launch-window long-prompt cost = %v, want %v", got, want)
	}
}

func TestRateAtTierFallsBackWithoutLongRate(t *testing.T) {
	// A model with no tier in any window prices a long-context request at its
	// short rate, so a caller passing the flag cannot invent a tier.
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	short, _ := RateAtTier("claude-opus-4-8", at, false)
	long, _ := RateAtTier("claude-opus-4-8", at, true)
	if short != long {
		t.Errorf("claude-opus-4-8 tier rate = %+v, want short rate %+v (no long tier)", long, short)
	}
	// GPT-5.6's launch window has no tier either.
	launchShort, _ := RateAtTier("gpt-5.6-sol", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), false)
	launchLong, _ := RateAtTier("gpt-5.6-sol", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), true)
	if launchShort != launchLong {
		t.Errorf("launch-window tier rate = %+v, want launch short rate %+v", launchLong, launchShort)
	}
}

func TestCacheSavingsLongContext(t *testing.T) {
	// Post-reprice Sol: a short event's cached read saves 4 - 0.40 = 3.60/M, a long
	// event's saves 8 - 0.80 = 7.20/M. Writes flip sign in both tiers: the write
	// premium is input-minus-write (4 - 5 = -1 short, 8 - 10 = -2 long).
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if got := CacheSavings("gpt-5.6-sol", at, false, 1_000_000, 0); math.Abs(got-3.60) > 1e-9 {
		t.Errorf("short read saving = %v, want 3.60", got)
	}
	if got := CacheSavings("gpt-5.6-sol", at, true, 1_000_000, 0); math.Abs(got-7.20) > 1e-9 {
		t.Errorf("long read saving = %v, want 7.20", got)
	}
	if got := CacheSavings("gpt-5.6-sol", at, false, 0, 1_000_000); math.Abs(got-(-1.00)) > 1e-9 {
		t.Errorf("short write saving = %v, want -1.00", got)
	}
	if got := CacheSavings("gpt-5.6-sol", at, true, 0, 1_000_000); math.Abs(got-(-2.00)) > 1e-9 {
		t.Errorf("long write saving = %v, want -2.00", got)
	}
	// The flag does not invent a tier where the window has none: launch Sol prices
	// the same either way.
	launch := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	short := CacheSavings("gpt-5.6-sol", launch, false, 1_000_000, 0)
	long := CacheSavings("gpt-5.6-sol", launch, true, 1_000_000, 0)
	if short != long {
		t.Errorf("launch savings differ by tier: short %v long %v, want equal (no launch tier)", short, long)
	}
}
