package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
)

func TestRelease(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewRelease(reg)

	// Every outcome series exists from the start, so rates and alerts on
	// expired or failed releases are defined before the first one.
	want := `
# HELP campaign_releases_total Scheduled campaign releases by outcome (released, release_failed, expired).
# TYPE campaign_releases_total counter
campaign_releases_total{outcome="expired"} 0
campaign_releases_total{outcome="release_failed"} 0
campaign_releases_total{outcome="released"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "campaign_releases_total"); err != nil {
		t.Error(err)
	}

	m.CampaignReleased(campaign.ReleaseReleased, 2*time.Second)
	m.CampaignReleased(campaign.ReleaseReleased, 4*time.Second)
	m.CampaignReleased(campaign.ReleaseExpired, 20*time.Minute)
	m.CampaignReleased(campaign.ReleaseOutcome("bogus"), time.Second) // never becomes a label

	want = `
# HELP campaign_releases_total Scheduled campaign releases by outcome (released, release_failed, expired).
# TYPE campaign_releases_total counter
campaign_releases_total{outcome="expired"} 1
campaign_releases_total{outcome="release_failed"} 0
campaign_releases_total{outcome="released"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "campaign_releases_total"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(m.lateness); n != 1 {
		t.Errorf("lateness series = %d, want 1 (no labels)", n)
	}
	h := &dtoHistogram{}
	h.read(t, reg, "campaign_release_lateness_seconds")
	if h.count != 3 || h.sum != 2+4+1200 {
		t.Errorf("lateness count = %d sum = %v, want 3 and 1206", h.count, h.sum)
	}
}

type dtoHistogram struct {
	count uint64
	sum   float64
}

func (h *dtoHistogram) read(t *testing.T, reg *prometheus.Registry, name string) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			hist := mf.GetMetric()[0].GetHistogram()
			h.count, h.sum = hist.GetSampleCount(), hist.GetSampleSum()
			return
		}
	}
	t.Fatalf("metric %s not found", name)
}
