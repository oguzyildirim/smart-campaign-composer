// Package campaign holds the campaign domain. This milestone covers AI
// generation: turning a marketer's brief into a validated segment proposal
// and validated message drafts. Model output is untrusted; it is decoded
// strictly, validated by the segment and content packages, and repaired at
// most a bounded number of times.
//
// The package has no HTTP, SQL, or LLM SDK dependencies.
package campaign

import "fmt"

// ErrorCode is a stable, client-safe error category.
type ErrorCode string

// Generation error codes.
const (
	// CodeInvalidBrief: the brief is empty, too long, or not plain text.
	CodeInvalidBrief ErrorCode = "invalid_brief"
	// CodeInvalidOutput: the model's output failed validation on every
	// attempt.
	CodeInvalidOutput ErrorCode = "invalid_output"
	// CodeUnsupportedBrief: the brief asks only for criteria the segment
	// rules cannot express.
	CodeUnsupportedBrief ErrorCode = "unsupported_brief"
	// CodeRefused: the model declined the request.
	CodeRefused ErrorCode = "refused"
	// CodeTimeout: the model did not answer in time.
	CodeTimeout ErrorCode = "timeout"
	// CodeUpstreamUnavailable: the model provider failed or is unreachable.
	CodeUpstreamUnavailable ErrorCode = "upstream_unavailable"
)

// Error is a generation failure. Message and UnsupportedCriteria are safe
// to return to clients; Err holds internal detail for logs only.
type Error struct {
	Code    ErrorCode
	Message string
	// UnsupportedCriteria lists criteria the model could not express, for
	// CodeUnsupportedBrief. They are validated plain text.
	UnsupportedCriteria []string
	Err                 error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func newError(code ErrorCode, cause error) *Error {
	return &Error{Code: code, Message: messages[code], Err: cause}
}

var messages = map[ErrorCode]string{
	CodeInvalidBrief:        "the brief is invalid",
	CodeInvalidOutput:       "the AI response could not be validated; try again or rephrase the brief",
	CodeUnsupportedBrief:    "the brief only uses criteria that audience rules cannot express",
	CodeRefused:             "the AI declined to process this brief",
	CodeTimeout:             "the AI did not respond in time",
	CodeUpstreamUnavailable: "the AI service is temporarily unavailable",
}
