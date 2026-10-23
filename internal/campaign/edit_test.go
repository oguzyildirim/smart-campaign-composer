package campaign_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

func TestEdit(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	edited := e.edit(t, c, func(ed *campaign.Edit) {
		ed.Channels = []string{"email", "sms"}
		ed.Drafts.SMS.Body = "Selam {{first_name}}!"
	})
	if edited.Version != c.Version+1 || edited.Drafts.SMS.Body != "Selam {{first_name}}!" ||
		edited.ContentHash == c.ContentHash || edited.ContentHash != campaign.ContentHash(edited) {
		t.Errorf("edited = %+v", edited)
	}
	if !slices.Equal(edited.Channels, []content.Channel{content.ChannelSMS, content.ChannelEmail}) {
		t.Errorf("channels not canonical: %v", edited.Channels)
	}
	events := e.store.Events(c.ID)
	if last := events[len(events)-1]; last.Action != campaign.ActionEdited || last.Actor != campaign.ActorMarketer {
		t.Errorf("last event = %+v", last)
	}

	// The same content again changes nothing.
	same := e.edit(t, edited, func(*campaign.Edit) {})
	if same.Version != edited.Version || len(e.store.Events(c.ID)) != len(events) {
		t.Errorf("no-op edit wrote: version %d → %d", edited.Version, same.Version)
	}
}

func TestEdit_StoresInvalidContentAndReportsIt(t *testing.T) {
	e := newEnv(t)
	c := e.edit(t, e.ready(t), func(ed *campaign.Edit) {
		ed.Drafts.SMS.Body = "Hi {{phone}}"
		ed.Drafts.Email.Subject = strings.Repeat("x", 81)
	})
	v := c.Validate()
	var paths []string
	for _, fe := range v.Errors {
		paths = append(paths, fe.Path)
	}
	if v.Valid || !slices.Equal(paths, []string{"sms.body", "email.subject"}) {
		t.Errorf("validation = %+v", v)
	}
}

func TestEdit_Rejections(t *testing.T) {
	e := newEnv(t)
	c := e.ready(t)
	tests := []struct {
		name   string
		mutate func(*campaign.Edit)
		want   campaign.ErrorCode
		path   string
	}{
		{"stale version", func(ed *campaign.Edit) { ed.Version-- }, campaign.CodeVersionConflict, ""},
		{"unknown channel", func(ed *campaign.Edit) { ed.Channels = []string{"push"} }, campaign.CodeInvalidRequest, "channels"},
		{"duplicate channel", func(ed *campaign.Edit) { ed.Channels = []string{"sms", "sms"} }, campaign.CodeInvalidRequest, "channels"},
		{"NUL byte", func(ed *campaign.Edit) { ed.Drafts.Email.Body = "a\x00b" }, campaign.CodeInvalidRequest, "email.body"},
		{"invalid UTF-8", func(ed *campaign.Edit) { ed.Drafts.SMS.Body = "\xff" }, campaign.CodeInvalidRequest, "sms.body"},
		{"over hard cap", func(ed *campaign.Edit) { ed.Drafts.SMS.Body = strings.Repeat("ş", 321) }, campaign.CodeInvalidRequest, "sms.body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := editOf(c)
			tt.mutate(&ed)
			_, err := e.svc.Edit(t.Context(), c.ID, ed)
			requireCode(t, err, tt.want)
			gerr, _ := errors.AsType[*campaign.Error](err)
			if tt.path != "" && (len(gerr.Details) != 1 || gerr.Details[0].Path != tt.path) {
				t.Errorf("details = %+v", gerr.Details)
			}
		})
	}
	if got, _ := e.svc.Get(t.Context(), c.ID); got.Version != c.Version {
		t.Errorf("rejected edits changed the version: %d → %d", c.Version, got.Version)
	}
}

func TestEdit_WrongStateOrGenerating(t *testing.T) {
	e := newEnv(t)
	audienceOnly := e.create(t)
	_, err := e.svc.Edit(t.Context(), audienceOnly.ID, editOf(audienceOnly))
	requireCode(t, err, campaign.CodeInvalidState)

	c := e.ready(t)
	if _, err := e.svc.BeginGeneration(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Edit(t.Context(), c.ID, editOf(c))
	requireCode(t, err, campaign.CodeGenerationInProgress)

	approved := e.approve(t, e.ready(t))
	_, err = e.svc.Edit(t.Context(), approved.ID, editOf(approved))
	requireCode(t, err, campaign.CodeInvalidState)
}
