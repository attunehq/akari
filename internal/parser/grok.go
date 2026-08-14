package parser

import (
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// reduceGrok advances a Grok CLI session over one raw region. The session file is
// the CLI's updates.jsonl: one ACP session update per line, wrapped as
// {"timestamp":<secs>,"method":...,"params":{"sessionId":...,"update":{...}}}.
// User prompts and assistant output arrive as *_chunk updates (the CLI finalizes
// whole blocks, but the reducer still folds a chunk run so a streamed split would
// parse the same); tool calls arrive as a tool_call line whose result is a later
// tool_call_update carrying a status, back-patched by toolCallId; turn_completed
// closes the turn and carries the turn's token usage per model, which includes any
// subagent's spend. A subagent's own transcript never names its parent, so the
// parent's subagent_spawned updates are the one place the relationship exists:
// they land in Identity.ChildSourceIDs, and the store links the claimed children
// and drops their own usage ledger so the spend counts once.
func (r *reducer) reduceGrok(region []byte, base int64) error {
	return eachLine(region, base, func(line []byte, offset int64) error {
		if !gjson.ValidBytes(line) {
			return nil
		}
		e := gjson.ParseBytes(line)
		u := e.Get("params.update")
		if !u.Exists() {
			return nil
		}
		ts := grokTime(e)
		r.observe(ts)

		kind := u.Get("sessionUpdate").String()
		if kind != "user_message_chunk" {
			r.flushGrokUser()
		}
		switch kind {
		case "user_message_chunk":
			// The prompt's model rides the user chunk's meta and is the sticky model
			// for the turn it opens (assistant chunks carry no model of their own).
			if m := u.Get("_meta.modelId").String(); m != "" {
				r.model = m
			}
			if t := u.Get("content.text").String(); t != "" {
				if len(r.grokUser) == 0 {
					r.grokUserTS = ts
				}
				r.grokUser = append(r.grokUser, t)
			}

		case "agent_thought_chunk":
			// Grok logs its reasoning as plaintext summaries, so the text length is
			// the trace weight; the exact reasoning-token count arrives separately on
			// turn_completed and takes precedence downstream.
			r.ensureAssistant(ts)
			t := u.Get("content.text").String()
			r.addOpenReasoning(t, len(t))

		case "agent_message_chunk":
			r.ensureAssistant(ts)
			r.addOpenContent(u.Get("content.text").String())

		case "tool_call":
			ord := r.ensureAssistant(ts)
			r.open.HasToolUse = true
			name := u.Get("_meta.x\\.ai/tool.name").String()
			if name == "" {
				name = u.Get("title").String()
			}
			tc := ToolCall{
				MessageOrdinal: ord, CallIndex: r.openCalls,
				ToolName: name, Category: toolCategory(name),
				FilePath: toolFilePath(u.Get("rawInput")),
				CallUID:  u.Get("toolCallId").String(),
			}
			setToolInput(&tc, u.Get("rawInput"), "application/json")
			r.d.ToolCalls = append(r.d.ToolCalls, tc)
			r.openCalls++

		case "tool_call_update":
			// Progress updates re-describe the call (kind, title, echoed rawInput)
			// and carry no terminal status; only the update that does resolves the
			// call. Its rawOutput is the result body.
			status := u.Get("status").String()
			if status != "completed" && status != "failed" {
				return nil
			}
			r.applyResult(u.Get("toolCallId").String(), u.Get("rawOutput"), status == "failed")

		case "turn_completed":
			r.closeTurn()
			var anchor *int
			if r.nextOrdinal > 0 {
				ord := r.nextOrdinal - 1
				anchor = &ord
			}
			usage := u.Get("usage")
			models := usage.Get("modelUsage").Map()
			keys := make([]string, 0, len(models))
			for k := range models {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				mu := models[k]
				// The combined input includes the cached reads and writes; split them
				// out so Input is the uncached remainder, as every agent here reports it.
				total := int(mu.Get("inputTokens").Int())
				cached := int(mu.Get("cachedReadTokens").Int())
				written := int(mu.Get("cacheCreationTokens").Int())
				input := total - cached - written
				if input < 0 {
					input = 0
				}
				r.addUsage(Usage{
					MessageOrdinal: anchor,
					Model:          grokModelID(k),
					Input:          input,
					Output:         int(mu.Get("outputTokens").Int()),
					CacheWrite:     written, CacheRead: cached,
					Reasoning:  int(mu.Get("reasoningTokens").Int()),
					OccurredAt: ts,
					DedupKey:   u.Get("prompt_id").String() + "/" + k,
				}, offset)
			}
			r.addEvent(EventTurnEnd, map[string]any{
				"duration_ms": usage.Get("apiDurationMs").Int(),
				"model_calls": usage.Get("modelCalls").Int(),
				"stop_reason": u.Get("stop_reason").String(),
			}, ts)

		case "subagent_spawned":
			if child := u.Get("child_session_id").String(); child != "" {
				r.claimChild(child)
				r.addEvent(EventSubagentActivity, map[string]any{
					"child_session_id": child,
					"state":            "started",
					"subagent_type":    u.Get("subagent_type").String(),
					"description":      u.Get("description").String(),
					"model":            u.Get("model").String(),
				}, ts)
			}

		case "subagent_finished":
			if child := u.Get("child_session_id").String(); child != "" {
				r.addEvent(EventSubagentActivity, map[string]any{
					"child_session_id": child,
					"state":            u.Get("status").String(),
					"duration_ms":      u.Get("duration_ms").Int(),
					"tokens_used":      u.Get("tokens_used").Int(),
				}, ts)
			}
		}
		return nil
	})
}

// flushGrokUser emits the buffered user message chunks as one user turn. Grok
// streams a prompt as one or more user_message_chunk lines with nothing between
// them, so the buffer folds a consecutive run and any other update flushes it;
// closeTurn also flushes so a prompt the model never answered still lands at
// Finish.
func (r *reducer) flushGrokUser() {
	if len(r.grokUser) == 0 {
		return
	}
	content := strings.Join(r.grokUser, "")
	ts := r.grokUserTS
	r.grokUser, r.grokUserTS = nil, time.Time{}
	r.addUser(content, ts)
}

// grokTime picks the line's timestamp: the params meta's millisecond
// agentTimestampMs when present, else the wrapper's whole-second timestamp.
func grokTime(e gjson.Result) time.Time {
	if ms := e.Get("params._meta.agentTimestampMs").Int(); ms > 0 {
		return time.UnixMilli(ms).UTC()
	}
	if s := e.Get("timestamp").Int(); s > 0 {
		return time.Unix(s, 0).UTC()
	}
	return time.Time{}
}

// grokModelID canonicalizes a usage map's model key. The billing engine reports
// the serving deployment ("grok-4.6-build"); the CLI, its docs, and the pricing
// table all use the bare model ID ("grok-4.6"), which is also what the prompt
// meta stamps on messages, so the suffix is stripped to keep one facet value.
func grokModelID(k string) string {
	return strings.TrimSuffix(k, "-build")
}
