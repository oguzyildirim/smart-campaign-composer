package content

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Message is one channel's content personalized for one recipient. Subject
// is empty for SMS.
type Message struct {
	Subject string
	Body    string
}

// RenderMessage personalizes channel c's fields with one recipient's
// values and re-validates the final text right before delivery: it must
// be non-empty plain text within the channel's limit, with no placeholder
// syntax left over (a value could itself contain "{{" or "}}"). Approval
// checked the same content statically; this check covers the values
// actually substituted. Messages never echo content or values.
func RenderMessage(d Drafts, c Channel, values map[Variable]string) (Message, []FieldError) {
	fields := Fields(d, c)
	if len(fields) == 0 {
		return Message{}, []FieldError{{Path: "channel", Message: errUnknownChannel.Error()}}
	}
	var (
		rendered []string
		errs     []FieldError
	)
	for _, f := range fields {
		text, msg := renderField(rulesByPath[f.Path], f.Text, values)
		if msg != "" {
			errs = append(errs, FieldError{Path: f.Path, Message: msg})
		}
		rendered = append(rendered, text)
	}
	if len(errs) > 0 {
		return Message{}, errs
	}
	if c == ChannelSMS {
		return Message{Body: rendered[0]}, nil
	}
	return Message{Subject: rendered[0], Body: rendered[1]}, nil
}

func renderField(rule fieldRule, text string, values map[Variable]string) (string, string) {
	tmpl, err := ParseTemplate(text)
	if err != nil {
		return "", err.Error()
	}
	out := tmpl.Render(values)
	switch n := utf8.RuneCountInString(out); {
	case strings.TrimSpace(out) == "":
		return "", "must not be empty after personalization"
	case n > rule.maxRunes:
		return "", fmt.Sprintf("length %d after personalization exceeds %d characters", n, rule.maxRunes)
	case strings.Contains(out, "{{") || strings.Contains(out, "}}"):
		return "", "placeholder syntax remains after personalization"
	}
	if msg := CheckPlainText(out, rule.multiline); msg != "" {
		return "", msg
	}
	return out, ""
}
