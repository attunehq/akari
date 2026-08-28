package parser

import (
	"strings"
	"testing"
	"time"
)

func TestParseOpenCode(t *testing.T) {
	s, err := Parse(AgentOpenCode, loadFixture(t, "opencode.jsonl"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if s.Cwd != "/home/grace/code/my-app" {
		t.Errorf("cwd = %q", s.Cwd)
	}
	if s.Identity.CustomTitle != "Fix the login bug" || s.Identity.Slug != "silent-tiger" {
		t.Errorf("identity title/slug = %+v", s.Identity)
	}
	if s.Identity.ReasoningEffort != "high" {
		t.Errorf("reasoning effort = %q", s.Identity.ReasoningEffort)
	}
	if s.Identity.ParentSourceID != "" {
		t.Errorf("parent source id = %q, want empty on the parent transcript", s.Identity.ParentSourceID)
	}

	if len(s.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(s.Messages))
	}
	if m := s.Messages[0]; m.Role != RoleUser || m.Content != "Fix the login bug" {
		t.Errorf("message 0 = %+v", m)
	}
	a1 := s.Messages[1]
	if a1.Role != RoleAssistant || a1.Content != "Looking at the auth module" {
		t.Errorf("message 1 content = %q", a1.Content)
	}
	if a1.Model != "grok-4.6" {
		t.Errorf("message 1 model = %q", a1.Model)
	}
	if !a1.HasThinking || a1.ThinkingText != "Consider the auth module" {
		t.Errorf("message 1 thinking = %q (has=%v)", a1.ThinkingText, a1.HasThinking)
	}
	if a1.ThinkingBytes != len("Consider the auth module") {
		t.Errorf("message 1 thinking bytes = %d", a1.ThinkingBytes)
	}
	if !a1.HasToolUse {
		t.Errorf("message 1 should have tool use")
	}

	if len(s.ToolCalls) != 3 {
		t.Fatalf("tool calls = %d, want 3", len(s.ToolCalls))
	}
	tc := s.ToolCalls[0]
	if tc.ToolName != "read" || tc.Category != "read" || tc.FilePath != "src/auth.ts" {
		t.Errorf("tool call 0 = %+v", tc)
	}
	if tc.ResultBody != "export function login() {}" || tc.ResultStatus != "ok" {
		t.Errorf("tool result 0 = %q (%s)", tc.ResultBody, tc.ResultStatus)
	}

	sh := s.ToolCalls[1]
	if sh.ToolName != "bash" || sh.Category != "bash" || sh.Detail != "go test ./..." {
		t.Errorf("tool call 1 = %+v", sh)
	}
	if sh.ResultStatus != "error" || sh.ResultBody != "go: build failed" {
		t.Errorf("tool result 1 = %q (%s)", sh.ResultBody, sh.ResultStatus)
	}

	task := s.ToolCalls[2]
	if task.ToolName != "task" || task.Detail != "Map the auth package" {
		t.Errorf("tool call 2 = %+v", task)
	}

	if len(s.UsageEvent) != 2 {
		t.Fatalf("usage events = %d, want 2", len(s.UsageEvent))
	}
	u0 := s.UsageEvent[0]
	if u0.Input != 100 || u0.Output != 50 || u0.CacheWrite != 10 || u0.CacheRead != 30 || u0.Reasoning != 20 {
		t.Errorf("usage 0 = %+v", u0)
	}
	if u0.Model != "grok-4.6" {
		t.Errorf("usage 0 model = %q", u0.Model)
	}

	if len(s.Events) != 2 {
		t.Fatalf("events = %d, want 2 (subagent + compaction)", len(s.Events))
	}
	if ev := s.Events[0]; ev.Kind != EventSubagentActivity || !strings.Contains(ev.AttrsJSON, "ses_child") {
		t.Errorf("event 0 = %+v", ev)
	}
	if ev := s.Events[1]; ev.Kind != EventCompaction {
		t.Errorf("event 1 = %+v", ev)
	}

	if got := s.StartedAt.UTC().Format(time.RFC3339); got != "2024-03-01T12:00:00Z" {
		t.Errorf("started = %s", got)
	}
}

func TestParseOpenCodeChildNamesParent(t *testing.T) {
	raw := []byte(`{"type":"session","id":"ses_child","directory":"/home/grace/code/my-app","parent_id":"ses_grace_login","title":"Map auth (@explore subagent)","slug":"playful-eagle","agent":"explore","time_created":1709294504000}
{"type":"message","id":"msg_c1","time_created":1709294504100,"data":{"role":"user","time":{"created":1709294504100},"agent":"explore"},"parts":[{"id":"prt_c1","time_created":1709294504110,"data":{"type":"text","text":"map it"}}]}
`)
	s, err := Parse(AgentOpenCode, raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Identity.ParentSourceID != "ses_grace_login" {
		t.Errorf("parent source id = %q", s.Identity.ParentSourceID)
	}
	if s.Identity.SubagentName != "explore" {
		t.Errorf("subagent name = %q", s.Identity.SubagentName)
	}
	if s.Identity.CustomTitle != "Map auth (@explore subagent)" {
		t.Errorf("title = %q", s.Identity.CustomTitle)
	}
}

func TestParseOpenCodeRedactedThinkingUsesEncryptedWeight(t *testing.T) {
	raw := []byte(`{"type":"session","id":"ses_x","directory":"/home/grace/app","time_created":1}
{"type":"message","id":"msg_a","time_created":2,"data":{"role":"assistant","modelID":"grok-4.6","time":{"created":2,"completed":3},"tokens":{"input":1,"output":1,"reasoning":8,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_r","time_created":2,"data":{"type":"reasoning","text":"","metadata":{"xai":{"reasoningEncryptedContent":"ABCD"}}}},{"id":"prt_t","time_created":3,"data":{"type":"text","text":"done"}}]}
`)
	s, err := Parse(AgentOpenCode, raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(s.Messages))
	}
	m := s.Messages[0]
	if !m.HasThinking || m.ThinkingText != "" || m.ThinkingBytes != 4 {
		t.Errorf("thinking = text %q bytes %d has=%v", m.ThinkingText, m.ThinkingBytes, m.HasThinking)
	}
}
