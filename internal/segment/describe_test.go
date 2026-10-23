package segment

import (
	"slices"
	"testing"
)

func TestDescribe(t *testing.T) {
	rule, err := Parse([]byte(`{"version":1,"conditions":[
		{"field":"order_count","op":"gte","value":2},
		{"field":"city","op":"in","value":["İstanbul","Ankara"]},
		{"field":"locale","op":"eq","value":"tr"},
		{"field":"added_to_cart_within_days","op":"eq","value":30},
		{"field":"no_purchase_within_days","op":"eq","value":7},
		{"field":"purchased_within_days","op":"eq","value":90}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"City is one of İstanbul, Ankara",
		"Language is Turkish",
		"Added to cart in the last 30 days",
		"No purchase in the last 7 days",
		"Purchased in the last 90 days",
		"At least 2 orders",
	}
	if got := Describe(rule); !slices.Equal(got, want) {
		t.Errorf("Describe() =\n%q\nwant\n%q", got, want)
	}
}

func TestDescribe_SingleValues(t *testing.T) {
	rule, err := Parse([]byte(`{"version":1,"conditions":[
		{"field":"city","op":"eq","value":"İzmir"},
		{"field":"locale","op":"in","value":["en","tr"]},
		{"field":"order_count","op":"lte","value":0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"City is İzmir", "Language is English or Turkish", "At most 0 orders"}
	if got := Describe(rule); !slices.Equal(got, want) {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}
