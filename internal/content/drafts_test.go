package content

import (
	"errors"
	"strings"
	"testing"
)

func validDrafts() Drafts {
	return Drafts{
		SMS: SMS{Body: "Merhaba {{first_name}}, sepetindeki ürünlerde %30 indirim seni bekliyor!"},
		Email: Email{
			Subject: "{{first_name}}, sepetin seni bekliyor",
			Body:    "Merhaba {{first_name}},\n\nSepetindeki ürünler için %30 indirim kodun hazır.\n\nSevgiler",
		},
	}
}

// pad returns a string of n runes using a two-byte Turkish letter, so
// tests prove limits count runes, not bytes.
func pad(n int) string { return strings.Repeat("ş", n) }

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Drafts)
		wantPath string // empty means valid
	}{
		{name: "valid", mutate: func(*Drafts) {}},

		// SMS limit: 160 runes after worst-case personalization.
		{name: "sms 160 runes", mutate: func(d *Drafts) { d.SMS.Body = pad(160) }},
		{name: "sms 161 runes", mutate: func(d *Drafts) { d.SMS.Body = pad(161) }, wantPath: "sms.body"},
		{name: "sms 160 with variable at max", mutate: func(d *Drafts) { d.SMS.Body = "{{first_name}}" + pad(160-MaxFirstNameRunes) }},
		{name: "sms 161 with variable at max", mutate: func(d *Drafts) { d.SMS.Body = "{{first_name}}" + pad(161-MaxFirstNameRunes) }, wantPath: "sms.body"},

		// Email subject limit: 80 runes.
		{name: "subject 80 runes", mutate: func(d *Drafts) { d.Email.Subject = pad(80) }},
		{name: "subject 81 runes", mutate: func(d *Drafts) { d.Email.Subject = pad(81) }, wantPath: "email.subject"},
		{name: "subject 81 via city", mutate: func(d *Drafts) { d.Email.Subject = "{{city}}" + pad(81-MaxCityRunes) }, wantPath: "email.subject"},

		// Email body cap.
		{name: "body at cap", mutate: func(d *Drafts) { d.Email.Body = pad(MaxEmailBodyRunes) }},
		{name: "body over cap", mutate: func(d *Drafts) { d.Email.Body = pad(MaxEmailBodyRunes + 1) }, wantPath: "email.body"},

		// Empty content.
		{name: "empty sms", mutate: func(d *Drafts) { d.SMS.Body = "" }, wantPath: "sms.body"},
		{name: "whitespace subject", mutate: func(d *Drafts) { d.Email.Subject = "  \t " }, wantPath: "email.subject"},
		{name: "empty body", mutate: func(d *Drafts) { d.Email.Body = "" }, wantPath: "email.body"},

		// Variables.
		{name: "unknown variable", mutate: func(d *Drafts) { d.SMS.Body = "Hi {{phone}}" }, wantPath: "sms.body"},
		{name: "malformed variable", mutate: func(d *Drafts) { d.Email.Subject = "Hi {{first_name}" }, wantPath: "email.subject"},

		// Plain text only.
		{name: "html tag in body", mutate: func(d *Drafts) { d.Email.Body = "Hi <b>there</b>" }, wantPath: "email.body"},
		{name: "script tag in sms", mutate: func(d *Drafts) { d.SMS.Body = "<script>alert(1)</script>" }, wantPath: "sms.body"},
		{name: "html entity", mutate: func(d *Drafts) { d.Email.Body = "Fish &amp; chips" }, wantPath: "email.body"},
		{name: "less-than with digit is text", mutate: func(d *Drafts) { d.SMS.Body = "Only <3 days left & more" }},

		// Control and invisible characters.
		{name: "newline in subject", mutate: func(d *Drafts) { d.Email.Subject = "a\nb" }, wantPath: "email.subject"},
		{name: "newline in body is fine", mutate: func(d *Drafts) { d.Email.Body = "a\n\nb" }},
		{name: "NUL in sms", mutate: func(d *Drafts) { d.SMS.Body = "a\x00b" }, wantPath: "sms.body"},
		{name: "bidi override in subject", mutate: func(d *Drafts) { d.Email.Subject = "a" + string(rune(0x202E)) + "b" }, wantPath: "email.subject"},
		{name: "invalid utf-8", mutate: func(d *Drafts) { d.SMS.Body = "a\xffb" }, wantPath: "sms.body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := validDrafts()
			tt.mutate(&d)
			_, err := Validate(d)
			if tt.wantPath == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			verr, ok := errors.AsType[*ValidationError](err)
			if !ok {
				t.Fatalf("Validate() error = %v, want *ValidationError", err)
			}
			if len(verr.Errors) != 1 || verr.Errors[0].Path != tt.wantPath {
				t.Errorf("Validate() errors = %+v, want one error at %s", verr.Errors, tt.wantPath)
			}
		})
	}
}

func TestValidate_ReportsEveryField(t *testing.T) {
	_, err := Validate(Drafts{})
	verr, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("Validate() error = %v, want *ValidationError", err)
	}
	var paths []string
	for _, fe := range verr.Errors {
		paths = append(paths, fe.Path)
	}
	if got, want := strings.Join(paths, ","), "sms.body,email.subject,email.body"; got != want {
		t.Errorf("error paths = %s, want %s", got, want)
	}
}

func TestValidate_WorstCase(t *testing.T) {
	d := Drafts{
		SMS:   SMS{Body: "Hi {{first_name}}"},                // 3 + 30
		Email: Email{Subject: "{{city}} deals", Body: "İyi"}, // 20 + 6, 3
	}
	wc, err := Validate(d)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	want := WorstCase{SMSBody: 33, EmailSubject: 26, EmailBody: 3}
	if wc != want {
		t.Errorf("WorstCase = %+v, want %+v", wc, want)
	}
}

func TestValidate_ErrorsNeverEchoContent(t *testing.T) {
	secret := "Istanbul'; DROP TABLE users;--"
	d := Drafts{
		SMS:   SMS{Body: secret + pad(200)},
		Email: Email{Subject: secret + "{{nope}}", Body: "<i>" + secret + "</i>"},
	}
	_, err := Validate(d)
	if err == nil {
		t.Fatal("Validate() succeeded, want error")
	}
	if strings.Contains(err.Error(), "DROP") {
		t.Errorf("error %q echoes content", err)
	}
}
