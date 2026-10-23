package mockllm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// SegmentProposal mirrors the segment output contract the real model is
// asked to produce.
type SegmentProposal struct {
	Rule                *Rule    `json:"rule"`
	UnsupportedCriteria []string `json:"unsupported_criteria"`
	Explanation         string   `json:"explanation"`
}

// Rule is an unvalidated segment rule in the DSL's JSON shape.
type Rule struct {
	Version    int         `json:"version"`
	Conditions []Condition `json:"conditions"`
}

// Condition is one rule condition.
type Condition struct {
	Field segment.Field `json:"field"`
	Op    segment.Op    `json:"op"`
	Value any           `json:"value"`
}

const defaultWindowDays = 30

// cities known to the seed, with their display names.
var cities = []struct{ key, name string }{
	{"istanbul", "İstanbul"}, {"ankara", "Ankara"}, {"izmir", "İzmir"}, {"bursa", "Bursa"},
	{"antalya", "Antalya"}, {"adana", "Adana"}, {"konya", "Konya"}, {"gaziantep", "Gaziantep"},
}

// Phrases are matched against folded text (see fold).
var (
	windowDays = regexp.MustCompile(`(\d{1,4})\s*(gun|day)`)

	cartPhrases       = []string{"sepet", "cart", "basket"}
	noPurchasePhrases = []string{
		"alisveris yapmayan", "alisveris yapmamis", "alisveris yapmadan",
		"satin almayan", "satin almamis", "satin almadan",
		"did not purchase", "didn't purchase", "did not buy", "didn't buy",
		"have not purchased", "haven't purchased", "not purchased",
		"without purchasing", "without buying", "no purchase",
	}
	purchasedPhrases = []string{
		"alisveris yapan", "alisveris yapmis", "satin alan", "satin almis",
		"purchased", "bought", "buyers",
	}
	englishPhrases = []string{"english-speaking", "english speaking", "english speakers", "ingilizce konusan"}
	turkishPhrases = []string{"turkish-speaking", "turkish speaking", "turkish speakers", "turkce konusan"}

	// unsupportedCriteria are audience attributes the DSL cannot express.
	unsupportedCriteria = []struct {
		label   string
		pattern *regexp.Regexp
	}{
		{"age", regexp.MustCompile(`\b(older|younger) than\b|\baged?\b|\byas(indan|inda|i|lari|larinda)?\b`)},
		{"gender", regexp.MustCompile(`\b(kadin|erkek|female|male|women|men|woman|man|gender|cinsiyet)\b`)},
		{"income", regexp.MustCompile(`\b(income|gelir|maas)\b`)},
		{"birthday", regexp.MustCompile(`\b(birthday|dogum gunu)\b`)},
	}

	turkishLetters = "çğıöşüÇĞİÖŞÜ"
	turkishWords   = []string{" ve ", "gonder", "kullanici", "indirim", "icin", "musteri"}

	percentSuffix = regexp.MustCompile(`\b(\d{1,2})\s?%`)
	percentPrefix = regexp.MustCompile(`%\s?(\d{1,2})\b`)
)

var turkishFolder = strings.NewReplacer(
	"İ", "i", "I", "i", "ı", "i", "Ş", "s", "ş", "s", "Ğ", "g", "ğ", "g",
	"Ü", "u", "ü", "u", "Ö", "o", "ö", "o", "Ç", "c", "ç", "c",
)

// fold lowercases s and maps Turkish letters to ASCII, so "İstanbul'daki"
// and "istanbul" match the same phrase.
func fold(s string) string { return strings.ToLower(turkishFolder.Replace(s)) }

func containsAny(s string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// ExtractSegment derives a segment proposal from brief using keywords.
func ExtractSegment(brief string) SegmentProposal {
	text := fold(brief)
	var (
		conds     []Condition
		explained []string
	)

	var found []string
	for _, c := range cities {
		if strings.Contains(text, c.key) {
			found = append(found, c.name)
		}
	}
	switch len(found) {
	case 0:
	case 1:
		conds = append(conds, Condition{segment.FieldCity, segment.OpEq, found[0]})
		explained = append(explained, "city "+found[0])
	default:
		conds = append(conds, Condition{segment.FieldCity, segment.OpIn, found})
		explained = append(explained, "cities "+strings.Join(found, ", "))
	}

	switch {
	case containsAny(text, englishPhrases):
		conds = append(conds, Condition{segment.FieldLocale, segment.OpEq, "en"})
		explained = append(explained, "English-speaking users")
	case containsAny(text, turkishPhrases):
		conds = append(conds, Condition{segment.FieldLocale, segment.OpEq, "tr"})
		explained = append(explained, "Turkish-speaking users")
	}

	days := windowFrom(text)
	if containsAny(text, cartPhrases) {
		conds = append(conds, Condition{segment.FieldAddedToCartWithinDays, segment.OpEq, days})
		explained = append(explained, fmt.Sprintf("added to cart within %d days", days))
	}
	switch {
	case containsAny(text, noPurchasePhrases):
		conds = append(conds, Condition{segment.FieldNoPurchaseWithinDays, segment.OpEq, days})
		explained = append(explained, fmt.Sprintf("no purchase within %d days", days))
	case containsAny(text, purchasedPhrases):
		conds = append(conds, Condition{segment.FieldPurchasedWithinDays, segment.OpEq, days})
		explained = append(explained, fmt.Sprintf("purchased within %d days", days))
	}

	unsupported := []string{}
	for _, u := range unsupportedCriteria {
		if u.pattern.MatchString(text) {
			unsupported = append(unsupported, u.label)
		}
	}

	if len(conds) == 0 {
		if len(unsupported) == 0 {
			unsupported = append(unsupported, "no supported audience criteria found in the brief")
		}
		return SegmentProposal{
			UnsupportedCriteria: unsupported,
			Explanation:         "The brief does not describe an audience the segment rules can express.",
		}
	}
	return SegmentProposal{
		Rule:                &Rule{Version: segment.Version, Conditions: conds},
		UnsupportedCriteria: unsupported,
		Explanation:         "Users matching: " + strings.Join(explained, "; ") + ".",
	}
}

// windowFrom returns the first "N days" window in text, clamped to the
// DSL's range, or the default window.
func windowFrom(text string) int {
	m := windowDays.FindStringSubmatch(text)
	if m == nil {
		return defaultWindowDays
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return defaultWindowDays
	}
	return min(max(n, segment.MinWindowDays), segment.MaxWindowDays)
}

// GenerateDrafts returns fixed Turkish or English drafts, mentioning a
// discount and the cart when the brief does.
func GenerateDrafts(brief string) content.Drafts {
	text := fold(brief)
	cart := containsAny(text, cartPhrases)
	pct := discountFrom(brief)
	if strings.ContainsAny(brief, turkishLetters) || containsAny(" "+text+" ", turkishWords) {
		return turkishDrafts(cart, pct)
	}
	return englishDrafts(cart, pct)
}

func discountFrom(brief string) string {
	if m := percentPrefix.FindStringSubmatch(brief); m != nil {
		return m[1]
	}
	if m := percentSuffix.FindStringSubmatch(brief); m != nil {
		return m[1]
	}
	return ""
}

func turkishDrafts(cart bool, pct string) content.Drafts {
	smsHook, subject, lead := "sana özel fırsatlar seni bekliyor!", "{{first_name}}, sana özel fırsatlar", "Sana özel fırsatlar seni bekliyor."
	if cart {
		smsHook, subject, lead = "sepetindeki ürünler seni bekliyor!", "{{first_name}}, sepetin seni bekliyor", "Sepetine eklediğin ürünler hâlâ seni bekliyor."
	}
	smsCTA, offer := "Hemen göz at.", "Hemen göz atmak için uygulamayı açabilirsin."
	if pct != "" {
		smsCTA = "%" + pct + " indirimle bugün tamamla."
		subject += " - %" + pct + " indirim"
		offer = "Sana özel %" + pct + " indirimle alışverişini bugün tamamlayabilirsin."
	}
	return content.Drafts{
		SMS: content.SMS{Body: "Merhaba {{first_name}}, " + smsHook + " " + smsCTA},
		Email: content.Email{
			Subject: subject,
			Body:    "Merhaba {{first_name}},\n\n" + lead + " " + offer + "\n\nSevgiler,\nSmart Campaign Ekibi",
		},
	}
}

func englishDrafts(cart bool, pct string) content.Drafts {
	smsHook, subject, lead := "we picked some offers just for you!", "{{first_name}}, offers picked for you", "We picked some offers just for you."
	if cart {
		smsHook, subject, lead = "your cart is waiting for you!", "{{first_name}}, your cart is waiting", "The items in your cart are still waiting for you."
	}
	smsCTA, offer := "Take a look today.", "Open the app to take a look."
	if pct != "" {
		smsCTA = "Enjoy " + pct + "% off today."
		subject += " - " + pct + "% off"
		offer = "Complete your order today and enjoy " + pct + "% off."
	}
	return content.Drafts{
		SMS: content.SMS{Body: "Hi {{first_name}}, " + smsHook + " " + smsCTA},
		Email: content.Email{
			Subject: subject,
			Body:    "Hi {{first_name}},\n\n" + lead + " " + offer + "\n\nBest,\nThe Smart Campaign Team",
		},
	}
}
