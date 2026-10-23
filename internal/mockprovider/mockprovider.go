// Package mockprovider simulates an SMS/email delivery provider for local
// development and tests. It never contacts a real messaging service: it
// stores messages in memory and only accepts the fake recipients the seed
// generates, a second safety net behind the database constraints.
//
// To exercise retry and rate-limit handling it injects faults: latency,
// 5xx errors, 429s with Retry-After, permanent rejections of a fixed
// subset of recipients, "accepted but response lost", and its own
// per-channel rate limit. Fault decisions are a hash of the seed, the
// idempotency key, and the attempt number for that key, so a fixed seed
// gives the same decisions however requests interleave.
//
// Idempotency: the first accepted request for a key is delivered; later
// requests with the same key return the stored result and count as
// suppressed duplicates. State is in memory, so a restart forgets it.
package mockprovider

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oguzyildirim/smart-campaign-composer/internal/strictjson"
)

// Recipient patterns: the same fake ranges the users table enforces.
var (
	phonePattern = regexp.MustCompile(`^\+9995[0-9]{9}$`)
	emailPattern = regexp.MustCompile(`^[a-z0-9._+-]+@example\.com$`)
	keyFormat    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// Request limits, mirroring real provider constraints.
const (
	maxRequestBytes  = 16 << 10
	maxSMSRunes      = 160
	maxSubjectRunes  = 80
	maxEmailRunes    = 10000
	defaultListLimit = 100
	maxListLimit     = 1000
)

// Channels.
const (
	channelSMS   = "sms"
	channelEmail = "email"
)

// Config sets the simulated behaviour. Rates are probabilities in [0, 1].
type Config struct {
	// Seed drives every fault decision.
	Seed uint64
	// Latency is uniform in [LatencyMin, LatencyMax] per accepted message.
	LatencyMin time.Duration
	LatencyMax time.Duration
	// RateLimitRate answers 429 with Retry-After: RetryAfter.
	RateLimitRate float64
	RetryAfter    time.Duration
	// ServerErrorRate answers 503.
	ServerErrorRate float64
	// PermanentFailureRate rejects this share of recipients with 422,
	// always the same recipients for a seed.
	PermanentFailureRate float64
	// LostResponseRate stores the message, then stalls for
	// LostResponseDelay (or until the client hangs up) and answers 504.
	LostResponseRate  float64
	LostResponseDelay time.Duration
	// MaxPerSecond answers 429 once a channel has received this many
	// requests in the last second; 0 disables the limit.
	MaxPerSecond int
}

// ChannelStats counts one channel's traffic.
type ChannelStats struct {
	// Received counts every send request that passed validation.
	Received int64 `json:"received"`
	// Delivered counts distinct messages accepted (one per key).
	Delivered int64 `json:"delivered"`
	// DuplicatesSuppressed counts resends of an already accepted key.
	DuplicatesSuppressed int64 `json:"duplicates_suppressed"`
	// DuplicateContent counts deliveries identical to an earlier one
	// (recipient, subject, body) under a different key: a double send the
	// key could not catch. It should stay 0.
	DuplicateContent int64 `json:"duplicate_content"`
	// RateLimited counts injected 429s; Throttled counts 429s from the
	// provider's own per-second limit.
	RateLimited int64 `json:"rate_limited"`
	Throttled   int64 `json:"throttled"`
	// ServerErrors counts injected 503s.
	ServerErrors int64 `json:"server_errors"`
	// PermanentFailures counts 422s for the rejected recipient subset.
	PermanentFailures int64 `json:"permanent_failures"`
	// LostResponses counts accepted messages whose response was dropped.
	LostResponses int64 `json:"lost_responses"`
	// RejectedRecipients counts recipients outside the fake ranges.
	RejectedRecipients int64 `json:"rejected_recipients"`
	// InvalidRequests counts other malformed requests.
	InvalidRequests int64 `json:"invalid_requests"`
	// MaxPerSecond is the most requests received in any one-second window.
	MaxPerSecond int `json:"max_per_second"`
}

// Stats is the GET /v1/stats response.
type Stats struct {
	Channels map[string]ChannelStats `json:"channels"`
}

// Delivered is one accepted message, as listed by GET /v1/messages.
type Delivered struct {
	MessageID      string    `json:"message_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Channel        string    `json:"channel"`
	To             string    `json:"to"`
	Subject        string    `json:"subject,omitempty"`
	Body           string    `json:"body"`
	DeliveredAt    time.Time `json:"delivered_at"`
}

// Server is the mock provider. It is safe for concurrent use.
type Server struct {
	cfg    Config
	logger *slog.Logger
	now    func() time.Time

	mu       sync.Mutex
	records  map[string]*record // by idempotency key
	attempts map[string]uint64  // requests that reached fault injection, by key
	contents map[string]bool    // fingerprints of delivered messages
	messages []Delivered
	stats    map[string]*ChannelStats
	windows  map[string][]time.Time // receive times within the last second
	seq      int64
}

// record is an accepted message. done closes once it is stored, so a
// concurrent resend waits for the original result.
type record struct {
	fingerprint string
	done        chan struct{}
	messageID   string
}

// New returns a Server.
func New(cfg Config, logger *slog.Logger) *Server {
	return &Server{
		cfg: cfg, logger: logger, now: time.Now,
		records:  map[string]*record{},
		attempts: map[string]uint64{},
		contents: map[string]bool{},
		stats:    map[string]*ChannelStats{channelSMS: {}, channelEmail: {}},
		windows:  map[string][]time.Time{},
	}
}

// Handler returns the provider API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sms", func(w http.ResponseWriter, r *http.Request) { s.send(w, r, channelSMS) })
	mux.HandleFunc("POST /v1/email", func(w http.ResponseWriter, r *http.Request) { s.send(w, r, channelEmail) })
	mux.HandleFunc("GET /v1/messages", s.listMessages)
	mux.HandleFunc("GET /v1/stats", s.getStats)
	return mux
}

type sendRequest struct {
	To      string  `json:"to"`
	Subject *string `json:"subject,omitempty"`
	Body    string  `json:"body"`
}

type sendResponse struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate"`
}

func (s *Server) send(w http.ResponseWriter, r *http.Request, channel string) {
	req, key, status, code := s.parse(r, channel)
	if status != 0 {
		s.count(channel, func(st *ChannelStats) {
			if code == "recipient_not_allowed" {
				st.RejectedRecipients++
			} else {
				st.InvalidRequests++
			}
		})
		writeError(w, status, code)
		return
	}
	subject := ""
	if req.Subject != nil {
		subject = *req.Subject
	}
	fingerprint := fingerprintOf(channel, req.To, subject, req.Body)

	s.mu.Lock()
	st := s.stats[channel]
	st.Received++
	st.MaxPerSecond = max(st.MaxPerSecond, s.observe(channel))
	if rec, ok := s.records[key]; ok {
		if rec.fingerprint != fingerprint {
			st.InvalidRequests++
			s.mu.Unlock()
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_reused")
			return
		}
		st.DuplicatesSuppressed++
		s.mu.Unlock()
		s.replay(w, r, rec)
		return
	}
	if s.cfg.MaxPerSecond > 0 && len(s.windows[channel]) > s.cfg.MaxPerSecond {
		st.Throttled++
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "throttled")
		return
	}
	attempt := s.attempts[key]
	s.attempts[key]++
	switch {
	case s.roll("rate_limit", key, attempt) < s.cfg.RateLimitRate:
		st.RateLimited++
		s.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(max(s.cfg.RetryAfter, time.Second).Seconds()))))
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	case s.roll("server_error", key, attempt) < s.cfg.ServerErrorRate:
		st.ServerErrors++
		s.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	case s.roll("permanent", req.To, 0) < s.cfg.PermanentFailureRate:
		st.PermanentFailures++
		s.mu.Unlock()
		writeError(w, http.StatusUnprocessableEntity, "recipient_rejected")
		return
	}
	rec := &record{fingerprint: fingerprint, done: make(chan struct{})}
	s.records[key] = rec
	lost := s.roll("lost_response", key, attempt) < s.cfg.LostResponseRate
	latency := s.cfg.LatencyMin + time.Duration(s.roll("latency", key, attempt)*float64(max(s.cfg.LatencyMax-s.cfg.LatencyMin, 0)))
	s.mu.Unlock()

	// Acceptance completes even if the client hangs up, as with a real
	// provider that already took the request.
	time.Sleep(latency)
	s.store(rec, Delivered{IdempotencyKey: key, Channel: channel, To: req.To, Subject: subject, Body: req.Body})

	if lost {
		s.count(channel, func(st *ChannelStats) { st.LostResponses++ })
		select {
		case <-time.After(s.cfg.LostResponseDelay):
		case <-r.Context().Done():
			return
		}
		writeError(w, http.StatusGatewayTimeout, "response_lost")
		return
	}
	writeJSON(w, http.StatusOK, sendResponse{MessageID: rec.messageID, Status: "delivered"})
}

// parse validates a send request. A non-zero status rejects it.
func (s *Server) parse(r *http.Request, channel string) (sendRequest, string, int, string) {
	var req sendRequest
	key := r.Header.Get("Idempotency-Key")
	if !keyFormat.MatchString(key) {
		return req, "", http.StatusBadRequest, "invalid_idempotency_key"
	}
	data, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxRequestBytes))
	if err != nil {
		return req, "", http.StatusBadRequest, "invalid_request"
	}
	if err := strictjson.Decode(data, &req); err != nil {
		return req, "", http.StatusBadRequest, "invalid_request"
	}
	bodyRunes := utf8.RuneCountInString(req.Body)
	switch channel {
	case channelSMS:
		switch {
		case req.Subject != nil:
			return req, "", http.StatusBadRequest, "invalid_request"
		case !phonePattern.MatchString(req.To):
			return req, "", http.StatusUnprocessableEntity, "recipient_not_allowed"
		case bodyRunes == 0 || bodyRunes > maxSMSRunes:
			return req, "", http.StatusUnprocessableEntity, "invalid_content"
		}
	case channelEmail:
		switch {
		case !emailPattern.MatchString(req.To):
			return req, "", http.StatusUnprocessableEntity, "recipient_not_allowed"
		case req.Subject == nil || *req.Subject == "" || utf8.RuneCountInString(*req.Subject) > maxSubjectRunes,
			bodyRunes == 0 || bodyRunes > maxEmailRunes:
			return req, "", http.StatusUnprocessableEntity, "invalid_content"
		}
	}
	return req, key, 0, ""
}

// observe records a request at now in channel's sliding window and returns
// the number of requests in the last second. Callers hold s.mu.
func (s *Server) observe(channel string) int {
	now := s.now()
	win := s.windows[channel]
	cut := 0
	for cut < len(win) && !win[cut].After(now.Add(-time.Second)) {
		cut++
	}
	win = append(win[cut:], now)
	s.windows[channel] = win
	return len(win)
}

// store records an accepted message and wakes resends waiting for it.
func (s *Server) store(rec *record, d Delivered) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	rec.messageID = fmt.Sprintf("msg_%012d", s.seq)
	d.MessageID, d.DeliveredAt = rec.messageID, s.now()
	st := s.stats[d.Channel]
	st.Delivered++
	if s.contents[rec.fingerprint] {
		st.DuplicateContent++
	}
	s.contents[rec.fingerprint] = true
	s.messages = append(s.messages, d)
	close(rec.done)
	s.logger.Debug("message delivered", "channel", d.Channel, "message_id", d.MessageID)
}

// replay answers a resend with the original result once it is stored.
func (s *Server) replay(w http.ResponseWriter, r *http.Request, rec *record) {
	select {
	case <-rec.done:
	case <-r.Context().Done():
		return
	}
	s.mu.Lock()
	id := rec.messageID
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, sendResponse{MessageID: id, Status: "delivered", Duplicate: true})
}

func (s *Server) count(channel string, fn func(*ChannelStats)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.stats[channel])
}

// roll returns a deterministic number in [0, 1) for a decision.
func (s *Server) roll(purpose, subject string, n uint64) float64 {
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], s.cfg.Seed)
	h.Write(buf[:])
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write([]byte(subject))
	binary.BigEndian.PutUint64(buf[:], n)
	h.Write(buf[:])
	sum := h.Sum(nil)
	return float64(binary.BigEndian.Uint64(sum[:8])>>11) / (1 << 53)
}

func fingerprintOf(channel, to, subject, body string) string {
	sum := sha256.Sum256([]byte(channel + "\x00" + to + "\x00" + subject + "\x00" + body))
	return string(sum[:])
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	if channel != "" && channel != channelSMS && channel != channelEmail {
		writeError(w, http.StatusBadRequest, "invalid_channel")
		return
	}
	limit := defaultListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "invalid_limit")
			return
		}
		limit = n
	}
	s.mu.Lock()
	out := make([]Delivered, 0, min(limit, len(s.messages)))
	for i := len(s.messages) - 1; i >= 0 && len(out) < limit; i-- {
		if m := s.messages[i]; channel == "" || m.Channel == channel {
			out = append(out, m)
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string][]Delivered{"messages": out})
}

func (s *Server) getStats(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := Stats{Channels: map[string]ChannelStats{}}
	for ch, st := range s.stats {
		out.Channels[ch] = *st
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // fails only when the client is gone
}
