// Package campaigntest provides in-memory fakes of the campaign service's
// ports for unit tests: a Store that serializes updates like a row lock,
// and a fixed AudienceInspector.
package campaigntest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// Store is an in-memory campaign.Store. Update holds a single lock while
// fn runs, so concurrent updates are serialized as under SELECT ... FOR
// UPDATE. Audience is the inspector passed to UpdateFunc.
type Store struct {
	Audience campaign.AudienceInspector

	mu        sync.Mutex
	campaigns map[campaign.ID]campaign.Campaign
	order     []campaign.ID
	events    map[campaign.ID][]campaign.Event
	plans     map[campaign.ID][]campaign.DeliveryPlan
	next      int
}

var _ campaign.Store = (*Store)(nil)

// NewStore returns an empty store.
func NewStore(audience campaign.AudienceInspector) *Store {
	return &Store{
		Audience:  audience,
		campaigns: map[campaign.ID]campaign.Campaign{},
		events:    map[campaign.ID][]campaign.Event{},
		plans:     map[campaign.ID][]campaign.DeliveryPlan{},
	}
}

// Create implements campaign.Store.
func (s *Store) Create(_ context.Context, c campaign.Campaign, events []campaign.Event) (campaign.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	c.ID = campaign.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", s.next))
	c.CreatedAt = time.Date(2026, 10, 22, 12, 0, s.next, 0, time.UTC)
	c.UpdatedAt = c.CreatedAt
	s.campaigns[c.ID] = clone(c)
	s.order = append(s.order, c.ID)
	s.events[c.ID] = append(s.events[c.ID], events...)
	return clone(c), nil
}

// Get implements campaign.Store.
func (s *Store) Get(_ context.Context, id campaign.ID) (campaign.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		return campaign.Campaign{}, campaign.ErrNotFound
	}
	return clone(c), nil
}

// List implements campaign.Store.
func (s *Store) List(_ context.Context, limit int) ([]campaign.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []campaign.Campaign
	for _, id := range slices.Backward(s.order) {
		if len(out) == limit {
			break
		}
		out = append(out, clone(s.campaigns[id]))
	}
	return out, nil
}

// Update implements campaign.Store.
func (s *Store) Update(ctx context.Context, id campaign.ID, fn campaign.UpdateFunc) (campaign.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		return campaign.Campaign{}, campaign.ErrNotFound
	}
	w, err := fn(ctx, s.Audience, clone(c))
	if err != nil {
		return campaign.Campaign{}, err
	}
	if w == nil {
		return clone(c), nil
	}
	next := clone(w.Campaign)
	next.ID, next.CreatedAt = c.ID, c.CreatedAt
	next.UpdatedAt = c.UpdatedAt.Add(time.Second)
	s.campaigns[id] = next
	s.events[id] = append(s.events[id], w.Events...)
	if w.Deliveries != nil {
		s.plans[id] = append(s.plans[id], *w.Deliveries)
	}
	return clone(next), nil
}

// Plans returns the delivery plans written for id, in order.
func (s *Store) Plans(id campaign.ID) []campaign.DeliveryPlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.plans[id])
}

// Events returns the audit events recorded for id.
func (s *Store) Events(id campaign.ID) []campaign.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events[id])
}

// Put replaces a stored campaign, for arranging test states.
func (s *Store) Put(c campaign.Campaign) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.campaigns[c.ID] = clone(c)
}

func clone(c campaign.Campaign) campaign.Campaign {
	c.Channels = slices.Clone(c.Channels)
	c.UnsupportedCriteria = slices.Clone(c.UnsupportedCriteria)
	c.Rule.Conditions = slices.Clone(c.Rule.Conditions)
	return c
}

// Audience is a fixed campaign.AudienceInspector: every predicate yields
// the configured audience for its channel.
type Audience struct {
	mu        sync.Mutex
	channels  map[content.Channel]campaign.ChannelAudience
	err       error
	inspected int
}

// NewAudience returns an inspector with the given per-channel audiences;
// channels not listed have no recipients.
func NewAudience(channels map[content.Channel]campaign.ChannelAudience) *Audience {
	return &Audience{channels: channels}
}

// Fail makes every later call return err.
func (a *Audience) Fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

// Inspected returns the number of calls so far.
func (a *Audience) Inspected() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inspected
}

// InspectAudience implements campaign.AudienceInspector.
func (a *Audience) InspectAudience(_ context.Context, _ segment.Predicate, ch content.Channel, samples int) (campaign.ChannelAudience, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inspected++
	if a.err != nil {
		return campaign.ChannelAudience{}, a.err
	}
	out := a.channels[ch]
	if len(out.Samples) > samples {
		out.Samples = out.Samples[:samples]
	}
	return out, nil
}

// Recipient returns a sample recipient.
func Recipient(id int64, firstName, city string) campaign.Recipient {
	return campaign.Recipient{UserID: id, Values: map[content.Variable]string{
		content.VarFirstName: firstName, content.VarCity: city,
	}}
}
