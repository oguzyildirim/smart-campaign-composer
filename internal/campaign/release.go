package campaign

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ReleaseOutcome is what a release did to a scheduled campaign.
type ReleaseOutcome string

// Release outcomes. ReleaseNone means nothing changed: the campaign was
// not scheduled (any more) or not due yet.
const (
	ReleaseNone     ReleaseOutcome = ""
	ReleaseReleased ReleaseOutcome = "released"
	ReleaseFailed   ReleaseOutcome = "release_failed"
	ReleaseExpired  ReleaseOutcome = "expired"
)

// ReleaseResult reports a release. Lateness is the time between the
// scheduled time and the release decision.
type ReleaseResult struct {
	Outcome  ReleaseOutcome
	Lateness time.Duration
}

// ReleaseMetrics receives release outcomes for operational monitoring.
// Implementations must be safe for concurrent use.
type ReleaseMetrics interface {
	CampaignReleased(o ReleaseOutcome, lateness time.Duration)
}

type nopReleaseMetrics struct{}

func (nopReleaseMetrics) CampaignReleased(ReleaseOutcome, time.Duration) {}

// Release releases a due scheduled campaign. It is the system's action,
// never a client's: the worker calls it for campaigns the store reports
// as due. Under the campaign lock it decides with the service clock:
//
//   - not scheduled, or not due yet: nothing changes (so concurrent or
//     repeated releases are harmless);
//   - due for longer than the release grace: the campaign expires and
//     nothing is sent;
//   - otherwise every approval check runs again against the audience as
//     it is now, with the scheduled time as the segment's reference time.
//     On success the campaign becomes approved and its deliveries are
//     created in the same transaction, from the predicate the checks
//     used; if a check fails the campaign becomes release_failed with the
//     reasons and nothing is created.
//
// An infrastructure error changes nothing and is returned; the next
// attempt retries.
func (s *Service) Release(ctx context.Context, id ID) (ReleaseResult, error) {
	var res ReleaseResult
	_, err := s.update(ctx, id, func(ctx context.Context, audience AudienceInspector, c Campaign) (*Write, error) {
		res = ReleaseResult{}
		now := s.now()
		if c.Status != StatusScheduled || now.Before(c.ScheduledAt) {
			return nil, nil
		}
		res.Lateness = now.Sub(c.ScheduledAt)
		from := c.Status
		c.Version++
		if res.Lateness > s.cfg.Schedule.ReleaseGrace {
			res.Outcome, c.Status = ReleaseExpired, StatusExpired
			return &Write{Campaign: c, Events: []Event{systemEvent(ActionExpired, from, c)}}, nil
		}
		pred, err := s.checkApproval(ctx, audience, c, c.ScheduledAt)
		if cerr, ok := errors.AsType[*Error](err); ok {
			res.Outcome, c.Status = ReleaseFailed, StatusReleaseFailed
			c.ReleaseFailure = &ReleaseFailure{Code: cerr.Code, Message: cerr.Message, Details: cerr.Details}
			return &Write{Campaign: c, Events: []Event{systemEvent(ActionReleaseFailed, from, c)}}, nil
		}
		if err != nil {
			return nil, err
		}
		res.Outcome, c.Status = ReleaseReleased, StatusApproved
		return &Write{
			Campaign:   c,
			Events:     []Event{systemEvent(ActionReleased, from, c)},
			Deliveries: s.deliveryPlan(c, pred),
		}, nil
	})
	if err != nil {
		return ReleaseResult{}, err
	}
	if res.Outcome != ReleaseNone {
		s.cfg.ReleaseMetrics.CampaignReleased(res.Outcome, res.Lateness)
	}
	return res, nil
}

// ReleaseDue releases up to limit campaigns the store reports as due and
// returns how many it released, failed, or expired. A campaign whose
// release returns an error is logged and left for the next call.
func (s *Service) ReleaseDue(ctx context.Context, limit int) (int, error) {
	ids, err := s.store.DueScheduled(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("find due campaigns: %w", err)
	}
	n := 0
	for _, id := range ids {
		res, err := s.Release(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			s.logger.WarnContext(ctx, "release scheduled campaign failed; retrying later", "campaign_id", string(id), "error", err)
			continue
		}
		if res.Outcome != ReleaseNone {
			n++
			s.logger.InfoContext(ctx, "scheduled campaign release decided", "campaign_id", string(id),
				"outcome", string(res.Outcome), "lateness", res.Lateness.String())
		}
	}
	return n, nil
}

func systemEvent(action string, from Status, c Campaign) Event {
	return Event{Action: action, Actor: ActorSystem, From: from, To: c.Status, Version: c.Version}
}
