// Package delivery sends approved campaign messages reliably. Each
// delivery is one (campaign, recipient, channel) row, created at approval;
// a queue message only names the row, and Postgres holds the state.
//
// Processing is at-least-once: a message may arrive more than once, and a
// worker may die mid-send. Duplicates are absorbed by an exclusive,
// leased claim on the row (attempts is the fencing token), and a resend
// after an uncertain outcome reuses the delivery id as the provider's
// idempotency key, so a recipient gets a message at most once as long as
// the provider honours that key.
//
// The package has no HTTP, SQL, or Kafka dependencies.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// ID is a delivery's UUID in canonical lower-case form. It is also the
// provider idempotency key.
type ID string

var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseID validates s as a UUID and returns it in canonical form.
func ParseID(s string) (ID, error) {
	s = strings.ToLower(s)
	if !idPattern.MatchString(s) {
		return "", errors.New("delivery id must be a UUID")
	}
	return ID(s), nil
}

// Status is a delivery's state.
type Status string

// Statuses. sent and failed are terminal.
const (
	// StatusQueued: waiting for a worker (first attempt, or a due retry
	// that was re-enqueued).
	StatusQueued Status = "queued"
	// StatusProcessing: claimed by a worker until its lease expires.
	StatusProcessing Status = "processing"
	// StatusSent: the provider accepted the message.
	StatusSent Status = "sent"
	// StatusFailed: a permanent error, or retries are exhausted.
	StatusFailed Status = "failed"
	// StatusRetrying: a transient failure; due again at next_attempt_at.
	StatusRetrying Status = "retrying"
)

// Terminal reports whether no further attempt follows.
func (s Status) Terminal() bool { return s == StatusSent || s == StatusFailed }

// Error codes stored in last_error_code. They are stable and contain no
// recipient data.
const (
	CodeConsentRevoked   = "consent_revoked"
	CodeInvalidContent   = "invalid_content"
	CodeRejected         = "provider_rejected"
	CodeRateLimited      = "provider_rate_limited"
	CodeUnavailable      = "provider_unavailable"
	CodeTimeout          = "provider_timeout"
	CodeInvalidResponse  = "provider_invalid_response"
	CodeInterrupted      = "interrupted"
	CodeInternal         = "internal_error"
	CodeLeaseExpired     = "lease_expired"
	CodeRetriesExhausted = "retries_exhausted"
)

// TopicPrefix plus a channel name is the queue topic of that channel's
// delivery jobs.
const TopicPrefix = "deliveries."

// Topic returns the queue topic for channel c.
func Topic(c content.Channel) string { return TopicPrefix + string(c) }

// Job is a claimed delivery. Attempt is the attempt this claim performs
// (1-based) and fences its outcome: only the holder of the current
// attempt can record one.
type Job struct {
	ID          ID
	CampaignID  string
	UserID      int64
	Channel     content.Channel
	Attempt     int
	MaxAttempts int
}

// Recipient is what a send needs, read right before sending: current
// consent, the address, personalization values, and the campaign's
// approved content. It is never logged.
type Recipient struct {
	Consented bool
	Address   string
	Values    map[content.Variable]string
	Drafts    content.Drafts
}

// Message is a rendered message handed to the provider.
type Message struct {
	IdempotencyKey ID
	Channel        content.Channel
	To             string
	Subject        string
	Body           string
}

// Outcome is the result of one attempt.
type Outcome struct {
	Status            Status
	ErrorCode         string
	ProviderMessageID string
	// RetryIn delays the next attempt of a retrying delivery.
	RetryIn time.Duration
	// Release gives the attempt back: the work stopped before reaching the
	// provider (shutdown), so it does not count against max_attempts.
	Release bool
}

// ProviderError is a failed send. Transient errors are retried; others
// fail the delivery.
type ProviderError struct {
	Code      string
	Transient bool
	// RetryAfter is the provider's requested wait, 0 when absent.
	RetryAfter time.Duration
	Err        error
}

func (e *ProviderError) Error() string {
	kind := "permanent"
	if e.Transient {
		kind = "transient"
	}
	if e.Err != nil {
		return fmt.Sprintf("%s provider error %s: %v", kind, e.Code, e.Err)
	}
	return fmt.Sprintf("%s provider error %s", kind, e.Code)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Provider sends one message. Implementations send msg.IdempotencyKey so
// that a resend of the same delivery is not delivered twice. Errors are
// *ProviderError, or the context's error when ctx ended.
type Provider interface {
	Send(ctx context.Context, msg Message) (providerMessageID string, err error)
}

// RateLimiter blocks until a send on channel c is allowed, across every
// worker, or until ctx ends.
type RateLimiter interface {
	Wait(ctx context.Context, c content.Channel) error
}

// Store holds delivery state.
type Store interface {
	// Claim takes an exclusive lease on a queued or due retrying delivery
	// and counts the attempt. ok is false when the delivery is not
	// claimable (already processing, terminal, not yet due, or unknown):
	// the message is a duplicate.
	Claim(ctx context.Context, id ID, lease time.Duration) (job Job, ok bool, err error)
	// Recipient reads the job's recipient and content as of now.
	Recipient(ctx context.Context, job Job) (Recipient, error)
	// Record stores the outcome if job still holds the claim (status
	// processing, same attempt) and completes the campaign when its last
	// delivery became terminal. recorded is false when the claim was lost,
	// for example to lease expiry. An error with recorded true means the
	// outcome is stored but the completion check failed.
	Record(ctx context.Context, job Job, o Outcome) (recorded bool, err error)
}
