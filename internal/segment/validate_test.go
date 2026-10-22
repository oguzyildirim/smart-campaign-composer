package segment

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse_AcceptsEveryAllowedFieldAndOp(t *testing.T) {
	tests := []struct {
		name string
		cond string
		want Condition
	}{
		{"city eq", `{"field":"city","op":"eq","value":"Istanbul"}`,
			Condition{Field: FieldCity, Op: OpEq, Strings: []string{"Istanbul"}}},
		{"city in", `{"field":"city","op":"in","value":["İstanbul","Ankara"]}`,
			Condition{Field: FieldCity, Op: OpIn, Strings: []string{"İstanbul", "Ankara"}}},
		{"locale eq", `{"field":"locale","op":"eq","value":"tr"}`,
			Condition{Field: FieldLocale, Op: OpEq, Strings: []string{"tr"}}},
		{"locale in", `{"field":"locale","op":"in","value":["tr","en"]}`,
			Condition{Field: FieldLocale, Op: OpIn, Strings: []string{"tr", "en"}}},
		{"added to cart", `{"field":"added_to_cart_within_days","op":"eq","value":30}`,
			Condition{Field: FieldAddedToCartWithinDays, Op: OpEq, Int: 30}},
		{"no purchase", `{"field":"no_purchase_within_days","op":"eq","value":1}`,
			Condition{Field: FieldNoPurchaseWithinDays, Op: OpEq, Int: 1}},
		{"purchased", `{"field":"purchased_within_days","op":"eq","value":365}`,
			Condition{Field: FieldPurchasedWithinDays, Op: OpEq, Int: 365}},
		{"order count gte", `{"field":"order_count","op":"gte","value":0}`,
			Condition{Field: FieldOrderCount, Op: OpGte, Int: 0}},
		{"order count lte", `{"field":"order_count","op":"lte","value":10000}`,
			Condition{Field: FieldOrderCount, Op: OpLte, Int: 10000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, err := Parse([]byte(`{"version":1,"conditions":[` + tt.cond + `]}`))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if len(rule.Conditions) != 1 || !reflect.DeepEqual(rule.Conditions[0], tt.want) {
				t.Errorf("conditions = %+v, want [%+v]", rule.Conditions, tt.want)
			}
		})
	}
}

func TestParse_NormalizesExampleRule(t *testing.T) {
	// Conditions arrive out of canonical order with padded, duplicated cities.
	in := `{
	  "conditions": [
	    {"field": "no_purchase_within_days", "op": "eq", "value": 30},
	    {"field": "city", "op": "in", "value": [" Istanbul ", "istanbul", "Ankara"]},
	    {"field": "added_to_cart_within_days", "op": "eq", "value": 30}
	  ],
	  "version": 1
	}`
	rule, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	got, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"version":1,"conditions":[` +
		`{"field":"city","op":"in","value":["Istanbul","Ankara"]},` +
		`{"field":"added_to_cart_within_days","op":"eq","value":30},` +
		`{"field":"no_purchase_within_days","op":"eq","value":30}]}`
	if string(got) != want {
		t.Errorf("normalized rule =\n%s\nwant\n%s", got, want)
	}

	// The normalized form is itself a valid rule and a fixed point.
	again, err := Parse(got)
	if err != nil || !reflect.DeepEqual(again, rule) {
		t.Errorf("re-parse = %+v, %v; want %+v", again, err, rule)
	}
}

func TestParse_Rejections(t *testing.T) {
	cond := func(c string) string { return `{"version":1,"conditions":[` + c + `]}` }
	tooMany := make([]string, MaxConditions+1)
	for i := range tooMany {
		tooMany[i] = `{"field":"city","op":"eq","value":"A"}`
	}
	manyCities := `"A"` + strings.Repeat(`,"A"`, MaxCityValues)

	tests := []struct {
		name     string
		input    string
		wantPath string
		wantMsg  string
	}{
		// Document structure.
		{"empty body", ``, "$", "malformed JSON"},
		{"not JSON", `DROP TABLE users`, "$", "malformed JSON"},
		{"array at top level", `[]`, "$", "must be a JSON object"},
		{"trailing data", `{"version":1,"conditions":[]} {}`, "$", "unexpected data"},
		{"duplicate top-level key", `{"version":1,"version":1,"conditions":[]}`, "$", "duplicate object key"},
		{"duplicate condition key",
			cond(`{"field":"locale","op":"eq","value":"tr","value":"en"}`), "$", "duplicate object key"},
		{"unknown top-level key", `{"version":1,"conditions":[],"sql":"SELECT 1"}`, "sql", "unknown field"},
		{"unknown condition key",
			cond(`{"field":"city","op":"eq","value":"A","raw_sql":"1=1"}`), "conditions[0].raw_sql", "unknown field"},

		// Version.
		{"missing version", `{"conditions":[{"field":"city","op":"eq","value":"A"}]}`, "version", "is required"},
		{"unsupported version", `{"version":2,"conditions":[{"field":"city","op":"eq","value":"A"}]}`,
			"version", "unsupported version"},
		{"string version", `{"version":"1","conditions":[{"field":"city","op":"eq","value":"A"}]}`,
			"version", "must be an integer"},

		// Conditions list.
		{"missing conditions", `{"version":1}`, "conditions", "is required"},
		{"null conditions", `{"version":1,"conditions":null}`, "conditions", "is required"},
		{"conditions not array", `{"version":1,"conditions":{}}`, "conditions", "must be an array"},
		{"no conditions", `{"version":1,"conditions":[]}`, "conditions", "between 1 and 10"},
		{"too many conditions", `{"version":1,"conditions":[` + strings.Join(tooMany, ",") + `]}`,
			"conditions", "between 1 and 10"},
		{"condition not object", cond(`"city = 'x'"`), "conditions[0]", "must be a JSON object"},
		{"duplicate field", cond(`{"field":"city","op":"eq","value":"A"},{"field":"city","op":"eq","value":"B"}`),
			"conditions[1].field", "duplicate field"},

		// Field and operator allowlists.
		{"missing field", cond(`{"op":"eq","value":"A"}`), "conditions[0].field", "is required"},
		{"unknown field", cond(`{"field":"email","op":"eq","value":"a@example.com"}`),
			"conditions[0].field", "unsupported field"},
		{"field is SQL", cond(`{"field":"1=1 OR city","op":"eq","value":"A"}`),
			"conditions[0].field", "unsupported field"},
		{"field wrong case", cond(`{"field":"City","op":"eq","value":"A"}`), "conditions[0].field", "unsupported field"},
		{"field not string", cond(`{"field":7,"op":"eq","value":"A"}`), "conditions[0].field", "must be a string"},
		{"unknown op", cond(`{"field":"city","op":"like","value":"A%"}`), "conditions[0].op", "unsupported operator"},
		{"op not allowed for field", cond(`{"field":"order_count","op":"eq","value":1}`),
			"conditions[0].op", "unsupported operator"},
		{"op gte on days", cond(`{"field":"added_to_cart_within_days","op":"gte","value":1}`),
			"conditions[0].op", "unsupported operator"},
		{"missing value", cond(`{"field":"city","op":"eq"}`), "conditions[0].value", "is required"},
		{"null value", cond(`{"field":"city","op":"eq","value":null}`), "conditions[0].value", "is required"},

		// Value types.
		{"city eq with array", cond(`{"field":"city","op":"eq","value":["A"]}`), "conditions[0].value", "must be a string"},
		{"city in with string", cond(`{"field":"city","op":"in","value":"A"}`), "conditions[0].value", "must be an array"},
		{"city in empty", cond(`{"field":"city","op":"in","value":[]}`), "conditions[0].value", "at least one"},
		{"city in with number", cond(`{"field":"city","op":"in","value":["A",3]}`), "conditions[0].value[1]", "must be a string"},
		{"city number", cond(`{"field":"city","op":"eq","value":34}`), "conditions[0].value", "must be a string"},
		{"days as string", cond(`{"field":"added_to_cart_within_days","op":"eq","value":"30"}`),
			"conditions[0].value", "must be an integer"},
		{"days as float", cond(`{"field":"added_to_cart_within_days","op":"eq","value":30.5}`),
			"conditions[0].value", "must be an integer"},
		{"days as 30.0", cond(`{"field":"added_to_cart_within_days","op":"eq","value":30.0}`),
			"conditions[0].value", "must be an integer"},
		{"days exponent", cond(`{"field":"added_to_cart_within_days","op":"eq","value":3e1}`),
			"conditions[0].value", "must be an integer"},
		{"days bool", cond(`{"field":"added_to_cart_within_days","op":"eq","value":true}`),
			"conditions[0].value", "must be an integer"},

		// Ranges.
		{"days zero", cond(`{"field":"added_to_cart_within_days","op":"eq","value":0}`),
			"conditions[0].value", "between 1 and 365"},
		{"days negative", cond(`{"field":"no_purchase_within_days","op":"eq","value":-1}`),
			"conditions[0].value", "between 1 and 365"},
		{"days too large", cond(`{"field":"purchased_within_days","op":"eq","value":366}`),
			"conditions[0].value", "between 1 and 365"},
		{"days overflow", cond(`{"field":"purchased_within_days","op":"eq","value":99999999999999999999999}`),
			"conditions[0].value", "out of range"},
		{"order count negative", cond(`{"field":"order_count","op":"gte","value":-1}`),
			"conditions[0].value", "between 0 and 10000"},
		{"order count too large", cond(`{"field":"order_count","op":"lte","value":10001}`),
			"conditions[0].value", "between 0 and 10000"},
		{"city empty", cond(`{"field":"city","op":"eq","value":"   "}`), "conditions[0].value", "1 to 64"},
		{"city too long", cond(`{"field":"city","op":"eq","value":"` + strings.Repeat("ş", MaxCityLength+1) + `"}`),
			"conditions[0].value", "1 to 64"},
		{"too many cities", cond(`{"field":"city","op":"in","value":[` + manyCities + `]}`),
			"conditions[0].value", "at most 20"},
		{"locale unknown", cond(`{"field":"locale","op":"eq","value":"de"}`), "conditions[0].value", "one of: tr, en"},
		{"locale wrong case", cond(`{"field":"locale","op":"in","value":["tr","EN"]}`),
			"conditions[0].value[1]", "one of: tr, en"},

		// Injection attempts in values are rejected by the character allowlist.
		{"sql injection in city", cond(`{"field":"city","op":"eq","value":"Istanbul'; DROP TABLE users;--"}`),
			"conditions[0].value", "only letters"},
		{"sql comment in city list", cond(`{"field":"city","op":"in","value":["Ankara","x' OR '1'='1"]}`),
			"conditions[0].value[1]", "only letters"},
		{"control char in city", cond(`{"field":"city","op":"eq","value":"Ist\u0000anbul"}`),
			"conditions[0].value", "only letters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.input))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Parse() error = %v, want *ValidationError", err)
			}
			for _, fe := range verr.Errors {
				if fe.Path == tt.wantPath && strings.Contains(fe.Message, tt.wantMsg) {
					return
				}
			}
			t.Errorf("errors = %+v, want path %q with message containing %q", verr.Errors, tt.wantPath, tt.wantMsg)
		})
	}
}

func TestParse_ReportsAllErrors(t *testing.T) {
	_, err := Parse([]byte(`{"version":1,"sql":"x","conditions":[
	  {"field":"city","op":"like","value":"A"},
	  {"field":"order_count","op":"gte","value":-5,"extra":1},
	  {"field":"nope","op":"eq","value":1}
	]}`))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Parse() error = %v, want *ValidationError", err)
	}
	want := []string{"sql", "conditions[0].op", "conditions[1].extra", "conditions[1].value", "conditions[2].field"}
	var got []string
	for _, fe := range verr.Errors {
		got = append(got, fe.Path)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("error paths = %v, want %v", got, want)
	}
}

func TestParse_ErrorsDoNotEchoValues(t *testing.T) {
	secret := "Istanbul'; DROP TABLE users;--"
	_, err := Parse([]byte(`{"version":1,"conditions":[{"field":"city","op":"eq","value":"` + secret + `"}],"x":"` + secret + `"}`))
	if err == nil {
		t.Fatal("Parse() = nil error")
	}
	if strings.Contains(err.Error(), "DROP TABLE") {
		t.Errorf("error echoes submitted value: %v", err)
	}
}

func TestParse_AcceptsTurkishCityNames(t *testing.T) {
	for _, city := range []string{"İstanbul", "Şanlıurfa", "Muğla", "Çanakkale", "Afyon-Karahisar", "St. Albans"} {
		if _, err := Parse([]byte(`{"version":1,"conditions":[{"field":"city","op":"eq","value":"` + city + `"}]}`)); err != nil {
			t.Errorf("Parse(%q) error = %v", city, err)
		}
	}
}
