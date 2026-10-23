package campaign

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

func TestCanTransition(t *testing.T) {
	legal := map[[2]Status]bool{
		{StatusDraft, StatusAudienceReady}:       true,
		{StatusAudienceReady, StatusDraftsReady}: true,
		{StatusDraftsReady, StatusDraftsReady}:   true, // regenerate drafts
		{StatusDraftsReady, StatusApproved}:      true,
	}
	all := []Status{StatusDraft, StatusAudienceReady, StatusDraftsReady, StatusApproved, Status("sending"), Status("")}
	for _, from := range all {
		for _, to := range all {
			want := legal[[2]Status{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestParseID(t *testing.T) {
	valid := []string{"0196f0a2-7c1e-7b3a-9d2e-3f4a5b6c7d8e", "0196F0A2-7C1E-7B3A-9D2E-3F4A5B6C7D8E"}
	for _, s := range valid {
		if id, err := ParseID(s); err != nil || string(id) != "0196f0a2-7c1e-7b3a-9d2e-3f4a5b6c7d8e" {
			t.Errorf("ParseID(%q) = %q, %v", s, id, err)
		}
	}
	invalid := []string{"", "1", "0196f0a27c1e7b3a9d2e3f4a5b6c7d8e", "0196f0a2-7c1e-7b3a-9d2e-3f4a5b6c7d8", "zzzzzzzz-7c1e-7b3a-9d2e-3f4a5b6c7d8e",
		"0196f0a2-7c1e-7b3a-9d2e-3f4a5b6c7d8e'--"}
	for _, s := range invalid {
		if _, err := ParseID(s); err == nil {
			t.Errorf("ParseID(%q) succeeded", s)
		}
	}
}

func hashFixture(t *testing.T) Campaign {
	t.Helper()
	rule, err := segment.Parse([]byte(`{"version":1,"conditions":[{"field":"city","op":"eq","value":"İstanbul"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return Campaign{
		Rule:     rule,
		Channels: []content.Channel{content.ChannelSMS, content.ChannelEmail},
		Drafts: content.Drafts{
			SMS:   content.SMS{Body: "Merhaba {{first_name}}"},
			Email: content.Email{Subject: "Konu", Body: "Gövde"},
		},
		Version: 3,
		Brief:   "brief",
	}
}

func TestContentHash_Stable(t *testing.T) {
	// The canonical encoding, written out by hand. Changing it invalidates
	// every stored approval binding, so it must be a deliberate change.
	const canonical = `{"v":1,"rule":{"version":1,"conditions":[{"field":"city","op":"eq","value":"İstanbul"}]},` +
		`"channels":["sms","email"],"sms_body":"Merhaba {{first_name}}","email_subject":"Konu","email_body":"Gövde"}`
	sum := sha256.Sum256([]byte(canonical))
	want := hex.EncodeToString(sum[:])

	c := hashFixture(t)
	if got := ContentHash(c); got != want {
		t.Errorf("ContentHash() = %s, want %s", got, want)
	}
	if again := ContentHash(hashFixture(t)); again != want {
		t.Errorf("hash of an equal campaign = %s, want %s", again, want)
	}
}

func TestContentHash_IgnoresNonContentFields(t *testing.T) {
	c := hashFixture(t)
	h := ContentHash(c)
	c.Version, c.Brief, c.Status, c.ApprovedBy = 9, "other brief", StatusApproved, "someone"
	c.Counts = segment.Counts{Total: 5}
	if ContentHash(c) != h {
		t.Error("hash changed for a non-content field")
	}
	c.Channels = nil
	empty := ContentHash(c)
	c.Channels = []content.Channel{}
	if ContentHash(c) != empty {
		t.Error("nil and empty channel lists hash differently")
	}
}

func TestContentHash_Sensitive(t *testing.T) {
	base := ContentHash(hashFixture(t))
	mutations := map[string]func(*Campaign){
		"sms body":      func(c *Campaign) { c.Drafts.SMS.Body += " " },
		"email subject": func(c *Campaign) { c.Drafts.Email.Subject = "Konu!" },
		"email body":    func(c *Campaign) { c.Drafts.Email.Body = "Govde" },
		"channels":      func(c *Campaign) { c.Channels = []content.Channel{content.ChannelSMS} },
		"rule": func(c *Campaign) {
			c.Rule.Conditions[0].Strings = []string{"Ankara"}
		},
		// Field boundaries are part of the encoding: moving text between
		// fields must change the hash.
		"text moved between fields": func(c *Campaign) {
			c.Drafts.Email.Subject, c.Drafts.Email.Body = "KonuGövde", ""
		},
	}
	for name, mutate := range mutations {
		c := hashFixture(t)
		c.Rule.Conditions = append([]segment.Condition(nil), c.Rule.Conditions...)
		mutate(&c)
		if ContentHash(c) == base {
			t.Errorf("%s: hash did not change", name)
		}
	}
}
