package parser

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// reduceCursor advances a Cursor CLI (cursor-agent) session over one raw region.
// The session file is the transcript under
// ~/.cursor/projects/<slug>/agent-transcripts/<id>/<id>.jsonl: role lines carry a
// message whose content is text and tool_use blocks, and a turn_ended line closes
// each turn. The transcript is the only append-only record Cursor keeps (the full
// store is a mutable SQLite blob DAG), and it is deliberately lossy: no model, no
// token usage, no tool results, and no tool-call ids, so a Cursor session reports
// conversation shape and tool inputs but prices at zero and its tool health stays
// unmeasured. Timestamps exist only on user turns, embedded in the prompt text
// the CLI sends the model.
func (r *reducer) reduceCursor(region []byte, base int64) error {
	return eachLine(region, base, func(line []byte, offset int64) error {
		if !gjson.ValidBytes(line) {
			return nil
		}
		e := gjson.ParseBytes(line)

		if e.Get("type").String() == "turn_ended" {
			r.closeTurn()
			status := e.Get("status").String()
			switch status {
			case "aborted":
				r.addEvent(EventTurnAborted, map[string]any{
					"reason": e.Get("error").String(),
				}, time.Time{})
			case "error":
				r.addEvent(EventAPIError, map[string]any{
					"message": e.Get("error").String(),
				}, time.Time{})
			}
			return nil
		}

		msg := e.Get("message")
		switch e.Get("role").String() {
		case "user":
			text := blockText(msg.Get("content"))
			prompt, ts := cursorUserPrompt(text)
			r.observe(ts)
			r.addUser(prompt, ts)

		case "assistant":
			// A turn writes one assistant line per model response (text and the
			// tool calls it issued); the lines between two turn closes fold into
			// one turn, closed by turn_ended or the next user line.
			ord := r.ensureAssistant(time.Time{})
			callIndex := r.openCalls
			for _, b := range msg.Get("content").Array() {
				switch b.Get("type").String() {
				case "text":
					r.addOpenContent(b.Get("text").String())
				case "tool_use":
					r.open.HasToolUse = true
					name := b.Get("name").String()
					tc := ToolCall{
						MessageOrdinal: ord, CallIndex: callIndex,
						ToolName: name, Category: toolCategory(name),
						FilePath: toolFilePath(b.Get("input")),
					}
					setToolInput(&tc, b.Get("input"), "application/json")
					r.d.ToolCalls = append(r.d.ToolCalls, tc)
					callIndex++
				}
			}
			r.openCalls = callIndex
		}
		return nil
	})
}

// cursorTimestamp matches the header the CLI prepends to each user prompt, e.g.
// "<timestamp>Thursday, Aug 13, 2026, 9:50 PM (UTC-7)</timestamp>".
var cursorTimestamp = regexp.MustCompile(
	`<timestamp>\w+, (\w+ \d+, \d+, \d+:\d+ [AP]M) \(UTC([+-]\d+)?(?::(\d+))?\)</timestamp>`)

// cursorUserQuery matches the wrapper around the human prompt itself.
var cursorUserQuery = regexp.MustCompile(`(?s)<user_query>\n?(.*?)\n?</user_query>`)

// cursorUserPrompt strips the CLI's user-turn framing, returning the bare prompt
// and the turn's timestamp. The transcript stores the prompt exactly as sent to
// the model: a <timestamp> header and the human text inside <user_query>. A line
// without the wrappers (an older CLI, a shape change) passes through whole with a
// zero time, so the turn is never dropped.
func cursorUserPrompt(text string) (string, time.Time) {
	var ts time.Time
	if m := cursorTimestamp.FindStringSubmatch(text); m != nil {
		offset := 0
		if m[2] != "" {
			hours, _ := strconv.Atoi(m[2])
			offset = hours * 3600
			if m[3] != "" {
				mins, _ := strconv.Atoi(m[3])
				if hours < 0 {
					offset -= mins * 60
				} else {
					offset += mins * 60
				}
			}
		}
		if t, err := time.Parse("Jan 2, 2006, 3:04 PM", m[1]); err == nil {
			ts = t.Add(-time.Duration(offset) * time.Second).UTC()
		}
	}
	if m := cursorUserQuery.FindStringSubmatch(text); m != nil {
		return m[1], ts
	}
	return strings.TrimSpace(cursorTimestamp.ReplaceAllString(text, "")), ts
}
