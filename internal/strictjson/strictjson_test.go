package strictjson

import (
	"errors"
	"strings"
	"testing"
)

type sample struct {
	Name  string `json:"name"`
	Inner struct {
		Count int `json:"count"`
	} `json:"inner"`
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantPath string // empty means success
		wantMsg  string
	}{
		{name: "valid", input: `{"name":"a","inner":{"count":2}}`},
		{name: "malformed", input: `{"name":`, wantPath: "$", wantMsg: "malformed JSON"},
		{name: "duplicate key", input: `{"name":"a","name":"b"}`, wantPath: "$", wantMsg: "duplicate object key"},
		{name: "nested duplicate key", input: `{"inner":{"count":1,"count":2}}`, wantPath: "$", wantMsg: "duplicate object key"},
		{name: "trailing data", input: `{"name":"a"} {}`, wantPath: "$", wantMsg: "unexpected data after JSON value"},
		{name: "unknown field", input: `{"name":"a","secret_field":1}`, wantPath: "$", wantMsg: "contains an unknown field"},
		{name: "wrong type", input: `{"name":5}`, wantPath: "name", wantMsg: "has the wrong JSON type"},
		{name: "nested wrong type", input: `{"inner":{"count":"x"}}`, wantPath: "inner.count", wantMsg: "has the wrong JSON type"},
		{name: "top-level array", input: `[1]`, wantPath: "$", wantMsg: "has the wrong JSON type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got sample
			err := Decode([]byte(tt.input), &got)
			if tt.wantPath == "" {
				if err != nil {
					t.Fatalf("Decode() error = %v, want nil", err)
				}
				return
			}
			e, ok := errors.AsType[*Error](err)
			if !ok {
				t.Fatalf("Decode() error = %v, want *Error", err)
			}
			if e.Path != tt.wantPath || e.Message != tt.wantMsg {
				t.Errorf("Decode() error = {%q, %q}, want {%q, %q}", e.Path, e.Message, tt.wantPath, tt.wantMsg)
			}
		})
	}
}

func TestDecode_ErrorNeverEchoesInput(t *testing.T) {
	inputs := []string{
		`{"name":"a","DROP TABLE users":1}`,
		`{"name":"a'; DROP TABLE users;--","name":"x"}`,
	}
	for _, in := range inputs {
		var got sample
		err := Decode([]byte(in), &got)
		if err == nil {
			t.Fatalf("Decode(%q) succeeded, want error", in)
		}
		if strings.Contains(err.Error(), "DROP") {
			t.Errorf("error %q echoes input", err)
		}
	}
}
