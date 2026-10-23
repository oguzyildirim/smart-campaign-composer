package campaign_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign/campaigntest"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
)

func TestApprove(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	approved := e.approve(t, c)
	if approved.Status != campaign.StatusApproved || approved.ApprovedBy != "Deniz Operator" ||
		!approved.ApprovedAt.Equal(e.clock.Now()) || approved.Version != c.Version+1 ||
		approved.ContentHash != c.ContentHash || approved.Drafts != c.Drafts {
		t.Errorf("approved = %+v", approved)
	}
	events := e.store.Events(c.ID)
	last := events[len(events)-1]
	if last.Action != campaign.ActionApproved || last.Actor != "Deniz Operator" ||
		last.From != campaign.StatusDraftsReady || last.To != campaign.StatusApproved {
		t.Errorf("approval event = %+v", last)
	}
}

func TestApprove_IdempotentWithSameHash(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	first := e.approve(t, c)
	e.clock.Advance(time.Hour)
	again, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c)) // old version, same hash
	if err != nil {
		t.Fatalf("second Approve() error = %v", err)
	}
	if again.Version != first.Version || !again.ApprovedAt.Equal(first.ApprovedAt) {
		t.Errorf("re-approval changed the campaign: %+v", again)
	}
	n := 0
	for _, ev := range e.store.Events(c.ID) {
		if ev.Action == campaign.ActionApproved {
			n++
		}
	}
	if n != 1 {
		t.Errorf("approval events = %d, want 1", n)
	}

	other := approvalOf(c)
	other.ContentHash = strings.Repeat("0", 64)
	_, err = e.svc.Approve(t.Context(), c.ID, other)
	requireCode(t, err, campaign.CodeContentChanged)
}

func TestApprove_Concurrent(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			_, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Approve() error = %v", err)
		}
	}
	n := 0
	for _, ev := range e.store.Events(c.ID) {
		if ev.Action == campaign.ActionApproved {
			n++
		}
	}
	if n != 1 {
		t.Errorf("approval events = %d, want 1", n)
	}
}

func TestApprove_Rejections(t *testing.T) {
	tests := []struct {
		name     string
		arrange  func(t *testing.T, e fixtureEnv, c campaign.Campaign) campaign.Campaign
		approval func(campaign.Approval) campaign.Approval
		want     campaign.ErrorCode
	}{
		{name: "stale version", approval: func(a campaign.Approval) campaign.Approval { a.Version--; return a },
			want: campaign.CodeVersionConflict},
		{name: "hash mismatch", approval: func(a campaign.Approval) campaign.Approval {
			a.ContentHash = strings.Repeat("a", 64)
			return a
		}, want: campaign.CodeContentChanged},
		{name: "content edited after review", arrange: func(t *testing.T, e fixtureEnv, c campaign.Campaign) campaign.Campaign {
			edited := e.edit(t, c, func(ed *campaign.Edit) { ed.Drafts.SMS.Body = "Changed" })
			edited.Version = c.Version // reviewer still holds the old version and hash
			edited.ContentHash = c.ContentHash
			return edited
		}, want: campaign.CodeVersionConflict},
		{name: "unknown variable", arrange: func(t *testing.T, e fixtureEnv, c campaign.Campaign) campaign.Campaign {
			return e.edit(t, c, func(ed *campaign.Edit) { ed.Drafts.SMS.Body = "Hi {{phone}}" })
		}, want: campaign.CodeInvalidContent},
		{name: "over worst-case limit", arrange: func(t *testing.T, e fixtureEnv, c campaign.Campaign) campaign.Campaign {
			return e.edit(t, c, func(ed *campaign.Edit) { ed.Drafts.Email.Subject = strings.Repeat("ş", 81) })
		}, want: campaign.CodeInvalidContent},
		{name: "over limit for the longest real first name", arrange: func(t *testing.T, e fixtureEnv, c campaign.Campaign) campaign.Campaign {
			// 130 + 30 fits the worst case (first_name 30) but not a 60-rune name.
			c = e.edit(t, c, func(ed *campaign.Edit) {
				ed.Drafts.SMS.Body = "{{first_name}}" + strings.Repeat("a", 130)
			})
			aud := defaultAudience()
			sms := aud[content.ChannelSMS]
			sms.Longest = map[content.Variable]int{content.VarFirstName: 60, content.VarCity: 8}
			aud[content.ChannelSMS] = sms
			e.store.Audience = campaigntest.NewAudience(aud)
			return c
		}, want: campaign.CodeInvalidContent},
		{name: "no channel selected", arrange: func(t *testing.T, e fixtureEnv, c campaign.Campaign) campaign.Campaign {
			return e.edit(t, c, func(ed *campaign.Edit) { ed.Channels = []string{} })
		}, want: campaign.CodeInvalidContent},
		{name: "no consented recipients", arrange: func(_ *testing.T, e fixtureEnv, c campaign.Campaign) campaign.Campaign {
			e.store.Audience = campaigntest.NewAudience(nil)
			return c
		}, want: campaign.CodeNoRecipients},
		{name: "generation in progress", arrange: func(t *testing.T, e fixtureEnv, c campaign.Campaign) campaign.Campaign {
			if _, err := e.svc.BeginGeneration(t.Context(), c.ID); err != nil {
				t.Fatal(err)
			}
			return c
		}, want: campaign.CodeGenerationInProgress},
		{name: "empty approver", approval: func(a campaign.Approval) campaign.Approval { a.ApprovedBy = " "; return a },
			want: campaign.CodeInvalidRequest},
		{name: "approver with control characters", approval: func(a campaign.Approval) campaign.Approval {
			a.ApprovedBy = "x\ny"
			return a
		}, want: campaign.CodeInvalidRequest},
		{name: "approver too long", approval: func(a campaign.Approval) campaign.Approval {
			a.ApprovedBy = strings.Repeat("a", campaign.MaxApproverRunes+1)
			return a
		}, want: campaign.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			c := e.ready(t)
			if tt.arrange != nil {
				c = tt.arrange(t, e, c)
			}
			a := approvalOf(c)
			if tt.approval != nil {
				a = tt.approval(a)
			}
			_, err := e.svc.Approve(t.Context(), c.ID, a)
			requireCode(t, err, tt.want)
			got, _ := e.svc.Get(t.Context(), c.ID)
			if got.Status == campaign.StatusApproved || got.ApprovedBy != "" {
				t.Errorf("rejected approval changed the campaign: %+v", got)
			}
		})
	}
}

func TestApprove_WrongState(t *testing.T) {
	e := newEnv(t)
	c := e.create(t)
	_, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c))
	requireCode(t, err, campaign.CodeInvalidState)
	_, err = e.svc.Approve(t.Context(), "00000000-0000-4000-8000-999999999999", approvalOf(c))
	requireCode(t, err, campaign.CodeNotFound)
}

func TestApprove_UnselectedChannelIsNotChecked(t *testing.T) {
	e := newEnv(t)
	c := e.edit(t, e.ready(t), func(ed *campaign.Edit) {
		ed.Channels = []string{"email"}
		ed.Drafts.SMS.Body = "Hi {{phone}}"
	})
	if got := e.approve(t, c); got.Status != campaign.StatusApproved {
		t.Errorf("status = %s", got.Status)
	}
}

func TestApprove_NoCodePathFromGeneration(t *testing.T) {
	// Generation, including a model trying to approve via an extra field,
	// never yields an approved campaign.
	e := newEnv(t, mockllm.Step{}, mockllm.Step{Fault: mockllm.FaultUnknownField})
	c := e.ready(t)
	if c.Status != campaign.StatusDraftsReady || c.ApprovedBy != "" {
		t.Errorf("campaign after generation = %+v", c)
	}
}

func TestApprove_PlansDeliveriesInTheSameWrite(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	e.approve(t, c)
	plans := e.store.Plans(c.ID)
	if len(plans) != 1 {
		t.Fatalf("delivery plans = %d, want exactly one with the approval", len(plans))
	}
	p := plans[0]
	if p.MaxAttempts != 4 || len(p.Channels) != len(c.Channels) || p.Predicate.SQL == "" {
		t.Errorf("plan = %+v, want the campaign's channels, 4 attempts, a compiled predicate", p)
	}
	for i, ch := range c.Channels {
		if p.Channels[i] != ch {
			t.Errorf("plan channels = %v, want %v", p.Channels, c.Channels)
		}
	}
}

func TestApprove_RejectedApprovalPlansNothing(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	stale := approvalOf(c)
	stale.Version--
	_, err := e.svc.Approve(t.Context(), c.ID, stale)
	requireCode(t, err, campaign.CodeVersionConflict)
	if plans := e.store.Plans(c.ID); len(plans) != 0 {
		t.Errorf("rejected approval planned deliveries: %+v", plans)
	}
}

func TestApprove_IdempotentAfterSendingStarted(t *testing.T) {
	for _, status := range []campaign.Status{campaign.StatusSending, campaign.StatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			e := newEnv(t)
			c := e.ready(t)
			approved := e.approve(t, c)
			approved.Status = status
			e.store.Put(approved)

			again, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c))
			if err != nil {
				t.Fatalf("re-approve error = %v", err)
			}
			if again.Status != status || len(e.store.Plans(c.ID)) != 1 {
				t.Errorf("re-approval changed state: status %s, plans %d", again.Status, len(e.store.Plans(c.ID)))
			}
			other := approvalOf(c)
			other.ContentHash = strings.Repeat("1", 64)
			_, err = e.svc.Approve(t.Context(), c.ID, other)
			requireCode(t, err, campaign.CodeContentChanged)
		})
	}
}

func TestApprovedStatusesAreImmutable(t *testing.T) {
	for _, status := range []campaign.Status{campaign.StatusSending, campaign.StatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			e := newEnv(t)
			c := e.approve(t, e.ready(t))
			c.Status = status
			e.store.Put(c)
			if !c.HasDrafts() || !c.Approved() {
				t.Errorf("%s campaign: HasDrafts=%v Approved=%v, want both", status, c.HasDrafts(), c.Approved())
			}
			_, err := e.svc.Edit(t.Context(), c.ID, campaign.Edit{Version: c.Version, Drafts: c.Drafts, Channels: []string{"sms"}})
			requireCode(t, err, campaign.CodeInvalidState)
			_, err = e.svc.BeginGeneration(t.Context(), c.ID)
			requireCode(t, err, campaign.CodeInvalidState)
		})
	}
}
