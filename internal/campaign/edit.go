package campaign

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// editCaps are hard size limits for edited text, in runes: twice each
// channel limit. Text within them is stored even if it fails content
// validation, so reviewers can keep working; approval rejects it.
var editCaps = map[string]int{
	"sms.body":      2 * content.MaxSMSRunes,
	"email.subject": 2 * content.MaxEmailSubjectRunes,
	"email.body":    2 * content.MaxEmailBodyRunes,
}

// Edit is a marketer's change to content and channel selection. Version is
// the version the marketer edited.
type Edit struct {
	Version  int
	Channels []string
	Drafts   content.Drafts
}

// Edit replaces the content and channels of a drafts_ready campaign. Edits
// must be structurally sound (CodeInvalidRequest otherwise) but may fail
// content validation: they are stored, reported by Validate, and rejected
// by Approve. An edit that changes nothing writes nothing.
func (s *Service) Edit(ctx context.Context, id ID, e Edit) (Campaign, error) {
	channels, err := checkEdit(e)
	if err != nil {
		return Campaign{}, err
	}
	return s.update(ctx, id, func(_ context.Context, _ AudienceInspector, c Campaign) (*Write, error) {
		switch {
		case c.Status != StatusDraftsReady:
			return nil, fail(CodeInvalidState, "only campaigns with drafts that are not approved can be edited")
		case c.Generating(s.now()):
			return nil, fail(CodeGenerationInProgress, "")
		case e.Version != c.Version:
			return nil, fail(CodeVersionConflict, "")
		case slices.Equal(channels, c.Channels) && e.Drafts == c.Drafts:
			return nil, nil
		}
		c.Channels, c.Drafts = channels, e.Drafts
		c.Version++
		c.ContentHash = ContentHash(c)
		return &Write{Campaign: c, Events: []Event{{
			Action: ActionEdited, Actor: ActorMarketer, From: c.Status, To: c.Status, Version: c.Version,
		}}}, nil
	})
}

// checkEdit applies the structural rules: known channels, and text that
// is valid UTF-8 without NUL bytes and within editCaps.
func checkEdit(e Edit) ([]content.Channel, error) {
	var problems []content.FieldError
	channels, err := content.ParseChannels(e.Channels)
	if err != nil {
		problems = append(problems, content.FieldError{Path: "channels", Message: err.Error()})
	}
	for path, text := range map[string]string{
		"sms.body":      e.Drafts.SMS.Body,
		"email.subject": e.Drafts.Email.Subject,
		"email.body":    e.Drafts.Email.Body,
	} {
		switch {
		case !utf8.ValidString(text) || strings.ContainsRune(text, 0):
			problems = append(problems, content.FieldError{Path: path, Message: "must be valid UTF-8 text without NUL characters"})
		case utf8.RuneCountInString(text) > editCaps[path]:
			problems = append(problems, content.FieldError{Path: path, Message: fmt.Sprintf("must be at most %d characters", editCaps[path])})
		}
	}
	if len(problems) > 0 {
		slices.SortFunc(problems, func(a, b content.FieldError) int { return strings.Compare(a.Path, b.Path) })
		return nil, fail(CodeInvalidRequest, "", problems...)
	}
	return channels, nil
}
