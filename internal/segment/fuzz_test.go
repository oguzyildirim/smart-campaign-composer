package segment

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var placeholder = regexp.MustCompile(`\$\d+`)

// FuzzParse: any input either fails validation or yields a rule that
// compiles to placeholder-only SQL and survives a JSON round trip.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		`{"version":1,"conditions":[{"field":"city","op":"eq","value":"Istanbul"},` +
			`{"field":"added_to_cart_within_days","op":"eq","value":30},{"field":"no_purchase_within_days","op":"eq","value":30}]}`,
		`{"version":1,"conditions":[{"field":"city","op":"in","value":["İstanbul","Ankara"]}]}`,
		`{"version":1,"conditions":[{"field":"order_count","op":"gte","value":2},{"field":"locale","op":"eq","value":"tr"}]}`,
		`{"version":1,"conditions":[{"field":"city","op":"eq","value":"x'; DROP TABLE users;--"}]}`,
		`{"version":1,"conditions":[{"field":"sql","op":"eq","value":"SELECT 1"}]}`,
		`{"version":2,"conditions":[]}`,
		`{"version":1,"conditions":[{"field":"city","op":"eq","value":"a","extra":1}]}`,
		`{"version":1,"conditions":[{"field":"added_to_cart_within_days","op":"eq","value":1e400}]}`,
		`[]`, `null`, `{`, "",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		rule, err := Parse(data)
		if err != nil {
			return
		}
		pred, err := Compile(rule, refTime)
		if err != nil {
			t.Fatalf("a parsed rule does not compile: %v", err)
		}
		if strings.ContainsAny(pred.SQL, "';") || strings.Contains(pred.SQL, "--") {
			t.Fatalf("SQL contains quote, semicolon, or comment: %s", pred.SQL)
		}
		if n := len(placeholder.FindAllString(pred.SQL, -1)); n != len(pred.Args) {
			t.Fatalf("%d placeholders for %d args", n, len(pred.Args))
		}
		again, err := json.Marshal(rule)
		if err != nil {
			t.Fatalf("marshal a parsed rule: %v", err)
		}
		reparsed, err := Parse(again)
		if err != nil {
			t.Fatalf("a parsed rule does not re-validate: %v", err)
		}
		if !reflect.DeepEqual(reparsed, rule) {
			t.Fatalf("round trip changed the rule: %+v → %+v", rule, reparsed)
		}
	})
}
