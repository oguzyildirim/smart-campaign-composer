// Package mockllm is a deterministic stand-in for a language model. It is
// used when no ANTHROPIC_API_KEY is configured and in every test, so the
// whole generation flow works without network access or an API key.
//
// Segment extraction is keyword based (Turkish and English); drafts are
// fixed templates with a few brief-dependent phrases. The same brief always
// yields byte-identical output, streamed in identical chunks.
//
// Failure modes (malformed JSON, refusal, timeout, ...) are injected only
// through New's script, which is Go code; nothing in a brief can trigger
// them.
package mockllm

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm"
)

// ChunkRunes is the size of each streamed draft fragment.
const ChunkRunes = 24

// Operation names recorded in Call.
const (
	OpExtractSegment = "extract_segment"
	OpGenerateDrafts = "generate_drafts"
)

// Fault is a scripted failure mode.
type Fault int

// Scripted failure modes. Content faults produce output of the right shape
// for the operation they hit (a segment proposal or drafts).
const (
	FaultNone             Fault = iota
	FaultMalformedJSON          // truncated, unparsable JSON
	FaultUnknownField           // valid shape plus an extra top-level field
	FaultUnsupportedField       // segment rule using a field outside the allowlist
	FaultOverlongSMS            // SMS body over 160 characters
	FaultUnknownVariable        // {{phone}} in the SMS body
	FaultRefusal                // llm.ErrRefused
	FaultAPIError               // llm.ErrUnavailable
	FaultIncomplete             // llm.ErrIncomplete (max_tokens)
	FaultHang                   // block until the context ends
)

// Step overrides one call. Output, when set, is returned verbatim; otherwise
// Fault applies; a zero Step means "behave normally".
type Step struct {
	Fault  Fault
	Output string
}

// Call records one invocation, for test assertions.
type Call struct {
	Op     string
	Brief  string
	Repair *llm.Repair
}

// Client is the mock. The zero value is not usable; call New.
type Client struct {
	mu     sync.Mutex
	script []Step
	calls  []Call
	delay  time.Duration
}

var _ llm.Client = (*Client)(nil)

// New returns a mock that applies script to its first calls, in order and
// across both operations, then behaves normally.
func New(script ...Step) *Client {
	return &Client{script: script}
}

// WithStreamDelay makes GenerateDrafts pause d between streamed chunks,
// so a UI shows drafts arriving the way a real model streams them. It
// returns c and must be called before c is used.
func (c *Client) WithStreamDelay(d time.Duration) *Client {
	c.delay = d
	return c
}

// Calls returns the calls made so far.
func (c *Client) Calls() []Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Call(nil), c.calls...)
}

func (c *Client) next(call Call) Step {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	if len(c.script) == 0 {
		return Step{}
	}
	s := c.script[0]
	c.script = c.script[1:]
	return s
}

// ExtractSegment implements llm.Client.
func (c *Client) ExtractSegment(ctx context.Context, req llm.SegmentRequest) (string, error) {
	step := c.next(Call{Op: OpExtractSegment, Brief: req.Brief, Repair: req.Repair})
	if out, done, err := apply(ctx, step, segmentFaultOutput); done {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("mock extract segment: %w", err)
	}
	return marshal(ExtractSegment(req.Brief))
}

// GenerateDrafts implements llm.Client. Output is streamed to onDelta in
// ChunkRunes-sized fragments.
func (c *Client) GenerateDrafts(ctx context.Context, req llm.DraftRequest, onDelta func(string)) (string, error) {
	step := c.next(Call{Op: OpGenerateDrafts, Brief: req.Brief, Repair: req.Repair})
	out, done, err := apply(ctx, step, draftFaultOutput)
	if !done {
		out, err = marshal(GenerateDrafts(req.Brief))
	}
	if err != nil {
		return "", err
	}
	first := true
	for chunk := range chunks(out, ChunkRunes) {
		if !first {
			if err := c.pause(ctx); err != nil {
				return "", fmt.Errorf("mock generate drafts: %w", err)
			}
		}
		first = false
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("mock generate drafts: %w", err)
		}
		if onDelta != nil {
			onDelta(chunk)
		}
	}
	return out, nil
}

// pause waits the stream delay or until ctx ends.
func (c *Client) pause(ctx context.Context) error {
	if c.delay <= 0 {
		return nil
	}
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// apply resolves a scripted step. done is false when the call should
// produce its normal output.
func apply(ctx context.Context, s Step, faultOutput func(Fault) string) (out string, done bool, err error) {
	if s.Output != "" {
		return s.Output, true, nil
	}
	switch s.Fault {
	case FaultNone:
		return "", false, nil
	case FaultRefusal:
		return "", true, fmt.Errorf("mock: %w", llm.ErrRefused)
	case FaultAPIError:
		return "", true, fmt.Errorf("mock: status 529: %w", llm.ErrUnavailable)
	case FaultIncomplete:
		return "", true, fmt.Errorf("mock: stop reason max_tokens: %w", llm.ErrIncomplete)
	case FaultHang:
		<-ctx.Done()
		return "", true, fmt.Errorf("mock: %w", ctx.Err())
	default:
		return faultOutput(s.Fault), true, nil
	}
}

func segmentFaultOutput(f Fault) string {
	switch f {
	case FaultMalformedJSON:
		return `{"rule": {"version": 1, "conditions": [`
	case FaultUnknownField:
		return `{"rule":{"version":1,"conditions":[{"field":"city","op":"eq","value":"İstanbul"}]},` +
			`"unsupported_criteria":[],"explanation":"","approved":true}`
	default: // FaultUnsupportedField and draft-only faults
		return `{"rule":{"version":1,"conditions":[{"field":"age","op":"gte","value":30}]},` +
			`"unsupported_criteria":[],"explanation":""}`
	}
}

func draftFaultOutput(f Fault) string {
	d := content.Drafts{
		SMS:   content.SMS{Body: "Merhaba {{first_name}}"},
		Email: content.Email{Subject: "Merhaba", Body: "Merhaba {{first_name}}"},
	}
	switch f {
	case FaultMalformedJSON:
		return `{"sms": {"body": "Merhaba`
	case FaultUnknownField:
		return `{"sms":{"body":"Merhaba"},"email":{"subject":"Merhaba","body":"Merhaba"},"send_now":true}`
	case FaultOverlongSMS:
		d.SMS.Body = strings.Repeat("Çok uzun bir mesaj. ", 9) // 180 runes
	default: // FaultUnknownVariable and segment-only faults
		d.SMS.Body = "Merhaba {{phone}}"
	}
	out, _ := marshal(d) // marshaling a struct of strings cannot fail
	return out
}

// marshal encodes v without HTML escaping, as a model would write it.
func marshal(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("mock marshal: %w", err)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// chunks yields s in fragments of n runes.
func chunks(s string, n int) iter.Seq[string] {
	return func(yield func(string) bool) {
		for s != "" {
			end, count := 0, 0
			for end < len(s) && count < n {
				_, size := utf8.DecodeRuneInString(s[end:])
				end += size
				count++
			}
			if !yield(s[:end]) {
				return
			}
			s = s[end:]
		}
	}
}
