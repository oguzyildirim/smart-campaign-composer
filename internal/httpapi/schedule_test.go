package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign/campaigntest"
)

// istanbul is the marketer's offset in the examples.
var istanbul = time.FixedZone("+03", 3*3600)

func (f apiFixture) scheduled(t *testing.T, at time.Time) campaign.Campaign {
	t.Helper()
	c := f.ready(t)
	out, err := f.svc.Approve(t.Context(), c.ID, campaign.Approval{
		Version: c.Version, ContentHash: c.ContentHash, ApprovedBy: "Deniz Operator", ScheduledAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func heldBody(t *testing.T, c campaign.Campaign, mutate func(map[string]any)) string {
	t.Helper()
	body := map[string]any{"version": c.Version, "content_hash": c.ContentHash, "actor": "Ece Planner"}
	if mutate != nil {
		mutate(body)
	}
	return jsonBody(t, body)
}

func TestApproveCampaign_Scheduled(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.ready(t)
	path := "/campaigns/" + string(c.ID) + "/approve"
	at := testNow.Add(48 * time.Hour).In(istanbul)

	for _, bad := range []any{"2026-10-24T15:00:00", "2026-10-24 15:00:00+03:00", "next friday", testNow.Add(-time.Hour).Format(time.RFC3339),
		testNow.Add(31 * 24 * time.Hour).Format(time.RFC3339)} {
		body := requireError(t, f.do(http.MethodPost, path, approveBody(t, c, func(b map[string]any) { b["scheduled_at"] = bad })),
			http.StatusBadRequest, "invalid_request")
		if len(body.Details) != 1 || body.Details[0].Path != "scheduled_at" {
			t.Errorf("scheduled_at %v: details = %+v", bad, body.Details)
		}
	}
	requireError(t, f.do(http.MethodPost, path, approveBody(t, c, func(b map[string]any) { b["scheduled_at"] = 1761300000 })),
		http.StatusBadRequest, "invalid_request")
	// An explicit null is a client bug, not "send now": rejected, so a
	// schedule can never turn into an immediate send.
	body := requireError(t, f.do(http.MethodPost, path, approveBody(t, c, func(b map[string]any) { b["scheduled_at"] = nil })),
		http.StatusBadRequest, "invalid_request")
	if len(body.Details) != 1 || body.Details[0].Path != "scheduled_at" {
		t.Errorf("null scheduled_at: details = %+v", body.Details)
	}
	if got, _ := f.svc.Get(t.Context(), c.ID); got.Status != campaign.StatusDraftsReady || len(f.store.Plans(c.ID)) != 0 {
		t.Fatalf("null scheduled_at changed the campaign: %s, %d plans", got.Status, len(f.store.Plans(c.ID)))
	}

	rec := f.do(http.MethodPost, path, approveBody(t, c, func(b map[string]any) { b["scheduled_at"] = at.Format(time.RFC3339) }))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decodeBody[CampaignResponse](t, rec)
	if got.Status != campaign.StatusScheduled || got.ScheduledAt == nil || !got.ScheduledAt.Equal(at) ||
		got.ApprovedBy == nil || got.ReleaseFailure != nil || got.Drafts == nil {
		t.Errorf("scheduled = %+v", got)
	}
	if n := len(f.store.Plans(c.ID)); n != 0 {
		t.Errorf("scheduled approval planned %d deliveries", n)
	}

	list := decodeBody[ListResponse](t, f.do(http.MethodGet, "/campaigns", ""))
	if len(list.Campaigns) != 1 || list.Campaigns[0].ScheduledAt == nil || !list.Campaigns[0].ScheduledAt.Equal(at) {
		t.Errorf("list = %+v", list.Campaigns)
	}
	// Not scheduled: the field is present and null.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(f.do(http.MethodGet, "/campaigns/"+string(f.ready(t).ID), "").Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"scheduled_at", "release_failure"} {
		if string(raw[key]) != "null" {
			t.Errorf("%s = %s, want null", key, raw[key])
		}
	}
}

func TestRescheduleCampaign(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.scheduled(t, testNow.Add(time.Hour))
	path := "/campaigns/" + string(c.ID) + "/reschedule"
	at := testNow.Add(72 * time.Hour).In(istanbul)
	with := func(m map[string]any) func(map[string]any) {
		return func(b map[string]any) {
			b["scheduled_at"] = at.Format(time.RFC3339)
			for k, v := range m {
				b[k] = v
			}
		}
	}

	requireError(t, f.do(http.MethodPost, path, heldBody(t, c, nil)), http.StatusBadRequest, "invalid_request") // no time
	requireError(t, f.do(http.MethodPost, path, heldBody(t, c, with(map[string]any{"approved_by": "x"}))), http.StatusBadRequest, "invalid_request")
	requireError(t, f.do(http.MethodPost, path, heldBody(t, c, with(map[string]any{"scheduled_at": "2026-10-30T10:00:00"}))),
		http.StatusBadRequest, "invalid_request")
	requireError(t, f.do(http.MethodPost, path, heldBody(t, c, with(map[string]any{"actor": ""}))), http.StatusBadRequest, "invalid_request")
	requireError(t, f.do(http.MethodPost, path, heldBody(t, c, with(map[string]any{"version": c.Version - 1}))), http.StatusConflict, "version_conflict")
	requireError(t, f.do(http.MethodPost, path, heldBody(t, c, with(map[string]any{"content_hash": "x"}))), http.StatusConflict, "content_changed")

	rec := f.do(http.MethodPost, path, heldBody(t, c, with(nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decodeBody[CampaignResponse](t, rec)
	if got.Status != campaign.StatusScheduled || got.ScheduledAt == nil || !got.ScheduledAt.Equal(at) || got.Version != c.Version+1 {
		t.Errorf("rescheduled = %+v", got)
	}

	// Once released, rescheduling fails clearly.
	ready := f.ready(t)
	requireError(t, f.do(http.MethodPost, "/campaigns/"+string(ready.ID)+"/reschedule", heldBody(t, ready, with(nil))),
		http.StatusConflict, "invalid_state")
}

func TestCancelCampaign(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.scheduled(t, testNow.Add(time.Hour))
	path := "/campaigns/" + string(c.ID) + "/cancel"

	requireError(t, f.do(http.MethodPost, path, `{"version":1}`), http.StatusBadRequest, "invalid_request")
	requireError(t, f.do(http.MethodPost, path, heldBody(t, c, func(b map[string]any) { b["scheduled_at"] = "2026-10-30T10:00:00Z" })),
		http.StatusBadRequest, "invalid_request")
	requireError(t, f.do(http.MethodPost, path, heldBody(t, c, func(b map[string]any) { b["version"] = c.Version + 1 })),
		http.StatusConflict, "version_conflict")

	rec := f.do(http.MethodPost, path, heldBody(t, c, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decodeBody[CampaignResponse](t, rec)
	if got.Status != campaign.StatusDraftsReady || got.ScheduledAt != nil || got.ApprovedBy != nil || got.ApprovedAt != nil {
		t.Errorf("canceled = %+v", got)
	}
	// Canceling again: nothing to cancel.
	requireError(t, f.do(http.MethodPost, path, heldBody(t, campaign.Campaign{Version: got.Version, ContentHash: got.ContentHash}, nil)),
		http.StatusConflict, "invalid_state")
}

func TestCampaignResponse_ReleaseFailure(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	at := testNow.Add(time.Hour)
	c := f.scheduled(t, at)
	f.store.Audience = campaigntest.NewAudience(nil) // nobody left at release
	release := campaign.NewService(nil, f.store, nil, campaign.ServiceConfig{
		Schedule: campaign.ScheduleConfig{ReleaseGrace: time.Hour},
	}, func() time.Time { return at }, slog.New(slog.DiscardHandler))
	if res, err := release.Release(t.Context(), c.ID); err != nil || res.Outcome != campaign.ReleaseFailed {
		t.Fatalf("Release() = %+v, %v", res, err)
	}

	got := decodeBody[CampaignResponse](t, f.do(http.MethodGet, "/campaigns/"+string(c.ID), ""))
	if got.Status != campaign.StatusReleaseFailed || got.ReleaseFailure == nil || got.ReleaseFailure.Code != "no_recipients" ||
		got.ReleaseFailure.Error == "" || got.ReleaseFailure.Details == nil || got.ScheduledAt == nil {
		t.Errorf("release_failed response = %+v (failure %+v)", got, got.ReleaseFailure)
	}
}

// TestNoReleaseEndpoint: release is the worker's action; no route offers it.
func TestNoReleaseEndpoint(t *testing.T) {
	f := newAPI(t, fixtureOptions{})
	c := f.scheduled(t, testNow.Add(time.Hour))
	for _, path := range []string{"/release", "/send", "/schedule"} {
		if rec := f.do(http.MethodPost, "/campaigns/"+string(c.ID)+path, `{}`); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want no such route", path, rec.Code)
		}
	}
	if got, _ := f.svc.Get(t.Context(), c.ID); got.Status != campaign.StatusScheduled || len(f.store.Plans(c.ID)) != 0 {
		t.Errorf("campaign changed: %s", got.Status)
	}
}
