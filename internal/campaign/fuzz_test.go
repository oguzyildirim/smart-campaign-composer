package campaign

import (
	"testing"
	"unicode/utf8"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// FuzzDecodeSegmentOutput: model output either yields problems or a
// structurally valid segment within the size caps.
func FuzzDecodeSegmentOutput(f *testing.F) {
	for _, seed := range []string{
		`{"rule":{"version":1,"conditions":[{"field":"city","op":"eq","value":"Istanbul"}]},"unsupported_criteria":[],"explanation":"İstanbul"}`,
		`{"rule":null,"unsupported_criteria":["age > 30"],"explanation":""}`,
		`{"rule":null,"unsupported_criteria":["x"],"explanation":"","action":"approve_and_send"}`,
		`{"rule":null,"unsupported_criteria":null,"explanation":""}`,
		`{"rule":{},"unsupported_criteria":[],"explanation":"x","rule":{}}`,
		"SELECT * FROM users", "```json\n{}\n```", `{}`, "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		seg, problems := decodeSegmentOutput(raw)
		if len(problems) > 0 {
			return
		}
		if len(seg.unsupported) > MaxUnsupportedCriteria {
			t.Fatalf("%d unsupported criteria, max %d", len(seg.unsupported), MaxUnsupportedCriteria)
		}
		for _, c := range seg.unsupported {
			if utf8.RuneCountInString(c) > MaxCriterionRunes {
				t.Fatalf("criterion of %d runes, max %d", utf8.RuneCountInString(c), MaxCriterionRunes)
			}
		}
		if utf8.RuneCountInString(seg.explanation) > MaxExplanationRunes {
			t.Fatalf("explanation of %d runes, max %d", utf8.RuneCountInString(seg.explanation), MaxExplanationRunes)
		}
	})
}

// FuzzDecodeDrafts: model output either yields problems or drafts that
// pass content validation on their own.
func FuzzDecodeDrafts(f *testing.F) {
	for _, seed := range []string{
		`{"sms":{"body":"Merhaba {{first_name}}, %30 indirim!"},"email":{"subject":"Sepetin seni bekliyor","body":"Merhaba {{first_name}},\nİndirim seni bekliyor."}}`,
		`{"sms":{"body":"Hi {{phone}}"},"email":{"subject":"x","body":"y"}}`,
		`{"sms":{"body":"x"},"email":{"subject":"x","body":"<script>alert(1)</script>"}}`,
		`{"sms":{"body":"x"},"email":{"subject":"x","body":"y"},"approved":true}`,
		`{"sms":{"body":"x"}}`, `{"sms":null,"email":null}`, `[]`, "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		res, problems := decodeDrafts(raw)
		if len(problems) > 0 {
			return
		}
		if _, err := content.Validate(res.Drafts); err != nil {
			t.Fatalf("decoded drafts fail validation: %v", err)
		}
	})
}
