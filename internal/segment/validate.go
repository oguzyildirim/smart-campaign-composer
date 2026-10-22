package segment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Structural limits of a rule.
const (
	MinConditions  = 1
	MaxConditions  = 10
	MaxCityValues  = 20
	MaxCityLength  = 64
	MinWindowDays  = 1
	MaxWindowDays  = 365
	MinOrderCount  = 0
	MaxOrderCount  = 10000
	maxLocaleCount = 2
)

// FieldError describes one problem with a rule. Path locates it in the
// submitted JSON, e.g. "conditions[1].value". Messages never echo
// submitted values, so they are safe to return to clients and to feed
// back to an LLM.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError lists every problem found in a rejected rule.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.Path + ": " + fe.Message
	}
	return "invalid segment rule: " + strings.Join(parts, "; ")
}

type valueKind int

const (
	kindCity valueKind = iota + 1
	kindLocale
	kindInt
)

type fieldSpec struct {
	ops      []Op
	kind     valueKind
	min, max int // kindInt only
}

// fieldSpecs is the allowlist. A field, operator, or value shape that is
// not described here is rejected.
var fieldSpecs = map[Field]fieldSpec{
	FieldCity:                  {ops: []Op{OpEq, OpIn}, kind: kindCity},
	FieldLocale:                {ops: []Op{OpEq, OpIn}, kind: kindLocale},
	FieldAddedToCartWithinDays: {ops: []Op{OpEq}, kind: kindInt, min: MinWindowDays, max: MaxWindowDays},
	FieldNoPurchaseWithinDays:  {ops: []Op{OpEq}, kind: kindInt, min: MinWindowDays, max: MaxWindowDays},
	FieldPurchasedWithinDays:   {ops: []Op{OpEq}, kind: kindInt, min: MinWindowDays, max: MaxWindowDays},
	FieldOrderCount:            {ops: []Op{OpGte, OpLte}, kind: kindInt, min: MinOrderCount, max: MaxOrderCount},
}

// fieldOrder is the canonical condition order of a normalized rule.
var fieldOrder = []Field{
	FieldCity, FieldLocale, FieldAddedToCartWithinDays,
	FieldNoPurchaseWithinDays, FieldPurchasedWithinDays, FieldOrderCount,
}

var locales = []string{"tr", "en"}

// jsonInteger matches a JSON number literal without fraction or exponent.
var jsonInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// Parse decodes and validates a rule from untrusted JSON and returns it
// in normalized form: conditions in canonical field order, city names
// trimmed and de-duplicated case-insensitively, locales de-duplicated.
//
// Decoding is strict: unknown or duplicate keys, trailing data, wrong
// JSON types, and non-integer numbers are all rejected. Any problem is
// reported as a *ValidationError.
func Parse(data []byte) (Rule, error) {
	v := &validator{}
	rule := v.parse(data)
	if len(v.errs) > 0 {
		return Rule{}, &ValidationError{Errors: v.errs}
	}
	slices.SortFunc(rule.Conditions, func(a, b Condition) int {
		return slices.Index(fieldOrder, a.Field) - slices.Index(fieldOrder, b.Field)
	})
	return rule, nil
}

type validator struct {
	errs []FieldError
}

func (v *validator) fail(path, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) parse(data []byte) Rule {
	if err := checkSyntax(data); err != nil {
		v.fail("$", "%s", err)
		return Rule{}
	}
	top, ok := v.object("$", data, "version", "conditions")
	if !ok {
		return Rule{}
	}

	var rule Rule
	if raw, ok := v.required(top, "$", "version"); ok {
		if n, ok := v.integer("version", raw); ok {
			if n != Version {
				v.fail("version", "unsupported version; must be %d", Version)
			}
			rule.Version = n
		}
	}

	raw, ok := v.required(top, "$", "conditions")
	if !ok {
		return rule
	}
	var items []json.RawMessage
	if jsonKind(raw) != '[' || json.Unmarshal(raw, &items) != nil {
		v.fail("conditions", "must be an array")
		return rule
	}
	if len(items) < MinConditions || len(items) > MaxConditions {
		v.fail("conditions", "must contain between %d and %d conditions", MinConditions, MaxConditions)
		return rule
	}

	seen := make(map[Field]bool, len(items))
	for i, item := range items {
		path := fmt.Sprintf("conditions[%d]", i)
		c, ok := v.condition(path, item)
		if !ok {
			continue
		}
		if seen[c.Field] {
			v.fail(path+".field", "duplicate field; each field may appear only once")
			continue
		}
		seen[c.Field] = true
		rule.Conditions = append(rule.Conditions, c)
	}
	return rule
}

func (v *validator) condition(path string, data json.RawMessage) (Condition, bool) {
	obj, ok := v.object(path, data, "field", "op", "value")
	if !ok {
		return Condition{}, false
	}
	rawField, okField := v.required(obj, path, "field")
	rawOp, okOp := v.required(obj, path, "op")
	rawValue, okValue := v.required(obj, path, "value")
	if !okField || !okOp || !okValue {
		return Condition{}, false
	}

	name, ok := v.str(path+".field", rawField)
	if !ok {
		return Condition{}, false
	}
	field := Field(name)
	spec, ok := fieldSpecs[field]
	if !ok {
		v.fail(path+".field", "unsupported field; allowed: %s", allowedFields())
		return Condition{}, false
	}

	opName, ok := v.str(path+".op", rawOp)
	if !ok {
		return Condition{}, false
	}
	op := Op(opName)
	if !slices.Contains(spec.ops, op) {
		v.fail(path+".op", "unsupported operator for %s; allowed: %s", field, joinOps(spec.ops))
		return Condition{}, false
	}

	c := Condition{Field: field, Op: op}
	valuePath := path + ".value"
	switch spec.kind {
	case kindInt:
		n, ok := v.integer(valuePath, rawValue)
		if !ok {
			return Condition{}, false
		}
		if n < spec.min || n > spec.max {
			v.fail(valuePath, "must be between %d and %d", spec.min, spec.max)
			return Condition{}, false
		}
		c.Int = n
	case kindCity, kindLocale:
		values, ok := v.stringValues(valuePath, op, rawValue)
		if !ok {
			return Condition{}, false
		}
		if spec.kind == kindCity {
			c.Strings, ok = v.cities(valuePath, values)
		} else {
			c.Strings, ok = v.locales(valuePath, values)
		}
		if !ok {
			return Condition{}, false
		}
	}
	return c, true
}

// stringValues returns the value as a list: "eq" takes one string and
// "in" takes a non-empty array of strings.
func (v *validator) stringValues(path string, op Op, raw json.RawMessage) ([]string, bool) {
	if op == OpEq {
		s, ok := v.str(path, raw)
		return []string{s}, ok
	}
	var items []json.RawMessage
	if jsonKind(raw) != '[' || json.Unmarshal(raw, &items) != nil {
		v.fail(path, "must be an array of strings for op in")
		return nil, false
	}
	if len(items) == 0 {
		v.fail(path, "must contain at least one value")
		return nil, false
	}
	out := make([]string, 0, len(items))
	ok := true
	for i, item := range items {
		s, itemOK := v.str(fmt.Sprintf("%s[%d]", path, i), item)
		ok = ok && itemOK
		out = append(out, s)
	}
	return out, ok
}

func (v *validator) cities(path string, values []string) ([]string, bool) {
	if len(values) > MaxCityValues {
		v.fail(path, "must contain at most %d cities", MaxCityValues)
		return nil, false
	}
	out := make([]string, 0, len(values))
	ok := true
	for i, raw := range values {
		name := strings.TrimSpace(raw)
		itemPath := path
		if len(values) > 1 {
			itemPath = fmt.Sprintf("%s[%d]", path, i)
		}
		switch n := utf8.RuneCountInString(name); {
		case n == 0 || n > MaxCityLength:
			v.fail(itemPath, "city must be 1 to %d characters", MaxCityLength)
			ok = false
			continue
		case !validCityName(name):
			v.fail(itemPath, "city may contain only letters, spaces, hyphens, and periods")
			ok = false
			continue
		}
		if !slices.ContainsFunc(out, func(s string) bool { return strings.EqualFold(s, name) }) {
			out = append(out, name)
		}
	}
	return out, ok
}

func (v *validator) locales(path string, values []string) ([]string, bool) {
	out := make([]string, 0, maxLocaleCount)
	ok := true
	for i, s := range values {
		if !slices.Contains(locales, s) {
			itemPath := path
			if len(values) > 1 {
				itemPath = fmt.Sprintf("%s[%d]", path, i)
			}
			v.fail(itemPath, "locale must be one of: %s", strings.Join(locales, ", "))
			ok = false
			continue
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, ok
}

// validCityName allows letters (any script, including Turkish), combining
// marks, spaces, hyphens, and periods. Quotes, semicolons, digits, and
// control characters are rejected outright; values are parameterized
// anyway, but nothing legitimate needs them.
func validCityName(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsMark(r) && r != ' ' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

// object decodes data as a JSON object and reports keys outside allowed.
// It returns false only when data is not an object, so validation of the
// allowed keys continues and every problem is reported at once.
func (v *validator) object(path string, data json.RawMessage, allowed ...string) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if jsonKind(data) != '{' || json.Unmarshal(data, &obj) != nil {
		v.fail(path, "must be a JSON object")
		return nil, false
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys) // deterministic error order
	for _, k := range keys {
		if !slices.Contains(allowed, k) {
			v.fail(joinPath(path, k), "unknown field; allowed: %s", strings.Join(allowed, ", "))
		}
	}
	return obj, true
}

// required returns obj[key], reporting it at parent.key when it is
// missing or null.
func (v *validator) required(obj map[string]json.RawMessage, parent, key string) (json.RawMessage, bool) {
	raw, ok := obj[key]
	if !ok || jsonKind(raw) == 'n' {
		v.fail(joinPath(parent, key), "is required")
		return nil, false
	}
	return raw, true
}

func (v *validator) str(path string, raw json.RawMessage) (string, bool) {
	var s string
	if jsonKind(raw) != '"' || json.Unmarshal(raw, &s) != nil {
		v.fail(path, "must be a string")
		return "", false
	}
	return s, true
}

func (v *validator) integer(path string, raw json.RawMessage) (int, bool) {
	lit := string(bytes.TrimSpace(raw))
	if !jsonInteger.MatchString(lit) {
		v.fail(path, "must be an integer")
		return 0, false
	}
	n, err := strconv.Atoi(lit)
	if err != nil {
		v.fail(path, "is out of range")
		return 0, false
	}
	return n, true
}

// jsonKind returns the first byte of a JSON value: '{', '[', '"', 'n'
// (null), 't'/'f' (booleans), or a digit/'-' for numbers; 0 if empty.
func jsonKind(raw json.RawMessage) byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0
	}
	return raw[0]
}

func joinPath(path, key string) string {
	if path == "$" {
		return key
	}
	return path + "." + key
}

func allowedFields() string {
	names := make([]string, len(fieldOrder))
	for i, f := range fieldOrder {
		names[i] = string(f)
	}
	return strings.Join(names, ", ")
}

func joinOps(ops []Op) string {
	names := make([]string, len(ops))
	for i, op := range ops {
		names[i] = string(op)
	}
	return strings.Join(names, ", ")
}

// errDuplicateKey is reported when an object repeats a key. encoding/json
// silently keeps the last occurrence, which would let a rule show one
// value to a reviewer and apply another.
var errDuplicateKey = errors.New("duplicate object key")

// checkSyntax verifies that data is exactly one well-formed JSON value
// with no duplicate object keys and nothing after it.
func checkSyntax(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walkValue(dec); err != nil {
		if errors.Is(err, errDuplicateKey) {
			return err
		}
		return errors.New("malformed JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after JSON value")
	}
	return nil
}

func walkValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar
	}
	switch delim {
	case '{':
		keys := make(map[string]bool)
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := keyTok.(string)
			if keys[key] {
				return errDuplicateKey
			}
			keys[key] = true
			if err := walkValue(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkValue(dec); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // closing delimiter
	return err
}
