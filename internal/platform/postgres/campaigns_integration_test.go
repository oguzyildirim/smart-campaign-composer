//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm/mockllm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/pgtest"
)

const exampleBrief = "Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun."

type campaignEnv struct {
	pool  *pgxpool.Pool
	store *postgres.CampaignStore
	svc   *campaign.Service
}

// newCampaignEnv migrates a fresh database, loads the audience fixture
// (see audience_integration_test.go), and wires the real service with the
// mock LLM and refNow as the clock.
func newCampaignEnv(t *testing.T) campaignEnv {
	t.Helper()
	dsn := pgtest.Start(t)
	pgtest.Migrate(t, dsn)
	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	loadFixture(t, pool)

	now := func() time.Time { return refNow }
	logger := slog.New(slog.DiscardHandler)
	audience := postgres.NewAudienceRepository(pool)
	gen := campaign.NewGenerator(mockllm.New(), segment.NewService(audience, now), campaign.GeneratorConfig{
		SegmentTimeout: 5 * time.Second, DraftsTimeout: 5 * time.Second, MaxAttempts: 2,
	}, logger)
	store := postgres.NewCampaignStore(pool)
	svc := campaign.NewService(gen, store, audience,
		campaign.ServiceConfig{GenerationTimeout: 10 * time.Second, PreviewSamples: 3}, now, logger)
	return campaignEnv{pool: pool, store: store, svc: svc}
}

func (e campaignEnv) ready(t *testing.T) campaign.Campaign {
	t.Helper()
	c, err := e.svc.Create(t.Context(), exampleBrief)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	g, err := e.svc.BeginGeneration(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("BeginGeneration() error = %v", err)
	}
	if c, err = g.Run(t.Context(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return c
}

func editOf(c campaign.Campaign, mutate func(*campaign.Edit)) campaign.Edit {
	ed := campaign.Edit{Version: c.Version, Drafts: c.Drafts}
	for _, ch := range c.Channels {
		ed.Channels = append(ed.Channels, string(ch))
	}
	mutate(&ed)
	return ed
}

func approvalOf(c campaign.Campaign) campaign.Approval {
	return campaign.Approval{Version: c.Version, ContentHash: c.ContentHash, ApprovedBy: "Deniz Operator"}
}

type eventRow struct {
	action, actor, from, to string
	version                 int
}

func events(t *testing.T, pool *pgxpool.Pool, id campaign.ID) []eventRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT action, actor, coalesce(from_status, ''), to_status, version
		FROM campaign_events WHERE campaign_id = $1 ORDER BY id`, string(id))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []eventRow
	for rows.Next() {
		var r eventRow
		if err := rows.Scan(&r.action, &r.actor, &r.from, &r.to, &r.version); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func requireCode(t *testing.T, err error, want campaign.ErrorCode) {
	t.Helper()
	if gerr, ok := errors.AsType[*campaign.Error](err); !ok || gerr.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

func TestCampaignStore_Lifecycle(t *testing.T) {
	e := newCampaignEnv(t)
	c := e.ready(t)

	got, err := e.svc.Get(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture: users 1,2,3,4,6,10 match; SMS 1,2,6,10; email 1,4,6,10.
	if got.Counts != (segment.Counts{Total: 6, SMS: 4, Email: 4}) || got.Status != campaign.StatusDraftsReady ||
		got.Drafts != mockllm.GenerateDrafts(exampleBrief) || got.ContentHash != campaign.ContentHash(got) ||
		!slices.Equal(got.Channels, []content.Channel{content.ChannelSMS, content.ChannelEmail}) {
		t.Errorf("stored campaign = %+v", got)
	}

	edited, err := e.svc.Edit(t.Context(), c.ID, editOf(got, func(ed *campaign.Edit) {
		ed.Drafts.SMS.Body = "Merhaba {{first_name}}, sepetin seni bekliyor!"
	}))
	if err != nil {
		t.Fatal(err)
	}
	approved, err := e.svc.Approve(t.Context(), c.ID, approvalOf(edited))
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if approved.Status != campaign.StatusApproved || approved.ApprovedBy != "Deniz Operator" ||
		!approved.ApprovedAt.Equal(refNow) || approved.Version != edited.Version+1 {
		t.Errorf("approved = %+v", approved)
	}

	want := []eventRow{
		{campaign.ActionCreated, campaign.ActorSystem, "", "draft", 1},
		{campaign.ActionAudienceProposed, campaign.ActorSystem, "draft", "audience_ready", 1},
		{campaign.ActionDraftsGenerated, campaign.ActorSystem, "audience_ready", "drafts_ready", 2},
		{campaign.ActionEdited, campaign.ActorMarketer, "drafts_ready", "drafts_ready", 3},
		{campaign.ActionApproved, "Deniz Operator", "drafts_ready", "approved", 4},
	}
	if got := events(t, e.pool, c.ID); !slices.Equal(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}

	list, err := e.svc.List(t.Context())
	if err != nil || len(list) != 1 || list[0].ID != c.ID {
		t.Errorf("List() = %+v, %v", list, err)
	}
	_, err = e.svc.Get(t.Context(), "00000000-0000-4000-8000-000000000000")
	requireCode(t, err, campaign.CodeNotFound)
}

func TestCampaignStore_OptimisticLock(t *testing.T) {
	e := newCampaignEnv(t)
	c := e.ready(t)

	// Concurrent edits of the same version: exactly one wins.
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		ok, stale int
	)
	for i := range 8 {
		wg.Go(func() {
			_, err := e.svc.Edit(t.Context(), c.ID, editOf(c, func(ed *campaign.Edit) {
				ed.Drafts.SMS.Body = strings.Repeat("x", i+1)
			}))
			mu.Lock()
			defer mu.Unlock()
			if gerr, isErr := errors.AsType[*campaign.Error](err); isErr && gerr.Code == campaign.CodeVersionConflict {
				stale++
			} else if err == nil {
				ok++
			} else {
				t.Errorf("Edit() error = %v", err)
			}
		})
	}
	wg.Wait()
	if ok != 1 || stale != 7 {
		t.Errorf("edits: %d succeeded, %d conflicted; want 1 and 7", ok, stale)
	}
	got, _ := e.svc.Get(t.Context(), c.ID)
	if got.Version != c.Version+1 {
		t.Errorf("version = %d, want %d", got.Version, c.Version+1)
	}

	// Approval with the pre-edit version is stale.
	_, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c))
	requireCode(t, err, campaign.CodeVersionConflict)
}

func TestCampaignStore_UpdateIsAtomic(t *testing.T) {
	e := newCampaignEnv(t)
	c := e.ready(t)
	before := len(events(t, e.pool, c.ID))

	// The event violates a CHECK constraint, so the campaign write in the
	// same transaction must roll back.
	_, err := e.store.Update(t.Context(), c.ID, func(_ context.Context, _ campaign.AudienceInspector, cur campaign.Campaign) (*campaign.Write, error) {
		cur.Status, cur.ApprovedBy, cur.ApprovedAt = campaign.StatusApproved, "x", refNow
		cur.Version++
		return &campaign.Write{Campaign: cur, Events: []campaign.Event{{Action: "bogus", Actor: "x", To: campaign.StatusApproved}}}, nil
	})
	if err == nil {
		t.Fatal("Update() succeeded with an invalid event")
	}
	got, _ := e.svc.Get(t.Context(), c.ID)
	if got.Status != campaign.StatusDraftsReady || got.ApprovedBy != "" || got.Version != c.Version {
		t.Errorf("campaign after failed update = %+v", got)
	}
	if n := len(events(t, e.pool, c.ID)); n != before {
		t.Errorf("events = %d, want %d", n, before)
	}
}

func TestCampaignStore_ConcurrentApprovalsRecordOne(t *testing.T) {
	e := newCampaignEnv(t)
	c := e.ready(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c)); err != nil {
				t.Errorf("Approve() error = %v", err)
			}
		})
	}
	wg.Wait()
	n := 0
	for _, ev := range events(t, e.pool, c.ID) {
		if ev.action == campaign.ActionApproved {
			n++
		}
	}
	if n != 1 {
		t.Errorf("approval events = %d, want 1", n)
	}
}

func TestCampaignStore_EventsAreAppendOnly(t *testing.T) {
	e := newCampaignEnv(t)
	c := e.ready(t)
	if _, err := e.pool.Exec(t.Context(), `UPDATE campaign_events SET actor = 'mallory' WHERE campaign_id = $1`, string(c.ID)); err == nil {
		t.Error("updating an audit event succeeded")
	}
}

func TestApprove_LongestRealFirstName(t *testing.T) {
	e := newCampaignEnv(t)
	// A matching Istanbul cart-abandoner with SMS consent and a 60-rune
	// name, longer than the 30-rune worst case drafts are checked with.
	longName := strings.Repeat("Ğ", 60)
	var id int64
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO users (first_name, last_name, email, phone, city, locale)
		VALUES ($1, 'Uzun', 'long@example.com', '+9995000000999', 'İstanbul', 'tr') RETURNING id`, longName).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO channel_consents (user_id, channel, granted) VALUES ($1, 'sms', true)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO cart_events (user_id, product_sku, added_at) VALUES ($1, 'SKU-9', $2)`, id, refNow.Add(-day)); err != nil {
		t.Fatal(err)
	}

	c := e.ready(t)
	// 30 + 130 = 160 passes the worst case; 60 + 130 = 190 does not fit.
	c, err := e.svc.Edit(t.Context(), c.ID, editOf(c, func(ed *campaign.Edit) {
		ed.Drafts.SMS.Body = "{{first_name}}" + strings.Repeat("a", 130)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if v := c.Validate(); !v.Valid {
		t.Fatalf("static validation failed: %+v", v)
	}

	p, err := e.svc.Preview(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	sms := p.Channels[0]
	if sms.Recipients != 5 || sms.Longest[content.VarFirstName] != 60 {
		t.Errorf("sms audience = %+v", sms)
	}
	if first := sms.Samples[0]; first.Recipient.UserID != id || first.Fields[0].Runes != 190 || !first.Fields[0].OverLimit {
		t.Errorf("first sample = %+v", first)
	}
	if p.Validation.Valid {
		t.Error("preview validation passed for an over-limit recipient")
	}

	_, err = e.svc.Approve(t.Context(), c.ID, approvalOf(c))
	requireCode(t, err, campaign.CodeInvalidContent)

	// Email only: its content fits every recipient, so approval succeeds.
	c, err = e.svc.Edit(t.Context(), c.ID, editOf(c, func(ed *campaign.Edit) { ed.Channels = []string{"email"} }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Approve(t.Context(), c.ID, approvalOf(c)); err != nil {
		t.Errorf("email-only Approve() error = %v", err)
	}
}

func TestInspectAudience_ConsentAndSamples(t *testing.T) {
	e := newCampaignEnv(t)
	rule, err := segment.Parse([]byte(`{"version":1,"conditions":[{"field":"city","op":"eq","value":"Istanbul"},
		{"field":"added_to_cart_within_days","op":"eq","value":30},{"field":"no_purchase_within_days","op":"eq","value":30}]}`))
	if err != nil {
		t.Fatal(err)
	}
	pred, err := segment.Compile(rule, refNow)
	if err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewAudienceRepository(e.pool)

	sms, err := repo.InspectAudience(t.Context(), pred, content.ChannelSMS, 3)
	if err != nil {
		t.Fatal(err)
	}
	if sms.Recipients != 4 || len(sms.Samples) != 3 || sms.Longest[content.VarCity] != len([]rune("İstanbul")) {
		t.Errorf("sms audience = %+v", sms)
	}

	// Users 3 (no consent rows) and 2 (email denied) never appear for email.
	email, err := repo.InspectAudience(t.Context(), pred, content.ChannelEmail, 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, r := range email.Samples {
		ids = append(ids, r.UserID)
	}
	slices.Sort(ids)
	if email.Recipients != 4 || !slices.Equal(ids, []int64{1, 4, 6, 10}) {
		t.Errorf("email audience = %d %v, want 4 [1 4 6 10]", email.Recipients, ids)
	}

	none, err := repo.InspectAudience(t.Context(), pred, content.ChannelSMS, 0)
	if err != nil || none.Samples != nil || none.Recipients != 4 {
		t.Errorf("no samples requested = %+v, %v", none, err)
	}
}
