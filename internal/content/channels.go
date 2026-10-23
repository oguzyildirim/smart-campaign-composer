package content

import (
	"errors"
	"fmt"
	"slices"
)

// Channel is a delivery channel a campaign can use.
type Channel string

// Supported channels, in canonical order.
const (
	ChannelSMS   Channel = "sms"
	ChannelEmail Channel = "email"
)

var channelOrder = []Channel{ChannelSMS, ChannelEmail}

// Fixed messages so errors never echo input.
var (
	errUnknownChannel   = errors.New("unknown channel; allowed: sms, email")
	errDuplicateChannel = errors.New("channels must not repeat")
)

// ParseChannels validates a channel selection and returns it in canonical
// order. An empty selection is valid here; approval requires one.
func ParseChannels(names []string) ([]Channel, error) {
	out := make([]Channel, 0, len(names))
	for _, name := range names {
		c := Channel(name)
		if !slices.Contains(channelOrder, c) {
			return nil, errUnknownChannel
		}
		if slices.Contains(out, c) {
			return nil, errDuplicateChannel
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Channel) int {
		return slices.Index(channelOrder, a) - slices.Index(channelOrder, b)
	})
	return out, nil
}

// Field is one message field of a channel with its limit.
type Field struct {
	Path     string
	Text     string
	MaxRunes int
}

// Fields returns the message fields that channel c sends.
func Fields(d Drafts, c Channel) []Field {
	switch c {
	case ChannelSMS:
		return []Field{smsBodyRule.field(d.SMS.Body)}
	case ChannelEmail:
		return []Field{emailSubjectRule.field(d.Email.Subject), emailBodyRule.field(d.Email.Body)}
	default:
		return nil
	}
}

func (r fieldRule) field(text string) Field {
	return Field{Path: r.path, Text: text, MaxRunes: r.maxRunes}
}

var rulesByPath = map[string]fieldRule{
	smsBodyRule.path:      smsBodyRule,
	emailSubjectRule.path: emailSubjectRule,
	emailBodyRule.path:    emailBodyRule,
}

// ValidateChannels checks the fields of the selected channels with the
// same rules as Validate. Fields of unselected channels are not checked,
// but their worst-case length is still reported when they parse. An empty
// selection is an error at path "channels".
func ValidateChannels(d Drafts, channels []Channel) (WorstCase, []FieldError) {
	var errs []FieldError
	if len(channels) == 0 {
		errs = append(errs, FieldError{Path: "channels", Message: "select at least one channel"})
	}
	for _, c := range channels {
		for _, f := range Fields(d, c) {
			if _, msg := checkField(rulesByPath[f.Path], f.Text); msg != "" {
				errs = append(errs, FieldError{Path: f.Path, Message: msg})
			}
		}
	}
	worst := func(text string) int {
		tmpl, err := ParseTemplate(text)
		if err != nil {
			return 0
		}
		return tmpl.WorstCaseRunes()
	}
	wc := WorstCase{
		SMSBody:      worst(d.SMS.Body),
		EmailSubject: worst(d.Email.Subject),
		EmailBody:    worst(d.Email.Body),
	}
	return wc, errs
}

// CheckAudienceLengths checks channel c's fields when every variable takes
// the longest value found among the channel's actual recipients (longest
// maps a variable to that length in runes). Fields that do not parse are
// skipped; ValidateChannels reports them.
func CheckAudienceLengths(d Drafts, c Channel, longest map[Variable]int) []FieldError {
	var errs []FieldError
	for _, f := range Fields(d, c) {
		tmpl, err := ParseTemplate(f.Text)
		if err != nil {
			continue
		}
		if n := tmpl.RunesWith(longest); n > f.MaxRunes {
			errs = append(errs, FieldError{Path: f.Path, Message: fmt.Sprintf(
				"with the longest values in this audience (first_name %d, city %d characters) the length is %d, over the %d-character limit",
				longest[VarFirstName], longest[VarCity], n, f.MaxRunes)})
		}
	}
	return errs
}
