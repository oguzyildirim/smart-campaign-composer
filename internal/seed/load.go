package seed

import (
	"context"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey serializes concurrent seed runs (e.g. two `compose up`
// invocations racing). The value is arbitrary but fixed.
const advisoryLockKey int64 = 0x5eed_0001

// Result reports what Load did.
type Result struct {
	// Inserted is false when the database already held users and the
	// load was skipped.
	Inserted   bool
	Users      int
	Consents   int
	CartEvents int
	Orders     int
}

// Load inserts the dataset in a single transaction. It is idempotent: if
// the users table is not empty, nothing is written. Partial loads cannot
// happen because everything commits together.
func Load(ctx context.Context, pool *pgxpool.Pool, ds Dataset) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockKey); err != nil {
			return fmt.Errorf("acquire seed lock: %w", err)
		}
		var seeded bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&seeded); err != nil {
			return fmt.Errorf("check existing users: %w", err)
		}
		if seeded {
			return nil
		}

		ids, err := copyUsers(ctx, tx, ds.Users)
		if err != nil {
			return err
		}
		res, err = copyActivity(ctx, tx, ds.Users, ids)
		if err != nil {
			return err
		}
		res.Inserted = true
		res.Users = len(ds.Users)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("load seed data: %w", err)
	}
	return res, nil
}

// copyUsers bulk-inserts users and returns their ids in dataset order.
func copyUsers(ctx context.Context, tx pgx.Tx, users []User) ([]int64, error) {
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"users"},
		[]string{"first_name", "last_name", "email", "phone", "city", "locale", "created_at"},
		pgx.CopyFromSlice(len(users), func(i int) ([]any, error) {
			u := users[i]
			return []any{u.FirstName, u.LastName, u.Email, u.Phone, u.City, u.Locale, u.CreatedAt}, nil
		}))
	if err != nil {
		return nil, fmt.Errorf("copy users: %w", err)
	}

	rows, err := tx.Query(ctx, `SELECT id, email FROM users`)
	if err != nil {
		return nil, fmt.Errorf("read user ids: %w", err)
	}
	byEmail := make(map[string]int64, len(users))
	var (
		id    int64
		email string
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &email}, func() error {
		byEmail[email] = id
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read user ids: %w", err)
	}

	ids := make([]int64, len(users))
	for i, u := range users {
		id, ok := byEmail[u.Email]
		if !ok {
			return nil, fmt.Errorf("user %d missing after copy", i)
		}
		ids[i] = id
	}
	return ids, nil
}

func copyActivity(ctx context.Context, tx pgx.Tx, users []User, ids []int64) (Result, error) {
	var consents, carts, orders [][]any
	for i, u := range users {
		for _, c := range u.Consents {
			consents = append(consents, []any{ids[i], c.Channel, c.Granted, c.UpdatedAt})
		}
		for _, e := range u.CartEvents {
			carts = append(carts, []any{ids[i], e.ProductSKU, e.AddedAt})
		}
		for _, o := range u.Orders {
			amount := pgtype.Numeric{Int: big.NewInt(o.TotalCents), Exp: -2, Valid: true}
			orders = append(orders, []any{ids[i], amount, o.CreatedAt})
		}
	}

	tables := []struct {
		name string
		cols []string
		rows [][]any
	}{
		{"channel_consents", []string{"user_id", "channel", "granted", "updated_at"}, consents},
		{"cart_events", []string{"user_id", "product_sku", "added_at"}, carts},
		{"orders", []string{"user_id", "total_amount", "created_at"}, orders},
	}
	for _, t := range tables {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{t.name}, t.cols, pgx.CopyFromRows(t.rows)); err != nil {
			return Result{}, fmt.Errorf("copy %s: %w", t.name, err)
		}
	}
	return Result{Consents: len(consents), CartEvents: len(carts), Orders: len(orders)}, nil
}
