package campaign

import (
	"context"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// Preview is a campaign's content rendered for real (fake) recipients,
// with every check approval would run.
type Preview struct {
	Campaign Campaign
	// Validation combines the static checks with the actual-audience
	// length and recipient checks.
	Validation Validation
	Channels   []ChannelPreview
}

// ChannelPreview is one selected channel's audience and samples.
type ChannelPreview struct {
	Channel    content.Channel
	Recipients int64
	Longest    map[content.Variable]int
	Samples    []SamplePreview
}

// SamplePreview is the channel's content rendered for one recipient.
type SamplePreview struct {
	Recipient Recipient
	Fields    []RenderedField
}

// RenderedField is a field after personalization, with its final length.
type RenderedField struct {
	Path      string
	Text      string
	Runes     int
	MaxRunes  int
	OverLimit bool
}

// Preview renders the campaign's content for sample recipients of each
// selected channel and runs approval's checks. Rendering happens only
// here, in the backend.
func (s *Service) Preview(ctx context.Context, id ID) (Preview, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Preview{}, err
	}
	if !c.HasDrafts() {
		return Preview{}, fail(CodeInvalidState, "the campaign has no drafts yet")
	}
	pred, err := segment.Compile(c.Rule, s.now().UTC())
	if err != nil {
		return Preview{}, fmt.Errorf("compile campaign segment: %w", err)
	}
	channels, audienceErrs, err := s.inspect(ctx, s.audience, c, pred, max(s.cfg.PreviewSamples, 1))
	if err != nil {
		return Preview{}, err
	}
	v := c.Validate()
	v.Errors = append(v.Errors, audienceErrs...)
	if !hasRecipients(channels) && len(c.Channels) > 0 {
		v.Errors = append(v.Errors, content.FieldError{Path: "channels", Message: messages[CodeNoRecipients]})
	}
	v.Valid = len(v.Errors) == 0
	return Preview{Campaign: c, Validation: v, Channels: channels}, nil
}

// inspect reads each selected channel's audience (users matching pred)
// and checks the content against its longest real values.
func (s *Service) inspect(ctx context.Context, audience AudienceInspector, c Campaign, pred segment.Predicate, samples int) ([]ChannelPreview, []content.FieldError, error) {
	var (
		out  []ChannelPreview
		errs []content.FieldError
	)
	for _, ch := range c.Channels {
		a, err := audience.InspectAudience(ctx, pred, ch, samples)
		if err != nil {
			return nil, nil, fmt.Errorf("inspect %s audience: %w", ch, err)
		}
		if a.Recipients > 0 {
			errs = append(errs, content.CheckAudienceLengths(c.Drafts, ch, a.Longest)...)
		}
		cp := ChannelPreview{Channel: ch, Recipients: a.Recipients, Longest: a.Longest}
		for _, r := range a.Samples {
			cp.Samples = append(cp.Samples, SamplePreview{Recipient: r, Fields: render(c.Drafts, ch, r)})
		}
		out = append(out, cp)
	}
	return out, errs, nil
}

func hasRecipients(channels []ChannelPreview) bool {
	return slices.ContainsFunc(channels, func(c ChannelPreview) bool { return c.Recipients > 0 })
}

// render personalizes channel ch's fields for r. Fields that do not parse
// are omitted; validation reports them.
func render(d content.Drafts, ch content.Channel, r Recipient) []RenderedField {
	var out []RenderedField
	for _, f := range content.Fields(d, ch) {
		tmpl, err := content.ParseTemplate(f.Text)
		if err != nil {
			continue
		}
		text := tmpl.Render(r.Values)
		n := utf8.RuneCountInString(text)
		out = append(out, RenderedField{Path: f.Path, Text: text, Runes: n, MaxRunes: f.MaxRunes, OverLimit: n > f.MaxRunes})
	}
	return out
}
