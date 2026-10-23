package campaign

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// DraftField names a streamed message field.
type DraftField string

// Streamed fields.
const (
	FieldSMSBody      DraftField = "sms_body"
	FieldEmailSubject DraftField = "email_subject"
	FieldEmailBody    DraftField = "email_body"
)

// FieldDelta is a fragment of one draft field's text, extracted from the
// model's partial output. It is for display only: drafts are used only
// after the complete output is validated. Attempt increases when a repair
// call starts; text from earlier attempts should be discarded.
type FieldDelta struct {
	Attempt int
	Field   DraftField
	Text    string
}

var streamedPaths = map[string]DraftField{
	"sms.body":      FieldSMSBody,
	"email.subject": FieldEmailSubject,
	"email.body":    FieldEmailBody,
}

// Bounds that keep the extractor's state small on hostile input.
const (
	maxStreamDepth = 8
	maxKeyBytes    = 32
)

// fieldExtractor is an incremental, lenient JSON scanner that emits the
// text of the string values at streamedPaths. It never fails: input it
// does not understand produces no output. Chunks may split runes and
// escape sequences anywhere.
type fieldExtractor struct {
	stack []frame

	inString bool
	isKey    bool
	key      strings.Builder
	field    DraftField // "" when the current string value is not streamed

	escape  bool
	hex     []byte // \uXXXX digits collected so far, after "\u"
	inHex   bool
	pending rune // high surrogate waiting for its low half; 0 if none

	out     []byte // value bytes not yet emitted
	results []FieldDelta
}

type frame struct {
	object    bool
	expectKey bool
	key       string
}

func newFieldExtractor() *fieldExtractor { return &fieldExtractor{} }

// Feed scans chunk and returns the text extracted from it, at most one
// delta per field run. Returned text is always valid, non-empty UTF-8.
func (x *fieldExtractor) Feed(chunk string) []FieldDelta {
	x.results = nil
	for i := 0; i < len(chunk); i++ {
		x.byte(chunk[i])
	}
	x.flush(false)
	return x.results
}

func (x *fieldExtractor) byte(b byte) {
	if x.inString {
		x.stringByte(b)
		return
	}
	switch b {
	case '{', '[':
		x.stack = append(x.stack, frame{object: b == '{', expectKey: b == '{'})
	case '}', ']':
		if n := len(x.stack); n > 0 {
			x.stack = x.stack[:n-1]
		}
	case ',':
		if top := x.top(); top != nil && top.object {
			top.expectKey = true
		}
	case '"':
		x.startString()
	}
}

func (x *fieldExtractor) top() *frame {
	if len(x.stack) == 0 {
		return nil
	}
	return &x.stack[len(x.stack)-1]
}

func (x *fieldExtractor) startString() {
	x.inString = true
	top := x.top()
	x.isKey = top != nil && top.object && top.expectKey
	x.key.Reset()
	x.field = ""
	if !x.isKey && top != nil && top.object && len(x.stack) <= maxStreamDepth {
		x.field = streamedPaths[x.path()]
	}
}

// path joins the current key of every enclosing object; arrays and keys
// containing "." make the path unmatchable.
func (x *fieldExtractor) path() string {
	parts := make([]string, len(x.stack))
	for i, f := range x.stack {
		if !f.object || strings.Contains(f.key, ".") {
			return ""
		}
		parts[i] = f.key
	}
	return strings.Join(parts, ".")
}

func (x *fieldExtractor) stringByte(b byte) {
	switch {
	case x.inHex:
		x.hexByte(b)
	case x.escape:
		x.escape = false
		x.escapeByte(b)
	case b == '\\':
		x.escape = true
	case b == '"':
		x.endString()
	default:
		x.flushPending()
		x.write([]byte{b})
	}
}

var simpleEscapes = map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

func (x *fieldExtractor) escapeByte(b byte) {
	if b == 'u' {
		x.inHex, x.hex = true, x.hex[:0]
		return
	}
	x.flushPending()
	if r, ok := simpleEscapes[b]; ok {
		x.write([]byte{r})
	}
}

func (x *fieldExtractor) hexByte(b byte) {
	x.hex = append(x.hex, b)
	if len(x.hex) < 4 {
		return
	}
	x.inHex = false
	var r rune
	for _, h := range x.hex {
		d, ok := hexValue(h)
		if !ok {
			x.flushPending()
			return
		}
		r = r<<4 | d
	}
	switch {
	case utf16.IsSurrogate(r) && r < 0xDC00:
		x.flushPending()
		x.pending = r
	case utf16.IsSurrogate(r) && x.pending != 0:
		x.writeRune(utf16.DecodeRune(x.pending, r))
		x.pending = 0
	default:
		x.flushPending()
		x.writeRune(r)
	}
}

func hexValue(h byte) (rune, bool) {
	switch {
	case h >= '0' && h <= '9':
		return rune(h - '0'), true
	case h >= 'a' && h <= 'f':
		return rune(h-'a') + 10, true
	case h >= 'A' && h <= 'F':
		return rune(h-'A') + 10, true
	}
	return 0, false
}

// flushPending writes a high surrogate that was not followed by a low one.
func (x *fieldExtractor) flushPending() {
	if x.pending != 0 {
		x.pending = 0
		x.writeRune(utf8.RuneError)
	}
}

func (x *fieldExtractor) writeRune(r rune) { x.write(utf8.AppendRune(nil, r)) }

func (x *fieldExtractor) write(p []byte) {
	switch {
	case x.isKey:
		if x.key.Len()+len(p) <= maxKeyBytes {
			x.key.Write(p)
		}
	case x.field != "":
		x.out = append(x.out, p...)
	}
}

func (x *fieldExtractor) endString() {
	x.flushPending()
	x.inString, x.escape, x.inHex = false, false, false
	if x.isKey {
		if top := x.top(); top != nil {
			top.key, top.expectKey = x.key.String(), false
		}
		return
	}
	x.flush(true)
	x.field = ""
}

// flush emits buffered value bytes. Unless the value ended, a trailing
// incomplete UTF-8 sequence is kept for the next chunk.
func (x *fieldExtractor) flush(final bool) {
	if x.field == "" || len(x.out) == 0 {
		return
	}
	n := len(x.out)
	if !final {
		n = completePrefix(x.out)
	}
	if n == 0 {
		return
	}
	text := strings.ToValidUTF8(string(x.out[:n]), string(utf8.RuneError))
	x.out = append(x.out[:0], x.out[n:]...)
	if l := len(x.results); l > 0 && x.results[l-1].Field == x.field {
		x.results[l-1].Text += text
		return
	}
	x.results = append(x.results, FieldDelta{Field: x.field, Text: text})
}

// completePrefix returns the length of p without a trailing incomplete
// UTF-8 sequence.
func completePrefix(p []byte) int {
	for i := len(p) - 1; i >= 0 && i >= len(p)-utf8.UTFMax; i-- {
		if utf8.RuneStart(p[i]) {
			if utf8.FullRune(p[i:]) {
				return len(p)
			}
			return i
		}
	}
	return len(p)
}
