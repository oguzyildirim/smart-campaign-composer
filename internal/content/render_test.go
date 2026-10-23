package content

import (
	"strings"
	"testing"
)

func TestRenderMessage(t *testing.T) {
	d := Drafts{
		SMS:   SMS{Body: "Merhaba {{first_name}}, {{city}} mağazasında %30 indirim!"},
		Email: Email{Subject: "{{first_name}}, sepetin seni bekliyor", Body: "Merhaba {{first_name}},\nİndirim seni bekliyor."},
	}
	values := map[Variable]string{VarFirstName: "Şükrü", VarCity: "İstanbul"}

	sms, errs := RenderMessage(d, ChannelSMS, values)
	if len(errs) > 0 {
		t.Fatalf("sms errors = %v", errs)
	}
	if want := "Merhaba Şükrü, İstanbul mağazasında %30 indirim!"; sms.Body != want || sms.Subject != "" {
		t.Errorf("sms = %+v, want body %q", sms, want)
	}

	email, errs := RenderMessage(d, ChannelEmail, values)
	if len(errs) > 0 {
		t.Fatalf("email errors = %v", errs)
	}
	if email.Subject != "Şükrü, sepetin seni bekliyor" || email.Body != "Merhaba Şükrü,\nİndirim seni bekliyor." {
		t.Errorf("email = %+v", email)
	}
}

func TestRenderMessage_Rejections(t *testing.T) {
	// 150 runes of Turkish multi-byte letters plus the variable.
	smsWith := func(body string) Drafts { return Drafts{SMS: SMS{Body: body}} }
	turkish := strings.Repeat("ş", 150)
	tests := []struct {
		name   string
		d      Drafts
		ch     Channel
		values map[Variable]string
		path   string
	}{
		{"sms exactly 160 runes is fine", smsWith(turkish + "{{first_name}}"), ChannelSMS,
			map[Variable]string{VarFirstName: strings.Repeat("ğ", 10)}, ""},
		{"sms 161 runes after a long name", smsWith(turkish + "{{first_name}}"), ChannelSMS,
			map[Variable]string{VarFirstName: strings.Repeat("ğ", 11)}, "sms.body"},
		{"subject 81 runes after personalization", Drafts{Email: Email{Subject: strings.Repeat("a", 75) + "{{first_name}}", Body: "x"}},
			ChannelEmail, map[Variable]string{VarFirstName: "Ayşegül"}, "email.subject"},
		{"leftover placeholder from a value", smsWith("Hi {{first_name}}"), ChannelSMS,
			map[Variable]string{VarFirstName: "{{city}}"}, "sms.body"},
		{"stray closing braces from a value", smsWith("Hi {{first_name}}"), ChannelSMS,
			map[Variable]string{VarFirstName: "a}}"}, "sms.body"},
		{"empty after rendering", smsWith("{{first_name}}"), ChannelSMS,
			map[Variable]string{VarFirstName: "  "}, "sms.body"},
		{"newline from a value in the subject", Drafts{Email: Email{Subject: "Hi {{first_name}}", Body: "x"}}, ChannelEmail,
			map[Variable]string{VarFirstName: "a\nb"}, "email.subject"},
		{"HTML from a value", smsWith("Hi {{first_name}}"), ChannelSMS,
			map[Variable]string{VarFirstName: "<b>x</b>"}, "sms.body"},
		{"malformed stored template", smsWith("Hi {{ first_name }}"), ChannelSMS, nil, "sms.body"},
		{"unknown channel", smsWith("Hi"), Channel("push"), nil, "channel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs := RenderMessage(tt.d, tt.ch, tt.values)
			if tt.path == "" {
				if len(errs) > 0 {
					t.Fatalf("errors = %v, want none", errs)
				}
				return
			}
			if len(errs) != 1 || errs[0].Path != tt.path {
				t.Fatalf("errors = %v, want one at %s", errs, tt.path)
			}
		})
	}
}
