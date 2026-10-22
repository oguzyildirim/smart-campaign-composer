package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"testing"
	"time"
	"unicode/utf8"
)

var seedTime = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// checksum hashes every generated value, so any change in output changes it.
func checksum(ds Dataset) string {
	h := sha256.New()
	for _, u := range ds.Users {
		fmt.Fprintf(h, "%+v\n", u)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestGenerate_Deterministic(t *testing.T) {
	opts := Options{UserCount: 2000, RandomSeed: 42, Now: seedTime}
	a, b := checksum(Generate(opts)), checksum(Generate(opts))
	if a != b {
		t.Fatalf("same options produced different data: %s vs %s", a, b)
	}

	opts.RandomSeed = 43
	if checksum(Generate(opts)) == a {
		t.Error("a different random seed produced identical data")
	}
}

// These patterns mirror the CHECK constraints in migrations/000002.
var (
	fakeEmail = regexp.MustCompile(`^[a-z0-9._+-]+@example\.com$`)
	fakePhone = regexp.MustCompile(`^\+9995[0-9]{9}$`)
)

func TestGenerate_FakeAndUniqueContactData(t *testing.T) {
	ds := Generate(Options{UserCount: 5000, RandomSeed: 1, Now: seedTime})
	if len(ds.Users) != 5000 {
		t.Fatalf("generated %d users, want 5000", len(ds.Users))
	}
	emails := make(map[string]bool)
	phones := make(map[string]bool)
	for i, u := range ds.Users {
		if !fakeEmail.MatchString(u.Email) || emails[u.Email] {
			t.Fatalf("user %d: email %q is not a unique example.com address", i, u.Email)
		}
		if !fakePhone.MatchString(u.Phone) || phones[u.Phone] {
			t.Fatalf("user %d: phone %q is not a unique number in the fake range", i, u.Phone)
		}
		emails[u.Email], phones[u.Phone] = true, true

		if n := utf8.RuneCountInString(u.FirstName); n < 1 || n > 100 {
			t.Errorf("user %d: first name length %d", i, n)
		}
		if u.Locale != "tr" && u.Locale != "en" {
			t.Errorf("user %d: locale %q", i, u.Locale)
		}
	}
}

func TestGenerate_TimestampsAreInThePast(t *testing.T) {
	ds := Generate(Options{UserCount: 1000, RandomSeed: 3, Now: seedTime})
	oldest := seedTime.Add(-historyWindow)
	for i, u := range ds.Users {
		for _, e := range u.CartEvents {
			if !e.AddedAt.Before(seedTime) || e.AddedAt.Before(oldest) {
				t.Fatalf("user %d: cart event at %s outside the history window", i, e.AddedAt)
			}
		}
		for _, o := range u.Orders {
			if !o.CreatedAt.Before(seedTime) || o.CreatedAt.Before(oldest) {
				t.Fatalf("user %d: order at %s outside the history window", i, o.CreatedAt)
			}
			if o.TotalCents < 0 {
				t.Fatalf("user %d: negative order total", i)
			}
		}
	}
}

func TestGenerate_Distribution(t *testing.T) {
	ds := Generate(Options{UserCount: 5000, RandomSeed: 20261022, Now: seedTime})

	perCity := make(map[string]int)
	var noConsent, smsOnly, emailOnly, both, denied, longNames, turkishChars int
	for _, u := range ds.Users {
		perCity[u.City]++
		granted := map[string]bool{}
		for _, c := range u.Consents {
			granted[c.Channel] = c.Granted
			if !c.Granted {
				denied++
			}
		}
		switch {
		case granted[ChannelSMS] && granted[ChannelEmail]:
			both++
		case granted[ChannelSMS]:
			smsOnly++
		case granted[ChannelEmail]:
			emailOnly++
		default:
			noConsent++
		}
		if utf8.RuneCountInString(u.FirstName) > 20 {
			longNames++
		}
		if regexp.MustCompile(`[çğıöşüÇĞİÖŞÜ]`).MatchString(u.FirstName + u.LastName) {
			turkishChars++
		}
	}

	if len(perCity) < 6 || len(perCity) > 10 {
		t.Errorf("%d cities, want 6-10", len(perCity))
	}
	for city, n := range perCity {
		if city != "İstanbul" && n >= perCity["İstanbul"] {
			t.Errorf("%s has %d users, not fewer than İstanbul (%d)", city, n, perCity["İstanbul"])
		}
	}
	for name, n := range map[string]int{
		"no consent": noConsent, "sms only": smsOnly, "email only": emailOnly, "both": both,
		"explicitly denied": denied, "long names": longNames, "Turkish characters": turkishChars,
	} {
		if n == 0 {
			t.Errorf("no users with %s", name)
		}
	}
}

func TestGenerate_ScenariosCoverExampleSegment(t *testing.T) {
	ds := Generate(Options{UserCount: 100, RandomSeed: 1, Now: seedTime})
	window := 30 * 24 * time.Hour

	var abandoners, cartThenPurchased int
	consentCombos := make(map[string]bool)
	for _, u := range ds.Users[:len(scenarios)] {
		if u.City != "İstanbul" {
			continue
		}
		recentCart := false
		for _, e := range u.CartEvents {
			recentCart = recentCart || seedTime.Sub(e.AddedAt) <= window
		}
		recentOrder := false
		for _, o := range u.Orders {
			recentOrder = recentOrder || seedTime.Sub(o.CreatedAt) <= window
		}
		switch {
		case recentCart && recentOrder:
			cartThenPurchased++
		case recentCart:
			abandoners++
			var sms, email string
			for _, c := range u.Consents {
				state := fmt.Sprintf("%t", c.Granted)
				if c.Channel == ChannelSMS {
					sms = state
				} else {
					email = state
				}
			}
			consentCombos[sms+"/"+email] = true
		}
	}
	if abandoners < 5 || cartThenPurchased < 1 {
		t.Errorf("abandoners = %d, cart-then-purchased = %d", abandoners, cartThenPurchased)
	}
	// Granted, denied (false), and missing ("") consent all appear.
	for _, combo := range []string{"true/true", "true/false", "/true", "/", "false/false"} {
		if !consentCombos[combo] {
			t.Errorf("no Istanbul abandoner with sms/email consent %q (have %v)", combo, consentCombos)
		}
	}
}

func TestAsciiSlug(t *testing.T) {
	tests := map[string]string{
		"Ayşe":                 "ayse",
		"İrem":                 "irem",
		"Çakıroğlu-Büyükşahin": "cakiroglu.buyuksahin",
		"Hatice Kübra":         "hatice.kubra",
		"ŞÜKRÜ":                "sukru",
		"!!!":                  "user",
	}
	for in, want := range tests {
		if got := asciiSlug(in); got != want {
			t.Errorf("asciiSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
