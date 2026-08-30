package mcpserver

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/server/store"
)

func TestCaptureExpansionDTOs(t *testing.T) {
	detail := sessionDetailToDTO(store.SessionDetail{
		Slug:            "quiet-circuit",
		PermissionMode:  "bypassPermissions",
		ReasoningEffort: "high",
		SubagentName:    "Explore",
		PRNumber:        42,
		PRURL:           "https://github.com/ada/engine/pull/42",
		PRRepo:          "ada/engine",
	})
	if detail.Slug != "quiet-circuit" || detail.PermissionMode != "bypassPermissions" || detail.ReasoningEffort != "high" || detail.SubagentName != "Explore" || detail.PRNumber != 42 || detail.PRRepo != "ada/engine" {
		t.Fatalf("identity DTO = %+v", detail)
	}

	tool := toolCallToDTO(store.ToolCallView{
		StructSHA256:      "abc",
		StructBytes:       17,
		StructMediaType:   "application/json",
		AttributionAgent:  "Explore",
		AttributionSkill:  "review",
		AttributionPlugin: "github",
	})
	if tool.StructSHA256 != "abc" || tool.StructBytes != 17 || tool.StructMediaType != "application/json" || tool.AttributionAgent != "Explore" || tool.AttributionSkill != "review" || tool.AttributionPlugin != "github" {
		t.Fatalf("tool DTO = %+v", tool)
	}

	ordinal := int64(7)
	occurred := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	event := sessionEventToDTO(store.SessionEvent{
		MessageOrdinal: &ordinal,
		Kind:           "api_error",
		Attrs:          json.RawMessage(`{"message":"retrying"}`),
		OccurredAt:     occurred,
	})
	if event.MessageOrdinal == nil || *event.MessageOrdinal != ordinal || event.Kind != "api_error" || event.Attrs["message"] != "retrying" || !event.OccurredAt.Equal(occurred) {
		t.Fatalf("event DTO = %+v", event)
	}
}

// TestEventAttrsAlwaysObject pins the two properties the tool's output schema
// depends on: attrs is never nil (a nil map encodes as null, which "object"
// rejects) and its numbers survive the decode without turning into floats.
func TestEventAttrsAlwaysObject(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("null"), json.RawMessage("{"), json.RawMessage("{}")} {
		if got := eventAttrs(raw); got == nil || len(got) != 0 {
			t.Fatalf("eventAttrs(%q) = %#v, want empty non-nil map", raw, got)
		}
	}
	attrs := eventAttrs(json.RawMessage(`{"pre_tokens":9007199254740993,"trigger":"auto"}`))
	b, err := json.Marshal(attrs)
	if err != nil {
		t.Fatalf("marshal attrs: %v", err)
	}
	if string(b) != `{"pre_tokens":9007199254740993,"trigger":"auto"}` {
		t.Fatalf("attrs round trip = %s", b)
	}
}
