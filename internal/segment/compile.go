package segment

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Predicate is a SQL boolean expression over the users table, which the
// caller must alias as "u". SQL contains only constant fragments and
// $1..$n placeholders; every rule value is in Args.
type Predicate struct {
	SQL  string
	Args []any
}

// fragments maps each field to a constant SQL template. Each %s is
// replaced with a placeholder ($n) and nothing else. Day windows are
// bounded on both sides by explicit timestamps so results depend only on
// the reference time passed to Compile, never on the database clock.
var fragments = map[Field]string{
	// city_key() folds case and Turkish letters on both sides; see
	// migrations/000002.
	FieldCity:   `city_key(u.city) IN (SELECT city_key(v) FROM unnest(%s::text[]) AS v)`,
	FieldLocale: `u.locale = ANY (%s::text[])`,
	FieldAddedToCartWithinDays: `EXISTS (SELECT 1 FROM cart_events ce WHERE ce.user_id = u.id` +
		` AND ce.added_at >= %s AND ce.added_at <= %s)`,
	FieldNoPurchaseWithinDays: `NOT EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id` +
		` AND o.created_at >= %s AND o.created_at <= %s)`,
	FieldPurchasedWithinDays: `EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id` +
		` AND o.created_at >= %s AND o.created_at <= %s)`,
	FieldOrderCount: `(SELECT count(*) FROM orders o WHERE o.user_id = u.id) %s %s`,
}

// comparisons maps order_count operators to constant SQL operators.
var comparisons = map[Op]string{OpGte: ">=", OpLte: "<="}

const day = 24 * time.Hour

// Compile translates a rule returned by Parse into a parameterized SQL
// predicate. now is the reference time for day windows: "within N days"
// means the closed interval [now - N*24h, now]. Compile returns an
// error only for a rule that did not come from Parse.
func Compile(rule Rule, now time.Time) (Predicate, error) {
	if len(rule.Conditions) == 0 {
		return Predicate{}, fmt.Errorf("compile segment: rule has no conditions")
	}
	b := &builder{}
	clauses := make([]string, 0, len(rule.Conditions))
	for _, c := range rule.Conditions {
		clause, err := b.condition(c, now)
		if err != nil {
			return Predicate{}, fmt.Errorf("compile segment: %w", err)
		}
		clauses = append(clauses, "("+clause+")")
	}
	return Predicate{SQL: strings.Join(clauses, " AND "), Args: b.args}, nil
}

type builder struct {
	args []any
}

// arg records a value and returns its placeholder.
func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

func (b *builder) condition(c Condition, now time.Time) (string, error) {
	tmpl, ok := fragments[c.Field]
	if !ok {
		return "", fmt.Errorf("unknown field %q", c.Field)
	}
	switch c.Field {
	case FieldCity, FieldLocale:
		if len(c.Strings) == 0 {
			return "", fmt.Errorf("%s has no values", c.Field)
		}
		return fmt.Sprintf(tmpl, b.arg(c.Strings)), nil
	case FieldAddedToCartWithinDays, FieldNoPurchaseWithinDays, FieldPurchasedWithinDays:
		from := now.Add(-time.Duration(c.Int) * day)
		return fmt.Sprintf(tmpl, b.arg(from), b.arg(now)), nil
	case FieldOrderCount:
		cmp, ok := comparisons[c.Op]
		if !ok {
			return "", fmt.Errorf("unsupported operator %q for %s", c.Op, c.Field)
		}
		return fmt.Sprintf(tmpl, cmp, b.arg(c.Int)), nil
	default:
		return "", fmt.Errorf("no compiler for field %q", c.Field)
	}
}
