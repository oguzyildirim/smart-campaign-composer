package content

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParseTemplate: a template either fails to parse or uses only
// allowlisted variables, and its rendered length matches its computed
// length.
func FuzzParseTemplate(f *testing.F) {
	for _, seed := range []string{
		"Merhaba {{first_name}}, {{city}} için %30 indirim!",
		"{{first_name}}{{first_name}}", "{{ first_name }}", "{{phone}}", "{{first_name}", "}}{{", "{{{{city}}}}",
		"{{FIRST_NAME}}", "{{}}", "", "ş\x00\xff",
	} {
		f.Add(seed, "Ayşe", "İstanbul")
	}
	f.Fuzz(func(t *testing.T, text, firstName, city string) {
		// Content and values are always valid UTF-8 (JSON-decoded drafts,
		// Postgres text). Invalid bytes could join into one rune across a
		// placeholder boundary, which the length property does not model.
		if !utf8.ValidString(text) || !utf8.ValidString(firstName) || !utf8.ValidString(city) {
			t.Skip()
		}
		tmpl, err := ParseTemplate(text)
		if err != nil {
			return
		}
		for _, v := range tmpl.Variables() {
			if v != VarFirstName && v != VarCity {
				t.Fatalf("parsed a non-allowlisted variable %q", v)
			}
		}
		values := map[Variable]string{VarFirstName: firstName, VarCity: city}
		lengths := map[Variable]int{VarFirstName: utf8.RuneCountInString(firstName), VarCity: utf8.RuneCountInString(city)}
		if got, want := utf8.RuneCountInString(tmpl.Render(values)), tmpl.RunesWith(lengths); got != want {
			t.Fatalf("rendered %d runes, computed %d", got, want)
		}
	})
}

// FuzzRenderMessage: whatever the drafts and recipient values, a message
// that renders without errors is within the channel limits, has no
// leftover placeholder syntax, and is plain text.
func FuzzRenderMessage(f *testing.F) {
	f.Add("Merhaba {{first_name}}, sepetin seni bekliyor!", "Sepetin seni bekliyor", "Merhaba {{first_name}},\n{{city}} için %30 indirim.", "Ayşe", "İstanbul")
	f.Add("Hi {{first_name}}", "{{first_name}}", "x", "{{city}}", "}}")
	f.Add(strings.Repeat("ş", 150)+"{{first_name}}", "s", "b", strings.Repeat("a", 11), "c")
	f.Add("<b>{{first_name}}</b>", "<script>", "javascript:alert(1)", "\u202e", "\x00")
	f.Fuzz(func(t *testing.T, smsBody, subject, emailBody, firstName, city string) {
		d := Drafts{SMS: SMS{Body: smsBody}, Email: Email{Subject: subject, Body: emailBody}}
		values := map[Variable]string{VarFirstName: firstName, VarCity: city}
		for _, ch := range []Channel{ChannelSMS, ChannelEmail} {
			msg, errs := RenderMessage(d, ch, values)
			if len(errs) > 0 {
				continue
			}
			checks := map[string]renderedField{"body": {msg.Body, MaxSMSRunes, true}}
			if ch == ChannelEmail {
				checks = map[string]renderedField{
					"subject": {msg.Subject, MaxEmailSubjectRunes, false},
					"body":    {msg.Body, MaxEmailBodyRunes, true},
				}
			}
			for name, c := range checks {
				n := utf8.RuneCountInString(c.text)
				switch {
				case strings.TrimSpace(c.text) == "" || n > c.limit:
					t.Fatalf("%s %s: %d runes, limit %d", ch, name, n, c.limit)
				case strings.Contains(c.text, "{{") || strings.Contains(c.text, "}}"):
					t.Fatalf("%s %s keeps placeholder syntax", ch, name)
				case CheckPlainText(c.text, c.multi) != "":
					t.Fatalf("%s %s is not plain text: %s", ch, name, CheckPlainText(c.text, c.multi))
				}
			}
		}
	})
}

type renderedField struct {
	text  string
	limit int
	multi bool
}
