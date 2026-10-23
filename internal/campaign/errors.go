// Package campaign holds the campaign domain: the lifecycle state machine,
// editing, and human approval (Service), and AI generation (Generator),
// which turns a marketer's brief into a validated segment proposal and
// validated message drafts. Model output is untrusted; it is decoded
// strictly, validated by the segment and content packages, and repaired at
// most a bounded number of times. Nothing the model returns can change a
// campaign's status beyond storing validated drafts; only Service.Approve
// approves, on an explicit human request.
//
// The package has no HTTP, SQL, or LLM SDK dependencies.
package campaign

import (
	"fmt"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

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

// Campaign lifecycle error codes.
const (
	// CodeInvalidRequest: a request is structurally invalid (see Details).
	CodeInvalidRequest ErrorCode = "invalid_request"
	// CodeNotFound: no campaign has the given id.
	CodeNotFound ErrorCode = "not_found"
	// CodeInvalidState: the campaign's status does not allow the action.
	CodeInvalidState ErrorCode = "invalid_state"
	// CodeVersionConflict: the campaign changed since the client read it.
	CodeVersionConflict ErrorCode = "version_conflict"
	// CodeContentChanged: the content hash the client reviewed does not
	// match the stored content.
	CodeContentChanged ErrorCode = "content_changed"
	// CodeGenerationInProgress: drafts are being generated for the campaign.
	CodeGenerationInProgress ErrorCode = "generation_in_progress"
	// CodeInvalidContent: the content fails validation (see Details).
	CodeInvalidContent ErrorCode = "invalid_content"
	// CodeNoRecipients: no selected channel has a consented recipient.
	CodeNoRecipients ErrorCode = "no_recipients"
)

// Error is a client-safe failure of generation or a lifecycle action.
// Message, UnsupportedCriteria, and Details are safe to return to clients;
// Err holds internal detail for logs only.
type Error struct {
	Code    ErrorCode
	Message string
	// UnsupportedCriteria lists criteria the model could not express, for
	// CodeUnsupportedBrief. They are validated plain text.
	UnsupportedCriteria []string
	// Details lists field-level problems for CodeInvalidRequest and
	// CodeInvalidContent. Messages never echo content.
	Details []content.FieldError
	Err     error
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

	CodeInvalidRequest:       "the request is invalid",
	CodeNotFound:             "campaign not found",
	CodeInvalidState:         "the campaign's status does not allow this action",
	CodeVersionConflict:      "the campaign was changed by someone else; reload it",
	CodeContentChanged:       "the content changed since it was reviewed; review it again",
	CodeGenerationInProgress: "drafts are being generated for this campaign",
	CodeInvalidContent:       "the content does not pass validation",
	CodeNoRecipients:         "no selected channel has a recipient who gave consent",
}

// fail returns a client-safe *Error with code's default message, or msg
// when given.
func fail(code ErrorCode, msg string, details ...content.FieldError) *Error {
	if msg == "" {
		msg = messages[code]
	}
	return &Error{Code: code, Message: msg, Details: details}
}
