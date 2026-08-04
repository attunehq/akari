package parser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

// ctxCheckBytes bounds how many bytes the streaming scanners feed between
// context cancellation checks. A single transcript line can be hundreds of MiB,
// so a per-chunk check alone is not enough when one next() chunk is itself huge;
// checking every ctxCheckBytes keeps cancellation responsive without paying an
// atomic load per byte.
const ctxCheckBytes = 64 << 10

// ValueSpan is the byte range [Start,End) of one located JSON value within a
// single JSONL line, relative to the line's first byte (offset 0 = first byte of
// the line). It matches gjson's value Index and Index+len(Raw) exactly.
type ValueSpan struct {
	Start int64
	End   int64
}

// Step is one segment of a path into a JSON document: either an object Key or an
// array Idx. The marker method keeps the two step kinds in a single closed set
// so callers cannot smuggle in an arbitrary type.
type Step interface{ isStep() }

// Key selects an object member by name.
type Key string

// Idx selects an array element by its 0-based position.
type Idx int

func (Key) isStep() {}
func (Idx) isStep() {}

// LocatedSpan pairs a located value's span with the index of the path that
// produced it, so callers can correlate results back to their request even when
// some paths are absent and skipped.
type LocatedSpan struct {
	PathIndex int
	Span      ValueSpan
}

// LocateValues scans the JSONL line exactly once, streaming, and returns the
// byte span of every requested path that is present, in source order (the order
// the values appear in the line, which for distinct leaf paths is also request
// order for well-formed transcripts). Absent paths are skipped.
//
// The motivation is lifting very large tool-call bodies (a single JSON value,
// possibly hundreds of MiB) out of a transcript line without ever buffering the
// whole line or the whole value. The returned spans are byte-identical to
// gjson's value .Index (Start) and .Index+len(.Raw) (End), so the exact bytes
// they delimit can be sha256'd to match what the server stored.
//
// next supplies the line incrementally: each call returns the next chunk of
// bytes (any size greater than zero) until it reports io.EOF. The scanner is
// correct for any chunking, including a single byte per call, and it retains
// only O(path depth) state plus the constant overhead of one chunk at a time. It
// never accumulates the bytes of a located value.
//
// ctx lets a caller cancel a scan of a huge line promptly: it is checked once
// per chunk returned by next() and again every ctxCheckBytes within a chunk, so
// a canceled hash or upload aborts instead of streaming the whole value.
func LocateValues(ctx context.Context, paths [][]Step, next func() ([]byte, error)) ([]LocatedSpan, error) {
	sc := newScanner(paths)
scan:
	for !sc.done() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, err := next()
		for i, b := range chunk {
			if i%ctxCheckBytes == 0 && i > 0 {
				if cerr := ctx.Err(); cerr != nil {
					return nil, cerr
				}
				if sc.done() {
					break scan
				}
			}
			if ferr := sc.feed(b); ferr != nil {
				return nil, ferr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		// A nil/empty chunk with a nil error is permitted; just ask again.
	}
	if err := sc.finish(); err != nil {
		return nil, err
	}
	return sc.results, nil
}

// LocateValue is the single-path convenience wrapper over LocateValues. ok is
// false when the path is absent from the line.
func LocateValue(ctx context.Context, path []Step, next func() ([]byte, error)) (ValueSpan, bool, error) {
	res, err := LocateValues(ctx, [][]Step{path}, next)
	if err != nil {
		return ValueSpan{}, false, err
	}
	if len(res) == 0 {
		return ValueSpan{}, false, nil
	}
	return res[0].Span, true, nil
}

// pendingValue tracks a path whose value the scan is currently inside, so its End can
// be recorded once the value closes. depth is the container-stack depth at which the
// value lives, used to detect when a container value has fully closed.
type pendingValue struct {
	pathIndex int
	depth     int
	// container is true when the located value is itself an object or array, so its
	// End is the matching close bracket; false for strings and scalars, whose End is
	// their own terminator.
	container bool
}

// scanner records the span of every requested path as the tokenizer walks past it.
type scanner struct {
	jsonTokenizer

	paths   [][]Step
	results []LocatedSpan

	// pendings holds values already entered but not yet closed, whose End is still
	// outstanding.
	pendings []pendingValue
}

func newScanner(paths [][]Step) *scanner {
	s := &scanner{paths: paths}
	s.ev = s
	return s
}

// atPath reports whether the structural position about to receive a value matches the
// given path. The position is described by the container stack plus the key or index
// the value sits under: a value living directly inside the stack's containers sits at
// depth len(stack), so its path must have exactly that many steps and every step must
// agree with the corresponding frame.
func (s *scanner) atPath(path []Step) bool {
	if len(path) != len(s.stack) {
		return false
	}
	for i, fr := range s.stack {
		switch step := path[i].(type) {
		case Key:
			if !fr.isObject || fr.curKey != string(step) {
				return false
			}
		case Idx:
			if fr.isObject || fr.arrIdx != int(step) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// valueStart records Start for any path matching the current structural position and
// registers a pending close so End can be captured when the value ends.
func (s *scanner) valueStart(start int64, kind valueKind) error {
	for pi, path := range s.paths {
		if s.alreadyLocated(pi) {
			continue
		}
		if s.atPath(path) {
			s.pendings = append(s.pendings, pendingValue{
				pathIndex: pi,
				depth:     len(s.stack),
				container: kind.container(),
			})
			// Stash Start immediately as a placeholder result; End is patched in when
			// the value closes, found by pathIndex.
			s.results = append(s.results, LocatedSpan{
				PathIndex: pi,
				Span:      ValueSpan{Start: start, End: -1},
			})
		}
	}
	return nil
}

// valueEnd closes the innermost open non-container pending. Strings and scalars cannot
// nest, so the most recently opened one is the one ending here.
func (s *scanner) valueEnd(end int64) error {
	for i := len(s.pendings) - 1; i >= 0; i-- {
		if s.pendings[i].container {
			continue
		}
		s.setEnd(s.pendings[i].pathIndex, end)
		s.pendings = append(s.pendings[:i], s.pendings[i+1:]...)
		return nil
	}
	return nil
}

// containerEnd closes every container pending entered at closedDepth: their immediate
// container was the one just popped, so their own depth equals closedDepth.
func (s *scanner) containerEnd(end int64, closedDepth int) error {
	for i := len(s.pendings) - 1; i >= 0; i-- {
		p := s.pendings[i]
		if !p.container || p.depth != closedDepth {
			continue
		}
		s.setEnd(p.pathIndex, end)
		s.pendings = append(s.pendings[:i], s.pendings[i+1:]...)
	}
	return nil
}

// done reports whether every requested path has been located and closed, so no later
// byte can change the answer. Matching is first-occurrence-wins (see alreadyLocated),
// which is exactly what makes stopping here output-identical to scanning to EOF: a span
// recorded now can never be replaced by a later one. It matters because the
// discriminator lookups read a type field near the front of a line whose tool body can
// run to hundreds of MiB.
func (s *scanner) done() bool {
	return len(s.results) == len(s.paths) && len(s.pendings) == 0
}

// alreadyLocated reports whether a path already has a recorded span, so the same path
// is not matched twice (the first occurrence wins, matching a single gjson lookup).
func (s *scanner) alreadyLocated(pi int) bool {
	for _, r := range s.results {
		if r.PathIndex == pi {
			return true
		}
	}
	return false
}

func (s *scanner) setEnd(pi int, end int64) {
	for i := range s.results {
		if s.results[i].PathIndex == pi && s.results[i].Span.End == -1 {
			s.results[i].Span.End = end
			return
		}
	}
}

// finish flushes any value still open at EOF and drops the results that never closed.
func (s *scanner) finish() error {
	if err := s.flushScalar(); err != nil {
		return err
	}
	// A result without an End came from malformed or truncated input: it would
	// otherwise carry the sentinel -1.
	filtered := s.results[:0]
	for _, r := range s.results {
		if r.Span.End != -1 {
			filtered = append(filtered, r)
		}
	}
	s.results = filtered
	return nil
}

// WalkArrayElements scans the JSONL line exactly once, streaming, and invokes
// visit for each direct element of the array located at arrPath, in source order.
// For every element it reports the element's own byte span plus, for an object
// element, the byte spans of any requested subKeys that are present as direct
// members. Elements that are not objects (a bare string, a number) simply carry
// an empty subSpans map.
//
// This is the single-pass primitive behind the block walkers: a transcript line's
// content array can hold many blocks, and probing each block index with its own
// LocateValues pass restreams the whole line per element (O(line * elements)).
// Walking the array once is O(line) total while keeping memory at O(path depth):
// element bodies (which can be hundreds of MiB) are never buffered, only the tiny
// element span and the small subKey spans are retained, and each is handed to
// visit as soon as the element closes so nothing accumulates across elements.
//
// next supplies the line incrementally exactly as LocateValues consumes it: each
// call returns the next chunk of bytes until it reports io.EOF, and the walker is
// correct for any chunking. visit is called with the element index (0-based), the
// element span, and a map from the matched subKey Step to its span. Returning a
// non-nil error from visit aborts the walk and is propagated. The reported spans
// are byte-identical to gjson (value .Index for Start, .Index+len(.Raw) for End).
//
// Only direct members of an element object are matched for subKeys: a subKey is a
// single Step (for example Key("type")), not a nested path, because block
// discriminators and bodies live one level under the element.
//
// ctx lets a caller abort a walk over a huge array promptly: it is checked once
// per chunk returned by next() and again every ctxCheckBytes within a chunk, so
// a canceled hash or upload of a large array result stops mid-enumeration rather
// than draining the whole region.
func WalkArrayElements(ctx context.Context, arrPath []Step, subKeys []Step, next func() ([]byte, error), visit func(idx int, elemSpan ValueSpan, subSpans map[Step]ValueSpan) error) error {
	w := newArrayWalker(arrPath, subKeys, visit)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, err := next()
		for i, b := range chunk {
			if i%ctxCheckBytes == 0 && i > 0 {
				if cerr := ctx.Err(); cerr != nil {
					return cerr
				}
			}
			if ferr := w.feed(b); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	return w.flushScalar()
}

// arrayWalker captures one element of the target array at a time, flushing each to the
// visit callback the instant the element closes so element bodies are never retained.
type arrayWalker struct {
	jsonTokenizer

	arrPath []Step
	subKeys []Step
	visit   func(idx int, elemSpan ValueSpan, subSpans map[Step]ValueSpan) error

	// arrDepth is the stack depth of the target array's frame once entered, or -1
	// when the walk is not inside the target array. Elements are the values that live
	// directly in that array, at stack depth arrDepth+1.
	arrDepth int

	// elem is the span of the array element currently being scanned, valid while
	// inElem is true. elemSubs collects the matched subKey spans for that element.
	// elemContainer marks an element that is itself an object or array, whose End is
	// its matching close bracket rather than a scalar or string terminator.
	inElem        bool
	elemContainer bool
	elem          ValueSpan
	elemSubs      map[Step]ValueSpan
	elemIdx       int

	// sub tracks a subKey value currently open inside the element object so its End
	// can be recorded when it closes. subActive is the matched Step; subContainer
	// distinguishes a container value's close-bracket terminator.
	subActive    Step
	subOpen      bool
	subContainer bool
	subStart     int64
	subDepth     int
}

func newArrayWalker(arrPath, subKeys []Step, visit func(idx int, elemSpan ValueSpan, subSpans map[Step]ValueSpan) error) *arrayWalker {
	w := &arrayWalker{arrPath: arrPath, subKeys: subKeys, visit: visit, arrDepth: -1}
	w.ev = w
	return w
}

// arrayMatchesPath reports whether the array value about to begin sits exactly at
// arrPath. It follows the same convention as the scanner's atPath: a value living
// directly inside the current containers sits at depth len(stack), so its path has
// exactly len(stack) steps and each enclosing frame's key or index must agree.
func (w *arrayWalker) arrayMatchesPath() bool {
	if len(w.arrPath) != len(w.stack) {
		return false
	}
	for i, fr := range w.stack {
		switch step := w.arrPath[i].(type) {
		case Key:
			if !fr.isObject || fr.curKey != string(step) {
				return false
			}
		case Idx:
			if fr.isObject || fr.arrIdx != int(step) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// inTargetArray reports whether the value about to begin is a direct element of the
// target array (the innermost frame is that array).
func (w *arrayWalker) inTargetArray() bool {
	return w.arrDepth >= 0 && len(w.stack) == w.arrDepth+1
}

// inElementObject reports whether the value about to begin is a direct member of the
// current element object (one level below the array).
func (w *arrayWalker) inElementObject() bool {
	return w.inElem && w.arrDepth >= 0 && len(w.stack) == w.arrDepth+2 && w.top() != nil && w.top().isObject
}

// valueStart records the start of an element or of a requested subKey when the position
// matches, and recognizes entry into the target array.
func (w *arrayWalker) valueStart(start int64, kind valueKind) error {
	if w.inTargetArray() {
		w.inElem = true
		w.elemContainer = kind.container()
		w.elem = ValueSpan{Start: start, End: -1}
		w.elemSubs = nil
		w.elemIdx = w.top().arrIdx
	} else if w.inElementObject() && !w.subOpen {
		key := w.top().curKey
		for _, sk := range w.subKeys {
			if k, ok := sk.(Key); ok && string(k) == key {
				w.subActive = sk
				w.subOpen = true
				w.subContainer = kind.container()
				w.subStart = start
				w.subDepth = len(w.stack)
				break
			}
		}
	}
	// The tokenizer has not pushed this value's frame yet, so the stack still holds
	// exactly the parent containers arrayMatchesPath inspects, and the array's own
	// frame will land at the current depth.
	if kind == arrayValue && w.arrDepth < 0 && w.arrayMatchesPath() {
		w.arrDepth = len(w.stack)
	}
	return nil
}

// valueEnd records the End of an open string or scalar element or subKey, flushing a
// completed element to visit.
func (w *arrayWalker) valueEnd(end int64) error {
	// A subKey value closes first: it is deeper than the element.
	if w.subOpen && !w.subContainer && w.subDepth == len(w.stack) {
		w.recordSub(end)
		return nil
	}
	if w.inElem && !w.elemContainer {
		w.elem.End = end
		return w.flushElem()
	}
	return nil
}

func (w *arrayWalker) containerEnd(end int64, closedDepth int) error {
	// An open subKey container closes when its own depth is the depth just popped.
	if w.subOpen && w.subContainer && w.subDepth == closedDepth {
		w.recordSub(end)
	}
	// An element container closes when the array frame is the innermost frame again
	// (the element lived one level deeper than the array).
	if w.inElem && w.elemContainer && w.arrDepth >= 0 && len(w.stack) == w.arrDepth+1 {
		w.elem.End = end
		if err := w.flushElem(); err != nil {
			return err
		}
	}
	// Leaving the target array entirely: the popped frame was the array frame.
	if w.arrDepth >= 0 && closedDepth == w.arrDepth {
		w.arrDepth = -1
	}
	return nil
}

// recordSub stores a matched subKey span on the current element.
func (w *arrayWalker) recordSub(end int64) {
	if w.elemSubs == nil {
		w.elemSubs = make(map[Step]ValueSpan, len(w.subKeys))
	}
	w.elemSubs[w.subActive] = ValueSpan{Start: w.subStart, End: end}
	w.subOpen = false
	w.subActive = nil
}

// flushElem hands the completed element to visit and clears element state so the next
// element starts fresh and nothing accumulates across elements.
func (w *arrayWalker) flushElem() error {
	subs := w.elemSubs
	if subs == nil {
		subs = map[Step]ValueSpan{}
	}
	idx := w.elemIdx
	elem := w.elem
	w.inElem = false
	w.elemSubs = nil
	w.elem = ValueSpan{}
	w.subOpen = false
	w.subActive = nil
	return w.visit(idx, elem, subs)
}

// decodeKey turns a raw quoted JSON string (including both surrounding quotes)
// into its member-name value, resolving the escape sequences that can appear in
// object keys. Keys are compared against path steps, so they must be decoded to
// match what the caller wrote.
func decodeKey(raw []byte) string {
	if len(raw) < 2 {
		return ""
	}
	body := raw[1 : len(raw)-1]
	// Fast path: no escapes means the body is the name verbatim.
	hasEsc := false
	for _, c := range body {
		if c == '\\' {
			hasEsc = true
			break
		}
	}
	if !hasEsc {
		return string(body)
	}
	var key string
	if err := json.Unmarshal(raw, &key); err != nil {
		return ""
	}
	return key
}

// decodeHex4 reads up to four hex digits and returns the code point. Malformed
// input (assumed not to occur for well-formed JSON strings) yields the
// replacement character.
func decodeHex4(b []byte) rune {
	if len(b) < 4 {
		return '\uFFFD'
	}
	var r rune
	for i := 0; i < 4; i++ {
		c := b[i]
		var v rune
		switch {
		case c >= '0' && c <= '9':
			v = rune(c - '0')
		case c >= 'a' && c <= 'f':
			v = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = rune(c-'A') + 10
		default:
			return '\uFFFD'
		}
		r = r<<4 | v
	}
	return r
}
