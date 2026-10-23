package campaign

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
	"github.com/oguzyildirim/smart-campaign-composer/internal/strictjson"
)

// Limits on model output, checked before any parsing.
const (
	MaxSegmentOutputBytes = 16 << 10
	MaxDraftsOutputBytes  = 32 << 10

	MaxExplanationRunes    = 500
	MaxUnsupportedCriteria = 10
	MaxCriterionRunes      = 200
)

// segmentOutput is the segment contract. Pointer fields distinguish a
// missing key from an empty value; Rule is nil when missing and "null"
// when the model declined to build a rule.
type segmentOutput struct {
	Rule                json.RawMessage `json:"rule"`
	UnsupportedCriteria *[]string       `json:"unsupported_criteria"`
	Explanation         *string         `json:"explanation"`
}

// decodedSegment is a structurally valid segment output whose rule has not
// been validated yet.
type decodedSegment struct {
	rule        json.RawMessage // nil when the model returned null
	unsupported []string
	explanation string
}

// decodeSegmentOutput checks everything except the rule itself, which only
// the segment package may validate. Problems are client-safe and never
// echo the output.
func decodeSegmentOutput(raw string) (decodedSegment, []string) {
	var out segmentOutput
	if err := strictjson.Decode([]byte(raw), &out); err != nil {
		return decodedSegment{}, []string{decodeProblem(err)}
	}

	var problems []string
	if out.Rule == nil {
		problems = append(problems, "rule: is required; use null when no supported criteria apply")
	}
	if out.UnsupportedCriteria == nil {
		problems = append(problems, "unsupported_criteria: is required; use [] when every criterion is supported")
	}
	if out.Explanation == nil {
		problems = append(problems, "explanation: is required")
	}
	if len(problems) > 0 {
		return decodedSegment{}, problems
	}

	d := decodedSegment{explanation: strings.TrimSpace(*out.Explanation)}
	if string(out.Rule) != "null" {
		d.rule = out.Rule
	}

	criteria := *out.UnsupportedCriteria
	if len(criteria) > MaxUnsupportedCriteria {
		problems = append(problems, fmt.Sprintf("unsupported_criteria: must contain at most %d items", MaxUnsupportedCriteria))
	} else {
		d.unsupported = make([]string, 0, len(criteria))
		for i, c := range criteria {
			c = strings.TrimSpace(c)
			if msg := checkShortText(c, MaxCriterionRunes, true); msg != "" {
				problems = append(problems, fmt.Sprintf("unsupported_criteria[%d]: %s", i, msg))
				continue
			}
			d.unsupported = append(d.unsupported, c)
		}
	}
	if msg := checkShortText(d.explanation, MaxExplanationRunes, false); msg != "" {
		problems = append(problems, "explanation: "+msg)
	}
	if d.rule == nil && len(problems) == 0 && len(d.unsupported) == 0 {
		problems = append(problems, "rule: is null but unsupported_criteria is empty; build a rule or list the unsupported criteria")
	}
	return d, problems
}

// checkShortText validates a short model-written string shown to users.
func checkShortText(s string, maxRunes int, required bool) string {
	if required && s == "" {
		return "must not be empty"
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return fmt.Sprintf("must be at most %d characters", maxRunes)
	}
	return content.CheckPlainText(s, false)
}

// ruleProblems converts segment validation errors into problems located
// under "rule".
func ruleProblems(verr *segment.ValidationError) []string {
	out := make([]string, len(verr.Errors))
	for i, fe := range verr.Errors {
		path := "rule"
		if fe.Path != "$" {
			path += "." + fe.Path
		}
		out[i] = path + ": " + fe.Message
	}
	return out
}

// draftsOutput is the drafts contract, with pointers to detect missing keys.
type draftsOutput struct {
	SMS *struct {
		Body *string `json:"body"`
	} `json:"sms"`
	Email *struct {
		Subject *string `json:"subject"`
		Body    *string `json:"body"`
	} `json:"email"`
}

// decodeDrafts decodes and fully validates drafts.
func decodeDrafts(raw string) (DraftsResult, []string) {
	var out draftsOutput
	if err := strictjson.Decode([]byte(raw), &out); err != nil {
		return DraftsResult{}, []string{decodeProblem(err)}
	}

	var problems []string
	required := func(path string, ok bool) {
		if !ok {
			problems = append(problems, path+": is required")
		}
	}
	required("sms", out.SMS != nil)
	required("email", out.Email != nil)
	if out.SMS != nil {
		required("sms.body", out.SMS.Body != nil)
	}
	if out.Email != nil {
		required("email.subject", out.Email.Subject != nil)
		required("email.body", out.Email.Body != nil)
	}
	if len(problems) > 0 {
		return DraftsResult{}, problems
	}

	d := content.Drafts{
		SMS:   content.SMS{Body: *out.SMS.Body},
		Email: content.Email{Subject: *out.Email.Subject, Body: *out.Email.Body},
	}
	wc, err := content.Validate(d)
	if verr, ok := errors.AsType[*content.ValidationError](err); ok {
		for _, fe := range verr.Errors {
			problems = append(problems, fe.Path+": "+fe.Message)
		}
		return DraftsResult{}, problems
	}
	return DraftsResult{Drafts: d, WorstCase: wc}, nil
}

func decodeProblem(err error) string {
	if serr, ok := errors.AsType[*strictjson.Error](err); ok {
		return serr.Path + ": " + serr.Message
	}
	return "$: could not be decoded"
}
