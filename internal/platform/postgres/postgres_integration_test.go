//go:build integration

package postgres_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/health"
	"github.com/oguzyildirim/smart-campaign-composer/internal/platform/postgres"
	"github.com/oguzyildirim/smart-campaign-composer/internal/testutil/pgtest"
)

func TestMigrationsApplyAndReadinessIsHealthy(t *testing.T) {
	dsn := pgtest.Start(t)

	if version := pgtest.Migrate(t, dsn); version < 1 {
		t.Fatalf("migration version = %d, want >= 1", version)
	}

	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	// The baseline migration is observable in the catalog.
	var comment string
	if err := pool.QueryRow(t.Context(),
		`SELECT obj_description('public'::regnamespace, 'pg_namespace')`).Scan(&comment); err != nil {
		t.Fatalf("read schema comment: %v", err)
	}
	if comment == "" {
		t.Error("baseline migration did not set the schema comment")
	}

	readiness := health.NewReadiness(map[string]health.Checker{
		"postgres": postgres.NewChecker(pool),
	}, 5*time.Second, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	readiness.ReadinessHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	var body health.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness body: %v", err)
	}
	if rec.Code != http.StatusOK || body.Checks["postgres"] != health.StatusOK {
		t.Errorf("readiness = %d %+v, want 200 with postgres ok", rec.Code, body)
	}
}

func TestMigrations_DownAndUpAgain(t *testing.T) {
	dsn := pgtest.Start(t)
	pgtest.Migrate(t, dsn)

	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	tables := []string{"users", "channel_consents", "cart_events", "orders"}
	exists := func(name string) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
			t.Fatalf("check %s: %v", name, err)
		}
		return ok
	}
	for _, name := range tables {
		if !exists(name) {
			t.Errorf("table %s missing after up", name)
		}
	}

	pgtest.MigrateDown(t, dsn)
	for _, name := range tables {
		if exists(name) {
			t.Errorf("table %s still exists after down", name)
		}
	}
	var fn bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regproc('city_key') IS NOT NULL`).Scan(&fn); err != nil || fn {
		t.Errorf("city_key function after down: exists=%v err=%v", fn, err)
	}

	if version := pgtest.Migrate(t, dsn); version < 2 {
		t.Errorf("re-applied version = %d, want >= 2", version)
	}
}

func TestSchema_RejectsRealContactDataAndBadEnums(t *testing.T) {
	dsn := pgtest.Start(t)
	pgtest.Migrate(t, dsn)
	pool, err := postgres.NewPool(t.Context(), dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	const insertUser = `INSERT INTO users (first_name, last_name, email, phone, city, locale)
		VALUES ('Test', 'User', $1, $2, 'İstanbul', $3) RETURNING id`
	var id int64
	if err := pool.QueryRow(t.Context(), insertUser, "ok@example.com", "+9995000000001", "tr").Scan(&id); err != nil {
		t.Fatalf("valid user rejected: %v", err)
	}

	bad := []struct {
		name                 string
		email, phone, locale string
	}{
		{"real email domain", "someone@gmail.com", "+9995000000002", "tr"},
		{"lookalike domain", "someone@example.com.evil.io", "+9995000000003", "tr"},
		{"real phone number", "a@example.com", "+905321234567", "tr"},
		{"unsupported locale", "b@example.com", "+9995000000004", "de"},
		{"duplicate email", "ok@example.com", "+9995000000005", "tr"},
		{"duplicate phone", "c@example.com", "+9995000000001", "tr"},
	}
	for _, tt := range bad {
		if _, err := pool.Exec(t.Context(), insertUser, tt.email, tt.phone, tt.locale); err == nil {
			t.Errorf("%s: insert succeeded, want constraint violation", tt.name)
		}
	}

	if _, err := pool.Exec(t.Context(),
		`INSERT INTO channel_consents (user_id, channel, granted) VALUES ($1, 'whatsapp', true)`, id); err == nil {
		t.Error("consent for unsupported channel accepted")
	}
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO cart_events (user_id, product_sku, added_at) VALUES (999999, 'SKU', now())`); err == nil {
		t.Error("cart event for missing user accepted")
	}
}
