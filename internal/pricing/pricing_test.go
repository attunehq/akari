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
		// Sonnet at $3/$15 from 3.5 through 4.6, and Sonnet 5 at $2/$10.
		{"claude-sonnet-5", 2, 10},
		{"claude-sonnet-4-20250514", 3, 15},
		{"claude-sonnet-4-0", 3, 15},
		{"claude-sonnet-4-5-20250929", 3, 15},
		{"claude-sonnet-4-6", 3, 15},
		{"claude-3-7-sonnet-20250219", 3, 15},
		{"claude-3-5-sonnet-20241022", 3, 15},
		// Haiku.
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
	// shape rateAt relies on: it seeds from the first window and keeps the last whose
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
		"gpt-5.4-turbo", "gpt-5.5-ultra", // same-version variants we never priced
		"gpt-5.6-mini", "gpt-5.6-nano", // GPT-5.6's real tiers are sol/terra/luna, not mini/nano
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
	// The same 1M input + 1M output on GPT-5.6 Sol prices at the launch rate before
	// the 2026-08-21 reprice and at the reduced rate from the boundary on.
	launch, _ := Cost("gpt-5.6-sol", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), 1_000_000, 1_000_000, 0, 0, 0)
	if math.Abs(launch-35.0) > 1e-9 {
		t.Errorf("launch cost = %v, want 35 (5 + 30)", launch)
	}
	reduced, _ := Cost("gpt-5.6-sol", gpt56SolReprice, 1_000_000, 1_000_000, 0, 0, 0)
	if math.Abs(reduced-24.0) > 1e-9 {
		t.Errorf("reduced cost = %v, want 24 (4 + 20)", reduced)
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
		saving := CacheSavings(c.model, anytime, c.read, c.write)
		if math.Abs(saving-c.wantSaving) > 1e-9 {
			t.Errorf("%s: saving = %v, want %v", c.name, saving, c.wantSaving)
		}
	}
}

func TestCacheSavingsSelectsDatedWindow(t *testing.T) {
	// GPT-5.6 Sol at launch (Input 5, CacheRead 0.50): 1M read saves 1 * (5 - 0.50) = 4.50.
	// After the reprice (Input 4, CacheRead 0.40): 1M read saves 1 * (4 - 0.40) = 3.60.
	launch := CacheSavings("gpt-5.6-sol", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), 1_000_000, 0)
	if math.Abs(launch-4.50) > 1e-9 {
		t.Errorf("launch saving = %v, want 4.50", launch)
	}
	reduced := CacheSavings("gpt-5.6-sol", gpt56SolReprice, 1_000_000, 0)
	if math.Abs(reduced-3.60) > 1e-9 {
		t.Errorf("reduced saving = %v, want 3.60", reduced)
	}
}
