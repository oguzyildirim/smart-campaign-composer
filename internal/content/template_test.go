package content

import (
	"errors"
	"slices"
	"testing"
)

func TestParseTemplate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   error
		wantRunes int // worst case, when valid
		wantVars  []Variable
	}{
		{name: "plain text", input: "Hello", wantRunes: 5},
		{name: "empty", input: "", wantRunes: 0},
		{name: "first_name counted at max", input: "Hi {{first_name}}!", wantRunes: 4 + MaxFirstNameRunes, wantVars: []Variable{VarFirstName}},
		{name: "city counted at max", input: "{{city}}", wantRunes: MaxCityRunes, wantVars: []Variable{VarCity}},
		{name: "repeated variable counted twice", input: "{{first_name}} {{first_name}}", wantRunes: 2*MaxFirstNameRunes + 1, wantVars: []Variable{VarFirstName}},
		{name: "both variables", input: "{{first_name}}/{{city}}", wantRunes: MaxFirstNameRunes + 1 + MaxCityRunes, wantVars: []Variable{VarFirstName, VarCity}},
		{name: "turkish letters are one rune each", input: "Şişli'de güzel İğne", wantRunes: 19},
		{name: "single braces are text", input: "a {b} c", wantRunes: 7},
		{name: "trailing single brace after placeholder", input: "{{city}}}", wantRunes: MaxCityRunes + 1, wantVars: []Variable{VarCity}},

		{name: "unknown variable", input: "Hi {{last_name}}", wantErr: ErrUnknownVariable},
		{name: "unknown email variable", input: "{{email}}", wantErr: ErrUnknownVariable},
		{name: "spaces inside placeholder", input: "Hi {{ first_name }}", wantErr: ErrMalformedPlaceholder},
		{name: "uppercase name", input: "Hi {{First_Name}}", wantErr: ErrMalformedPlaceholder},
		{name: "unclosed placeholder", input: "Hi {{first_name", wantErr: ErrMalformedPlaceholder},
		{name: "stray closing braces", input: "Hi first_name}}", wantErr: ErrMalformedPlaceholder},
		{name: "closing before opening", input: "}} {{city}}", wantErr: ErrMalformedPlaceholder},
		{name: "triple braces", input: "{{{first_name}}}", wantErr: ErrMalformedPlaceholder},
		{name: "empty placeholder", input: "{{}}", wantErr: ErrMalformedPlaceholder},
		{name: "template syntax", input: "{{.FirstName}}", wantErr: ErrMalformedPlaceholder},
		{name: "expression", input: "{{first_name | upper}}", wantErr: ErrMalformedPlaceholder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := ParseTemplate(tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseTemplate(%q) error = %v, want %v", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTemplate(%q) error = %v", tt.input, err)
			}
			if got := tmpl.WorstCaseRunes(); got != tt.wantRunes {
				t.Errorf("WorstCaseRunes() = %d, want %d", got, tt.wantRunes)
			}
			if got := tmpl.Variables(); !slices.Equal(got, tt.wantVars) {
				t.Errorf("Variables() = %v, want %v", got, tt.wantVars)
			}
		})
	}
}
