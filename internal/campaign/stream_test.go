package campaign

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// collect feeds chunks and joins the extracted text per field.
func collect(chunks []string) map[DraftField]string {
	x := newFieldExtractor()
	got := map[DraftField]string{}
	for _, c := range chunks {
		for _, d := range x.Feed(c) {
			got[d.Field] += d.Text
		}
	}
	return got
}

// splitEvery cuts s into n-byte chunks, deliberately splitting runes and
// escape sequences.
func splitEvery(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

const streamedDrafts = `{"sms": {"body": "Merhaba {{first_name}}, %30 \"indirim\"\nşimdi İstanbul 😀"},` +
	` "email": {"subject": "Sepetin \\ seni bekliyor", "body": "Satır 1\nSatır 2\t/ \/"}}`

func TestFieldExtractor_AnyChunking(t *testing.T) {
	want := map[DraftField]string{
		FieldSMSBody:      "Merhaba {{first_name}}, %30 \"indirim\"\nşimdi İstanbul 😀",
		FieldEmailSubject: `Sepetin \ seni bekliyor`,
		FieldEmailBody:    "Satır 1\nSatır 2\t/ /",
	}
	for _, n := range []int{1, 2, 3, 5, 7, 24, len(streamedDrafts)} {
		got := collect(splitEvery(streamedDrafts, n))
		for f, w := range want {
			if got[f] != w {
				t.Errorf("chunk %d: %s = %q, want %q", n, f, got[f], w)
			}
		}
	}
}

func TestFieldExtractor_EmitsOnlyValidUTF8(t *testing.T) {
	x := newFieldExtractor()
	for _, c := range splitEvery(streamedDrafts, 1) {
		for _, d := range x.Feed(c) {
			if !utf8.ValidString(d.Text) || d.Text == "" {
				t.Fatalf("delta %q is empty or not valid UTF-8", d.Text)
			}
		}
	}
}

func TestFieldExtractor_IgnoresOtherPaths(t *testing.T) {
	input := `{"sms": {"note": "x", "body": "ok", "nested": {"body": "no"}}, "body": "no",` +
		` "list": [{"sms": {"body": "no"}}], "email": {"subject": "s", "body": "b"}, "sms.body": "no"}`
	got := collect([]string{input})
	want := map[DraftField]string{FieldSMSBody: "ok", FieldEmailSubject: "s", FieldEmailBody: "b"}
	if len(got) != len(want) {
		t.Errorf("got %q, want %q", got, want)
	}
	for f, w := range want {
		if got[f] != w {
			t.Errorf("%s = %q, want %q", f, got[f], w)
		}
	}
}

func TestFieldExtractor_GarbageNeverPanics(t *testing.T) {
	inputs := []string{
		`}}}]]]"""`, `{"sms":{"body":"\u12`, `{"sms":{"body":"\ud800 lone"}}`,
		strings.Repeat("{", 1000) + `"x"`, "\xff\xfe{\"sms\":{\"body\":\"\xff\"}}",
		`{"sms":{"body":"\q"}}`,
	}
	for _, in := range inputs {
		x := newFieldExtractor()
		for _, d := range x.Feed(in) {
			if !utf8.ValidString(d.Text) {
				t.Errorf("Feed(%q) emitted invalid UTF-8", in)
			}
		}
	}
}
