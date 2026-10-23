package campaign_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign/campaigntest"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

const exampleBrief = "Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun."

type fixedCounter segment.Counts

func (c fixedCounter) CountAudience(context.Context, segment.Predicate) (segment.Counts, error) {
	return segment.Counts(c), nil
}

// clock is a settable time source.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixtureEnv struct {
	svc      *campaign.Service
	store    *campaigntest.Store
	audience *campaigntest.Audience
	llm      *mockllm.Client
	clock    *clock
}

// defaultAudience has consented recipients on both channels with short
// names.
func defaultAudience() map[content.Channel]campaign.ChannelAudience {
	longest := map[content.Variable]int{content.VarFirstName: 12, content.VarCity: 8}
	return map[content.Channel]campaign.ChannelAudience{
		content.ChannelSMS: {Recipients: 80, Longest: longest, Samples: []campaign.Recipient{
			campaigntest.Recipient(1, "Ayşe", "İstanbul"), campaigntest.Recipient(2, "Mehmet Can", "İstanbul"),
		}},
		content.ChannelEmail: {Recipients: 95, Longest: longest, Samples: []campaign.Recipient{
			campaigntest.Recipient(3, "Zeynep", "İstanbul"),
		}},
	}
}

func newEnv(t *testing.T, script ...mockllm.Step) fixtureEnv {
	t.Helper()
	return newEnvWithAudience(t, defaultAudience(), script...)
}

// newEnvWithAudience uses aud for both previews and approval.
func newEnvWithAudience(t *testing.T, aud map[content.Channel]campaign.ChannelAudience, script ...mockllm.Step) fixtureEnv {
	t.Helper()
	e := fixtureEnv{
		audience: campaigntest.NewAudience(aud),
		llm:      mockllm.New(script...),
		clock:    &clock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)},
	}
	e.store = campaigntest.NewStore(e.audience)
	logger := slog.New(slog.DiscardHandler)
	segments := segment.NewService(fixedCounter{Total: 120, SMS: 80, Email: 95}, e.clock.Now)
	gen := campaign.NewGenerator(e.llm, segments, campaign.GeneratorConfig{
		SegmentTimeout: 5 * time.Second, DraftsTimeout: 5 * time.Second, MaxAttempts: 2,
	}, logger)
	e.svc = campaign.NewService(gen, e.store, e.audience,
		campaign.ServiceConfig{GenerationTimeout: 10 * time.Second, PreviewSamples: 3, DeliveryMaxAttempts: 4}, e.clock.Now, logger)
	return e
}

func requireCode(t *testing.T, err error, want campaign.ErrorCode) {
	t.Helper()
	gerr, ok := errors.AsType[*campaign.Error](err)
	if !ok {
		t.Fatalf("error = %v, want *campaign.Error with code %s", err, want)
	}
	if gerr.Code != want {
		t.Fatalf("code = %s (%v), want %s", gerr.Code, gerr, want)
	}
}

func (e fixtureEnv) create(t *testing.T) campaign.Campaign {
	t.Helper()
	c, err := e.svc.Create(t.Context(), exampleBrief)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return c
}

func (e fixtureEnv) generate(t *testing.T, id campaign.ID) campaign.Campaign {
	t.Helper()
	g, err := e.svc.BeginGeneration(t.Context(), id)
	if err != nil {
		t.Fatalf("BeginGeneration() error = %v", err)
	}
	c, err := g.Run(t.Context(), nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return c
}

// ready returns a campaign in drafts_ready with the mock's drafts.
func (e fixtureEnv) ready(t *testing.T) campaign.Campaign {
	t.Helper()
	return e.generate(t, e.create(t).ID)
}

func actions(events []campaign.Event) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.Action
	}
	return out
}

func TestCreate(t *testing.T) {
	e := newEnv(t)
	c := e.create(t)

	if c.Status != campaign.StatusAudienceReady || c.Version != 1 || c.ID == "" {
		t.Errorf("campaign = %+v", c)
	}
	if got := segment.Describe(c.Rule); !slices.Equal(got, []string{
		"City is İstanbul", "Added to cart in the last 30 days", "No purchase in the last 30 days",
	}) {
		t.Errorf("rule = %q", got)
	}
	if c.Counts != (segment.Counts{Total: 120, SMS: 80, Email: 95}) {
		t.Errorf("counts = %+v", c.Counts)
	}
	if !slices.Equal(c.Channels, []content.Channel{content.ChannelSMS, content.ChannelEmail}) {
		t.Errorf("channels = %v", c.Channels)
	}
	if c.ContentHash != campaign.ContentHash(c) {
		t.Error("content hash not set")
	}
	events := e.store.Events(c.ID)
	if !slices.Equal(actions(events), []string{campaign.ActionCreated, campaign.ActionAudienceProposed}) ||
		events[1].From != campaign.StatusDraft || events[1].To != campaign.StatusAudienceReady {
		t.Errorf("events = %+v", events)
	}
}

func TestCreate_FailuresStoreNothing(t *testing.T) {
	tests := []struct {
		name  string
		brief string
		step  mockllm.Step
		want  campaign.ErrorCode
	}{
		{"empty brief", "  ", mockllm.Step{}, campaign.CodeInvalidBrief},
		{"unsupported brief", "Send to users older than 30", mockllm.Step{}, campaign.CodeUnsupportedBrief},
		{"refusal", exampleBrief, mockllm.Step{Fault: mockllm.FaultRefusal}, campaign.CodeRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.step)
			_, err := e.svc.Create(t.Context(), tt.brief)
			requireCode(t, err, tt.want)
			if list, _ := e.svc.List(t.Context()); len(list) != 0 {
				t.Errorf("stored %d campaigns", len(list))
			}
		})
	}
}

// edit applies mutate to an Edit built from c and returns the result.
func (e fixtureEnv) edit(t *testing.T, c campaign.Campaign, mutate func(*campaign.Edit)) campaign.Campaign {
	t.Helper()
	ed := editOf(c)
	mutate(&ed)
	out, err := e.svc.Edit(t.Context(), c.ID, ed)
	if err != nil {
		t.Fatalf("Edit() error = %v", err)
	}
	return out
}

func editOf(c campaign.Campaign) campaign.Edit {
	names := make([]string, len(c.Channels))
	for i, ch := range c.Channels {
		names[i] = string(ch)
	}
	return campaign.Edit{Version: c.Version, Channels: names, Drafts: c.Drafts}
}

func approvalOf(c campaign.Campaign) campaign.Approval {
	return campaign.Approval{Version: c.Version, ContentHash: c.ContentHash, ApprovedBy: "Deniz Operator"}
}

func (e fixtureEnv) approve(t *testing.T, c campaign.Campaign) campaign.Campaign {
	t.Helper()
	out, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c))
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	return out
}
