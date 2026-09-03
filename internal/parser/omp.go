package parser

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// reduceOMP advances an OMP version-3 session over one raw region. OMP's message
// envelope descends from pi's, but its session format also records a fixed-width
// title slot, combined provider/model selectors, injected context, compactions,
// and explicit failed or aborted assistant messages.
func (r *reducer) reduceOMP(region []byte, base int64) error {
	return eachLine(region, base, func(line []byte, offset int64) error {
		if !gjson.ValidBytes(line) {
			return nil
		}
		e := gjson.ParseBytes(line)
		typ := e.Get("type").String()
		ts := parseTime(e.Get("timestamp").String())
		r.observe(ts)

		switch typ {
		case "title", "title_change":
			// Both records are authoritative, including an explicit empty title
			// that clears a prior name and falls the UI back to the first prompt.
			r.d.Identity.CustomTitle = e.Get("title").String()

		case "session":
			if cwd := e.Get("cwd").String(); cwd != "" {
				r.d.Cwd = cwd
			}
			if parent := ompSourceID(e.Get("parentSession").String()); parent != "" {
				r.d.Identity.ParentSourceID = parent
			}

		case "model_change":
			provider, model := ompModelSelector(e.Get("model").String())
			r.addEvent(EventModelChange, map[string]any{
				"provider": provider,
				"model":    model,
			}, ts)

		case "thinking_level_change":
			level := e.Get("thinkingLevel").String()
			if configured := e.Get("configured").String(); configured != "" {
				r.d.Identity.ReasoningEffort = configured
			} else {
				r.d.Identity.ReasoningEffort = level
			}
			r.addEvent(EventThinkingLevelChange, map[string]any{"level": level}, ts)

		case "custom_message":
			if content := blockText(e.Get("content")); content != "" {
				r.addContextInline(content, ts)
			}

		case "compaction":
			attrs := map[string]any{}
			if tokens := e.Get("tokensBefore"); tokens.Exists() {
				attrs["pre_tokens"] = tokens.Int()
			}
			r.addEvent(EventCompaction, attrs, ts)

		case "message":
			r.ompMessage(e, offset, ts)
		}
		return nil
	})
}

func (r *reducer) ompMessage(e gjson.Result, offset int64, ts time.Time) {
	msg := e.Get("message")
	switch msg.Get("role").String() {
	case "user":
		r.addUser(blockText(msg.Get("content")), ts)

	case "assistant":
		ord := r.nextOrdinal
		op := MessageOp{Ordinal: ord, Role: RoleAssistant, Model: msg.Get("model").String(), Timestamp: ts}
		var textParts, thinkParts []string
		callIndex := 0
		for _, b := range msg.Get("content").Array() {
			switch b.Get("type").String() {
			case "text":
				textParts = append(textParts, b.Get("text").String())
			case "thinking":
				thinking := b.Get("thinking").String()
				if thinking != "" {
					thinkParts = append(thinkParts, thinking)
				}
				op.ThinkingBytes += len(thinking)
				op.HasThinking = true
			case "toolCall":
				op.HasToolUse = true
				name := b.Get("name").String()
				tc := ToolCall{
					MessageOrdinal: ord,
					CallIndex:      callIndex,
					ToolName:       name,
					Category:       toolCategory(name),
					FilePath:       toolFilePath(b.Get("arguments")),
					CallUID:        b.Get("id").String(),
				}
				setToolInput(&tc, b.Get("arguments"), "application/json")
				r.d.ToolCalls = append(r.d.ToolCalls, tc)
				callIndex++
			}
		}
		op.Content = strings.Join(textParts, "\n")
		op.ThinkingText = strings.Join(thinkParts, "\n")

		usage := msg.Get("usage")
		// OMP persists a contentless assistant record for a request that failed or
		// was aborted before producing anything. The lifecycle event below is the
		// observable result; do not manufacture an empty transcript message for it.
		if op.Content != "" || op.HasThinking || op.HasToolUse || usage.Exists() {
			r.nextOrdinal++
			r.d.Messages = append(r.d.Messages, op)
			if usage.Exists() {
				o := ord
				r.addUsage(Usage{
					MessageOrdinal: &o,
					Model:          op.Model,
					Input:          int(usage.Get("input").Int()),
					Output:         int(usage.Get("output").Int()),
					CacheWrite:     int(usage.Get("cacheWrite").Int()),
					CacheRead:      int(usage.Get("cacheRead").Int()),
					Reasoning:      int(usage.Get("reasoningTokens").Int()),
					OccurredAt:     ts,
					DedupKey:       e.Get("id").String(),
				}, offset)
			}
		}

		switch msg.Get("stopReason").String() {
		case "error":
			r.addEvent(EventAPIError, map[string]any{"message": msg.Get("errorMessage").String()}, ts)
		case "aborted":
			reason := msg.Get("errorMessage").String()
			if reason == "" {
				reason = "aborted"
			}
			r.addEvent(EventTurnAborted, map[string]any{"reason": reason}, ts)
		}

	case "toolResult":
		r.applyResult(msg.Get("toolCallId").String(), msg.Get("content"), msg.Get("isError").Bool())
	}
}

// ompModelSelector splits OMP's provider/model selector into the event fields
// shared with pi. Assistant messages already carry the bare model id used for
// pricing; this path is only session-level switch telemetry.
func ompModelSelector(selector string) (provider, model string) {
	provider, model, ok := strings.Cut(selector, "/")
	if !ok {
		return "", selector
	}
	return provider, model
}

// ompSourceID normalizes parentSession, which OMP intentionally permits to be
// either an id or a session path. Session filenames end in _<id>.jsonl; extracting
// that suffix makes both forms match the child's source_session_id relation.
func ompSourceID(parent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return ""
	}
	if strings.HasSuffix(parent, ".jsonl") {
		name := strings.TrimSuffix(lastPathSegment(parent), ".jsonl")
		if i := strings.LastIndexByte(name, '_'); i >= 0 && i+1 < len(name) {
			return name[i+1:]
		}
		return name
	}
	return parent
}
