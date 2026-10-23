package content

import (
	"slices"
	"strings"
	"testing"
)

func paths(errs []FieldError) []string {
	out := make([]string, len(errs))
	for i, fe := range errs {
		out[i] = fe.Path
	}
	return out
}

func TestParseChannels(t *testing.T) {
	tests := []struct {
		in      []string
		want    []Channel
		wantErr bool
	}{
		{in: []string{"sms", "email"}, want: []Channel{ChannelSMS, ChannelEmail}},
		{in: []string{"email", "sms"}, want: []Channel{ChannelSMS, ChannelEmail}}, // canonical order
		{in: []string{"email"}, want: []Channel{ChannelEmail}},
		{in: []string{}, want: []Channel{}},
		{in: []string{"sms", "sms"}, wantErr: true},
		{in: []string{"push"}, wantErr: true},
		{in: []string{"SMS"}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseChannels(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseChannels(%v) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && !slices.Equal(got, tt.want) {
			t.Errorf("ParseChannels(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestValidateChannels(t *testing.T) {
	bad := validDrafts()
	bad.SMS.Body = "Hi {{phone}}"
	bad.Email.Subject = pad(81)

	tests := []struct {
		name      string
		drafts    Drafts
		channels  []Channel
		wantPaths []string
	}{
		{"valid, both channels", validDrafts(), []Channel{ChannelSMS, ChannelEmail}, nil},
		{"invalid sms and subject", bad, []Channel{ChannelSMS, ChannelEmail}, []string{"sms.body", "email.subject"}},
		{"unselected sms is not checked", bad, []Channel{ChannelEmail}, []string{"email.subject"}},
		{"unselected email is not checked", bad, []Channel{ChannelSMS}, []string{"sms.body"}},
		{"no channel selected", validDrafts(), nil, []string{"channels"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs := ValidateChannels(tt.drafts, tt.channels)
			if got := paths(errs); !slices.Equal(got, tt.wantPaths) {
				t.Errorf("paths = %v, want %v (%v)", got, tt.wantPaths, errs)
			}
		})
	}
}

func TestValidateChannels_ReportsWorstCaseForParsableFields(t *testing.T) {
	d := Drafts{SMS: SMS{Body: "Hi {{first_name}}"}, Email: Email{Subject: "{{phone}}", Body: "x"}}
	wc, _ := ValidateChannels(d, []Channel{ChannelSMS})
	if wc.SMSBody != 3+MaxFirstNameRunes || wc.EmailSubject != 0 || wc.EmailBody != 1 {
		t.Errorf("worst case = %+v", wc)
	}
}

func TestCheckAudienceLengths(t *testing.T) {
	sms := func(body string) Drafts {
		d := validDrafts()
		d.SMS.Body = body
		return d
	}
	tests := []struct {
		name      string
		drafts    Drafts
		channel   Channel
		longest   map[Variable]int
		wantPaths []string
	}{
		{"fits with real names", sms("{{first_name}}" + pad(150)), ChannelSMS, map[Variable]int{VarFirstName: 10}, nil},
		{"exactly 160 with longest name", sms("{{first_name}}" + pad(100)), ChannelSMS, map[Variable]int{VarFirstName: 60}, nil},
		{"161 with longest name", sms("{{first_name}}" + pad(101)), ChannelSMS, map[Variable]int{VarFirstName: 60}, []string{"sms.body"}},
		{"city counted with real length", sms("{{city}}" + pad(151)), ChannelSMS, map[Variable]int{VarCity: 10}, []string{"sms.body"}},
		{"email channel checks subject", func() Drafts {
			d := validDrafts()
			d.Email.Subject = "{{first_name}}" + pad(30)
			return d
		}(), ChannelEmail, map[Variable]int{VarFirstName: 51}, []string{"email.subject"}},
		{"unparsable field is skipped", sms("{{phone}}"), ChannelSMS, map[Variable]int{VarFirstName: 200}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := CheckAudienceLengths(tt.drafts, tt.channel, tt.longest)
			if got := paths(errs); !slices.Equal(got, tt.wantPaths) {
				t.Errorf("paths = %v, want %v (%v)", got, tt.wantPaths, errs)
			}
			for _, fe := range errs {
				if strings.Contains(fe.Message, "ş") {
					t.Errorf("message echoes content: %q", fe.Message)
				}
			}
		})
	}
}

func TestFields(t *testing.T) {
	d := validDrafts()
	got := Fields(d, ChannelEmail)
	if len(got) != 2 || got[0].Path != "email.subject" || got[0].Text != d.Email.Subject ||
		got[0].MaxRunes != MaxEmailSubjectRunes || got[1].Path != "email.body" {
		t.Errorf("Fields(email) = %+v", got)
	}
	if got := Fields(d, ChannelSMS); len(got) != 1 || got[0].MaxRunes != MaxSMSRunes {
		t.Errorf("Fields(sms) = %+v", got)
	}
}
