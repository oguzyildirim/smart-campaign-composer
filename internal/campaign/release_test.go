package campaign_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign/campaigntest"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

func lastEvent(e fixtureEnv, id campaign.ID) campaign.Event {
	events := e.store.Events(id)
	return events[len(events)-1]
}

func TestRelease_BeforeDueIsNoOp(t *testing.T) {
	e := newEnv(t)
	c := e.schedule(t, e.ready(t), time.Hour)
	e.clock.Advance(time.Hour - time.Second)
	res, err := e.svc.Release(t.Context(), c.ID)
	if err != nil || res.Outcome != campaign.ReleaseNone {
		t.Fatalf("Release() = %+v, %v; want no-op", res, err)
	}
	if got, _ := e.svc.Get(t.Context(), c.ID); got.Status != campaign.StatusScheduled || got.Version != c.Version {
		t.Errorf("campaign changed: %+v", got)
	}
}

func TestRelease_AtDue(t *testing.T) {
	e := newEnv(t)
	c := e.schedule(t, e.ready(t), time.Hour)
	e.clock.Advance(time.Hour + 30*time.Second)

	res, err := e.svc.Release(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if res.Outcome != campaign.ReleaseReleased || res.Lateness != 30*time.Second {
		t.Errorf("result = %+v, want released 30s late", res)
	}
	got, _ := e.svc.Get(t.Context(), c.ID)
	if got.Status != campaign.StatusApproved || got.Version != c.Version+1 || !got.ScheduledAt.Equal(c.ScheduledAt) ||
		got.ApprovedBy != c.ApprovedBy || !got.ApprovedAt.Equal(c.ApprovedAt) || got.ContentHash != c.ContentHash {
		t.Errorf("released = %+v", got)
	}
	ev := lastEvent(e, c.ID)
	if ev.Action != campaign.ActionReleased || ev.Actor != campaign.ActorSystem ||
		ev.From != campaign.StatusScheduled || ev.To != campaign.StatusApproved || ev.Version != got.Version {
		t.Errorf("release event = %+v", ev)
	}
	plans := e.store.Plans(c.ID)
	if len(plans) != 1 || plans[0].MaxAttempts != 4 || len(plans[0].Channels) != len(c.Channels) {
		t.Fatalf("plans = %+v, want one with the release", plans)
	}
	// Day windows are measured from the scheduled time, not from approval
	// or from the moment the worker got to it.
	var fromScheduled, fromClock bool
	for _, a := range plans[0].Predicate.Args {
		if ts, ok := a.(time.Time); ok {
			fromScheduled = fromScheduled || ts.Equal(c.ScheduledAt)
			fromClock = fromClock || ts.Equal(e.clock.Now()) || ts.Equal(c.ApprovedAt)
		}
	}
	if !fromScheduled || fromClock {
		t.Errorf("predicate args %v: want the scheduled time %v as reference", plans[0].Predicate.Args, c.ScheduledAt)
	}

	// Releasing again is a no-op.
	res, err = e.svc.Release(t.Context(), c.ID)
	if err != nil || res.Outcome != campaign.ReleaseNone || len(e.store.Plans(c.ID)) != 1 {
		t.Errorf("second Release() = %+v, %v, plans %d", res, err, len(e.store.Plans(c.ID)))
	}
}

func TestRelease_GraceBoundary(t *testing.T) {
	tests := []struct {
		late time.Duration
		want campaign.ReleaseOutcome
	}{
		{15 * time.Minute, campaign.ReleaseReleased},
		{15*time.Minute + time.Second, campaign.ReleaseExpired},
		{48 * time.Hour, campaign.ReleaseExpired},
	}
	for _, tt := range tests {
		t.Run(tt.late.String(), func(t *testing.T) {
			e := newEnv(t)
			c := e.schedule(t, e.ready(t), time.Hour)
			e.clock.Advance(time.Hour + tt.late)
			inspected := e.audience.Inspected()
			res, err := e.svc.Release(t.Context(), c.ID)
			if err != nil || res.Outcome != tt.want || res.Lateness != tt.late {
				t.Fatalf("Release() = %+v, %v; want %s after %s", res, err, tt.want, tt.late)
			}
			if tt.want != campaign.ReleaseExpired {
				return
			}
			got, _ := e.svc.Get(t.Context(), c.ID)
			if got.Status != campaign.StatusExpired || got.Version != c.Version+1 || !got.ScheduledAt.Equal(c.ScheduledAt) {
				t.Errorf("expired = %+v", got)
			}
			if n := len(e.store.Plans(c.ID)); n != 0 {
				t.Errorf("expired campaign planned %d deliveries", n)
			}
			if e.audience.Inspected() != inspected {
				t.Errorf("expiry inspected the audience")
			}
			if ev := lastEvent(e, c.ID); ev.Action != campaign.ActionExpired || ev.Actor != campaign.ActorSystem ||
				ev.From != campaign.StatusScheduled || ev.To != campaign.StatusExpired {
				t.Errorf("expiry event = %+v", ev)
			}
		})
	}
}

func TestRelease_CheckFailure(t *testing.T) {
	tests := []struct {
		name     string
		audience func() map[content.Channel]campaign.ChannelAudience
		want     campaign.ErrorCode
		detail   string
	}{
		{"nobody left in the audience", func() map[content.Channel]campaign.ChannelAudience { return nil },
			campaign.CodeNoRecipients, ""},
		{"a new recipient's name is too long", func() map[content.Channel]campaign.ChannelAudience {
			aud := defaultAudience()
			sms := aud[content.ChannelSMS]
			sms.Longest = map[content.Variable]int{content.VarFirstName: 200, content.VarCity: 8}
			aud[content.ChannelSMS] = sms
			return aud
		}, campaign.CodeInvalidContent, "sms.body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			c := e.schedule(t, e.ready(t), time.Hour)
			e.store.Audience = campaigntest.NewAudience(tt.audience())
			e.clock.Advance(time.Hour)

			res, err := e.svc.Release(t.Context(), c.ID)
			if err != nil || res.Outcome != campaign.ReleaseFailed {
				t.Fatalf("Release() = %+v, %v; want release_failed", res, err)
			}
			got, _ := e.svc.Get(t.Context(), c.ID)
			if got.Status != campaign.StatusReleaseFailed || got.Version != c.Version+1 || got.ReleaseFailure == nil ||
				got.ReleaseFailure.Code != tt.want || got.ReleaseFailure.Message == "" {
				t.Fatalf("release_failed campaign = %+v (failure %+v)", got, got.ReleaseFailure)
			}
			if tt.detail != "" && !strings.Contains(got.ReleaseFailure.Details[0].Path, tt.detail) {
				t.Errorf("details = %+v, want %s", got.ReleaseFailure.Details, tt.detail)
			}
			if n := len(e.store.Plans(c.ID)); n != 0 {
				t.Errorf("failed release planned %d deliveries", n)
			}
			if ev := lastEvent(e, c.ID); ev.Action != campaign.ActionReleaseFailed || ev.Actor != campaign.ActorSystem ||
				ev.From != campaign.StatusScheduled || ev.To != campaign.StatusReleaseFailed {
				t.Errorf("event = %+v", ev)
			}
			// Not retried automatically.
			if res, _ := e.svc.Release(t.Context(), c.ID); res.Outcome != campaign.ReleaseNone {
				t.Errorf("second Release() = %+v", res)
			}
		})
	}
}

func TestRelease_InfrastructureErrorChangesNothing(t *testing.T) {
	e := newEnv(t)
	c := e.schedule(t, e.ready(t), time.Hour)
	boom := errors.New("connection reset")
	e.audience.Fail(boom)
	e.clock.Advance(time.Hour)
	if _, err := e.svc.Release(t.Context(), c.ID); !errors.Is(err, boom) {
		t.Fatalf("Release() error = %v, want %v", err, boom)
	}
	if got, _ := e.svc.Get(t.Context(), c.ID); got.Status != campaign.StatusScheduled || got.Version != c.Version {
		t.Errorf("campaign changed: %+v", got)
	}
}

func TestRelease_NotScheduledIsNoOp(t *testing.T) {
	e := newEnv(t)
	ready := e.ready(t)
	approved := e.approve(t, e.ready(t))
	for _, c := range []campaign.Campaign{ready, approved} {
		e.clock.Advance(time.Hour)
		res, err := e.svc.Release(t.Context(), c.ID)
		if err != nil || res.Outcome != campaign.ReleaseNone {
			t.Errorf("Release(%s) = %+v, %v", c.Status, res, err)
		}
		if got, _ := e.svc.Get(t.Context(), c.ID); got.Version != c.Version {
			t.Errorf("Release(%s) changed the campaign", c.Status)
		}
	}
	if _, err := e.svc.Release(t.Context(), "00000000-0000-4000-8000-999999999999"); err == nil {
		t.Error("Release(unknown) succeeded")
	}
}

type recordedRelease struct {
	outcome  campaign.ReleaseOutcome
	lateness time.Duration
}

type releaseRecorder struct{ got []recordedRelease }

func (r *releaseRecorder) CampaignReleased(o campaign.ReleaseOutcome, d time.Duration) {
	r.got = append(r.got, recordedRelease{o, d})
}

func TestReleaseDue(t *testing.T) {
	e := newEnv(t)
	rec := &releaseRecorder{}
	e.svc = e.withReleaseMetrics(rec)
	e.store.Now = e.clock.Now

	soon := e.schedule(t, e.ready(t), time.Hour)
	sooner := e.schedule(t, e.ready(t), 50*time.Minute)
	later := e.schedule(t, e.ready(t), 3*time.Hour)
	ready := e.ready(t)
	e.clock.Advance(time.Hour + time.Minute)

	n, err := e.svc.ReleaseDue(t.Context(), 10)
	if err != nil || n != 2 {
		t.Fatalf("ReleaseDue() = %d, %v; want 2", n, err)
	}
	for _, c := range []campaign.Campaign{soon, sooner} {
		if got, _ := e.svc.Get(t.Context(), c.ID); got.Status != campaign.StatusApproved {
			t.Errorf("due campaign status = %s, want approved", got.Status)
		}
	}
	for _, c := range []campaign.Campaign{later, ready} {
		if got, _ := e.svc.Get(t.Context(), c.ID); got.Status != c.Status {
			t.Errorf("campaign not due changed to %s", got.Status)
		}
	}
	want := []recordedRelease{{campaign.ReleaseReleased, 11 * time.Minute}, {campaign.ReleaseReleased, time.Minute}}
	if len(rec.got) != 2 || rec.got[0] != want[0] || rec.got[1] != want[1] {
		t.Errorf("metrics = %+v, want %+v (oldest due first)", rec.got, want)
	}
	if n, err := e.svc.ReleaseDue(t.Context(), 10); err != nil || n != 0 {
		t.Errorf("second ReleaseDue() = %d, %v", n, err)
	}
}

func TestReleaseDue_RespectsLimit(t *testing.T) {
	e := newEnv(t)
	e.store.Now = e.clock.Now
	for range 3 {
		e.schedule(t, e.ready(t), time.Hour)
	}
	e.clock.Advance(time.Hour)
	if n, _ := e.svc.ReleaseDue(t.Context(), 2); n != 2 {
		t.Errorf("ReleaseDue(limit 2) = %d", n)
	}
	if n, _ := e.svc.ReleaseDue(t.Context(), 2); n != 1 {
		t.Errorf("second ReleaseDue(limit 2) = %d", n)
	}
}

func TestRelease_ConcurrentWithCancel(t *testing.T) {
	e := newEnv(t)
	c := e.schedule(t, e.ready(t), time.Hour)
	e.clock.Advance(time.Hour)
	var wg sync.WaitGroup
	var cancelErr error
	for range 5 {
		wg.Go(func() {
			if _, err := e.svc.Release(t.Context(), c.ID); err != nil {
				t.Errorf("Release() error = %v", err)
			}
		})
	}
	wg.Go(func() { _, cancelErr = e.svc.Cancel(t.Context(), c.ID, cancelOf(c)) })
	wg.Wait()

	got, _ := e.svc.Get(t.Context(), c.ID)
	plans := len(e.store.Plans(c.ID))
	switch got.Status {
	case campaign.StatusApproved: // release won
		if plans != 1 || cancelErr == nil {
			t.Errorf("released: plans = %d, cancel error = %v; want 1 plan and a failed cancel", plans, cancelErr)
		}
		requireCode(t, cancelErr, campaign.CodeInvalidState)
	case campaign.StatusDraftsReady: // cancel won
		if plans != 0 || cancelErr != nil {
			t.Errorf("canceled: plans = %d, cancel error = %v", plans, cancelErr)
		}
	default:
		t.Fatalf("status = %s", got.Status)
	}
}
