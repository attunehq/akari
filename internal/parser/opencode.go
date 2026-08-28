package parser

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// reduceOpenCode advances an OpenCode session over one raw region. The client
// materializes the SQLite-backed session (~/.local/share/opencode/opencode.db)
// into JSONL: a type=session header followed by one type=message line per turn,
// each carrying its parts. A message is already one turn (no fold across lines).
// Tool calls live on the same line as their results. Subagent transcripts set
// parent_id on the header (the child names its parent, like Codex); each session
// keeps its own usage ledger.
func (r *reducer) reduceOpenCode(region []byte, base int64) error {
	return eachLine(region, base, func(line []byte, offset int64) error {
		if !gjson.ValidBytes(line) {
			return nil
		}
		e := gjson.ParseBytes(line)
		switch e.Get("type").String() {
		case "session":
			r.reduceOpenCodeSession(e)
		case "message":
			r.reduceOpenCodeMessage(e, offset)
		}
		return nil
	})
}

func (r *reducer) reduceOpenCodeSession(e gjson.Result) {
	if cwd := e.Get("directory").String(); cwd != "" {
		r.d.Cwd = cwd
	}
	if b := e.Get("branch").String(); b != "" {
		r.d.GitBranch = b
	}
	r.observe(parseUnixMilli(e.Get("time_created").Int()))

	title := e.Get("title").String()
	if title != "" && !strings.HasPrefix(title, "New session -") {
		r.d.Identity.CustomTitle = title
	}
	if slug := e.Get("slug").String(); slug != "" {
		r.d.Identity.Slug = slug
	}
	if v := e.Get("model.variant").String(); v != "" {
		r.d.Identity.ReasoningEffort = v
	}
	if parent := e.Get("parent_id").String(); parent != "" {
		r.d.Identity.ParentSourceID = parent
		if agent := e.Get("agent").String(); agent != "" {
			r.d.Identity.SubagentName = agent
		}
	}
}

func (r *reducer) reduceOpenCodeMessage(e gjson.Result, offset int64) {
	data := e.Get("data")
	ts := openCodeMessageTime(data, e)
	r.observe(ts)
	r.closeTurn()

	switch data.Get("role").String() {
	case "user":
		r.addUser(openCodeText(e), ts)
		r.reduceOpenCodeEvents(e, ts)

	case "assistant":
		ord := r.nextOrdinal
		r.nextOrdinal++
		model := data.Get("modelID").String()
		op := MessageOp{Ordinal: ord, Role: RoleAssistant, Model: model, Timestamp: ts}
		var textParts, thinkParts []string
		callIndex := 0
		for _, p := range e.Get("parts").Array() {
			pd := p.Get("data")
			switch pd.Get("type").String() {
			case "text":
				if t := pd.Get("text").String(); t != "" {
					textParts = append(textParts, t)
				}
			case "reasoning":
				text := pd.Get("text").String()
				enc := pd.Get("metadata.xai.reasoningEncryptedContent").String()
				weight := len(text)
				if weight == 0 {
					weight = len(enc)
				}
				if text != "" {
					thinkParts = append(thinkParts, text)
				}
				op.ThinkingBytes += weight
				op.HasThinking = true
			case "tool":
				op.HasToolUse = true
				name := pd.Get("tool").String()
				state := pd.Get("state")
				tc := ToolCall{
					MessageOrdinal: ord, CallIndex: callIndex,
					ToolName: name, Category: toolCategory(name),
					FilePath: toolFilePath(state.Get("input")),
					CallUID:  pd.Get("callID").String(),
				}
				setToolInput(&tc, state.Get("input"), "application/json")
				r.d.ToolCalls = append(r.d.ToolCalls, tc)
				openCodeApplyToolResult(r, tc.CallUID, state)
				if name == "task" {
					if child := pd.Get("metadata.sessionId").String(); child != "" {
						r.addEvent(EventSubagentActivity, map[string]any{
							"child_session_id": child,
							"state":            state.Get("status").String(),
							"subagent_type":    state.Get("input.subagent_type").String(),
							"description":      state.Get("input.description").String(),
						}, ts)
					}
				}
				callIndex++
			case "compaction":
				r.addEvent(EventCompaction, map[string]any{
					"auto":          pd.Get("auto").Bool(),
					"tail_start_id": pd.Get("tail_start_id").String(),
				}, ts)
			}
		}
		op.Content = strings.Join(textParts, "\n")
		op.ThinkingText = strings.Join(thinkParts, "\n")
		r.d.Messages = append(r.d.Messages, op)

		if errMsg := openCodeAPIError(data); errMsg != "" {
			r.addEvent(EventAPIError, map[string]any{"message": errMsg}, ts)
		}
		if u := data.Get("tokens"); u.Exists() {
			input := int(u.Get("input").Int())
			output := int(u.Get("output").Int())
			reasoning := int(u.Get("reasoning").Int())
			cacheRead := int(u.Get("cache.read").Int())
			cacheWrite := int(u.Get("cache.write").Int())
			if input != 0 || output != 0 || reasoning != 0 || cacheRead != 0 || cacheWrite != 0 {
				o := ord
				r.addUsage(Usage{
					MessageOrdinal: &o, Model: model,
					Input: input, Output: output,
					CacheWrite: cacheWrite, CacheRead: cacheRead,
					Reasoning:  reasoning,
					OccurredAt: ts, DedupKey: e.Get("id").String(),
				}, offset)
			}
		}

	default:
		r.reduceOpenCodeEvents(e, ts)
	}
}

// reduceOpenCodeEvents records non-turn parts on a user (or other) message,
// today just a compaction marker.
func (r *reducer) reduceOpenCodeEvents(e gjson.Result, ts time.Time) {
	for _, p := range e.Get("parts").Array() {
		pd := p.Get("data")
		if pd.Get("type").String() == "compaction" {
			r.addEvent(EventCompaction, map[string]any{
				"auto":          pd.Get("auto").Bool(),
				"tail_start_id": pd.Get("tail_start_id").String(),
			}, ts)
		}
	}
}

func openCodeApplyToolResult(r *reducer, callUID string, state gjson.Result) {
	status := state.Get("status").String()
	switch status {
	case "completed":
		out := state.Get("output")
		if !out.Exists() {
			return
		}
		r.applyResult(callUID, out, false)
	case "error":
		if out := state.Get("output"); out.Exists() {
			r.applyResult(callUID, out, true)
			return
		}
		if errv := state.Get("error"); errv.Exists() {
			r.applyResult(callUID, errv, true)
		}
	}
}

func openCodeText(e gjson.Result) string {
	var parts []string
	for _, p := range e.Get("parts").Array() {
		pd := p.Get("data")
		if pd.Get("type").String() == "text" {
			if t := pd.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func openCodeMessageTime(data, e gjson.Result) time.Time {
	if t := parseUnixMilli(data.Get("time.created").Int()); !t.IsZero() {
		return t
	}
	if t := parseUnixMilli(data.Get("time.completed").Int()); !t.IsZero() {
		return t
	}
	return parseUnixMilli(e.Get("time_created").Int())
}

func openCodeAPIError(data gjson.Result) string {
	err := data.Get("error")
	if !err.Exists() {
		return ""
	}
	if m := err.Get("data.message").String(); m != "" {
		return m
	}
	if m := err.Get("message").String(); m != "" {
		return m
	}
	return err.Get("name").String()
}

func parseUnixMilli(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// opencodeBodyFields lifts OpenCode tool inputs (state.input) and terminal
// results (state.output, or state.error on failure). The cases mirror
// reduceOpenCode's tool branch.
func opencodeBodyFields(e gjson.Result) []bodyField {
	if e.Get("type").String() != "message" {
		return nil
	}
	var fields []bodyField
	for _, p := range e.Get("parts").Array() {
		pd := p.Get("data")
		if pd.Get("type").String() != "tool" {
			continue
		}
		state := pd.Get("state")
		in := state.Get("input")
		if f, ok := rawField(in, in.Raw, "application/json", bodyKindInput); ok {
			fields = append(fields, f)
		}
		switch state.Get("status").String() {
		case "completed":
			out := state.Get("output")
			c, media := bodyContent(out)
			if f, ok := rawField(out, c, media, bodyKindResult); ok {
				fields = append(fields, f)
			}
		case "error":
			body := state.Get("output")
			if !body.Exists() {
				body = state.Get("error")
			}
			c, media := bodyContent(body)
			if f, ok := rawField(body, c, media, bodyKindResult); ok {
				fields = append(fields, f)
			}
		}
	}
	return fields
}

// locateOpenCode finds OpenCode tool bodies: each tool part's state.input and,
// when the call is terminal, its state.output or state.error. It is the
// streaming twin of opencodeBodyFields.
func locateOpenCode(s *lineSource, emit func(BodyLocation) error) error {
	typ, err := s.topType(Key("type"))
	if err != nil || typ != "message" {
		return err
	}
	return WalkArrayElements(s.ctx, []Step{Key("parts")}, []Step{Key("data")}, s.reader(),
		func(_ int, _ ValueSpan, subs map[Step]ValueSpan) error {
			data, ok := subs[Key("data")]
			if !ok {
				return nil
			}
			return s.locateOpenCodePart(data, emit)
		})
}

func (s *lineSource) locateOpenCodePart(data ValueSpan, emit func(BodyLocation) error) error {
	paths := [][]Step{
		{Key("type")},
		{Key("state"), Key("status")},
		{Key("state"), Key("input")},
		{Key("state"), Key("output")},
		{Key("state"), Key("error")},
	}
	r := CanonicalBodyReader(s.ctx, s.f, s.base, data, BodyRaw)
	located, err := LocateValues(s.ctx, paths, readerWindows(r))
	if err != nil {
		return err
	}
	spans := make(map[int]ValueSpan, len(located))
	for _, ls := range located {
		sp := ls.Span
		sp.Start += data.Start
		sp.End += data.Start
		spans[ls.PathIndex] = sp
	}
	typeSpan, ok := spans[0]
	if !ok {
		return nil
	}
	bt, err := s.unquoted(typeSpan)
	if err != nil || bt != "tool" {
		return err
	}
	if in, ok := spans[2]; ok && in.End > in.Start {
		loc := BodyLocation{Span: in, Kind: BodyRaw, Media: "application/json"}
		if loc.FilePath, loc.Detail, err = s.inputProjections(in, BodyRaw, "application/json"); err != nil {
			return err
		}
		if err := emit(loc); err != nil {
			return err
		}
	}
	status := ""
	if st, ok := spans[1]; ok {
		status, err = s.unquoted(st)
		if err != nil {
			return err
		}
	}
	if status != "completed" && status != "error" {
		return nil
	}
	out, ok := spans[3]
	if !ok || out.End <= out.Start {
		out, ok = spans[4]
		if !ok || out.End <= out.Start {
			return nil
		}
	}
	loc, ok, err := s.classifyResult(out)
	if err != nil || !ok {
		return err
	}
	return emit(loc)
}
