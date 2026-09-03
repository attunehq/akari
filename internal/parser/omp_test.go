package parser

import (
	"fmt"
	"testing"
)

// TestParseOMP covers OMP's version-3 session format. OMP descends from pi but
// adds a physical title slot, combined provider/model selectors, injected
// context, cache and reasoning token classes, compaction records, and explicit
// failed/aborted assistant messages.
func TestParseOMP(t *testing.T) {
	s, err := Parse(AgentOMP, loadFixture(t, "omp.jsonl"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if s.Cwd != "/home/grace/code/proj" {
		t.Errorf("cwd = %q", s.Cwd)
	}
	if len(s.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(s.Messages))
	}
	if m := s.Messages[0]; m.Role != RoleUser || m.Content != "Fix the login bug" {
		t.Errorf("message 0 = %+v", m)
	}
	if m := s.Messages[1]; m.Role != RoleContext || m.Content != "Repository rules loaded" {
		t.Errorf("message 1 = %+v", m)
	}
	a := s.Messages[2]
	if a.Role != RoleAssistant || a.Model != "gpt-5.6-sol" || !a.HasToolUse {
		t.Errorf("message 2 = %+v", a)
	}
	if !a.HasThinking || a.ThinkingText != "Inspect the auth package" || a.ThinkingBytes != len("Inspect the auth package") {
		t.Errorf("message 2 thinking = %q/%d (has=%v)", a.ThinkingText, a.ThinkingBytes, a.HasThinking)
	}
	if m := s.Messages[3]; m.Content != "The missing session guard is fixed." {
		t.Errorf("message 3 = %+v", m)
	}

	if len(s.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(s.ToolCalls))
	}
	tc := s.ToolCalls[0]
	if tc.ToolName != "read" || tc.Category != "read" || tc.FilePath != "auth.go" || tc.Detail != "" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.ResultBody != "package auth" || tc.ResultStatus != "ok" || tc.ResultMediaType != "text/plain" {
		t.Errorf("tool result = %q (%s, %s)", tc.ResultBody, tc.ResultStatus, tc.ResultMediaType)
	}

	if len(s.UsageEvent) != 2 {
		t.Fatalf("usage events = %d, want 2", len(s.UsageEvent))
	}
	u := s.UsageEvent[0]
	if u.Input != 100 || u.Output != 50 || u.CacheRead != 25 || u.CacheWrite != 10 || u.Reasoning != 30 {
		t.Errorf("usage 0 = %+v", u)
	}
	if u.Model != "gpt-5.6-sol" || u.DedupKey != "a1" {
		t.Errorf("usage 0 identity = %+v", u)
	}
	if u2 := s.UsageEvent[1]; u2.Input != 120 || u2.Output != 20 || u2.CacheRead != 80 || u2.DedupKey != "a2" {
		t.Errorf("usage 1 = %+v", u2)
	}

	if s.Identity.CustomTitle != "Fix login session guard" || s.Identity.ReasoningEffort != "high" {
		t.Errorf("identity title/effort = %+v", s.Identity)
	}
	if s.Identity.ParentSourceID != "01b00000-0000-7000-8000-000000000099" {
		t.Errorf("identity parent = %q", s.Identity.ParentSourceID)
	}

	wantEvents := map[string]string{
		EventModelChange:         `{"model":"gpt-5.6-sol","provider":"openai-codex"}`,
		EventThinkingLevelChange: `{"level":"high"}`,
		EventCompaction:          `{"pre_tokens":64000}`,
		EventAPIError:            `{"message":"upstream reset"}`,
		EventTurnAborted:         `{"reason":"operator interrupted"}`,
	}
	if len(s.Events) != len(wantEvents) {
		t.Fatalf("events = %d, want %d: %+v", len(s.Events), len(wantEvents), s.Events)
	}
	for _, ev := range s.Events {
		want, ok := wantEvents[ev.Kind]
		if !ok {
			t.Errorf("unexpected event = %+v", ev)
			continue
		}
		if ev.AttrsJSON != want {
			t.Errorf("%s attrs = %s, want %s", ev.Kind, ev.AttrsJSON, want)
		}
	}
}

func TestOMPSourceID(t *testing.T) {
	for _, tc := range []struct {
		name, parent, want string
	}{
		{"id", "01b00000-0000-7000-8000-000000000099", "01b00000-0000-7000-8000-000000000099"},
		{"unix path", "/home/grace/.omp/agent/sessions/p/2026-08-20T08-00-00-000Z_parent-id.jsonl", "parent-id"},
		{"windows path", `C:\Users\Grace\.omp\agent\sessions\p\2026-08-20T08-00-00-000Z_parent-id.jsonl`, "parent-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ompSourceID(tc.parent); got != tc.want {
				t.Errorf("ompSourceID(%q) = %q, want %q", tc.parent, got, tc.want)
			}
		})
	}
}

// TestOMPModelSelector covers ompModelSelector's split of the combined
// provider/model selector session/model_change records carry. A selector with
// no slash has no provider slot to split off, so the whole string reads as the
// model rather than erroring or dropping it.
func TestOMPModelSelector(t *testing.T) {
	for _, tc := range []struct {
		name         string
		selector     string
		wantProvider string
		wantModel    string
	}{
		{"provider and model", "openai-codex/gpt-5.6-sol", "openai-codex", "gpt-5.6-sol"},
		{"no slash: whole string is the model", "gpt-5.6-sol", "", "gpt-5.6-sol"},
		{"extra slash: only the first splits off", "openrouter/openai/gpt-5.6", "openrouter", "openai/gpt-5.6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, model := ompModelSelector(tc.selector)
			if provider != tc.wantProvider || model != tc.wantModel {
				t.Errorf("ompModelSelector(%q) = (%q, %q), want (%q, %q)", tc.selector, provider, model, tc.wantProvider, tc.wantModel)
			}
		})
	}
}

// TestOMPFailedAssistantRecordsNoMessage covers OMP's most distinctive
// divergence from the pi reducer it descends from: a request that failed or
// was aborted before producing output still persists as an assistant record
// in the transcript (so it carries a stopReason and, for aborted, an
// errorMessage), but the reducer deliberately does not manufacture a
// transcript message for it. The only observable trace is the lifecycle event
// (EventAPIError for "error", EventTurnAborted for "aborted"). Because the
// ordinal is reserved via r.nextOrdinal but the increment sits behind the same
// gate that appends the message, a failed record never actually consumes its
// ordinal: the next real message reuses it, and the lifecycle event (which has
// no open turn to anchor to) attaches to the last real message instead of a
// phantom row for the failed one.
func TestOMPFailedAssistantRecordsNoMessage(t *testing.T) {
	const lineFmt = `{"type":"session","version":3,"id":"s1","timestamp":"2026-08-20T09:00:00.000Z","cwd":"/home/grace/code/proj"}
{"type":"message","id":"u1","timestamp":"2026-08-20T09:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"first"}]}}
{"type":"message","id":"a1","timestamp":"2026-08-20T09:00:02.000Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.6-sol","content":[],"stopReason":%q,"errorMessage":%q}}
{"type":"message","id":"u2","timestamp":"2026-08-20T09:00:03.000Z","message":{"role":"user","content":[{"type":"text","text":"second"}]}}
`

	cases := []struct {
		name       string
		stopReason string
		errMsg     string
		wantKind   string
		wantAttrs  string
	}{
		{"error", "error", "upstream reset", EventAPIError, `{"message":"upstream reset"}`},
		{"aborted with a reason", "aborted", "operator interrupted", EventTurnAborted, `{"reason":"operator interrupted"}`},
		{"aborted with no reason falls back", "aborted", "", EventTurnAborted, `{"reason":"aborted"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(lineFmt, tc.stopReason, tc.errMsg))
			s, err := Parse(AgentOMP, raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			// Only the two real user turns land: the failed record emits no
			// transcript message of its own.
			if len(s.Messages) != 2 {
				t.Fatalf("messages = %d, want 2 (the failed assistant record emits none)", len(s.Messages))
			}
			// The counter never advanced past the failed record's reserved slot,
			// so the second user turn reuses ordinal 1 rather than landing on 2.
			if s.Messages[1].Ordinal != 1 || s.Messages[1].Content != "second" {
				t.Errorf("second message = %+v, want ordinal 1 content %q", s.Messages[1], "second")
			}

			if len(s.Events) != 1 {
				t.Fatalf("events = %d, want 1", len(s.Events))
			}
			ev := s.Events[0]
			if ev.Kind != tc.wantKind || ev.AttrsJSON != tc.wantAttrs {
				t.Errorf("event = %+v, want kind %s attrs %s", ev, tc.wantKind, tc.wantAttrs)
			}
			// With no open turn to anchor to, the event falls back to the most
			// recent message: ordinal 0 (the first user turn), never a row for
			// the failed record's unconsumed ordinal.
			if ev.MessageOrdinal == nil || *ev.MessageOrdinal != 0 {
				t.Errorf("event ordinal = %v, want 0", ev.MessageOrdinal)
			}
		})
	}
}

// TestOMPTitleRecordsAreAuthoritative covers title and title_change: both
// write Identity.CustomTitle unconditionally, in transcript order, with no
// guard against overwriting a name with an explicit empty string. That last
// case matters because an empty title is how the UI is told to fall back to
// showing the session's first prompt instead of a stale custom name.
func TestOMPTitleRecordsAreAuthoritative(t *testing.T) {
	raw := []byte(`{"type":"session","version":3,"id":"s1","timestamp":"2026-08-20T09:00:00.000Z","cwd":"/home/grace/code/proj"}
{"type":"title","title":"First pass","updatedAt":"2026-08-20T09:00:00.100Z"}
{"type":"title_change","title":"Renamed by the user","source":"user","previousTitle":"First pass","timestamp":"2026-08-20T09:00:01.000Z"}
{"type":"title_change","title":"","source":"user","previousTitle":"Renamed by the user","timestamp":"2026-08-20T09:00:02.000Z"}
`)
	s, err := Parse(AgentOMP, raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Identity.CustomTitle != "" {
		t.Errorf("custom title = %q, want empty after the explicit clear", s.Identity.CustomTitle)
	}
}

// TestOMPCompactionPreTokens covers the compaction record's pre_tokens attr:
// it rides only when tokensBefore is present on the line, checked with gjson's
// Exists rather than a truthiness test, so a reported zero still counts as
// present and a genuinely absent field produces no attrs at all rather than a
// spurious zero.
func TestOMPCompactionPreTokens(t *testing.T) {
	const sessionLine = `{"type":"session","version":3,"id":"s1","timestamp":"2026-08-20T09:00:00.000Z","cwd":"/home/grace/code/proj"}` + "\n"

	cases := []struct {
		name      string
		line      string
		wantAttrs string
	}{
		{
			name:      "tokensBefore present",
			line:      `{"type":"compaction","id":"k1","timestamp":"2026-08-20T09:00:08.000Z","tokensBefore":64000}`,
			wantAttrs: `{"pre_tokens":64000}`,
		},
		{
			name:      "tokensBefore reported zero still counts as present",
			line:      `{"type":"compaction","id":"k2","timestamp":"2026-08-20T09:00:08.000Z","tokensBefore":0}`,
			wantAttrs: `{"pre_tokens":0}`,
		},
		{
			name:      "tokensBefore absent",
			line:      `{"type":"compaction","id":"k3","timestamp":"2026-08-20T09:00:08.000Z"}`,
			wantAttrs: `{}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(sessionLine + tc.line + "\n")
			s, err := Parse(AgentOMP, raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(s.Events) != 1 {
				t.Fatalf("events = %d, want 1", len(s.Events))
			}
			if ev := s.Events[0]; ev.Kind != EventCompaction || ev.AttrsJSON != tc.wantAttrs {
				t.Errorf("event = %+v, want kind %s attrs %s", ev, EventCompaction, tc.wantAttrs)
			}
		})
	}
}
