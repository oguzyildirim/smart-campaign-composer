package content

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Channel limits, in runes, after worst-case personalization.
const (
	MaxSMSRunes          = 160
	MaxEmailSubjectRunes = 80
	// MaxEmailBodyRunes is a product cap that keeps emails short and LLM
	// responses bounded; it is not a protocol limit.
	MaxEmailBodyRunes = 2000
)

// Drafts is the proposed content for both channels. Email bodies are plain
// text; HTML is rejected.
type Drafts struct {
	SMS   SMS   `json:"sms"`
	Email Email `json:"email"`
}

// SMS is the SMS draft.
type SMS struct {
	Body string `json:"body"`
}

// Email is the email draft.
type Email struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// WorstCase reports each field's length, in runes, when every variable
// takes its maximum length.
type WorstCase struct {
	SMSBody      int `json:"sms_body"`
	EmailSubject int `json:"email_subject"`
	EmailBody    int `json:"email_body"`
}

// FieldError describes one problem with a draft. Messages never echo the
// content, so they are safe to log, return, and feed back to an LLM.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError lists every problem found in rejected drafts.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.Path + ": " + fe.Message
	}
	return "invalid drafts: " + strings.Join(parts, "; ")
}

type fieldRule struct {
	path      string
	maxRunes  int
	multiline bool
}

var (
	smsBodyRule      = fieldRule{path: "sms.body", maxRunes: MaxSMSRunes, multiline: true}
	emailSubjectRule = fieldRule{path: "email.subject", maxRunes: MaxEmailSubjectRunes}
	emailBodyRule    = fieldRule{path: "email.body", maxRunes: MaxEmailBodyRunes, multiline: true}
)

// htmlLike matches tags (<b>, </p>, <!--, <?xml) and character references
// (&amp; &#39; &#x27;). "<3" and a bare "&" stay plain text.
var htmlLike = regexp.MustCompile(`<\s*/?\s*[a-zA-Z!?]|&(#[0-9]+|#[xX][0-9a-fA-F]+|[a-zA-Z][a-zA-Z0-9]*);`)

// Validate checks every field and returns the worst-case lengths of valid
// drafts. Any problem is reported as a *ValidationError listing all fields.
func Validate(d Drafts) (WorstCase, error) {
	var errs []FieldError
	check := func(rule fieldRule, text string) int {
		n, msg := checkField(rule, text)
		if msg != "" {
			errs = append(errs, FieldError{Path: rule.path, Message: msg})
		}
		return n
	}
	wc := WorstCase{
		SMSBody:      check(smsBodyRule, d.SMS.Body),
		EmailSubject: check(emailSubjectRule, d.Email.Subject),
		EmailBody:    check(emailBodyRule, d.Email.Body),
	}
	if len(errs) > 0 {
		return WorstCase{}, &ValidationError{Errors: errs}
	}
	return wc, nil
}

// checkField returns the worst-case length and, if the text is invalid,
// a client-safe message for the first problem found.
func checkField(rule fieldRule, text string) (int, string) {
	if strings.TrimSpace(text) == "" {
		return 0, "must not be empty"
	}
	if msg := CheckPlainText(text, rule.multiline); msg != "" {
		return 0, msg
	}
	tmpl, err := ParseTemplate(text)
	if err != nil {
		return 0, err.Error()
	}
	n := tmpl.WorstCaseRunes()
	if n > rule.maxRunes {
		return n, fmt.Sprintf("worst-case length %d exceeds %d characters (variables counted at maximum length: first_name %d, city %d)",
			n, rule.maxRunes, MaxFirstNameRunes, MaxCityRunes)
	}
	return n, ""
}

// CheckPlainText returns a client-safe message if text is not plain,
// displayable text: invalid UTF-8, control characters (newlines are
// allowed when multiline), bidirectional controls, or HTML. It returns ""
// for acceptable text. It does not check length or emptiness.
func CheckPlainText(text string, multiline bool) string {
	if !utf8.ValidString(text) {
		return "must be valid UTF-8 text"
	}
	if msg := checkCharacters(text, multiline); msg != "" {
		return msg
	}
	if htmlLike.MatchString(text) {
		return "must be plain text; HTML tags and entities are not allowed"
	}
	return ""
}

// checkCharacters rejects control characters (newlines are allowed in
// multiline fields) and bidirectional overrides, which can make text read
// differently from how it is stored.
func checkCharacters(text string, multiline bool) string {
	for _, r := range text {
		switch {
		case r == '\n' && multiline:
		case r == '\n':
			return "must be a single line"
		case unicode.IsControl(r):
			return "must not contain control characters"
		case isBidiControl(r):
			return "must not contain bidirectional control characters"
		}
	}
	return ""
}

func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) ||
		r == 0x200E || r == 0x200F || r == 0x061C
}
