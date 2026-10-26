package campaign

import (
	"context"
	"fmt"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// checkScheduledAt applies the scheduled-time rules relative to now: at
// least MinLead and at most MaxHorizon ahead.
func (s *Service) checkScheduledAt(at, now time.Time) error {
	var msg string
	switch cfg := s.cfg.Schedule; {
	case at.Before(now):
		msg = "must not be in the past"
	case at.Sub(now) < cfg.MinLead:
		msg = fmt.Sprintf("must be at least %s from now", cfg.MinLead)
	case at.Sub(now) > cfg.MaxHorizon:
		msg = fmt.Sprintf("must be at most %s from now", cfg.MaxHorizon)
	}
	if msg != "" {
		return fail(CodeInvalidRequest, "", content.FieldError{Path: "scheduled_at", Message: msg})
	}
	return nil
}

// ParseScheduledAt parses a scheduled time: RFC 3339 with an explicit
// offset ("Z" or ±hh:mm), so a client never has its time read in a zone it
// did not choose.
func ParseScheduledAt(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fail(CodeInvalidRequest, "",
			content.FieldError{Path: "scheduled_at", Message: "must be an RFC 3339 time with an offset, such as 2026-10-30T10:00:00+03:00"})
	}
	return t, nil
}

// Reschedule is a human's request to move a scheduled campaign. Version
// and ContentHash are the values the actor saw.
type Reschedule struct {
	Version     int
	ContentHash string
	ScheduledAt time.Time
	Actor       string
}

// Reschedule moves a scheduled campaign to another time, keeping its
// approval of the unchanged content. Under the campaign lock it serializes
// with Release: once a campaign was released, it fails with
// CodeInvalidState.
func (s *Service) Reschedule(ctx context.Context, id ID, r Reschedule) (Campaign, error) {
	actor, err := checkActor(r.Actor, "actor")
	if err != nil {
		return Campaign{}, err
	}
	if r.ScheduledAt.IsZero() {
		return Campaign{}, fail(CodeInvalidRequest, "", content.FieldError{Path: "scheduled_at", Message: "is required"})
	}
	return s.update(ctx, id, func(_ context.Context, _ AudienceInspector, c Campaign) (*Write, error) {
		if err := checkHeld(c, StatusScheduled, r.Version, r.ContentHash, "only scheduled campaigns can be rescheduled"); err != nil {
			return nil, err
		}
		if err := s.checkScheduledAt(r.ScheduledAt, s.now()); err != nil {
			return nil, err
		}
		c.ScheduledAt = r.ScheduledAt
		c.Version++
		return &Write{Campaign: c, Events: []Event{{
			Action: ActionRescheduled, Actor: actor, From: StatusScheduled, To: StatusScheduled, Version: c.Version,
		}}}, nil
	})
}

// checkHeld checks a human action on an approved-for-later campaign: the
// transition to to is legal, and version and hash are the ones the actor
// saw.
func checkHeld(c Campaign, to Status, version int, hash, stateMsg string) error {
	switch {
	case !c.HoldsApproval() || !CanTransition(c.Status, to):
		return fail(CodeInvalidState, stateMsg)
	case version != c.Version:
		return fail(CodeVersionConflict, "")
	case hash != c.ContentHash:
		return fail(CodeContentChanged, "")
	}
	return nil
}

// Cancel is a human's request to stop a campaign approved for later.
// Version and ContentHash are the values the actor saw.
type Cancel struct {
	Version     int
	ContentHash string
	Actor       string
}

// Cancel returns a scheduled, release_failed, or expired campaign to
// drafts_ready: the approval and the schedule are cleared and the version
// moves on, so sending it needs a fresh review and approval. Under the
// campaign lock it serializes with Release: once a campaign was released,
// it fails with CodeInvalidState.
func (s *Service) Cancel(ctx context.Context, id ID, r Cancel) (Campaign, error) {
	actor, err := checkActor(r.Actor, "actor")
	if err != nil {
		return Campaign{}, err
	}
	return s.update(ctx, id, func(_ context.Context, _ AudienceInspector, c Campaign) (*Write, error) {
		if err := checkHeld(c, StatusDraftsReady, r.Version, r.ContentHash,
			"only scheduled, release_failed, or expired campaigns can be canceled"); err != nil {
			return nil, err
		}
		from := c.Status
		c.Status = StatusDraftsReady
		c.ApprovedBy, c.ApprovedAt, c.ScheduledAt, c.ReleaseFailure = "", time.Time{}, time.Time{}, nil
		c.Version++
		return &Write{Campaign: c, Events: []Event{{
			Action: ActionScheduleCanceled, Actor: actor, From: from, To: c.Status, Version: c.Version,
		}}}, nil
	})
}
