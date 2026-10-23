package campaign_test

import (
	"strings"
	"testing"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign/campaigntest"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

func TestPreview(t *testing.T) {
	e := newEnv(t)
	c := e.edit(t, e.ready(t), func(ed *campaign.Edit) {
		ed.Drafts.SMS.Body = "Merhaba {{first_name}} ({{city}})"
	})
	p, err := e.svc.Preview(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Validation.Valid || len(p.Channels) != 2 {
		t.Fatalf("preview = %+v", p)
	}
	sms := p.Channels[0]
	if sms.Channel != content.ChannelSMS || sms.Recipients != 80 || len(sms.Samples) != 2 {
		t.Fatalf("sms preview = %+v", sms)
	}
	f := sms.Samples[0].Fields[0]
	if f.Path != "sms.body" || f.Text != "Merhaba Ayşe (İstanbul)" || f.Runes != 23 || f.MaxRunes != 160 || f.OverLimit {
		t.Errorf("rendered = %+v", f)
	}
	if email := p.Channels[1]; len(email.Samples[0].Fields) != 2 {
		t.Errorf("email fields = %+v", email.Samples[0].Fields)
	}
}

func TestPreview_ReportsRealAudienceViolations(t *testing.T) {
	aud := defaultAudience()
	sms := aud[content.ChannelSMS]
	sms.Longest = map[content.Variable]int{content.VarFirstName: 60}
	sms.Samples = []campaign.Recipient{campaigntest.Recipient(9, strings.Repeat("Ö", 60), "Bursa")}
	aud[content.ChannelSMS] = sms
	e := newEnvWithAudience(t, aud)
	// 130 + 30 fits the worst case (first_name 30) but not a 60-rune name.
	c := e.edit(t, e.ready(t), func(ed *campaign.Edit) {
		ed.Drafts.SMS.Body = "{{first_name}}" + strings.Repeat("a", 130)
	})

	p, err := e.svc.Preview(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Validation.Valid || len(p.Validation.Errors) != 1 || p.Validation.Errors[0].Path != "sms.body" {
		t.Errorf("validation = %+v", p.Validation)
	}
	f := p.Channels[0].Samples[0].Fields[0]
	if f.Runes != 190 || !f.OverLimit {
		t.Errorf("rendered = %+v", f)
	}
}

func TestPreview_NoRecipients(t *testing.T) {
	e := newEnvWithAudience(t, nil)
	p, err := e.svc.Preview(t.Context(), e.ready(t).ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Validation.Valid || p.Validation.Errors[0].Path != "channels" {
		t.Errorf("validation = %+v", p.Validation)
	}
}

func TestPreview_RequiresDrafts(t *testing.T) {
	e := newEnv(t)
	c := e.create(t)
	_, err := e.svc.Preview(t.Context(), c.ID)
	requireCode(t, err, campaign.CodeInvalidState)
}
