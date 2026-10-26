package campaign_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
)

// scheduleOf approves c for at.
func scheduleOf(c campaign.Campaign, at time.Time) campaign.Approval {
	a := approvalOf(c)
	a.ScheduledAt = at
	return a
}

func (e fixtureEnv) schedule(t *testing.T, c campaign.Campaign, in time.Duration) campaign.Campaign {
	t.Helper()
	out, err := e.svc.Approve(t.Context(), c.ID, scheduleOf(c, e.clock.Now().Add(in)))
	if err != nil {
		t.Fatalf("Approve(scheduled) error = %v", err)
	}
	return out
}

func TestApprove_Scheduled(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	at := e.clock.Now().Add(2 * time.Hour)
	got, err := e.svc.Approve(t.Context(), c.ID, scheduleOf(c, at))
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if got.Status != campaign.StatusScheduled || !got.ScheduledAt.Equal(at) || got.ApprovedBy != "Deniz Operator" ||
		!got.ApprovedAt.Equal(e.clock.Now()) || got.Version != c.Version+1 || got.ContentHash != c.ContentHash {
		t.Errorf("scheduled campaign = %+v", got)
	}
	if plans := e.store.Plans(c.ID); len(plans) != 0 {
		t.Errorf("scheduled approval planned deliveries: %+v", plans)
	}
	events := e.store.Events(c.ID)
	last := events[len(events)-1]
	if last.Action != campaign.ActionScheduled || last.Actor != "Deniz Operator" ||
		last.From != campaign.StatusDraftsReady || last.To != campaign.StatusScheduled || last.Version != got.Version {
		t.Errorf("schedule event = %+v", last)
	}
}

func TestApprove_ScheduledTimeRules(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		ok   bool
	}{
		{"in the past", -time.Second, false},
		{"now", 0, false},
		{"below the minimum lead", 59 * time.Second, false},
		{"exactly the minimum lead", time.Minute, true},
		{"exactly the maximum horizon", 30 * 24 * time.Hour, true},
		{"beyond the maximum horizon", 30*24*time.Hour + time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			c := e.ready(t)
			got, err := e.svc.Approve(t.Context(), c.ID, scheduleOf(c, e.clock.Now().Add(tt.in)))
			if tt.ok {
				if err != nil || got.Status != campaign.StatusScheduled {
					t.Fatalf("Approve() = %s, %v; want scheduled", got.Status, err)
				}
				return
			}
			requireCode(t, err, campaign.CodeInvalidRequest)
			requireDetail(t, err, "scheduled_at")
			if got, _ := e.svc.Get(t.Context(), c.ID); got.Status != campaign.StatusDraftsReady {
				t.Errorf("rejected schedule changed status to %s", got.Status)
			}
		})
	}
}

func TestParseScheduledAt(t *testing.T) {
	want := time.Date(2026, 10, 30, 10, 0, 0, 0, time.FixedZone("", 3*3600))
	for _, s := range []string{"2026-10-30T10:00:00+03:00", "2026-10-30T07:00:00Z", "2026-10-30T07:00:00.000Z"} {
		got, err := campaign.ParseScheduledAt(s)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseScheduledAt(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "2026-10-30T10:00:00", "2026-10-30 10:00:00+03:00", "2026-10-30", "tomorrow",
		"2026-13-30T10:00:00Z", "2026-10-30T10:00:00+3", "10000-01-01T00:00:00Z"} {
		_, err := campaign.ParseScheduledAt(s)
		requireCode(t, err, campaign.CodeInvalidRequest)
		requireDetail(t, err, "scheduled_at")
	}
}

func requireDetail(t *testing.T, err error, path string) {
	t.Helper()
	gerr, _ := errors.AsType[*campaign.Error](err)
	for _, d := range gerr.Details {
		if d.Path == path {
			return
		}
	}
	t.Errorf("details = %+v, want one for %s", gerr.Details, path)
}

func TestApprove_ScheduledIsIdempotent(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	first := e.schedule(t, c, 2*time.Hour)

	// Same hash with another time or no time: unchanged, schedule included.
	for _, at := range []time.Time{e.clock.Now().Add(5 * time.Hour), {}} {
		again, err := e.svc.Approve(t.Context(), c.ID, scheduleOf(c, at))
		if err != nil {
			t.Fatalf("re-approve error = %v", err)
		}
		if again.Status != campaign.StatusScheduled || again.Version != first.Version || !again.ScheduledAt.Equal(first.ScheduledAt) {
			t.Errorf("re-approval changed the campaign: %+v", again)
		}
	}
	if n := len(e.store.Plans(c.ID)); n != 0 {
		t.Errorf("re-approval planned %d deliveries", n)
	}
	other := approvalOf(c)
	other.ContentHash = c.ContentHash[:63] + "x"
	_, err := e.svc.Approve(t.Context(), c.ID, other)
	requireCode(t, err, campaign.CodeContentChanged)
}

func TestScheduledStatusesAreImmutable(t *testing.T) {
	for _, status := range []campaign.Status{campaign.StatusScheduled, campaign.StatusReleaseFailed, campaign.StatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			e := newEnv(t)
			c := e.schedule(t, e.ready(t), time.Hour)
			c.Status = status
			e.store.Put(c)
			if !c.HasDrafts() || !c.HoldsApproval() || c.Approved() {
				t.Errorf("HasDrafts=%v HoldsApproval=%v Approved=%v", c.HasDrafts(), c.HoldsApproval(), c.Approved())
			}
			_, err := e.svc.Edit(t.Context(), c.ID, campaign.Edit{Version: c.Version, Drafts: c.Drafts, Channels: []string{"sms"}})
			requireCode(t, err, campaign.CodeInvalidState)
			_, err = e.svc.BeginGeneration(t.Context(), c.ID)
			requireCode(t, err, campaign.CodeInvalidState)
		})
	}
}

func TestApprove_ReleaseFailedOrExpiredNeedsCancel(t *testing.T) {
	for _, status := range []campaign.Status{campaign.StatusReleaseFailed, campaign.StatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			e := newEnv(t)
			c := e.schedule(t, e.ready(t), time.Hour)
			c.Status = status
			e.store.Put(c)
			_, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c))
			requireCode(t, err, campaign.CodeInvalidState)
		})
	}
}

func rescheduleOf(c campaign.Campaign, at time.Time) campaign.Reschedule {
	return campaign.Reschedule{Version: c.Version, ContentHash: c.ContentHash, ScheduledAt: at, Actor: "Ece Planner"}
}

func TestReschedule(t *testing.T) {
	e := newEnv(t)
	c := e.schedule(t, e.ready(t), time.Hour)
	at := e.clock.Now().Add(26 * time.Hour)
	got, err := e.svc.Reschedule(t.Context(), c.ID, rescheduleOf(c, at))
	if err != nil {
		t.Fatalf("Reschedule() error = %v", err)
	}
	if got.Status != campaign.StatusScheduled || !got.ScheduledAt.Equal(at) || got.Version != c.Version+1 ||
		got.ContentHash != c.ContentHash || got.ApprovedBy != c.ApprovedBy || !got.ApprovedAt.Equal(c.ApprovedAt) {
		t.Errorf("rescheduled = %+v", got)
	}
	events := e.store.Events(c.ID)
	last := events[len(events)-1]
	if last.Action != campaign.ActionRescheduled || last.Actor != "Ece Planner" ||
		last.From != campaign.StatusScheduled || last.To != campaign.StatusScheduled || last.Version != got.Version {
		t.Errorf("reschedule event = %+v", last)
	}
	if n := len(e.store.Plans(c.ID)); n != 0 {
		t.Errorf("reschedule planned %d deliveries", n)
	}
}

func TestReschedule_Rejections(t *testing.T) {
	tests := []struct {
		name    string
		status  campaign.Status // overrides scheduled when set
		mutate  func(e fixtureEnv, r *campaign.Reschedule)
		want    campaign.ErrorCode
		wantFor string
	}{
		{name: "drafts_ready", status: campaign.StatusDraftsReady, want: campaign.CodeInvalidState},
		{name: "approved", status: campaign.StatusApproved, want: campaign.CodeInvalidState},
		{name: "sending", status: campaign.StatusSending, want: campaign.CodeInvalidState},
		{name: "release_failed", status: campaign.StatusReleaseFailed, want: campaign.CodeInvalidState},
		{name: "expired", status: campaign.StatusExpired, want: campaign.CodeInvalidState},
		{name: "stale version", mutate: func(_ fixtureEnv, r *campaign.Reschedule) { r.Version-- },
			want: campaign.CodeVersionConflict},
		{name: "different hash", mutate: func(_ fixtureEnv, r *campaign.Reschedule) { r.ContentHash = r.ContentHash[:63] + "x" },
			want: campaign.CodeContentChanged},
		{name: "time in the past", mutate: func(e fixtureEnv, r *campaign.Reschedule) { r.ScheduledAt = e.clock.Now().Add(-time.Hour) },
			want: campaign.CodeInvalidRequest, wantFor: "scheduled_at"},
		{name: "time beyond the horizon", mutate: func(e fixtureEnv, r *campaign.Reschedule) {
			r.ScheduledAt = e.clock.Now().Add(31 * 24 * time.Hour)
		}, want: campaign.CodeInvalidRequest, wantFor: "scheduled_at"},
		{name: "no time", mutate: func(_ fixtureEnv, r *campaign.Reschedule) { r.ScheduledAt = time.Time{} },
			want: campaign.CodeInvalidRequest, wantFor: "scheduled_at"},
		{name: "empty actor", mutate: func(_ fixtureEnv, r *campaign.Reschedule) { r.Actor = " " },
			want: campaign.CodeInvalidRequest, wantFor: "actor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			c := e.schedule(t, e.ready(t), time.Hour)
			if tt.status != "" {
				c.Status = tt.status
				e.store.Put(c)
			}
			r := rescheduleOf(c, e.clock.Now().Add(3*time.Hour))
			if tt.mutate != nil {
				tt.mutate(e, &r)
			}
			_, err := e.svc.Reschedule(t.Context(), c.ID, r)
			requireCode(t, err, tt.want)
			if tt.wantFor != "" {
				requireDetail(t, err, tt.wantFor)
			}
			if got, _ := e.svc.Get(t.Context(), c.ID); got.Version != c.Version || !got.ScheduledAt.Equal(c.ScheduledAt) {
				t.Errorf("rejected reschedule changed the campaign: %+v", got)
			}
		})
	}
}

func cancelOf(c campaign.Campaign) campaign.Cancel {
	return campaign.Cancel{Version: c.Version, ContentHash: c.ContentHash, Actor: "Ece Planner"}
}

func TestCancel(t *testing.T) {
	for _, status := range []campaign.Status{campaign.StatusScheduled, campaign.StatusReleaseFailed, campaign.StatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			e := newEnv(t)
			c := e.schedule(t, e.ready(t), time.Hour)
			c.Status = status
			if status == campaign.StatusReleaseFailed {
				c.ReleaseFailure = &campaign.ReleaseFailure{Code: campaign.CodeNoRecipients, Message: "x"}
			}
			e.store.Put(c)

			got, err := e.svc.Cancel(t.Context(), c.ID, cancelOf(c))
			if err != nil {
				t.Fatalf("Cancel() error = %v", err)
			}
			if got.Status != campaign.StatusDraftsReady || got.ApprovedBy != "" || !got.ApprovedAt.IsZero() ||
				!got.ScheduledAt.IsZero() || got.ReleaseFailure != nil || got.Version != c.Version+1 ||
				got.ContentHash != c.ContentHash || got.Drafts != c.Drafts {
				t.Errorf("canceled = %+v", got)
			}
			events := e.store.Events(c.ID)
			last := events[len(events)-1]
			if last.Action != campaign.ActionScheduleCanceled || last.Actor != "Ece Planner" ||
				last.From != status || last.To != campaign.StatusDraftsReady || last.Version != got.Version {
				t.Errorf("cancel event = %+v", last)
			}

			// Editable again, and a fresh approval needs the new version.
			e.edit(t, got, func(ed *campaign.Edit) { ed.Drafts.SMS.Body = "Yeni metin" })
			_, err = e.svc.Approve(t.Context(), c.ID, approvalOf(c))
			requireCode(t, err, campaign.CodeVersionConflict)
		})
	}
}

func TestCancel_Rejections(t *testing.T) {
	tests := []struct {
		name   string
		status campaign.Status
		mutate func(*campaign.Cancel)
		want   campaign.ErrorCode
	}{
		{name: "drafts_ready", status: campaign.StatusDraftsReady, want: campaign.CodeInvalidState},
		{name: "approved", status: campaign.StatusApproved, want: campaign.CodeInvalidState},
		{name: "sending", status: campaign.StatusSending, want: campaign.CodeInvalidState},
		{name: "completed", status: campaign.StatusCompleted, want: campaign.CodeInvalidState},
		{name: "stale version", mutate: func(c *campaign.Cancel) { c.Version-- }, want: campaign.CodeVersionConflict},
		{name: "different hash", mutate: func(c *campaign.Cancel) { c.ContentHash = "" }, want: campaign.CodeContentChanged},
		{name: "actor too long", mutate: func(c *campaign.Cancel) { c.Actor = strings.Repeat("a", campaign.MaxApproverRunes+1) },
			want: campaign.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			c := e.schedule(t, e.ready(t), time.Hour)
			if tt.status != "" {
				c.Status = tt.status
				e.store.Put(c)
			}
			req := cancelOf(c)
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			_, err := e.svc.Cancel(t.Context(), c.ID, req)
			requireCode(t, err, tt.want)
			if got, _ := e.svc.Get(t.Context(), c.ID); got.Status != c.Status || got.Version != c.Version {
				t.Errorf("rejected cancel changed the campaign: %+v", got)
			}
		})
	}
}
