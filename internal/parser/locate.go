package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// readFull reads exactly len(buf) bytes at off from f, treating any short read as
// a hard error rather than silently zero-filling. A transcript line declares the
// byte ranges of its values; if the file holds fewer bytes than a span claims the
// file is truncated, and reporting that as io.ErrUnexpectedEOF (with the range)
// keeps a corrupted store from being mistaken for valid empty data. It mirrors the
// full-read-or-error discipline of internal/client/upload.readAt, adapted to the
// io.ReaderAt the parser is handed rather than an *os.File.
func readFull(f io.ReaderAt, buf []byte, off int64) error {
	n, err := f.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err == nil || err == io.EOF {
		return fmt.Errorf("short read at [%d,%d): got %d of %d bytes: %w", off, off+int64(len(buf)), n, len(buf), io.ErrUnexpectedEOF)
	}
	// A non-EOF read failure keeps the same [off,off+len) context so the caller can
	// tell which span could not be read, not just that a read failed somewhere.
	return fmt.Errorf("read at [%d,%d): %w", off, off+int64(len(buf)), err)
}

// BodyLocation is one tool body found in a transcript line by streaming, ready to
// be lifted to the CAS without ever buffering the body. Span is the raw byte range
// of the value within the line (relative to the line's first byte), the bytes the
// sentinel replaces. Kind and Media say how to canonicalize the raw value into the
// bytes the CAS stores (CanonicalBodyReader), so the streamed body is byte
// identical to what the server records inline today. FilePath is the top-level
// file_path string of a JSON tool input (empty otherwise), and Detail is the
// input's short human-scannable summary. IsError preserves a Codex result status
// derived from its leading banner. These fields let the streaming and buffered
// paths build byte-identical sentinels.
type BodyLocation struct {
	Span     ValueSpan
	Kind     BodyKind
	Media    string
	FilePath string
	Detail   string
	IsError  bool
}

// LocateToolBodies enumerates the tool input and result bodies in one transcript
// line, in source order, by streaming the line from the file rather than parsing
// it whole. It is the streaming twin of toolBodyFields: the same agent knowledge
// (which paths are bodies, which media each gets), but expressed as byte spans and
// a canonicalization kind so a hundreds-of-MiB body is never resident.
//
// The line lives at [lineOff, lineOff+lineLen) in f. Enumeration reads only the
// small structural parts (block `type` discriminators), never a body. A line whose
// shape is unknown or carries no tool body yields nothing.
//
// Results stream through emit, called once per located body in source order,
// rather than being collected into a slice. This lets the client lift one body at
// a time (upload it, rewrite its span) without the parser holding a slice whose
// size grows with the block count, so peak memory stays bounded by the structural
// scan, not by how many bodies a line carries. If emit returns an error the walk
// aborts and that error is returned. ctx threads through the structural scans so a
// canceled lift stops promptly even mid-line.
func LocateToolBodies(ctx context.Context, agent Agent, f io.ReaderAt, lineOff, lineLen int64, emit func(BodyLocation) error) error {
	src := &lineSource{ctx: ctx, f: f, base: lineOff, size: lineLen}
	switch agent {
	case AgentClaude:
		return locateClaude(src, emit)
	case AgentCodex:
		return locateCodex(src, emit)
	case AgentPi:
		return locatePi(src, emit)
	case AgentCursor:
		return locateCursor(src, emit)
	case AgentGrok:
		return locateGrok(src, emit)
	case AgentOpenCode:
		return locateOpenCode(src, emit)
	default:
		return nil
	}
}

// lineSource streams a single line's bytes from a file span and reads small fixed
// spans within it. It exists so the enumerator can both run a streaming
// LocateValues pass (via reader) and pull a tiny value (a block `type`) by span
// without buffering the whole line. ctx carries the caller's cancellation into
// every streaming scan the source drives.
type lineSource struct {
	ctx  context.Context
	f    io.ReaderAt
	base int64 // file offset of the line's first byte
	size int64 // line length in bytes
}

// scanChunk bounds how much of the line the enumerator pulls per read while
// streaming it through LocateValues. It is small: enumeration only walks structure
// and never materializes a body.
const scanChunk = 64 << 10

// reader returns a next() that streams the whole line in bounded windows, for a
// LocateValues pass.
func (s *lineSource) reader() func() ([]byte, error) {
	pos := int64(0)
	// One window, reused across calls: the scan consumes each chunk byte by byte and
	// retains none of it, so a fresh allocation per 64 KiB would churn once per window
	// for every pass over a line that can run to hundreds of MiB.
	buf := make([]byte, scanChunk)
	return func() ([]byte, error) {
		if pos >= s.size {
			return nil, io.EOF
		}
		n := s.size - pos
		if n > scanChunk {
			n = scanChunk
		}
		win := buf[:n]
		// The window lies wholly within the declared line, so a short read here means
		// the file is shorter than the line claims: a truncation, not a clean EOF.
		if err := readFull(s.f, win, s.base+pos); err != nil {
			return nil, err
		}
		pos += n
		var perr error
		if pos >= s.size {
			perr = io.EOF
		}
		return win, perr
	}
}

// readSpan pulls a small value's bytes (a block `type` discriminator) by its span.
// It refuses spans larger than a tiny cap so a malformed line can never trick the
// enumerator into buffering a body here; a body's own bytes are only ever streamed
// through CanonicalBodyReader.
func (s *lineSource) readSpan(sp ValueSpan) (string, error) {
	const cap = 4 << 10
	n := sp.End - sp.Start
	if n <= 0 || n > cap {
		return "", nil
	}
	buf := make([]byte, n)
	// The span sits within the line, so fewer bytes than the span length means a
	// truncated file rather than a legitimately short value.
	if err := readFull(s.f, buf, s.base+sp.Start); err != nil {
		return "", err
	}
	return string(buf), nil
}

// locate runs one streaming LocateValues pass for the given paths and returns the
// spans keyed by their path index.
func (s *lineSource) locate(paths [][]Step) (map[int]ValueSpan, error) {
	spans, err := LocateValues(s.ctx, paths, s.reader())
	if err != nil {
		return nil, fmt.Errorf("locate tool body spans: %w", err)
	}
	out := make(map[int]ValueSpan, len(spans))
	for _, ls := range spans {
		out[ls.PathIndex] = ls.Span
	}
	return out, nil
}

// unquoted returns the decoded contents of a small JSON string value at span, used
// to read a block `type`. The value is tiny, so a one-shot decode is fine.
func (s *lineSource) unquoted(sp ValueSpan) (string, error) {
	raw, err := s.readSpan(sp)
	if err != nil {
		return "", err
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return jsonUnquote(raw), nil
	}
	return raw, nil
}

// locateClaude finds Claude tool inputs and the bodies duplicated across user lines.
// User fields are registered together so their byte spans, rather than schema order,
// decide emission order when toolUseResult appears before message.
func locateClaude(s *lineSource, emit func(BodyLocation) error) error {
	typ, err := s.topType(Key("type"))
	if err != nil {
		return err
	}
	switch typ {
	case "assistant":
		return s.locateBlocks(
			[]Step{Key("message"), Key("content")},
			"tool_use", Key("input"), BodyRaw, "application/json", emit)
	case "user":
		return s.locateClaudeUser(emit)
	}
	return nil
}

func (s *lineSource) locateClaudeUser(emit func(BodyLocation) error) error {
	contentPath := []Step{Key("message"), Key("content")}
	resultPath := []Step{Key("toolUseResult")}
	paths := [][]Step{
		contentPath,
		resultPath,
		{Key("toolUseResult"), Key(sentinelKey)},
	}
	spans, err := s.locate(paths)
	if err != nil {
		return err
	}
	content, hasContent := spans[0]
	result, hasResult := spans[1]
	_, resultIsSentinel := spans[2]

	emitBlocks := func() error {
		if !hasContent {
			return nil
		}
		return s.locateClaudeUserBlocks(contentPath, emit)
	}
	emitResult := func() error {
		if !hasResult || resultIsSentinel {
			return nil
		}
		return s.emitClaudeToolUseResult(result, emit)
	}
	if hasResult && (!hasContent || result.Start < content.Start) {
		if err := emitResult(); err != nil {
			return err
		}
		return emitBlocks()
	}
	if err := emitBlocks(); err != nil {
		return err
	}
	return emitResult()
}

func (s *lineSource) locateClaudeUserBlocks(arr []Step, emit func(BodyLocation) error) error {
	return WalkArrayElements(s.ctx, arr, []Step{Key("type"), Key("content"), Key("source")}, s.reader(),
		func(_ int, _ ValueSpan, subs map[Step]ValueSpan) error {
			typeSpan, ok := subs[Key("type")]
			if !ok {
				return nil
			}
			bt, err := s.unquoted(typeSpan)
			if err != nil {
				return err
			}
			switch bt {
			case "tool_result":
				body, ok := subs[Key("content")]
				if !ok || body.End <= body.Start {
					return nil
				}
				loc, ok, err := s.classifyResult(body)
				if err != nil || !ok {
					return err
				}
				return emit(loc)
			case "image":
				source, ok := subs[Key("source")]
				if !ok {
					return nil
				}
				return s.locateImageInSpan(source, emit)
			}
			return nil
		})
}

// locateImageInSpan keeps source.data streaming even though the array walker only
// reports direct block fields. Its result span is translated back to line offsets.
func (s *lineSource) locateImageInSpan(source ValueSpan, emit func(BodyLocation) error) error {
	r := CanonicalBodyReader(s.ctx, s.f, s.base, source, BodyRaw)
	sp, ok, err := LocateValue(s.ctx, []Step{Key("data")}, readerWindows(r))
	if err != nil || !ok {
		return err
	}
	sp.Start += source.Start
	sp.End += source.Start
	return s.emitImage(sp, emit)
}

// emitClaudeToolUseResult deliberately keeps arrays as JSON. Typed-block flattening
// belongs only to message.content tool results.
func (s *lineSource) emitClaudeToolUseResult(sp ValueSpan, emit func(BodyLocation) error) error {
	head, err := s.readHead(sp)
	if err != nil {
		return err
	}
	switch head {
	case '{', '[':
		return emit(BodyLocation{Span: sp, Kind: BodyRaw, Media: "application/json"})
	case '"':
		return emit(BodyLocation{Span: sp, Kind: BodyJSONString, Media: "text/plain"})
	default:
		return nil
	}
}

// locatePi finds pi tool inputs (assistant) and tool results (toolResult message).
func locatePi(s *lineSource, emit func(BodyLocation) error) error {
	typ, err := s.topType(Key("type"))
	if err != nil {
		return err
	}
	if typ != "message" {
		return nil
	}
	role, err := s.unquotedAt([]Step{Key("message"), Key("role")})
	if err != nil {
		return err
	}
	switch role {
	case "assistant":
		return s.locateBlocks(
			[]Step{Key("message"), Key("content")},
			"toolCall", Key("arguments"), BodyRaw, "application/json", emit)
	case "toolResult":
		return s.locateSingleResult([]Step{Key("message"), Key("content")}, emit)
	}
	return nil
}

// locateCursor finds Cursor tool inputs (the tool_use blocks of an assistant
// transcript line); the transcript records no tool results. It is the streaming
// twin of cursorBodyFields.
func locateCursor(s *lineSource, emit func(BodyLocation) error) error {
	role, err := s.topType(Key("role"))
	if err != nil {
		return err
	}
	if role != "assistant" {
		return nil
	}
	return s.locateBlocks(
		[]Step{Key("message"), Key("content")},
		"tool_use", Key("input"), BodyRaw, "application/json", emit)
}

// locateGrok finds Grok tool bodies: a tool_call line's rawInput and a terminal
// tool_call_update's rawOutput. It is the streaming twin of grokBodyFields.
func locateGrok(s *lineSource, emit func(BodyLocation) error) error {
	kind, err := s.unquotedAt([]Step{Key("params"), Key("update"), Key("sessionUpdate")})
	if err != nil {
		return err
	}
	update := func(k string) []Step { return []Step{Key("params"), Key("update"), Key(k)} }
	switch kind {
	case "tool_call":
		return s.locateSingle(update("rawInput"), BodyRaw, "application/json", emit)
	case "tool_call_update":
		status, err := s.unquotedAt(update("status"))
		if err != nil {
			return err
		}
		if status != "completed" && status != "failed" {
			return nil
		}
		return s.locateSingleResult(update("rawOutput"), emit)
	}
	return nil
}

// locateCodex finds every liftable Codex body in source order: tool inputs
// (function_call arguments, custom_tool_call input), tool results
// (function_call_output and custom_tool_call_output), and the base64 images Codex
// inlines (image_generation results, data-URI image_url blocks in a user turn, and the
// images array of a user_message event). It is the streaming twin of codexBodyFields,
// so the two agree on which bytes are bodies and how each canonicalizes.
func locateCodex(s *lineSource, emit func(BodyLocation) error) error {
	typ, err := s.topType(Key("type"))
	if err != nil {
		return err
	}
	ptype, err := s.unquotedAt([]Step{Key("payload"), Key("type")})
	if err != nil {
		return err
	}
	payload := func(k string) []Step { return []Step{Key("payload"), Key(k)} }
	switch typ {
	case "response_item":
		// The discriminators mirror reduceCodex (and codexBodyFields): a tool item is
		// keyed by payload.type, a user turn by payload.role, since a Codex message has
		// no payload.type. Reading role only when no tool type matched keeps the common
		// path to one extra structural lookup.
		switch {
		case ptype == "function_call":
			return s.locateSingle(payload("arguments"), BodyJSONString, "application/json", emit)
		case ptype == "custom_tool_call":
			return s.locateSingle(payload("input"), BodyJSONString, "text/plain", emit)
		case ptype == "function_call_output", ptype == "custom_tool_call_output":
			return s.locateCodexResult(payload("output"), emit)
		case ptype == "image_generation_call":
			return s.locateImage(payload("result"), emit)
		default:
			role, err := s.unquotedAt([]Step{Key("payload"), Key("role")})
			if err != nil {
				return err
			}
			if role == "user" {
				return s.locateImageBlocks(payload("content"), emit)
			}
		}
	case "event_msg":
		switch ptype {
		case "image_generation_end":
			return s.locateImage(payload("result"), emit)
		case "user_message":
			return s.locateImageArray(payload("images"), emit)
		}
	}
	return nil
}

func (s *lineSource) locateCodexResult(path []Step, emit func(BodyLocation) error) error {
	spans, err := s.locate([][]Step{path})
	if err != nil {
		return err
	}
	sp, ok := spans[0]
	if !ok || sp.End <= sp.Start {
		return nil
	}
	loc, ok, err := s.classifyResult(sp)
	if err != nil || !ok {
		return err
	}
	loc.IsError, err = codexResultReaderIsErr(CanonicalBodyReader(s.ctx, s.f, s.base, sp, loc.Kind))
	if err != nil {
		return fmt.Errorf("read codex result banner: %w", err)
	}
	return emit(loc)
}

// locateImage emits the base64 image at a single fixed path as a BodyBase64 body,
// classifying its media from the value's head. A value that is absent, empty, or not a
// recognizable base64 image yields nothing (it stays inline).
func (s *lineSource) locateImage(path []Step, emit func(BodyLocation) error) error {
	spans, err := s.locate([][]Step{path})
	if err != nil {
		return err
	}
	sp, ok := spans[0]
	if !ok {
		return nil
	}
	return s.emitImage(sp, emit)
}

// locateImageBlocks walks a content array once, emitting each block's base64 image_url
// as a BodyBase64 body. It keys off the presence of a base64 image_url (not the block
// type) so it covers any image block kind, matching codexImageBlocks.
func (s *lineSource) locateImageBlocks(arr []Step, emit func(BodyLocation) error) error {
	return WalkArrayElements(s.ctx, arr, []Step{Key("image_url")}, s.reader(),
		func(_ int, _ ValueSpan, subs map[Step]ValueSpan) error {
			sp, ok := subs[Key("image_url")]
			if !ok {
				return nil
			}
			return s.emitImage(sp, emit)
		})
}

// locateImageArray walks a flat array of image strings once (the images field of a
// user_message event), emitting each base64 element as a BodyBase64 body.
func (s *lineSource) locateImageArray(arr []Step, emit func(BodyLocation) error) error {
	return WalkArrayElements(s.ctx, arr, nil, s.reader(),
		func(_ int, elem ValueSpan, _ map[Step]ValueSpan) error {
			return s.emitImage(elem, emit)
		})
}

// emitImage classifies a string value's head and emits it as a BodyBase64 body when it
// is a recognizable base64 image, choosing its media type the same way the buffered
// imageField does. A non-image or sub-quote-length span is skipped, so a non-image
// element of a walked array is passed over rather than lifted.
func (s *lineSource) emitImage(sp ValueSpan, emit func(BodyLocation) error) error {
	if sp.End-sp.Start < 2 {
		return nil // too short to be a quoted string value
	}
	head, err := s.imageHead(sp)
	if err != nil {
		return err
	}
	if !looksLikeBase64Image(head) {
		return nil
	}
	return emit(BodyLocation{Span: sp, Kind: BodyBase64, Media: imageMediaType(head)})
}

// imageHead reads the leading content bytes of a string value (inside the quotes) to
// classify it as an image and pick its media type. Base64/data-URI content carries no
// JSON escapes, so the raw bytes are the literal content, matching the buffered
// imageHead over the decoded string. The read is bounded, so a huge image is never
// buffered just to classify it.
func (s *lineSource) imageHead(sp ValueSpan) (string, error) {
	start := sp.Start + 1 // skip the opening quote
	end := sp.End - 1     // stop before the closing quote
	if end <= start {
		return "", nil
	}
	n := end - start
	if n > int64(imageHeadLen) {
		n = int64(imageHeadLen)
	}
	buf := make([]byte, n)
	if err := readFull(s.f, buf, s.base+start); err != nil {
		return "", err
	}
	return string(buf), nil
}

// topType reads a top-level discriminator string (the line `type`).
func (s *lineSource) topType(key Step) (string, error) {
	return s.unquotedAt([]Step{key})
}

// unquotedAt locates a small string value at a path and returns its decoded
// contents, or "" when absent.
func (s *lineSource) unquotedAt(path []Step) (string, error) {
	spans, err := s.locate([][]Step{path})
	if err != nil {
		return "", err
	}
	sp, ok := spans[0]
	if !ok {
		return "", nil
	}
	return s.unquoted(sp)
}

// locateSingle emits the body at a single fixed path with a known kind/media. The
// single-body cases call emit at most once. Its callers are all tool inputs, so a
// JSON body gets its file_path and detail extracted for the sentinel.
func (s *lineSource) locateSingle(path []Step, kind BodyKind, media string, emit func(BodyLocation) error) error {
	spans, err := s.locate([][]Step{path})
	if err != nil {
		return err
	}
	sp, ok := spans[0]
	if !ok || sp.End <= sp.Start {
		return nil
	}
	loc := BodyLocation{Span: sp, Kind: kind, Media: media}
	if loc.FilePath, loc.Detail, err = s.inputProjections(sp, kind, media); err != nil {
		return err
	}
	return emit(loc)
}

// inputProjections extracts the two body-derived fields a JSON tool input's
// sentinel carries (its top-level file_path and its short detail) without ever
// buffering the body, mirroring sentinelFilePath and sentinelDetail exactly (the
// buffered and streaming rewrite paths must produce byte-identical sentinels). It
// returns "" for a non-JSON input, and for each field independently when its value
// is absent, non-string, empty, or over cap. It locates file_path plus every
// detail candidate in a single LocateValues pass over the canonical body stream,
// then reads only the small spans that came back. The pass costs O(span) time at
// O(1) memory and runs only for tool inputs on the rare big-line path.
func (s *lineSource) inputProjections(sp ValueSpan, kind BodyKind, media string) (filePath, detail string, err error) {
	if media != "application/json" {
		return "", "", nil
	}
	// The canonical bytes of the input are a JSON document of their own (BodyRaw:
	// the raw object; BodyJSONString: the decoded contents of a JSON-encoded
	// string). One structural pass locates file_path and every detail candidate; a
	// candidate absent from the body simply has no span in the result map.
	var paths [][]Step
	for _, key := range filePathKeys {
		paths = append(paths, []Step{Key(key)})
	}
	for _, key := range detailKeys {
		paths = append(paths, []Step{Key(key)})
	}
	body := func() io.Reader { return CanonicalBodyReader(s.ctx, s.f, s.base, sp, kind) }
	located, err := LocateValues(s.ctx, paths, readerWindows(body()))
	if err != nil {
		return "", "", fmt.Errorf("locate input projections: %w", err)
	}
	spans := make(map[int]ValueSpan, len(located))
	for _, ls := range located {
		spans[ls.PathIndex] = ls.Span
	}
	// path indexes 0..len(filePathKeys)-1 are the file-path keys, the rest are
	// detailKeys, each list in priority order.
	for i := range filePathKeys {
		v, ok := spans[i]
		if !ok {
			continue
		}
		fp, ok, err := s.readStringSpan(sp, kind, v, maxSentinelFilePath)
		if err != nil {
			return "", "", err
		}
		if ok {
			filePath = fp
			break
		}
	}
	for i := range detailKeys {
		v, ok := spans[i+len(filePathKeys)]
		if !ok {
			continue
		}
		str, ok, err := s.readStringSpan(sp, kind, v, maxSentinelDetail)
		if err != nil {
			return "", "", err
		}
		if ok && str != "" {
			detail = str
			break
		}
	}
	return filePath, detail, nil
}

// readStringSpan reads the JSON string value at v (a span relative to the body
// document at sp) and returns its decoded contents, or ok=false when the value is
// not a string or decodes to more than maxLen. It bounds the raw span before
// reading so an over-long value is dropped without materializing it, then reads the
// small span the way the field's canonicalization allows: a raw body maps straight
// onto the file, a decoded body has no random access and is re-streamed and skipped.
// It is the shared span-read-and-unmarshal both projections use, so file_path and
// detail stay byte-identical to the buffered path.
func (s *lineSource) readStringSpan(sp ValueSpan, kind BodyKind, v ValueSpan, maxLen int) (string, bool, error) {
	// A JSON string of decoded length <= maxLen occupies at most 6 bytes per
	// character (a \uXXXX escape) plus the quotes, so a raw span past that bound
	// cannot decode under the cap and is dropped without reading.
	n := v.End - v.Start
	if n < 2 || n > int64(6*maxLen+2) {
		return "", false, nil
	}
	raw := make([]byte, n)
	if kind == BodyRaw {
		// A raw body's canonical bytes are its source bytes, so the located span maps
		// straight onto the file.
		if err := readFull(s.f, raw, s.base+sp.Start+v.Start); err != nil {
			return "", false, err
		}
	} else {
		// A decoded stream has no random access; re-stream and skip to the value.
		r := CanonicalBodyReader(s.ctx, s.f, s.base, sp, kind)
		if _, err := io.CopyN(io.Discard, r, v.Start); err != nil {
			return "", false, fmt.Errorf("seek input string span: %w", err)
		}
		if _, err := io.ReadFull(r, raw); err != nil {
			return "", false, fmt.Errorf("read input string span: %w", err)
		}
	}
	var str string
	if json.Unmarshal(raw, &str) != nil {
		return "", false, nil // not a string value; the buffered path drops it too
	}
	if len(str) > maxLen {
		return "", false, nil
	}
	return str, true, nil
}

// readerWindows adapts an io.Reader to the chunked next() a streaming scan
// consumes, pulling one bounded window per call.
func readerWindows(r io.Reader) func() ([]byte, error) {
	// Reused across calls; the scan consumes each window fully before asking for the
	// next and retains none of its bytes.
	buf := make([]byte, scanChunk)
	return func() ([]byte, error) {
		n, err := r.Read(buf)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if n == 0 {
			return nil, io.EOF
		}
		if err == io.EOF {
			return buf[:n], io.EOF
		}
		return buf[:n], nil
	}
}

// locateSingleResult emits a single result body at a fixed path, classifying its
// kind and media from its first byte (string, array, or object).
func (s *lineSource) locateSingleResult(path []Step, emit func(BodyLocation) error) error {
	spans, err := s.locate([][]Step{path})
	if err != nil {
		return err
	}
	sp, ok := spans[0]
	if !ok || sp.End <= sp.Start {
		return nil
	}
	loc, ok, err := s.classifyResult(sp)
	if err != nil || !ok {
		return err
	}
	return emit(loc)
}

// locateBlocks walks an array of content blocks in a single streaming pass,
// emitting the body at bodyKey for each block whose `type` matches wantType.
// Inputs use a fixed kind/media. Walking the array once (rather than re-streaming
// the whole line per batch of indices) keeps enumeration O(line); the walker hands
// back only the tiny type and body spans per element, never the body bytes. Each
// matching body is streamed to emit the instant its block is visited, so peak
// memory does not scale with the block count. Its callers are all tool inputs, so
// a JSON body gets its file_path and detail extracted for the sentinel.
func (s *lineSource) locateBlocks(arr []Step, wantType string, bodyKey Step, kind BodyKind, media string, emit func(BodyLocation) error) error {
	return s.walkBlocks(arr, bodyKey, func(typeSpan, bodySpan ValueSpan, hasBody bool) error {
		bt, err := s.unquoted(typeSpan)
		if err != nil {
			return err
		}
		if bt != wantType {
			return nil
		}
		if hasBody && bodySpan.End > bodySpan.Start {
			loc := BodyLocation{Span: bodySpan, Kind: kind, Media: media}
			var err error
			if loc.FilePath, loc.Detail, err = s.inputProjections(bodySpan, kind, media); err != nil {
				return err
			}
			return emit(loc)
		}
		return nil
	})
}

// walkBlocks runs one WalkArrayElements pass over the content array, invoking
// onBlock for each element that carries a `type` discriminator. It is the
// single-pass spine of locateBlocks: the caller needs each block's type span (to
// decide whether it is the wanted kind) and its body span (the value at bodyKey),
// and must preserve source order, which the walker guarantees. An element without
// a `type` (a bare string element of a result array) is skipped here because the
// caller keys off the discriminator.
func (s *lineSource) walkBlocks(arr []Step, bodyKey Step, onBlock func(typeSpan, bodySpan ValueSpan, hasBody bool) error) error {
	subKeys := []Step{Key("type"), bodyKey}
	return WalkArrayElements(s.ctx, arr, subKeys, s.reader(), func(_ int, _ ValueSpan, subs map[Step]ValueSpan) error {
		typeSpan, hasType := subs[Key("type")]
		if !hasType {
			return nil
		}
		bodySpan, hasBody := subs[bodyKey]
		return onBlock(typeSpan, bodySpan, hasBody)
	})
}

// classifyResult reads a result value's first byte to choose its canonicalization
// kind and media type, matching bodyContent's switch.
func (s *lineSource) classifyResult(sp ValueSpan) (BodyLocation, bool, error) {
	head, err := s.readHead(sp)
	if err != nil || head == 0 {
		return BodyLocation{}, false, err
	}
	kind, media := ClassifyResultBody(head)
	return BodyLocation{Span: sp, Kind: kind, Media: media}, true, nil
}

// readHead returns the first byte of a value span, the discriminator ClassifyResultBody needs.
func (s *lineSource) readHead(sp ValueSpan) (byte, error) {
	if sp.End <= sp.Start {
		return 0, nil
	}
	var b [1]byte
	// The span is non-empty (checked above), so the first byte must exist; a short
	// read here is a truncated file, not an absent value.
	if err := readFull(s.f, b[:], s.base+sp.Start); err != nil {
		return 0, err
	}
	return b[0], nil
}

// jsonUnquote decodes a small JSON string literal (a block `type`), falling back to
// the raw text when it is not a well-formed string. Bodies never go through here;
// they stream through CanonicalBodyReader, so decoding into memory is bounded to a
// discriminator's length.
func jsonUnquote(raw string) string {
	var s string
	if json.Unmarshal([]byte(raw), &s) != nil {
		return raw
	}
	return s
}
