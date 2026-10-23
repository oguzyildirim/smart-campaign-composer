package segment

import (
	"fmt"
	"strings"
)

var localeNames = map[string]string{"tr": "Turkish", "en": "English"}

// Describe renders a validated rule as one plain-language line per
// condition, for display and as audience context for the LLM.
func Describe(rule Rule) []string {
	out := make([]string, 0, len(rule.Conditions))
	for _, c := range rule.Conditions {
		out = append(out, describe(c))
	}
	return out
}

func describe(c Condition) string {
	switch c.Field {
	case FieldCity:
		if c.Op == OpIn {
			return "City is one of " + strings.Join(c.Strings, ", ")
		}
		return "City is " + strings.Join(c.Strings, ", ")
	case FieldLocale:
		names := make([]string, len(c.Strings))
		for i, l := range c.Strings {
			names[i] = localeNames[l]
		}
		return "Language is " + strings.Join(names, " or ")
	case FieldAddedToCartWithinDays:
		return fmt.Sprintf("Added to cart in the last %d days", c.Int)
	case FieldNoPurchaseWithinDays:
		return fmt.Sprintf("No purchase in the last %d days", c.Int)
	case FieldPurchasedWithinDays:
		return fmt.Sprintf("Purchased in the last %d days", c.Int)
	case FieldOrderCount:
		if c.Op == OpGte {
			return fmt.Sprintf("At least %d orders", c.Int)
		}
		return fmt.Sprintf("At most %d orders", c.Int)
	default:
		return string(c.Field)
	}
}
