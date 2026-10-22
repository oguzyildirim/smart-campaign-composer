//go:build integration

package postgres_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/pgtest"
)

var refNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

// consent states for fixture users. "" means no consent row at all.
const (
	granted = "granted"
	denied  = "denied"
)

type fixtureUser struct {
	city, locale string
	sms, email   string
	cartAgo      []time.Duration
	orderAgo     []time.Duration
}

// fixture is a small dataset with hand-computed expectations for the
// example rule (Istanbul, cart within 30 days, no purchase within 30):
//
//	#   city       consent sms/email  cart   order  example rule
//	1   İstanbul   granted/granted     2d     -      match
//	2   istanbul   granted/denied     29d     -      match (lower-case data)
//	3   ISTANBUL   -/-                 1d     -      match (no consent rows)
//	4   İstanbul   -/granted          10d     -      match (locale en)
//	5   İstanbul   granted/granted    10d     3d     cart then purchased
//	6   İstanbul   granted/granted     5d    45d     match (old order)
//	7   İstanbul   granted/granted    31d     -      cart outside window
//	8   Ankara     granted/granted     2d     -      other city
//	9   İstanbul   granted/granted    -1d     -      cart after reference time
//	10  İstanbul   granted/granted    30d     -      match (window boundary)
var fixture = []fixtureUser{
	{city: "İstanbul", locale: "tr", sms: granted, email: granted, cartAgo: []time.Duration{2 * day}},
	{city: "istanbul", locale: "tr", sms: granted, email: denied, cartAgo: []time.Duration{29 * day}},
	{city: "ISTANBUL", locale: "tr", cartAgo: []time.Duration{1 * day}},
	{city: "İstanbul", locale: "en", email: granted, cartAgo: []time.Duration{10 * day}},
	{city: "İstanbul", locale: "tr", sms: granted, email: granted, cartAgo: []time.Duration{10 * day}, orderAgo: []time.Duration{3 * day}},
	{city: "İstanbul", locale: "tr", sms: granted, email: granted, cartAgo: []time.Duration{5 * day}, orderAgo: []time.Duration{45 * day}},
	{city: "İstanbul", locale: "tr", sms: granted, email: granted, cartAgo: []time.Duration{31 * day}},
	{city: "Ankara", locale: "tr", sms: granted, email: granted, cartAgo: []time.Duration{2 * day}},
	{city: "İstanbul", locale: "tr", sms: granted, email: granted, cartAgo: []time.Duration{-1 * day}},
	{city: "İstanbul", locale: "tr", sms: granted, email: granted, cartAgo: []time.Duration{30 * day}},
}

func loadFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	for i, u := range fixture {
		var id int64
		err := pool.QueryRow(ctx, `INSERT INTO users (first_name, last_name, email, phone, city, locale)
			VALUES ('Test', 'User', $1, $2, $3, $4) RETURNING id`,
			fmt.Sprintf("user%d@example.com", i+1), fmt.Sprintf("+9995%09d", i+1), u.city, u.locale).Scan(&id)
		if err != nil {
			t.Fatalf("insert user %d: %v", i+1, err)
		}
		for channel, state := range map[string]string{"sms": u.sms, "email": u.email} {
			if state == "" {
				continue
			}
			if _, err := pool.Exec(ctx, `INSERT INTO channel_consents (user_id, channel, granted) VALUES ($1, $2, $3)`,
				id, channel, state == granted); err != nil {
				t.Fatalf("insert consent: %v", err)
			}
		}
		for _, ago := range u.cartAgo {
			if _, err := pool.Exec(ctx, `INSERT INTO cart_events (user_id, product_sku, added_at) VALUES ($1, 'SKU-1', $2)`,
				id, refNow.Add(-ago)); err != nil {
				t.Fatalf("insert cart event: %v", err)
			}
		}
		for _, ago := range u.orderAgo {
			if _, err := pool.Exec(ctx, `INSERT INTO orders (user_id, total_amount, created_at) VALUES ($1, 100, $2)`,
				id, refNow.Add(-ago)); err != nil {
				t.Fatalf("insert order: %v", err)
			}
		}
	}
}

func TestAudienceRepository_CountsFixture(t *testing.T) {
	dsn := pgtest.Start(t)
	pgtest.Migrate(t, dsn)
	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	loadFixture(t, pool)

	svc := segment.NewService(postgres.NewAudienceRepository(pool), func() time.Time { return refNow })

	tests := []struct {
		name  string
		conds string
		want  segment.Counts
	}{
		{"example rule",
			`{"field":"city","op":"eq","value":"Istanbul"},
			 {"field":"added_to_cart_within_days","op":"eq","value":30},
			 {"field":"no_purchase_within_days","op":"eq","value":30}`,
			// Users 1,2,3,4,6,10. SMS: 1,2,6,10. Email: 1,4,6,10.
			segment.Counts{Total: 6, SMS: 4, Email: 4}},
		{"city is case and Turkish-letter insensitive",
			`{"field":"city","op":"eq","value":"İSTANBUL"}`,
			segment.Counts{Total: 9, SMS: 7, Email: 7}},
		{"city in",
			`{"field":"city","op":"in","value":["ankara","istanbul"]}`,
			segment.Counts{Total: 10, SMS: 8, Email: 8}},
		{"unknown city",
			`{"field":"city","op":"eq","value":"İzmir"}`,
			segment.Counts{}},
		{"locale",
			`{"field":"locale","op":"eq","value":"en"}`,
			segment.Counts{Total: 1, SMS: 0, Email: 1}},
		{"purchased within days",
			`{"field":"purchased_within_days","op":"eq","value":7}`,
			segment.Counts{Total: 1, SMS: 1, Email: 1}},
		{"order count gte",
			`{"field":"order_count","op":"gte","value":1}`,
			segment.Counts{Total: 2, SMS: 2, Email: 2}},
		{"order count lte",
			`{"field":"order_count","op":"lte","value":0}`,
			segment.Counts{Total: 8, SMS: 6, Email: 6}},
		{"future cart events are outside every window",
			`{"field":"added_to_cart_within_days","op":"eq","value":365}`,
			segment.Counts{Total: 9, SMS: 7, Email: 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, err := svc.Preview(t.Context(), []byte(`{"version":1,"conditions":[`+tt.conds+`]}`))
			if err != nil {
				t.Fatalf("Preview() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("counts = %+v, want %+v", got, tt.want)
			}
		})
	}

	// Values that bypass Parse still travel only as parameters.
	t.Run("hostile values stay parameters", func(t *testing.T) {
		pred, err := segment.Compile(segment.Rule{Version: 1, Conditions: []segment.Condition{{
			Field: segment.FieldCity, Op: segment.OpIn,
			Strings: []string{"x'; DROP TABLE users;--", "' OR '1'='1"},
		}}}, refNow)
		if err != nil {
			t.Fatalf("Compile() error = %v", err)
		}
		got, err := postgres.NewAudienceRepository(pool).CountAudience(t.Context(), pred)
		if err != nil || got != (segment.Counts{}) {
			t.Errorf("CountAudience() = %+v, %v; want zero counts", got, err)
		}
		var n int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM users`).Scan(&n); err != nil || n != len(fixture) {
			t.Errorf("users after hostile query = %d, %v; want %d", n, err, len(fixture))
		}
	})
}
