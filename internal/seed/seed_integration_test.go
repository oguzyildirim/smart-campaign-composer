//go:build integration

package seed_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/seed"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/pgtest"
)

var seedNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func startDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := pgtest.Start(t)
	pgtest.Migrate(t, dsn)
	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// dbChecksum hashes the user-visible content of every table in a stable
// order, independent of generated ids.
func dbChecksum(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var sum string
	err := pool.QueryRow(t.Context(), `
		SELECT md5(string_agg(row_text, E'\n' ORDER BY row_text)) FROM (
			SELECT concat_ws('|', 'u', email, phone, first_name, last_name, city, locale, created_at) AS row_text FROM users
			UNION ALL
			SELECT concat_ws('|', 'c', u.email, c.channel, c.granted, c.updated_at)
				FROM channel_consents c JOIN users u ON u.id = c.user_id
			UNION ALL
			SELECT concat_ws('|', 'e', u.email, e.product_sku, e.added_at)
				FROM cart_events e JOIN users u ON u.id = e.user_id
			UNION ALL
			SELECT concat_ws('|', 'o', u.email, o.total_amount, o.created_at)
				FROM orders o JOIN users u ON u.id = o.user_id
		) rows`).Scan(&sum)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	return sum
}

func TestLoad_IdempotentAndDeterministic(t *testing.T) {
	pool := startDB(t)
	ds := seed.Generate(seed.Options{UserCount: 500, RandomSeed: 7, Now: seedNow})

	first, err := seed.Load(t.Context(), pool, ds)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	if !first.Inserted || first.Users != 500 || first.CartEvents == 0 || first.Orders == 0 || first.Consents == 0 {
		t.Fatalf("first Load = %+v", first)
	}
	before := dbChecksum(t, pool)

	// A second run, even with a later clock, writes nothing.
	again := seed.Generate(seed.Options{UserCount: 500, RandomSeed: 7, Now: seedNow.Add(time.Hour)})
	second, err := seed.Load(t.Context(), pool, again)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if second.Inserted {
		t.Errorf("second Load inserted data: %+v", second)
	}
	if after := dbChecksum(t, pool); after != before {
		t.Error("database changed after the second Load")
	}

	// The same options load identical content into a fresh database.
	other := startDB(t)
	if _, err := seed.Load(t.Context(), other, ds); err != nil {
		t.Fatalf("Load into second database: %v", err)
	}
	if got := dbChecksum(t, other); got != before {
		t.Errorf("same seed produced different database content: %s vs %s", got, before)
	}
}

// TestExampleSegmentMatchesIndependentSQL compares the compiled segment
// against a query written separately (joins and interval arithmetic
// instead of EXISTS and Go-computed bounds) over the full default seed.
func TestExampleSegmentMatchesIndependentSQL(t *testing.T) {
	pool := startDB(t)
	ds := seed.Generate(seed.Options{UserCount: 5000, RandomSeed: 20261022, Now: seedNow})
	if _, err := seed.Load(t.Context(), pool, ds); err != nil {
		t.Fatalf("Load: %v", err)
	}

	svc := segment.NewService(postgres.NewAudienceRepository(pool), func() time.Time { return seedNow })
	_, got, err := svc.Preview(t.Context(), []byte(`{"version":1,"conditions":[
		{"field":"city","op":"eq","value":"Istanbul"},
		{"field":"added_to_cart_within_days","op":"eq","value":30},
		{"field":"no_purchase_within_days","op":"eq","value":30}
	]}`))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	var want segment.Counts
	err = pool.QueryRow(t.Context(), `
		WITH target AS (
			SELECT DISTINCT u.id
			FROM users u
			JOIN cart_events ce ON ce.user_id = u.id
			WHERE u.city = 'İstanbul'
			  AND ce.added_at BETWEEN $1::timestamptz - interval '30 days' AND $1::timestamptz
			  AND u.id NOT IN (
			      SELECT o.user_id FROM orders o
			      WHERE o.created_at BETWEEN $1::timestamptz - interval '30 days' AND $1::timestamptz)
		)
		SELECT count(*),
		       count(*) FILTER (WHERE sms.granted IS TRUE),
		       count(*) FILTER (WHERE em.granted IS TRUE)
		FROM target t
		LEFT JOIN channel_consents sms ON sms.user_id = t.id AND sms.channel = 'sms'
		LEFT JOIN channel_consents em  ON em.user_id  = t.id AND em.channel  = 'email'`,
		seedNow).Scan(&want.Total, &want.SMS, &want.Email)
	if err != nil {
		t.Fatalf("independent query: %v", err)
	}

	if got != want {
		t.Errorf("segment counts = %+v, independent SQL = %+v", got, want)
	}
	if want.Total < 100 || want.SMS >= want.Total || want.Email >= want.Total {
		t.Errorf("seed does not exercise the example segment well: %+v", want)
	}
	t.Logf("example segment over 5000 seeded users: %+v", got)
}
