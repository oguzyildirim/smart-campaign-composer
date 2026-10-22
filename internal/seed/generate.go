// Package seed generates and loads the fake user dataset.
//
// Every recipient is fictional: emails use the reserved example.com
// domain and phone numbers use the +999 prefix, an ITU-T country code
// that is not assigned to any country. The database enforces both with
// CHECK constraints (migrations/000002).
//
// Generation is deterministic: the same Options produce the same Dataset.
// Timestamps are relative to Options.Now so the data always looks recent.
package seed

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

const (
	// historyWindow is how far back cart events and orders go.
	historyWindow = 90 * 24 * time.Hour
	// signupWindow is how far back account creation goes.
	signupWindow = 3 * 365 * 24 * time.Hour
)

// Channel names as stored in channel_consents.
const (
	ChannelSMS   = "sms"
	ChannelEmail = "email"
)

// Options controls generation.
type Options struct {
	UserCount  int
	RandomSeed uint64
	Now        time.Time
}

// Dataset is the generated data, ready to load.
type Dataset struct {
	Users []User
}

// User is a fake user together with their consents and activity.
type User struct {
	FirstName  string
	LastName   string
	Email      string
	Phone      string
	City       string
	Locale     string
	CreatedAt  time.Time
	Consents   []Consent
	CartEvents []CartEvent
	Orders     []Order
}

// Consent is one channel consent record. Granted=false models a user who
// explicitly declined or revoked consent; a missing record means the user
// was never asked. Both mean "do not message".
type Consent struct {
	Channel   string
	Granted   bool
	UpdatedAt time.Time
}

// CartEvent records a product added to the cart.
type CartEvent struct {
	ProductSKU string
	AddedAt    time.Time
}

// Order is a completed purchase. TotalCents avoids floating point.
type Order struct {
	TotalCents int64
	CreatedAt  time.Time
}

// weightedCity is a city and its relative share of users.
type weightedCity struct {
	name   string
	weight int
}

// cities: Istanbul is deliberately the largest. Names are stored with
// Turkish letters; segment matching folds them (city_key in SQL).
var cities = []weightedCity{
	{"İstanbul", 34}, {"Ankara", 15}, {"İzmir", 12}, {"Bursa", 9},
	{"Antalya", 9}, {"Adana", 7}, {"Konya", 7}, {"Gaziantep", 7},
}

var (
	turkishFirstNames = []string{
		"Ayşe", "Fatma", "Emine", "Hatice", "Zeynep", "Elif", "Merve", "Büşra",
		"Gül", "Özge", "Şule", "Çiğdem", "İrem", "Ebru", "Gökçe", "Sıla",
		"Mehmet", "Mustafa", "Ahmet", "Ali", "Hüseyin", "Hasan", "İbrahim", "İsmail",
		"Murat", "Ömer", "Oğuz", "Uğur", "Çağrı", "Barış", "Gökhan", "Emre",
	}
	turkishLastNames = []string{
		"Yılmaz", "Kaya", "Demir", "Şahin", "Çelik", "Yıldız", "Yıldırım", "Öztürk",
		"Aydın", "Özdemir", "Arslan", "Doğan", "Kılıç", "Aslan", "Çetin", "Kara",
		"Koç", "Kurt", "Özkan", "Şimşek", "Güneş", "Erdoğan", "Akgül", "Bulut",
	}
	englishFirstNames = []string{
		"Emma", "Olivia", "Amelia", "Isla", "Ava", "Grace", "Lily", "Sophie",
		"Jack", "Oliver", "Harry", "George", "Noah", "Leo", "James", "Thomas",
	}
	englishLastNames = []string{
		"Smith", "Jones", "Taylor", "Brown", "Williams", "Wilson", "Johnson",
		"Davies", "Evans", "Walker", "Wright", "Robinson",
	}
	// Long names exercise SMS length limits once {{first_name}} is
	// rendered (later milestones).
	longFirstNames = []string{
		"Muhammed Mustafa Abdurrahman", "Hatice Kübra Gülizar Nurşen",
		"Alexandria Elizabeth Josephine", "Şükriye Gülşen Çiğdem Özlem",
	}
	longLastNames = []string{
		"Karamanoğlu-Ağaoğlu", "Montgomery-Fairweather", "Çakıroğlu-Büyükşahin",
	}
)

// Generate returns the dataset for opts. The first users are fixed
// scenarios (see scenarios) that guarantee coverage of the example
// segment and its edge cases; the rest are random but reproducible.
func Generate(opts Options) Dataset {
	now := opts.Now.UTC().Truncate(time.Second)
	//nolint:gosec // G404: reproducible fake data, not a security context.
	rng := rand.New(rand.NewPCG(opts.RandomSeed, 0x5eed))
	g := generator{rng: rng, now: now}

	users := make([]User, 0, opts.UserCount)
	for i := range opts.UserCount {
		var u User
		if i < len(scenarios) {
			u = g.scenarioUser(scenarios[i])
		} else {
			u = g.randomUser()
		}
		u.Email = email(u.FirstName, u.LastName, i+1)
		u.Phone = phone(i + 1)
		users = append(users, u)
	}
	return Dataset{Users: users}
}

type generator struct {
	rng *rand.Rand
	now time.Time
}

func (g generator) ago(d time.Duration) time.Time { return g.now.Add(-d) }

// within returns a time uniformly distributed between window and one
// second before now.
func (g generator) within(window time.Duration) time.Time {
	return g.ago(time.Duration(g.rng.Int64N(int64(window/time.Second))+1) * time.Second)
}

func (g generator) pick(list []string) string { return list[g.rng.IntN(len(list))] }

func (g generator) city() string {
	total := 0
	for _, c := range cities {
		total += c.weight
	}
	n := g.rng.IntN(total)
	for _, c := range cities {
		if n < c.weight {
			return c.name
		}
		n -= c.weight
	}
	return cities[0].name
}

func (g generator) randomUser() User {
	u := User{City: g.city(), Locale: "tr", CreatedAt: g.within(signupWindow)}
	if g.rng.IntN(100) < 15 {
		u.Locale = "en"
	}
	switch {
	case g.rng.IntN(100) < 1:
		u.FirstName, u.LastName = g.pick(longFirstNames), g.pick(longLastNames)
	case u.Locale == "en":
		u.FirstName, u.LastName = g.pick(englishFirstNames), g.pick(englishLastNames)
	default:
		u.FirstName, u.LastName = g.pick(turkishFirstNames), g.pick(turkishLastNames)
	}
	u.Consents = g.randomConsents()

	if g.rng.IntN(100) < 55 {
		for range 1 + g.rng.IntN(4) {
			u.CartEvents = append(u.CartEvents, CartEvent{ProductSKU: g.sku(), AddedAt: g.within(historyWindow)})
		}
	}
	if g.rng.IntN(100) < 50 {
		for range 1 + g.rng.IntN(5) {
			u.Orders = append(u.Orders, Order{TotalCents: g.amount(), CreatedAt: g.within(historyWindow)})
		}
	}
	return u
}

// randomConsents: ~45% both channels, ~20% SMS only, ~20% email only,
// ~15% neither. Users without a channel either have no record or an
// explicit granted=false record.
func (g generator) randomConsents() []Consent {
	var sms, email bool
	switch n := g.rng.IntN(100); {
	case n < 45:
		sms, email = true, true
	case n < 65:
		sms = true
	case n < 85:
		email = true
	}
	var out []Consent
	for _, ch := range []struct {
		name    string
		granted bool
	}{{ChannelSMS, sms}, {ChannelEmail, email}} {
		if ch.granted || g.rng.IntN(2) == 0 {
			out = append(out, Consent{Channel: ch.name, Granted: ch.granted, UpdatedAt: g.within(signupWindow)})
		}
	}
	return out
}

func (g generator) sku() string { return fmt.Sprintf("SKU-%05d", g.rng.IntN(500)+1) }

func (g generator) amount() int64 { return 1999 + g.rng.Int64N(500000) }

// scenario is a hand-specified user. Offsets are durations before now.
type scenario struct {
	first, last, city, locale string
	sms, email                consentState
	cartAgo                   []time.Duration
	orderAgo                  []time.Duration
}

type consentState int

const (
	consentMissing consentState = iota
	consentGranted
	consentDenied
)

const day = 24 * time.Hour

// scenarios cover the example segment "Istanbul, added to cart in the
// last 30 days, no purchase in the last 30 days" from every side.
var scenarios = []scenario{
	// Matching abandoners with each consent combination.
	{first: "Ayşe", last: "Yılmaz", city: "İstanbul", locale: "tr", sms: consentGranted, email: consentGranted, cartAgo: []time.Duration{2 * day}},
	{first: "Mehmet", last: "Kaya", city: "İstanbul", locale: "tr", sms: consentGranted, email: consentDenied, cartAgo: []time.Duration{5 * day}},
	{first: "Zeynep", last: "Demir", city: "İstanbul", locale: "tr", sms: consentMissing, email: consentGranted, cartAgo: []time.Duration{12 * day}},
	{first: "Oliver", last: "Smith", city: "İstanbul", locale: "en", sms: consentMissing, email: consentMissing, cartAgo: []time.Duration{3 * day}},
	{first: "İrem", last: "Şahin", city: "İstanbul", locale: "tr", sms: consentDenied, email: consentDenied, cartAgo: []time.Duration{20 * day}},
	// Added to cart, then purchased: not an abandoner.
	{first: "Çağrı", last: "Öztürk", city: "İstanbul", locale: "tr", sms: consentGranted, email: consentGranted,
		cartAgo: []time.Duration{10 * day}, orderAgo: []time.Duration{3 * day}},
	// Purchased long ago, abandoned recently: matches the 30-day rule.
	{first: "Gökhan", last: "Çelik", city: "İstanbul", locale: "tr", sms: consentGranted, email: consentGranted,
		cartAgo: []time.Duration{5 * day}, orderAgo: []time.Duration{45 * day}},
	// Cart activity only outside the window.
	{first: "Grace", last: "Taylor", city: "İstanbul", locale: "en", sms: consentGranted, email: consentGranted, cartAgo: []time.Duration{40 * day}},
	// Abandoner in another city.
	{first: "Uğur", last: "Arslan", city: "Ankara", locale: "tr", sms: consentGranted, email: consentGranted, cartAgo: []time.Duration{4 * day}},
	// Matching abandoners with long names (SMS length tests later).
	{first: "Muhammed Mustafa Abdurrahman", last: "Karamanoğlu-Ağaoğlu", city: "İstanbul", locale: "tr",
		sms: consentGranted, email: consentGranted, cartAgo: []time.Duration{6 * day}},
	{first: "Alexandria Elizabeth Josephine", last: "Montgomery-Fairweather", city: "İstanbul", locale: "en",
		sms: consentGranted, email: consentGranted, cartAgo: []time.Duration{8 * day}},
	{first: "Şükriye Gülşen Çiğdem Özlem", last: "Çakıroğlu-Büyükşahin", city: "İstanbul", locale: "tr",
		sms: consentGranted, email: consentMissing, cartAgo: []time.Duration{1 * day}},
}

func (g generator) scenarioUser(s scenario) User {
	u := User{
		FirstName: s.first, LastName: s.last, City: s.city, Locale: s.locale,
		CreatedAt: g.ago(400 * day),
	}
	for _, c := range []struct {
		name  string
		state consentState
	}{{ChannelSMS, s.sms}, {ChannelEmail, s.email}} {
		if c.state != consentMissing {
			u.Consents = append(u.Consents, Consent{Channel: c.name, Granted: c.state == consentGranted, UpdatedAt: g.ago(300 * day)})
		}
	}
	for i, ago := range s.cartAgo {
		u.CartEvents = append(u.CartEvents, CartEvent{ProductSKU: fmt.Sprintf("SKU-%05d", 900+i), AddedAt: g.ago(ago)})
	}
	for _, ago := range s.orderAgo {
		u.Orders = append(u.Orders, Order{TotalCents: 24990, CreatedAt: g.ago(ago)})
	}
	return u
}

// email builds a unique address on the reserved example.com domain.
func email(first, last string, n int) string {
	return fmt.Sprintf("%s.%s.%05d@example.com", asciiSlug(first), asciiSlug(last), n)
}

// phone returns a unique fake number for n in [0, 10^9). The local
// part is n scrambled by an affine map that is a bijection modulo 10^9
// (7919 is coprime with 10), so numbers look varied but never collide.
func phone(n int) string {
	return fmt.Sprintf("+9995%09d", (n*7919+104729)%1_000_000_000) // no overflow: n < 10^9
}

var asciiFold = strings.NewReplacer(
	"ç", "c", "Ç", "c", "ğ", "g", "Ğ", "g", "ı", "i", "I", "i", "İ", "i",
	"ö", "o", "Ö", "o", "ş", "s", "Ş", "s", "ü", "u", "Ü", "u",
)

// asciiSlug folds Turkish letters and keeps [a-z0-9], joining words with
// dots, e.g. "Hatice Kübra" -> "hatice.kubra".
func asciiSlug(s string) string {
	s = strings.ToLower(asciiFold.Replace(s))
	var b strings.Builder
	dot := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dot && b.Len() > 0 {
				b.WriteByte('.')
			}
			dot = false
			b.WriteRune(r)
			continue
		}
		dot = true
	}
	if b.Len() == 0 {
		return "user"
	}
	return b.String()
}
