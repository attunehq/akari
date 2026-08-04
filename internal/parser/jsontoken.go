package parser

// maxKeyLen bounds how many bytes of a single object key are buffered while it is
// read. A key longer than this cannot equal any path step the callers use, so the
// surplus is dropped: without the cap a pathological key (a whole tool body used as a
// member name) would allocate its full length, defeating the streaming design.
const maxKeyLen = 4096

// valueKind distinguishes the three value shapes a jsonTokenizer reports at a value's
// first byte. Arrays are named apart from objects because the array walk has to
// recognize its target array while the stack still holds only that array's parents,
// which is before the array's own frame is pushed.
type valueKind int

const (
	scalarValue valueKind = iota // string, number, true, false, null
	objectValue
	arrayValue
)

func (k valueKind) container() bool { return k != scalarValue }

// jsonEvents receives the value boundaries a jsonTokenizer finds. Offsets are absolute
// byte offsets into the scanned region, and an end offset is one past the value's last
// byte, so a span [start, end) delimits exactly the bytes gjson would report as .Raw.
type jsonEvents interface {
	// valueStart fires at the first byte of a value, once the container stack
	// describes that value's structural position.
	valueStart(start int64, kind valueKind) error
	// valueEnd fires at the end of a string or scalar value. A container's end
	// arrives through containerEnd instead.
	valueEnd(end int64) error
	// containerEnd fires when an object or array closes. closedDepth is the stack
	// depth its frame occupied, which is also the depth at which the values it held
	// were entered.
	containerEnd(end int64, closedDepth int) error
}

// frame is one level of the JSON container stack. It records what kind of container
// the tokenizer is inside and the structural position needed to match path steps: for
// objects, the key of the member whose value is being scanned; for arrays, the index
// of the element being scanned.
type frame struct {
	isObject bool
	// curKey is the most recently completed object key, valid while scanning the
	// value that follows it. Meaningful only when isObject.
	curKey string
	// arrIdx is the 0-based index of the array element currently being scanned. It
	// starts at -1 and increments as each element value begins. Meaningful only when
	// !isObject.
	arrIdx int
}

// jsonTokenizer is the byte-level JSON reader both span walks share. It tracks string
// and escape state so structural bytes inside a string stay literal, maintains the
// container stack with each frame's current key and element index, and reports the
// start and end of every value it passes. It retains only object keys (capped at
// maxKeyLen), never a value's bytes, so a walk costs O(depth) no matter how large a
// value is.
//
// LocateValues and WalkArrayElements differ only in what they do with those events, so
// each embeds this and supplies itself as the jsonEvents. One tokenizer is what keeps
// their spans identical: two hand-maintained copies of the same escape and key handling
// would eventually disagree by a byte, and a body span off by a byte is a corrupt body
// that still hashes and stores cleanly.
type jsonTokenizer struct {
	ev jsonEvents

	// off is the absolute offset of the NEXT byte to be fed, equivalently the count
	// of bytes consumed so far.
	off   int64
	stack []frame

	// String state. inString is true while consuming a string's contents; escNext
	// skips the byte after a backslash; uLeft counts the remaining hex digits of a
	// \uXXXX escape.
	inString bool
	escNext  bool
	uLeft    int
	// stringIsKey marks the current string as an object member key, so completing it
	// sets the enclosing frame's curKey instead of ending a value.
	stringIsKey bool
	keyBuf      []byte

	// inScalar is true while consuming a bare token (number, true, false, null),
	// which ends at the first byte that is not part of the token.
	inScalar bool

	// expectKey is true when, inside an object, the next string is a member key
	// rather than a value.
	expectKey bool
}

// top returns the innermost container frame, or nil at the document root.
func (t *jsonTokenizer) top() *frame {
	if len(t.stack) == 0 {
		return nil
	}
	return &t.stack[len(t.stack)-1]
}

// feed advances the tokenizer by a single byte b, whose absolute offset is the current
// t.off.
func (t *jsonTokenizer) feed(b byte) error {
	off := t.off
	t.off = off + 1

	// String contents: consume until the closing quote, honoring escapes. Brace and
	// bracket bytes inside a string are literal and must not affect the container
	// stack, which is the whole point of tracking string state.
	if t.inString {
		// Accumulate bytes only for object keys, which are small and must be decoded
		// for path matching. Value strings (which can be hundreds of MiB) are skipped
		// without retaining any bytes.
		if t.stringIsKey && len(t.keyBuf) < maxKeyLen {
			t.keyBuf = append(t.keyBuf, b)
		}
		switch {
		case t.escNext:
			t.escNext = false
			if b == 'u' {
				t.uLeft = 4
			}
		case t.uLeft > 0:
			t.uLeft--
		case b == '\\':
			t.escNext = true
		case b == '"':
			return t.endString(off)
		}
		return nil
	}

	// Bare scalar: ends at the first byte that is not part of the token. That
	// terminator byte is NOT consumed here; it is re-dispatched below as ordinary
	// structure.
	if t.inScalar {
		if isScalarByte(b) {
			return nil
		}
		t.inScalar = false
		// End of a scalar is the offset of the first non-token byte, which is
		// exactly off.
		if err := t.ev.valueEnd(off); err != nil {
			return err
		}
		// fall through to handle b as structure
	}

	switch b {
	case ' ', '\t', '\n', '\r':
		return nil
	case '"':
		return t.startString(off)
	case '{':
		return t.openContainer(off, objectValue)
	case '[':
		return t.openContainer(off, arrayValue)
	case '}', ']':
		return t.closeContainer(off)
	case ':':
		// Separates a key from its value inside an object: the next value is a
		// member value, not a key.
		return nil
	case ',':
		// Separates members and elements: inside an object the next string is a key;
		// inside an array the element index advances when its value begins.
		if top := t.top(); top != nil && top.isObject {
			t.expectKey = true
		}
		return nil
	default:
		// Start of a bare scalar.
		t.inScalar = true
		return t.startValue(off, scalarValue)
	}
}

// startValue runs the bookkeeping every value's first byte needs: advancing the
// enclosing array's element index so the stack describes this value's position, then
// reporting the start.
func (t *jsonTokenizer) startValue(start int64, kind valueKind) error {
	if top := t.top(); top != nil && !top.isObject {
		top.arrIdx++
	}
	return t.ev.valueStart(start, kind)
}

func (t *jsonTokenizer) startString(off int64) error {
	if top := t.top(); top != nil && top.isObject && t.expectKey {
		// This string is a member key, not a value.
		t.inString = true
		t.stringIsKey = true
		t.expectKey = false
		t.keyBuf = append(t.keyBuf[:0], '"')
		return nil
	}
	// A string value.
	t.inString = true
	t.stringIsKey = false
	t.keyBuf = t.keyBuf[:0]
	return t.startValue(off, scalarValue)
}

func (t *jsonTokenizer) endString(off int64) error {
	t.inString = false
	t.escNext = false
	t.uLeft = 0
	if t.stringIsKey {
		// keyBuf holds the raw quoted key including both quotes; decode it to the
		// member name and stash it on the enclosing frame.
		if top := t.top(); top != nil {
			top.curKey = decodeKey(t.keyBuf)
		}
		t.stringIsKey = false
		t.keyBuf = t.keyBuf[:0]
		return nil
	}
	// End of a string value is the byte just past the closing quote.
	t.keyBuf = t.keyBuf[:0]
	return t.ev.valueEnd(off + 1)
}

func (t *jsonTokenizer) openContainer(off int64, kind valueKind) error {
	if err := t.startValue(off, kind); err != nil {
		return err
	}
	t.stack = append(t.stack, frame{isObject: kind == objectValue, arrIdx: -1})
	if kind == objectValue {
		t.expectKey = true
	}
	return nil
}

func (t *jsonTokenizer) closeContainer(off int64) error {
	// Pop the frame; the closed container's values lived at depth len(stack)-1, so
	// anything entered at that depth ends here, with End just past this bracket.
	if len(t.stack) == 0 {
		return nil
	}
	closedDepth := len(t.stack) - 1
	t.stack = t.stack[:len(t.stack)-1]
	t.expectKey = false
	return t.ev.containerEnd(off+1, closedDepth)
}

// flushScalar closes a scalar still open at the end of the input, which has no
// trailing structural byte to terminate it.
func (t *jsonTokenizer) flushScalar() error {
	if !t.inScalar {
		return nil
	}
	t.inScalar = false
	return t.ev.valueEnd(t.off)
}

// isScalarByte reports whether b can be part of a bare JSON scalar token (number,
// true, false, null). The set is deliberately permissive: the input is assumed
// well-formed, so this only needs to distinguish token bytes from the structural bytes
// and whitespace that terminate a token.
func isScalarByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', ',', '}', ']', ':':
		return false
	}
	return true
}
