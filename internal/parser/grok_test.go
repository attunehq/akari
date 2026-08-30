package parser

import (
	"fmt"
	"math"
	"testing"
)

func grokTurnCompleted(usage string) []byte {
	return []byte(`{"timestamp":1709280006,"method":"session/update","params":{"update":{"sessionUpdate":"turn_completed","prompt_id":"p-1","usage":` + usage + `}}}` + "\n")
}

func grokUSD(v float64) *float64 { return &v }

func grokCostByModel(t *testing.T, usage string) map[string]*float64 {
	t.Helper()
	s, err := Parse(AgentGrok, grokTurnCompleted(usage))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := make(map[string]*float64, len(s.UsageEvent))
	for _, u := range s.UsageEvent {
		got[u.Model] = u.ReportedCostUSD
	}
	return got
}

func assertGrokCost(t *testing.T, got, want *float64) {
	t.Helper()
	switch {
	case got == nil && want == nil:
		return
	case got == nil || want == nil:
		t.Fatalf("cost = %s, want %s", grokCostText(got), grokCostText(want))
	case math.Abs(*got-*want) > 1e-12:
		t.Fatalf("cost = %s, want %s", grokCostText(got), grokCostText(want))
	}
}

func grokCostText(c *float64) string {
	if c == nil {
		return "nil (estimate)"
	}
	return fmt.Sprintf("%v", *c)
}

func TestGrokReportedCost(t *testing.T) {
	const tokens = `"inputTokens":100,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0`

	cases := []struct {
		name  string
		usage string
		want  map[string]*float64
	}{
		{
			name:  "top-level only, one model",
			usage: `{"costUsdTicks":10000000000,"modelUsage":{"grok-4.6-build":{` + tokens + `}}}`,
			want:  map[string]*float64{"grok-4.6": grokUSD(1)},
		},
		{
			name: "top-level only, allocate by token share",
			usage: `{"costUsdTicks":10000000000,"modelUsage":{` +
				`"grok-4.6-build":{"inputTokens":100,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0},` +
				`"grok-4.5-build":{"inputTokens":300,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0}}}`,
			want: map[string]*float64{"grok-4.6": grokUSD(0.25), "grok-4.5": grokUSD(0.75)},
		},
		{
			name: "per-model ticks, no top-level",
			usage: `{"modelUsage":{` +
				`"grok-4.6-build":{"inputTokens":100,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0,"costUsdTicks":4000000000},` +
				`"grok-4.5-build":{"inputTokens":300,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0}}}`,
			want: map[string]*float64{"grok-4.6": grokUSD(0.4), "grok-4.5": nil},
		},
		{
			name: "mixed: remainder only on unpriced rows",
			usage: `{"costUsdTicks":10000000000,"modelUsage":{` +
				`"grok-4.6-build":{"inputTokens":100,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0,"costUsdTicks":4000000000},` +
				`"grok-4.5-build":{"inputTokens":200,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0},` +
				`"other-build":{"inputTokens":600,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0}}}`,
			want: map[string]*float64{
				"grok-4.6": grokUSD(0.4),
				"grok-4.5": grokUSD(0.15),
				"other":    grokUSD(0.45),
			},
		},
		{
			name: "contradictory top-level total leaves missing rows unreported",
			usage: `{"costUsdTicks":10000000000,"modelUsage":{` +
				`"grok-4.6-build":{"inputTokens":100,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0,"costUsdTicks":12000000000},` +
				`"grok-4.5-build":{"inputTokens":300,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":0}}}`,
			want: map[string]*float64{"grok-4.6": grokUSD(1.2), "grok-4.5": nil},
		},
		{
			name:  "zero ticks is reported free, not missing",
			usage: `{"costUsdTicks":0,"modelUsage":{"grok-4.6-build":{` + tokens + `,"costUsdTicks":0}}}`,
			want:  map[string]*float64{"grok-4.6": grokUSD(0)},
		},
		{
			name:  "missing ticks stay unreported",
			usage: `{"modelUsage":{"grok-4.6-build":{` + tokens + `}}}`,
			want:  map[string]*float64{"grok-4.6": nil},
		},
		{
			name:  "negative ticks are invalid",
			usage: `{"costUsdTicks":-1,"modelUsage":{"grok-4.6-build":{` + tokens + `,"costUsdTicks":-5}}}`,
			want:  map[string]*float64{"grok-4.6": nil},
		},
		{
			name:  "negative row ticks still take the top-level remainder",
			usage: `{"costUsdTicks":10000000000,"modelUsage":{"grok-4.6-build":{` + tokens + `,"costUsdTicks":-1}}}`,
			want:  map[string]*float64{"grok-4.6": grokUSD(1)},
		},
		{
			name:  "matching top-level and per-model ticks are not double counted",
			usage: `{"costUsdTicks":15300000,"modelUsage":{"grok-4.6-build":{"inputTokens":1200,"outputTokens":80,"cachedReadTokens":700,"cacheCreationTokens":0,"reasoningTokens":30,"costUsdTicks":15300000}}}`,
			want:  map[string]*float64{"grok-4.6": grokUSD(0.00153)},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := grokCostByModel(t, tt.usage)
			if len(got) != len(tt.want) {
				t.Fatalf("models = %v, want %v", got, tt.want)
			}
			for model, want := range tt.want {
				assertGrokCost(t, got[model], want)
			}
		})
	}
}
