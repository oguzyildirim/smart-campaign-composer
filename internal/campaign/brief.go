package campaign

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxBriefRunes caps a brief. Short briefs keep prompts small and limit
// the room for prompt-injection payloads.
const MaxBriefRunes = 1000

// ValidateBrief returns the trimmed brief or a CodeInvalidBrief *Error.
// A brief is data for the model, never instructions to the system, so the
// only requirements are size and plain characters.
func ValidateBrief(brief string) (string, error) {
	brief = strings.TrimSpace(brief)
	invalid := func(msg string) error {
		return &Error{Code: CodeInvalidBrief, Message: msg}
	}
	if !utf8.ValidString(brief) {
		return "", invalid("brief must be valid UTF-8 text")
	}
	if n := utf8.RuneCountInString(brief); n == 0 || n > MaxBriefRunes {
		return "", invalid(fmt.Sprintf("brief must be 1 to %d characters", MaxBriefRunes))
	}
	for _, r := range brief {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", invalid("brief must not contain control characters")
		}
	}
	return brief, nil
}
