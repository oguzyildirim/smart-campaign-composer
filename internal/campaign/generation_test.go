package campaign_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
)

func TestGeneration_StreamsAndStoresDrafts(t *testing.T) {
	e := newEnv(t)
	created := e.create(t)
	g, err := e.svc.BeginGeneration(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	streamed := map[campaign.DraftField]string{}
	c, err := g.Run(t.Context(), func(d campaign.FieldDelta) {
		if d.Attempt != 1 {
			t.Errorf("attempt = %d", d.Attempt)
		}
		streamed[d.Field] += d.Text
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := mockllm.GenerateDrafts(exampleBrief)
	if c.Drafts != want || c.Status != campaign.StatusDraftsReady || c.Version != 2 {
		t.Errorf("campaign = %+v", c)
	}
	if streamed[campaign.FieldSMSBody] != want.SMS.Body || streamed[campaign.FieldEmailSubject] != want.Email.Subject ||
		streamed[campaign.FieldEmailBody] != want.Email.Body {
		t.Errorf("streamed = %q", streamed)
	}
	if c.GenerationToken != "" || c.ContentHash != campaign.ContentHash(c) {
		t.Errorf("lease not cleared or hash stale: %+v", c)
	}
	if calls := e.llm.Calls(); len(calls) != 2 || !strings.Contains(calls[1].Brief, "sepetine") {
		t.Errorf("llm calls = %+v", calls)
	}
	events := e.store.Events(c.ID)
	if last := events[len(events)-1]; last.Action != campaign.ActionDraftsGenerated ||
		last.From != campaign.StatusAudienceReady || last.To != campaign.StatusDraftsReady {
		t.Errorf("last event = %+v", last)
	}
}

func TestGeneration_RepairAttemptRestartsStream(t *testing.T) {
	// Segment call normal; first drafts attempt has an unknown variable.
	e := newEnv(t, mockllm.Step{}, mockllm.Step{Fault: mockllm.FaultUnknownVariable})
	created := e.create(t)
	g, err := e.svc.BeginGeneration(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	byAttempt := map[int]string{}
	c, err := g.Run(t.Context(), func(d campaign.FieldDelta) {
		if d.Field == campaign.FieldSMSBody {
			byAttempt[d.Attempt] += d.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if byAttempt[1] != "Merhaba {{phone}}" || byAttempt[2] != c.Drafts.SMS.Body {
		t.Errorf("streamed by attempt = %v, stored %q", byAttempt, c.Drafts.SMS.Body)
	}
}

func TestBeginGeneration_OnePerCampaign(t *testing.T) {
	e := newEnv(t)
	c := e.create(t)
	if _, err := e.svc.BeginGeneration(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.BeginGeneration(t.Context(), c.ID)
	requireCode(t, err, campaign.CodeGenerationInProgress)

	// A lease left by a crashed process expires.
	e.clock.Advance(time.Minute)
	if _, err := e.svc.BeginGeneration(t.Context(), c.ID); err != nil {
		t.Errorf("BeginGeneration() after lease expiry error = %v", err)
	}
}

func TestBeginGeneration_StateAndLookup(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.BeginGeneration(t.Context(), "00000000-0000-4000-8000-999999999999")
	requireCode(t, err, campaign.CodeNotFound)

	c := e.approve(t, e.ready(t))
	_, err = e.svc.BeginGeneration(t.Context(), c.ID)
	requireCode(t, err, campaign.CodeInvalidState)
}

func TestGeneration_FailureKeepsStateAndReleasesLease(t *testing.T) {
	e := newEnv(t, mockllm.Step{}, mockllm.Step{Fault: mockllm.FaultAPIError})
	created := e.create(t)
	g, err := e.svc.BeginGeneration(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Run(t.Context(), nil)
	requireCode(t, err, campaign.CodeUpstreamUnavailable)

	c, _ := e.svc.Get(t.Context(), created.ID)
	if c.Status != campaign.StatusAudienceReady || c.Version != 1 || c.GenerationToken != "" {
		t.Errorf("campaign after failure = %+v", c)
	}
	if _, err := e.svc.BeginGeneration(t.Context(), created.ID); err != nil {
		t.Errorf("lease not released: %v", err)
	}
}

func TestGeneration_CancellationStopsLLMAndReleasesLease(t *testing.T) {
	e := newEnv(t, mockllm.Step{}, mockllm.Step{Fault: mockllm.FaultHang})
	created := e.create(t)
	g, err := e.svc.BeginGeneration(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := g.Run(ctx, nil)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	c, _ := e.svc.Get(t.Context(), created.ID)
	if c.Status != campaign.StatusAudienceReady || c.GenerationToken != "" {
		t.Errorf("campaign after cancel = %+v", c)
	}
}

func TestGeneration_RegenerateReplacesEdits(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	c = e.edit(t, c, func(ed *campaign.Edit) { ed.Drafts.SMS.Body = "Edited" })
	regen := e.generate(t, c.ID)
	if regen.Drafts.SMS.Body == "Edited" || regen.Version != c.Version+1 || regen.Status != campaign.StatusDraftsReady {
		t.Errorf("regenerated = %+v", regen)
	}
}

func TestGeneration_ExpiredLeaseDoesNotSave(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	g, err := e.svc.BeginGeneration(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The lease expires while the generation runs, which lets an edit in.
	e.clock.Advance(time.Minute)
	edited := e.edit(t, c, func(ed *campaign.Edit) { ed.Drafts.SMS.Body = "Edited after expiry" })

	_, err = g.Run(t.Context(), nil)
	requireCode(t, err, campaign.CodeInvalidState)
	got, _ := e.svc.Get(t.Context(), c.ID)
	if got.Drafts.SMS.Body != "Edited after expiry" || got.Version != edited.Version {
		t.Errorf("expired generation overwrote the edit: %+v", got)
	}
}
