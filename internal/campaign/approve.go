package campaign

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// MaxApproverRunes caps the free-text approver name.
const MaxApproverRunes = 100

// Approval is a human's approval request. Version and ContentHash are the
// values the reviewer saw.
type Approval struct {
	Version     int
	ContentHash string
	ApprovedBy  string
}

// Approve records explicit human approval of exactly the reviewed content.
// Inside one transaction it re-checks the version and content hash,
// re-validates the segment rule and content, checks lengths against the
// longest real values of each selected channel's consented recipients,
// and requires at least one recipient. The same transaction creates one
// delivery per consented recipient and channel, compiled from the same
// predicate the checks used. Approving an approved (or sending, or
// completed) campaign with the same content hash returns it unchanged and
// creates nothing.
func (s *Service) Approve(ctx context.Context, id ID, a Approval) (Campaign, error) {
	approver, err := checkApprover(a.ApprovedBy)
	if err != nil {
		return Campaign{}, err
	}
	return s.update(ctx, id, func(ctx context.Context, audience AudienceInspector, c Campaign) (*Write, error) {
		now := s.now()
		if c.Approved() {
			if a.ContentHash == c.ContentHash {
				return nil, nil
			}
			return nil, fail(CodeContentChanged, "the campaign is already approved with different content")
		}
		switch {
		case !CanTransition(c.Status, StatusApproved):
			return nil, fail(CodeInvalidState, "only campaigns with drafts can be approved")
		case c.Generating(now):
			return nil, fail(CodeGenerationInProgress, "")
		case a.Version != c.Version:
			return nil, fail(CodeVersionConflict, "")
		case a.ContentHash != c.ContentHash || ContentHash(c) != c.ContentHash:
			return nil, fail(CodeContentChanged, "")
		}
		if err := revalidateRule(c.Rule); err != nil {
			return nil, err
		}
		if v := c.Validate(); !v.Valid {
			return nil, fail(CodeInvalidContent, "", v.Errors...)
		}
		pred, err := segment.Compile(c.Rule, now.UTC())
		if err != nil {
			return nil, fmt.Errorf("compile campaign segment: %w", err)
		}
		channels, audienceErrs, err := s.inspect(ctx, audience, c, pred, 0)
		if err != nil {
			return nil, err
		}
		if len(audienceErrs) > 0 {
			return nil, fail(CodeInvalidContent, "the content exceeds a limit for recipients in this audience", audienceErrs...)
		}
		if !hasRecipients(channels) {
			return nil, fail(CodeNoRecipients, "")
		}
		from := c.Status
		c.Status, c.ApprovedBy, c.ApprovedAt = StatusApproved, approver, now
		c.Version++
		return &Write{
			Campaign: c,
			Events: []Event{{
				Action: ActionApproved, Actor: approver, From: from, To: c.Status, Version: c.Version,
			}},
			Deliveries: &DeliveryPlan{Predicate: pred, Channels: c.Channels, MaxAttempts: s.cfg.DeliveryMaxAttempts},
		}, nil
	})
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

// checkApprover validates the free-text approver name.
func checkApprover(name string) (string, error) {
	name = strings.TrimSpace(name)
	msg := content.CheckPlainText(name, false)
	switch {
	case name == "":
		msg = "must not be empty"
	case msg == "" && utf8.RuneCountInString(name) > MaxApproverRunes:
		msg = fmt.Sprintf("must be at most %d characters", MaxApproverRunes)
	}
	if msg != "" {
		return "", fail(CodeInvalidRequest, "", content.FieldError{Path: "approved_by", Message: msg})
	}
	return name, nil
}
