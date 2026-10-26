package campaign

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// MaxApproverRunes caps the free-text approver (or actor) name.
const MaxApproverRunes = 100

// Approval is a human's approval request. Version and ContentHash are the
// values the reviewer saw. A non-zero ScheduledAt approves the campaign
// for release at that time instead of sending now.
type Approval struct {
	Version     int
	ContentHash string
	ApprovedBy  string
	ScheduledAt time.Time
}

// Approve records explicit human approval of exactly the reviewed content.
// Inside one transaction it re-checks the version and content hash,
// re-validates the segment rule and content, checks lengths against the
// longest real values of each selected channel's consented recipients,
// and requires at least one recipient.
//
// Without a scheduled time, the same transaction creates one delivery per
// consented recipient and channel, compiled from the same predicate the
// checks used. With one, the campaign becomes scheduled and nothing is
// created: Release repeats the checks and creates the deliveries at that
// time.
//
// Approving an approved (or later) or scheduled campaign with the same
// content hash returns it unchanged, schedule included, and creates
// nothing.
func (s *Service) Approve(ctx context.Context, id ID, a Approval) (Campaign, error) {
	approver, err := checkActor(a.ApprovedBy, "approved_by")
	if err != nil {
		return Campaign{}, err
	}
	return s.update(ctx, id, func(ctx context.Context, audience AudienceInspector, c Campaign) (*Write, error) {
		now := s.now()
		if c.Approved() || c.Status == StatusScheduled {
			if a.ContentHash == c.ContentHash {
				return nil, nil
			}
			return nil, fail(CodeContentChanged, "the campaign is already approved with different content")
		}
		scheduled := !a.ScheduledAt.IsZero()
		to := StatusApproved
		if scheduled {
			to = StatusScheduled
			if err := s.checkScheduledAt(a.ScheduledAt, now); err != nil {
				return nil, err
			}
		}
		switch {
		case !CanTransition(c.Status, to):
			return nil, fail(CodeInvalidState, "only campaigns with drafts can be approved")
		case c.Generating(now):
			return nil, fail(CodeGenerationInProgress, "")
		case a.Version != c.Version:
			return nil, fail(CodeVersionConflict, "")
		case a.ContentHash != c.ContentHash || ContentHash(c) != c.ContentHash:
			return nil, fail(CodeContentChanged, "")
		}
		pred, err := s.checkApproval(ctx, audience, c, now)
		if err != nil {
			return nil, err
		}
		from := c.Status
		c.Status, c.ApprovedBy, c.ApprovedAt = to, approver, now
		c.Version++
		action := ActionApproved
		var plan *DeliveryPlan
		if scheduled {
			action, c.ScheduledAt = ActionScheduled, a.ScheduledAt
		} else {
			plan = s.deliveryPlan(c, pred)
		}
		return &Write{
			Campaign:   c,
			Events:     []Event{{Action: action, Actor: approver, From: from, To: c.Status, Version: c.Version}},
			Deliveries: plan,
		}, nil
	})
}

// checkApproval runs every content and audience check of an approval or
// release: the segment rule and content are re-validated, the rule is
// compiled with ref as reference time, lengths are checked against the
// longest real values of each selected channel's consented recipients,
// and at least one recipient is required. It returns the compiled
// predicate, so deliveries target exactly the audience that was checked.
// Check failures are *Error; anything else is an infrastructure error.
func (s *Service) checkApproval(ctx context.Context, audience AudienceInspector, c Campaign, ref time.Time) (segment.Predicate, error) {
	if err := revalidateRule(c.Rule); err != nil {
		return segment.Predicate{}, err
	}
	if v := c.Validate(); !v.Valid {
		return segment.Predicate{}, fail(CodeInvalidContent, "", v.Errors...)
	}
	pred, err := segment.Compile(c.Rule, ref.UTC())
	if err != nil {
		return segment.Predicate{}, fmt.Errorf("compile campaign segment: %w", err)
	}
	channels, audienceErrs, err := s.inspect(ctx, audience, c, pred, 0)
	if err != nil {
		return segment.Predicate{}, err
	}
	if len(audienceErrs) > 0 {
		return segment.Predicate{}, fail(CodeInvalidContent, "the content exceeds a limit for recipients in this audience", audienceErrs...)
	}
	if !hasRecipients(channels) {
		return segment.Predicate{}, fail(CodeNoRecipients, "")
	}
	return pred, nil
}

func (s *Service) deliveryPlan(c Campaign, pred segment.Predicate) *DeliveryPlan {
	return &DeliveryPlan{Predicate: pred, Channels: c.Channels, MaxAttempts: s.cfg.DeliveryMaxAttempts}
}

// revalidateRule runs the stored rule through the segment validator again,
// so approval never relies on what was checked at creation.
func revalidateRule(rule segment.Rule) error {
	data, err := json.Marshal(rule)
	if err == nil {
		_, err = segment.Parse(data)
	}
	if err != nil {
		return fail(CodeInvalidContent, "the audience rule is no longer valid",
			content.FieldError{Path: "rule", Message: "failed segment validation"})
	}
	return nil
}

// checkActor validates a free-text operator name sent as field path.
func checkActor(name, path string) (string, error) {
	name = strings.TrimSpace(name)
	msg := content.CheckPlainText(name, false)
	switch {
	case name == "":
		msg = "must not be empty"
	case msg == "" && utf8.RuneCountInString(name) > MaxApproverRunes:
		msg = fmt.Sprintf("must be at most %d characters", MaxApproverRunes)
	}
	if msg != "" {
		return "", fail(CodeInvalidRequest, "", content.FieldError{Path: path, Message: msg})
	}
	return name, nil
}
