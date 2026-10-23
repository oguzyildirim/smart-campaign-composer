// Package content defines campaign message content: SMS and email drafts,
// the {{variable}} personalization syntax, and the channel limits content
// must respect. Drafts usually come from an LLM, so every rule here treats
// them as untrusted input.
//
// Lengths are counted in Unicode code points (runes), so a Turkish letter
// such as "ş" counts once even though it takes two bytes. Real SMS networks
// count differently: any character outside the GSM-7 alphabet (ş, ğ, ı, İ,
// emoji, ...) switches the whole message to UCS-2, where one segment holds
// 70 characters instead of 160. The 160 limit here is the product rule
// from the case specification, not a guarantee of single-segment delivery.
//
// The package has no HTTP, database, or LLM dependencies.
package content

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Variable is an allowlisted personalization variable.
type Variable string

// Allowlisted variables. Anything else inside {{ }} is rejected.
const (
	VarFirstName Variable = "first_name"
	VarCity      Variable = "city"
)

// Worst-case lengths, in runes, used to check limits before the real
// audience is known. The longest seeded first name is 30 runes and the
// longest seeded city is 9; per-recipient rendering re-checks real values
// before delivery.
const (
	MaxFirstNameRunes = 30
	MaxCityRunes      = 20
)

var variableMaxRunes = map[Variable]int{
	VarFirstName: MaxFirstNameRunes,
	VarCity:      MaxCityRunes,
}

// Template errors. Messages are fixed strings so they never echo content.
var (
	ErrMalformedPlaceholder = errors.New("malformed placeholder; write variables exactly as {{first_name}} or {{city}}")
	ErrUnknownVariable      = errors.New("unknown variable; allowed: {{first_name}}, {{city}}")
)

// placeholderName is the syntax inside {{ }}: lowercase letters and
// underscores, no spaces, no expressions.
var placeholderName = regexp.MustCompile(`^[a-z_]+$`)

// Template is parsed message text: literal runs and variable references.
type Template struct {
	literalRunes int
	vars         []Variable // in order of appearance, with repeats
	parts        []part
}

// part is a literal run or, when v is set, a variable reference.
type part struct {
	literal string
	v       Variable
}

// ParseTemplate parses s strictly. A "{{" must be followed by an
// allowlisted variable name and "}}"; a stray "}}" is also rejected.
// Single braces are ordinary text.
func ParseTemplate(s string) (Template, error) {
	var t Template
	rest := s
	for {
		open := strings.Index(rest, "{{")
		closing := strings.Index(rest, "}}")
		if open < 0 {
			if closing >= 0 {
				return Template{}, ErrMalformedPlaceholder
			}
			t.addLiteral(rest)
			return t, nil
		}
		if closing >= 0 && closing < open {
			return Template{}, ErrMalformedPlaceholder
		}
		t.addLiteral(rest[:open])

		after := rest[open+2:]
		end := strings.Index(after, "}}")
		if end < 0 {
			return Template{}, ErrMalformedPlaceholder
		}
		name := after[:end]
		if !placeholderName.MatchString(name) {
			return Template{}, ErrMalformedPlaceholder
		}
		v := Variable(name)
		if _, ok := variableMaxRunes[v]; !ok {
			return Template{}, ErrUnknownVariable
		}
		t.vars = append(t.vars, v)
		t.parts = append(t.parts, part{v: v})
		rest = after[end+2:]
	}
}

func (t *Template) addLiteral(s string) {
	if s == "" {
		return
	}
	t.literalRunes += utf8.RuneCountInString(s)
	t.parts = append(t.parts, part{literal: s})
}

// Render substitutes values for variables. A variable without a value
// renders as empty text.
func (t Template) Render(values map[Variable]string) string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.v != "" {
			b.WriteString(values[p.v])
			continue
		}
		b.WriteString(p.literal)
	}
	return b.String()
}

// RunesWith is the rendered length when each variable takes the length
// given in lengths (0 when absent).
func (t Template) RunesWith(lengths map[Variable]int) int {
	n := t.literalRunes
	for _, v := range t.vars {
		n += lengths[v]
	}
	return n
}

// WorstCaseRunes is the rendered length when every variable takes its
// maximum length.
func (t Template) WorstCaseRunes() int {
	return t.RunesWith(variableMaxRunes)
}

// Variables returns the distinct variables used, in order of first use.
func (t Template) Variables() []Variable {
	var out []Variable
	for _, v := range t.vars {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
