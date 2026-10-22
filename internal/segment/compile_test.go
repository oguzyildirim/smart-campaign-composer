package segment

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var refTime = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func mustParse(t *testing.T, s string) Rule {
	t.Helper()
	rule, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse(%s) error = %v", s, err)
	}
	return rule
}

func TestCompile_ExampleRule(t *testing.T) {
	rule := mustParse(t, `{"version":1,"conditions":[
	  {"field":"city","op":"eq","value":"Istanbul"},
	  {"field":"added_to_cart_within_days","op":"eq","value":30},
	  {"field":"no_purchase_within_days","op":"eq","value":30}
	]}`)
	pred, err := Compile(rule, refTime)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	wantSQL := `(city_key(u.city) IN (SELECT city_key(v) FROM unnest($1::text[]) AS v))` +
		` AND (EXISTS (SELECT 1 FROM cart_events ce WHERE ce.user_id = u.id AND ce.added_at >= $2 AND ce.added_at <= $3))` +
		` AND (NOT EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id AND o.created_at >= $4 AND o.created_at <= $5))`
	if pred.SQL != wantSQL {
		t.Errorf("SQL =\n%s\nwant\n%s", pred.SQL, wantSQL)
	}
	from := refTime.Add(-30 * 24 * time.Hour)
	wantArgs := []any{[]string{"Istanbul"}, from, refTime, from, refTime}
	if !reflect.DeepEqual(pred.Args, wantArgs) {
		t.Errorf("args = %#v, want %#v", pred.Args, wantArgs)
	}
}

func TestCompile_EveryField(t *testing.T) {
	tests := []struct {
		cond     string
		contains string
		args     []any
	}{
		{`{"field":"city","op":"in","value":["A","B"]}`, "unnest($1::text[])", []any{[]string{"A", "B"}}},
		{`{"field":"locale","op":"eq","value":"en"}`, "u.locale = ANY ($1::text[])", []any{[]string{"en"}}},
		{`{"field":"purchased_within_days","op":"eq","value":7}`, "EXISTS (SELECT 1 FROM orders o",
			[]any{refTime.Add(-7 * 24 * time.Hour), refTime}},
		{`{"field":"order_count","op":"gte","value":3}`, "WHERE o.user_id = u.id) >= $1", []any{3}},
		{`{"field":"order_count","op":"lte","value":0}`, "WHERE o.user_id = u.id) <= $1", []any{0}},
	}
	for _, tt := range tests {
		t.Run(tt.cond, func(t *testing.T) {
			pred, err := Compile(mustParse(t, `{"version":1,"conditions":[`+tt.cond+`]}`), refTime)
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			if !strings.Contains(pred.SQL, tt.contains) {
				t.Errorf("SQL %q does not contain %q", pred.SQL, tt.contains)
			}
			if !reflect.DeepEqual(pred.Args, tt.args) {
				t.Errorf("args = %#v, want %#v", pred.Args, tt.args)
			}
		})
	}
}

// TestCompile_ValuesNeverReachSQLText compiles rules whose values are
// hostile and checks that the SQL text is made only of constant fragments
// and placeholders. Parse rejects these city names, so the rules are
// built directly to prove the compiler does not depend on that.
func TestCompile_ValuesNeverReachSQLText(t *testing.T) {
	hostile := []string{
		"Istanbul'; DROP TABLE users;--",
		`x' OR '1'='1`,
		"$99",
		"); DELETE FROM orders; --",
	}
	rule := Rule{Version: 1, Conditions: []Condition{
		{Field: FieldCity, Op: OpIn, Strings: hostile},
		{Field: FieldLocale, Op: OpEq, Strings: []string{"tr' OR 1=1 --"}},
		{Field: FieldOrderCount, Op: OpGte, Int: 2},
	}}
	pred, err := Compile(rule, refTime)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	for _, s := range append(hostile, "tr' OR 1=1 --") {
		if strings.Contains(pred.SQL, s) {
			t.Errorf("SQL contains value %q: %s", s, pred.SQL)
		}
	}
	if strings.ContainsAny(pred.SQL, "';-") {
		t.Errorf("SQL contains quote, semicolon, or comment characters: %s", pred.SQL)
	}
	placeholders := regexp.MustCompile(`\$\d+`).FindAllString(pred.SQL, -1)
	if len(placeholders) != len(pred.Args) {
		t.Errorf("%d placeholders for %d args", len(placeholders), len(pred.Args))
	}
	if !reflect.DeepEqual(pred.Args[0], hostile) {
		t.Errorf("first arg = %#v, want the hostile values unchanged", pred.Args[0])
	}
}

func TestCompile_PlaceholdersAreSequential(t *testing.T) {
	rule := mustParse(t, `{"version":1,"conditions":[
	  {"field":"city","op":"eq","value":"A"},
	  {"field":"locale","op":"eq","value":"tr"},
	  {"field":"added_to_cart_within_days","op":"eq","value":1},
	  {"field":"no_purchase_within_days","op":"eq","value":2},
	  {"field":"purchased_within_days","op":"eq","value":3},
	  {"field":"order_count","op":"gte","value":4}
	]}`)
	pred, err := Compile(rule, refTime)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	got := regexp.MustCompile(`\$\d+`).FindAllString(pred.SQL, -1)
	for i, p := range got {
		if want := "$" + strconv.Itoa(i+1); p != want {
			t.Fatalf("placeholder %d = %s, want %s (all: %v)", i, p, want, got)
		}
	}
	if len(got) != len(pred.Args) || len(got) != 9 {
		t.Errorf("got %d placeholders and %d args, want 9 each", len(got), len(pred.Args))
	}
}

func TestCompile_RejectsRulesNotFromParse(t *testing.T) {
	bad := []Rule{
		{Version: 1},
		{Version: 1, Conditions: []Condition{{Field: "email", Op: OpEq, Strings: []string{"x"}}}},
		{Version: 1, Conditions: []Condition{{Field: FieldCity, Op: OpEq}}},
		{Version: 1, Conditions: []Condition{{Field: FieldOrderCount, Op: OpEq, Int: 1}}},
	}
	for _, r := range bad {
		if _, err := Compile(r, refTime); err == nil {
			t.Errorf("Compile(%+v) = nil error", r)
		}
	}
}

type fakeCounter struct {
	got    *Predicate
	counts Counts
	err    error
}

func (f *fakeCounter) CountAudience(_ context.Context, p Predicate) (Counts, error) {
	f.got = &p
	return f.counts, f.err
}

func TestService_Preview(t *testing.T) {
	counter := &fakeCounter{counts: Counts{Total: 10, SMS: 7, Email: 4}}
	svc := NewService(counter, func() time.Time { return refTime })

	rule, counts, err := svc.Preview(t.Context(), []byte(`{"version":1,"conditions":[{"field":"added_to_cart_within_days","op":"eq","value":30}]}`))
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if counts != counter.counts || len(rule.Conditions) != 1 {
		t.Errorf("Preview() = %+v, %+v", rule, counts)
	}
	if counter.got == nil || counter.got.Args[1] != refTime {
		t.Errorf("counter predicate = %+v, want reference time from the injected clock", counter.got)
	}
}

func TestService_PreviewInvalidRuleNeverReachesCounter(t *testing.T) {
	counter := &fakeCounter{}
	svc := NewService(counter, func() time.Time { return refTime })

	_, _, err := svc.Preview(t.Context(), []byte(`{"version":1,"conditions":[{"field":"city","op":"eq","value":"x'; DROP TABLE users;--"}]}`))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Preview() error = %v, want *ValidationError", err)
	}
	if counter.got != nil {
		t.Error("counter was called for an invalid rule")
	}
}

func TestService_PreviewWrapsCounterError(t *testing.T) {
	cause := errors.New("connection refused")
	svc := NewService(&fakeCounter{err: cause}, func() time.Time { return refTime })

	_, _, err := svc.Preview(t.Context(), []byte(`{"version":1,"conditions":[{"field":"locale","op":"eq","value":"tr"}]}`))
	if !errors.Is(err, cause) {
		t.Errorf("Preview() error = %v, want wrapped %v", err, cause)
	}
	var verr *ValidationError
	if errors.As(err, &verr) {
		t.Error("counter failure reported as a validation error")
	}
}
