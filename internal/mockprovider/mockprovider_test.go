package mockprovider

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	srv   *Server
	url   string
	clock *clock
}

func newEnv(t *testing.T, cfg Config) env {
	t.Helper()
	if cfg.Seed == 0 {
		cfg.Seed = 42
	}
	s := New(cfg, slog.New(slog.DiscardHandler))
	c := &clock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	s.now = c.Now
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return env{srv: s, url: hs.URL, clock: c}
}

type result struct {
	status     int
	retryAfter string
	body       map[string]any
}

func (e env) post(t *testing.T, path, key, body string) result {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := result{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out.body)
	return out
}

func sms(to string) string { return fmt.Sprintf(`{"to":%q,"body":"Merhaba Ayşe"}`, to) }

const fakePhone = "+9995000000001"

func (e env) stats(t *testing.T) Stats {
	t.Helper()
	resp, err := http.Get(e.url + "/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s Stats
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSend_DeliversOnce(t *testing.T) {
	e := newEnv(t, Config{})
	first := e.post(t, "/v1/sms", "key-1", sms(fakePhone))
	if first.status != http.StatusOK || first.body["message_id"] == "" || first.body["duplicate"] != false {
		t.Fatalf("first send = %+v", first)
	}
	again := e.post(t, "/v1/sms", "key-1", sms(fakePhone))
	if again.status != http.StatusOK || again.body["message_id"] != first.body["message_id"] || again.body["duplicate"] != true {
		t.Fatalf("resend = %+v, want the original result marked duplicate", again)
	}
	st := e.stats(t).Channels["sms"]
	if st.Received != 2 || st.Delivered != 1 || st.DuplicatesSuppressed != 1 || st.DuplicateContent != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestSend_KeyReusedForOtherContentIsRejected(t *testing.T) {
	e := newEnv(t, Config{})
	e.post(t, "/v1/sms", "key-1", sms(fakePhone))
	if r := e.post(t, "/v1/sms", "key-1", sms("+9995000000002")); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", r.status)
	}
	if st := e.stats(t).Channels["sms"]; st.Delivered != 1 {
		t.Errorf("delivered = %d, want 1", st.Delivered)
	}
}

func TestSend_ConcurrentSameKeyDeliversOnce(t *testing.T) {
	e := newEnv(t, Config{LatencyMin: 20 * time.Millisecond, LatencyMax: 20 * time.Millisecond})
	var wg sync.WaitGroup
	ids := make([]any, 10)
	for i := range ids {
		wg.Go(func() { ids[i] = e.post(t, "/v1/sms", "same", sms(fakePhone)).body["message_id"] })
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] || id == nil {
			t.Fatalf("message ids = %v, want one id for every response", ids)
		}
	}
	if st := e.stats(t).Channels["sms"]; st.Delivered != 1 || st.DuplicatesSuppressed != 9 {
		t.Errorf("stats = %+v, want 1 delivered, 9 suppressed", st)
	}
}

func TestSend_DetectsSameContentUnderDifferentKeys(t *testing.T) {
	e := newEnv(t, Config{})
	e.post(t, "/v1/sms", "key-1", sms(fakePhone))
	e.post(t, "/v1/sms", "key-2", sms(fakePhone))
	if st := e.stats(t).Channels["sms"]; st.DuplicateContent != 1 {
		t.Errorf("duplicate_content = %d, want 1", st.DuplicateContent)
	}
}

func TestSend_RejectsInvalidRequests(t *testing.T) {
	e := newEnv(t, Config{})
	tests := []struct {
		name, path, key, body string
		want                  int
	}{
		{"missing idempotency key", "/v1/sms", "", sms(fakePhone), http.StatusBadRequest},
		{"oversized key", "/v1/sms", strings.Repeat("k", 200), sms(fakePhone), http.StatusBadRequest},
		{"unknown field", "/v1/sms", "k1", `{"to":"+9995000000001","body":"x","cc":"y"}`, http.StatusBadRequest},
		{"malformed json", "/v1/sms", "k2", `{"to":`, http.StatusBadRequest},
		{"real-looking phone", "/v1/sms", "k3", sms("+905551234567"), http.StatusUnprocessableEntity},
		{"real email domain", "/v1/email", "k4", `{"to":"ayse@gmail.com","subject":"s","body":"b"}`, http.StatusUnprocessableEntity},
		{"lookalike domain", "/v1/email", "k5", `{"to":"ayse@example.com.evil.io","subject":"s","body":"b"}`, http.StatusUnprocessableEntity},
		{"sms over 160 runes", "/v1/sms", "k6", fmt.Sprintf(`{"to":%q,"body":%q}`, fakePhone, strings.Repeat("ş", 161)), http.StatusUnprocessableEntity},
		{"email without subject", "/v1/email", "k7", `{"to":"ayse@example.com","subject":"","body":"b"}`, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r := e.post(t, tt.path, tt.key, tt.body); r.status != tt.want {
				t.Errorf("status = %d, want %d", r.status, tt.want)
			}
		})
	}
	st := e.stats(t)
	if st.Channels["sms"].Delivered+st.Channels["email"].Delivered != 0 {
		t.Errorf("invalid requests were delivered: %+v", st)
	}
	if st.Channels["sms"].RejectedRecipients != 1 || st.Channels["email"].RejectedRecipients != 2 {
		t.Errorf("rejected recipients = %+v", st)
	}
}

func TestSend_EmailAccepted(t *testing.T) {
	e := newEnv(t, Config{})
	r := e.post(t, "/v1/email", "k", `{"to":"ayse.yilmaz+1@example.com","subject":"Konu","body":"Merhaba\nAyşe"}`)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d", r.status)
	}
}

// statuses sends n distinct messages sequentially and returns the status
// of each.
func statuses(t *testing.T, e env, n int) []int {
	t.Helper()
	out := make([]int, n)
	for i := range out {
		out[i] = e.post(t, "/v1/sms", fmt.Sprintf("key-%d", i), sms(fmt.Sprintf("+9995%09d", i))).status
	}
	return out
}

func TestFaults_DeterministicWithSeed(t *testing.T) {
	cfg := Config{Seed: 7, ServerErrorRate: 0.2, RateLimitRate: 0.2, PermanentFailureRate: 0.1, RetryAfter: 2 * time.Second}
	a := statuses(t, newEnv(t, cfg), 300)
	b := statuses(t, newEnv(t, cfg), 300)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatal("same seed produced different fault sequences")
	}
	cfg.Seed = 8
	if c := statuses(t, newEnv(t, cfg), 300); fmt.Sprint(a) == fmt.Sprint(c) {
		t.Error("different seeds produced identical fault sequences")
	}
	count := map[int]int{}
	for _, s := range a {
		count[s]++
	}
	// 20% 429, then 20% of the rest 503, then 10% of the rest 422.
	for status, want := range map[int]int{429: 60, 503: 48, 422: 19, 200: 173} {
		if got := count[status]; got < want*6/10 || got > want*14/10 {
			t.Errorf("%d responses = %d, want about %d", status, got, want)
		}
	}
}

func TestFaults_RetryAfterHeader(t *testing.T) {
	e := newEnv(t, Config{RateLimitRate: 1, RetryAfter: 3 * time.Second})
	r := e.post(t, "/v1/sms", "k", sms(fakePhone))
	if r.status != http.StatusTooManyRequests || r.retryAfter != "3" {
		t.Fatalf("response = %+v, want 429 with Retry-After 3", r)
	}
}

func TestFaults_PermanentFailuresStickToRecipients(t *testing.T) {
	e := newEnv(t, Config{PermanentFailureRate: 0.3})
	rejected := 0
	for i := range 50 {
		to := fmt.Sprintf("+9995%09d", i)
		first := e.post(t, "/v1/sms", fmt.Sprintf("a-%d", i), sms(to)).status
		second := e.post(t, "/v1/sms", fmt.Sprintf("b-%d", i), sms(to)).status
		if first != second {
			t.Fatalf("recipient %s: %d then %d, want the same outcome", to, first, second)
		}
		if first == http.StatusUnprocessableEntity {
			rejected++
		}
	}
	if rejected == 0 || rejected == 50 {
		t.Errorf("rejected %d of 50 recipients, want a subset", rejected)
	}
}

func TestFaults_TransientFailuresEventuallySucceed(t *testing.T) {
	e := newEnv(t, Config{ServerErrorRate: 0.5, RateLimitRate: 0.3})
	for i := range 20 {
		key, ok := fmt.Sprintf("key-%d", i), false
		for range 30 {
			if e.post(t, "/v1/sms", key, sms(fakePhone)).status == http.StatusOK {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("%s never succeeded: faults must vary between attempts", key)
		}
	}
}

func TestFaults_LostResponseStillDelivers(t *testing.T) {
	e := newEnv(t, Config{LostResponseRate: 1, LostResponseDelay: 10 * time.Millisecond})
	if r := e.post(t, "/v1/sms", "k", sms(fakePhone)); r.status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 after the lost response", r.status)
	}
	r := e.post(t, "/v1/sms", "k", sms(fakePhone))
	if r.status != http.StatusOK || r.body["duplicate"] != true {
		t.Fatalf("retry = %+v, want the stored result", r)
	}
	if st := e.stats(t).Channels["sms"]; st.Delivered != 1 || st.LostResponses != 1 {
		t.Errorf("stats = %+v, want 1 delivered, 1 lost response", st)
	}
}

func TestRateLimit_OwnLimitAndMaxObservedRate(t *testing.T) {
	e := newEnv(t, Config{MaxPerSecond: 5})
	var got []int
	for i := range 7 {
		got = append(got, e.post(t, "/v1/sms", fmt.Sprintf("k%d", i), sms(fakePhone)).status)
		e.clock.Advance(100 * time.Millisecond)
	}
	want := []int{200, 200, 200, 200, 200, 429, 429}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	e.clock.Advance(time.Second)
	if s := e.post(t, "/v1/sms", "later", sms(fakePhone)).status; s != http.StatusOK {
		t.Fatalf("after the window: %d, want 200", s)
	}
	st := e.stats(t).Channels["sms"]
	if st.MaxPerSecond != 7 || st.Throttled != 2 {
		t.Errorf("stats = %+v, want max_per_second 7 (every request counts), throttled 2", st)
	}
	if e.stats(t).Channels["email"].MaxPerSecond != 0 {
		t.Error("channels must be limited independently")
	}
}

func TestMessages_ListsDeliveredNewestFirst(t *testing.T) {
	e := newEnv(t, Config{})
	e.post(t, "/v1/sms", "k1", sms(fakePhone))
	e.post(t, "/v1/email", "k2", `{"to":"a@example.com","subject":"S","body":"B"}`)
	resp, err := http.Get(e.url + "/v1/messages?channel=email")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Messages []Delivered `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0].IdempotencyKey != "k2" || out.Messages[0].Subject != "S" {
		t.Errorf("messages = %+v", out.Messages)
	}
}
