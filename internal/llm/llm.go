// Package llm is the boundary between campaign generation and a language
// model. It defines the Client port, its request types, and the errors an
// adapter reports; adapters (the Claude API in internal/platform/claude,
// the deterministic mock in internal/llm/mockllm) implement it.
//
// A Client returns raw text. Callers treat that text as untrusted input:
// it is decoded strictly and validated before anything uses it, and it can
// never approve, send, or query anything by itself.
package llm

import (
	"context"
	"errors"
)

// Client produces structured proposals from a marketer's brief.
//
// Implementations must honor ctx cancellation and deadlines promptly and
// return an error wrapping ctx.Err() when ctx ends the call.
type Client interface {
	// ExtractSegment returns a JSON object proposing an audience segment:
	// {"rule": <segment rule> | null, "unsupported_criteria": [string],
	// "explanation": string}.
	ExtractSegment(ctx context.Context, req SegmentRequest) (string, error)

	// GenerateDrafts returns a JSON object with SMS and email drafts:
	// {"sms": {"body": string}, "email": {"subject": string, "body": string}}.
	// onDelta, if non-nil, receives raw text fragments of that object as
	// they are produced; fragments are for display only, and only the
	// returned value is validated and used.
	GenerateDrafts(ctx context.Context, req DraftRequest, onDelta func(string)) (string, error)
}

// SegmentRequest asks for an audience segment.
type SegmentRequest struct {
	Brief  string
	Repair *Repair
}

// DraftRequest asks for message drafts.
type DraftRequest struct {
	Brief string
	// AudienceSummary optionally describes the chosen audience in plain
	// words, giving the model context for tone. It is data, not
	// instructions.
	AudienceSummary string
	Repair          *Repair
}

// Repair asks the model to correct its previous output. Problems are
// validator messages, which never contain user content.
type Repair struct {
	PreviousOutput string
	Problems       []string
}

// Errors reported by adapters, wrapped with detail for logs.
var (
	// ErrRefused means the model declined the request (stop reason
	// "refusal").
	ErrRefused = errors.New("llm: model refused the request")
	// ErrIncomplete means the model stopped before finishing its output
	// (for example, it hit max_tokens) or returned no text.
	ErrIncomplete = errors.New("llm: output incomplete")
	// ErrUnavailable means the provider could not be reached or answered
	// with an error (rate limit, overload, server error, bad response).
	ErrUnavailable = errors.New("llm: provider unavailable")
)
