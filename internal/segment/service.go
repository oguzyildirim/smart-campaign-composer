package segment

import (
	"context"
	"fmt"
	"time"
)

// Counts is the size of a segment's audience. Total counts every matching
// user; SMS and Email count only matching users who granted consent for
// that channel.
type Counts struct {
	Total int64 `json:"total"`
	SMS   int64 `json:"sms"`
	Email int64 `json:"email"`
}

// AudienceCounter counts users matching a predicate. Implementations own
// the consent filter, so callers cannot forget or bypass it.
type AudienceCounter interface {
	CountAudience(ctx context.Context, p Predicate) (Counts, error)
}

// Service validates rules and sizes their audiences.
type Service struct {
	counter AudienceCounter
	now     func() time.Time
}

// NewService returns a Service. now supplies the reference time for day
// windows; pass time.Now in production and a fixed clock in tests.
func NewService(counter AudienceCounter, now func() time.Time) *Service {
	return &Service{counter: counter, now: now}
}

// Preview parses an untrusted rule and counts its audience. Invalid rules
// return a *ValidationError and never reach the counter.
func (s *Service) Preview(ctx context.Context, data []byte) (Rule, Counts, error) {
	rule, err := Parse(data)
	if err != nil {
		return Rule{}, Counts{}, err
	}
	pred, err := Compile(rule, s.now().UTC())
	if err != nil {
		return Rule{}, Counts{}, err
	}
	counts, err := s.counter.CountAudience(ctx, pred)
	if err != nil {
		return Rule{}, Counts{}, fmt.Errorf("count audience: %w", err)
	}
	return rule, counts, nil
}
