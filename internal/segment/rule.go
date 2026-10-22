// Package segment defines the audience segment rule language: a small,
// allowlisted JSON DSL that is parsed strictly, validated, and compiled to
// parameterized SQL. Rules are untrusted input (later milestones receive
// them from an LLM), so no part of a rule is ever interpolated into SQL
// text: field and operator names select constant fragments, and values
// travel only as query arguments.
//
// The package has no HTTP or database driver dependencies.
package segment

import (
	"encoding/json"
	"fmt"
)

// Version is the only rule format version currently accepted.
const Version = 1

// Field identifies an allowlisted attribute a condition can filter on.
type Field string

// Allowlisted fields. The order of this list is the canonical order of
// conditions in a normalized rule.
const (
	FieldCity                  Field = "city"
	FieldLocale                Field = "locale"
	FieldAddedToCartWithinDays Field = "added_to_cart_within_days"
	FieldNoPurchaseWithinDays  Field = "no_purchase_within_days"
	FieldPurchasedWithinDays   Field = "purchased_within_days"
	FieldOrderCount            Field = "order_count"
)

// Op is a comparison operator.
type Op string

// Supported operators. Which operators a field accepts is defined by the
// field specs in validate.go.
const (
	OpEq  Op = "eq"
	OpIn  Op = "in"
	OpGte Op = "gte"
	OpLte Op = "lte"
)

// Rule is a validated segment rule. Its conditions are ANDed. Construct
// rules with Parse; a zero Rule is not valid.
type Rule struct {
	Version    int         `json:"version"`
	Conditions []Condition `json:"conditions"`
}

// Condition is one validated filter. Exactly one of Strings (city,
// locale) or Int (day windows, order count) is meaningful, depending on
// the field.
type Condition struct {
	Field   Field
	Op      Op
	Strings []string
	Int     int
}

type conditionJSON struct {
	Field Field `json:"field"`
	Op    Op    `json:"op"`
	Value any   `json:"value"`
}

// MarshalJSON renders the condition in the same shape Parse accepts:
// "eq" on a string field has a string value, "in" has an array, and
// integer fields have a number.
func (c Condition) MarshalJSON() ([]byte, error) {
	out := conditionJSON{Field: c.Field, Op: c.Op}
	spec, ok := fieldSpecs[c.Field]
	if !ok {
		return nil, fmt.Errorf("marshal condition: unknown field %q", c.Field)
	}
	switch {
	case spec.kind == kindInt:
		out.Value = c.Int
	case c.Op == OpIn:
		out.Value = c.Strings
	case len(c.Strings) == 1:
		out.Value = c.Strings[0]
	default:
		return nil, fmt.Errorf("marshal condition: %s %s needs exactly one value", c.Field, c.Op)
	}
	return json.Marshal(out)
}
