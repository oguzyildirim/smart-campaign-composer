package campaign

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// ServiceConfig bounds the service's work.
type ServiceConfig struct {
	// GenerationTimeout bounds one draft generation, every attempt
	// included. The generation lease lasts a little longer.
	GenerationTimeout time.Duration
	// PreviewSamples is the number of recipients rendered per channel.
	PreviewSamples int
	// DeliveryMaxAttempts is the attempt limit of each delivery created at
	// approval.
	DeliveryMaxAttempts int
}

// ListLimit is the number of campaigns List returns.
const ListLimit = 100

// Service runs the campaign lifecycle. It is safe for concurrent use.
type Service struct {
	gen      AIGenerator
	store    Store
	audience AudienceInspector
	cfg      ServiceConfig
	now      func() time.Time
	logger   *slog.Logger
}

// NewService returns a Service. audience reads outside transactions (for
// previews); approval reads through the Store's transaction instead.
func NewService(gen AIGenerator, store Store, audience AudienceInspector, cfg ServiceConfig, now func() time.Time, logger *slog.Logger) *Service {
	return &Service{gen: gen, store: store, audience: audience, cfg: cfg, now: now, logger: logger}
}

// Create validates the brief, asks the model for an audience, and stores a
// new campaign in audience_ready. Nothing is stored if the proposal fails.
func (s *Service) Create(ctx context.Context, brief string) (Campaign, error) {
	brief, err := ValidateBrief(brief)
	if err != nil {
		return Campaign{}, err
	}
	p, err := s.gen.ProposeSegment(ctx, brief)
	if err != nil {
		return Campaign{}, err
	}
	c := Campaign{
		Brief:               brief,
		Status:              StatusAudienceReady,
		Rule:                p.Rule,
		Explanation:         p.Explanation,
		UnsupportedCriteria: p.UnsupportedCriteria,
		Counts:              p.Counts,
		Channels:            defaultChannels(p.Counts),
		Version:             1,
	}
	c.ContentHash = ContentHash(c)
	events := []Event{
		{Action: ActionCreated, Actor: ActorSystem, To: StatusDraft, Version: 1},
		{Action: ActionAudienceProposed, Actor: ActorSystem, From: StatusDraft, To: StatusAudienceReady, Version: 1},
	}
	created, err := s.store.Create(ctx, c, events)
	if err != nil {
		return Campaign{}, fmt.Errorf("create campaign: %w", err)
	}
	return created, nil
}

// defaultChannels preselects every channel with consented recipients.
func defaultChannels(c segment.Counts) []content.Channel {
	out := []content.Channel{}
	if c.SMS > 0 {
		out = append(out, content.ChannelSMS)
	}
	if c.Email > 0 {
		out = append(out, content.ChannelEmail)
	}
	return out
}

// Get returns a campaign.
func (s *Service) Get(ctx context.Context, id ID) (Campaign, error) {
	c, err := s.store.Get(ctx, id)
	return c, storeError(err)
}

// List returns the most recent campaigns, newest first.
func (s *Service) List(ctx context.Context) ([]Campaign, error) {
	cs, err := s.store.List(ctx, ListLimit)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	return cs, nil
}

// storeError maps store sentinels to client-safe errors.
func storeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return fail(CodeNotFound, "")
	case errors.Is(err, ErrConflict):
		return fail(CodeVersionConflict, "")
	default:
		return err
	}
}

// update runs fn under the store's lock and maps store errors.
func (s *Service) update(ctx context.Context, id ID, fn UpdateFunc) (Campaign, error) {
	c, err := s.store.Update(ctx, id, fn)
	if _, ok := errors.AsType[*Error](err); ok {
		return Campaign{}, err
	}
	return c, storeError(err)
}
