package delivery_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
)

const deliveryID delivery.ID = "0199f0a0-0000-7000-8000-000000000001"

var drafts = content.Drafts{
	SMS:   content.SMS{Body: "Merhaba {{first_name}}, %30 indirim seni bekliyor!"},
	Email: content.Email{Subject: "{{first_name}}, sepetin hazır", Body: "Merhaba {{first_name}}"},
}

// row is one delivery in fakeStore.
type row struct {
	status      delivery.Status
	channel     content.Channel
	attempts    int
	maxAttempts int
	outcome     delivery.Outcome
	recipient   delivery.Recipient
}

// fakeStore is an in-memory delivery.Store with the same claim and
// fencing rules as the Postgres store.
type fakeStore struct {
	mu           sync.Mutex
	rows         map[delivery.ID]*row
	recipientErr error
	// loseClaim simulates lease expiry and a re-claim before Record.
	loseClaim bool
	// completeErr fails the campaign completion check after storing.
	completeErr error
}

func newStore(r row) *fakeStore {
	if r.status == "" {
		r.status = delivery.StatusQueued
	}
	if r.channel == "" {
		r.channel = content.ChannelSMS
	}
	if r.maxAttempts == 0 {
		r.maxAttempts = 5
	}
	if r.recipient.Address == "" {
		r.recipient = delivery.Recipient{
			Consented: true, Address: "+9995000000001", Drafts: drafts,
			Values: map[content.Variable]string{content.VarFirstName: "Ayşe", content.VarCity: "İstanbul"},
		}
	}
	return &fakeStore{rows: map[delivery.ID]*row{deliveryID: &r}}
}

func (s *fakeStore) Claim(_ context.Context, id delivery.ID, _ time.Duration) (delivery.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || (r.status != delivery.StatusQueued && r.status != delivery.StatusRetrying) || r.attempts >= r.maxAttempts {
		return delivery.Job{}, false, nil
	}
	r.status = delivery.StatusProcessing
	r.attempts++
	return delivery.Job{ID: id, CampaignID: "c1", UserID: 7, Channel: r.channel, Attempt: r.attempts, MaxAttempts: r.maxAttempts}, true, nil
}

func (s *fakeStore) Recipient(_ context.Context, job delivery.Job) (delivery.Recipient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recipientErr != nil {
		return delivery.Recipient{}, s.recipientErr
	}
	return s.rows[job.ID].recipient, nil
}

func (s *fakeStore) Record(_ context.Context, job delivery.Job, o delivery.Outcome) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[job.ID]
	if s.loseClaim {
		r.attempts++ // another worker re-claimed after the lease expired
	}
	if r.status != delivery.StatusProcessing || r.attempts != job.Attempt {
		return false, nil
	}
	r.status, r.outcome = o.Status, o
	if o.Release {
		r.attempts--
	}
	return true, s.completeErr
}

func (s *fakeStore) row() row {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.rows[deliveryID]
}

// fakeProvider returns scripted results in order; the last one repeats.
type fakeProvider struct {
	mu      sync.Mutex
	results []error
	sent    []delivery.Message
	// block makes Send wait for ctx to end.
	block bool
}

func (p *fakeProvider) Send(ctx context.Context, msg delivery.Message) (string, error) {
	p.mu.Lock()
	p.sent = append(p.sent, msg)
	block := p.block
	var err error
	if n := len(p.results); n > 0 {
		err = p.results[min(len(p.sent), n)-1]
	}
	p.mu.Unlock()
	if block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return "msg-1", nil
}

func (p *fakeProvider) calls() []delivery.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]delivery.Message(nil), p.sent...)
}

type fakeLimiter struct {
	mu    sync.Mutex
	waits []content.Channel
}

func (l *fakeLimiter) Wait(ctx context.Context, c content.Channel) error {
	l.mu.Lock()
	l.waits = append(l.waits, c)
	l.mu.Unlock()
	return ctx.Err()
}

func newProcessor(store delivery.Store, p delivery.Provider, l delivery.RateLimiter) *delivery.Processor {
	return delivery.NewProcessor(store, l, p, delivery.ProcessorConfig{
		Lease:  time.Minute,
		Policy: delivery.RetryPolicy{BaseDelay: time.Second, Factor: 2, MaxDelay: time.Minute},
		Rand:   func() float64 { return 0.5 },
	}, slog.New(slog.DiscardHandler))
}

func transient(code string, retryAfter time.Duration) error {
	return &delivery.ProviderError{Code: code, Transient: true, RetryAfter: retryAfter}
}

func TestProcess_Sent(t *testing.T) {
	store := newStore(row{})
	prov, lim := &fakeProvider{}, &fakeLimiter{}
	if err := newProcessor(store, prov, lim).Process(t.Context(), deliveryID); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	got := store.row()
	if got.status != delivery.StatusSent || got.outcome.ProviderMessageID != "msg-1" || got.attempts != 1 {
		t.Errorf("row = %+v, want sent with provider id after 1 attempt", got)
	}
	calls := prov.calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	want := delivery.Message{IdempotencyKey: deliveryID, Channel: content.ChannelSMS, To: "+9995000000001",
		Body: "Merhaba Ayşe, %30 indirim seni bekliyor!"}
	if calls[0] != want {
		t.Errorf("sent %+v, want %+v", calls[0], want)
	}
	if len(lim.waits) != 1 || lim.waits[0] != content.ChannelSMS {
		t.Errorf("rate limiter waits = %v, want one for sms", lim.waits)
	}
}

func TestProcess_EmailRendersSubjectAndBody(t *testing.T) {
	r := newStore(row{}).row().recipient
	r.Address = "ayse@example.com"
	store := newStore(row{channel: content.ChannelEmail, recipient: r})
	prov := &fakeProvider{}
	if err := newProcessor(store, prov, &fakeLimiter{}).Process(t.Context(), deliveryID); err != nil {
		t.Fatal(err)
	}
	if c := prov.calls(); len(c) != 1 || c[0].Subject != "Ayşe, sepetin hazır" || c[0].Body != "Merhaba Ayşe" || c[0].To != "ayse@example.com" {
		t.Errorf("sent %+v", c)
	}
}

func TestProcess_NotClaimableIsSkipped(t *testing.T) {
	for _, status := range []delivery.Status{delivery.StatusProcessing, delivery.StatusSent, delivery.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			store := newStore(row{status: status})
			prov, lim := &fakeProvider{}, &fakeLimiter{}
			if err := newProcessor(store, prov, lim).Process(t.Context(), deliveryID); err != nil {
				t.Fatalf("Process() error = %v", err)
			}
			if len(prov.calls()) != 0 || len(lim.waits) != 0 || store.row().status != status {
				t.Errorf("duplicate was processed: row %+v, calls %d", store.row(), len(prov.calls()))
			}
		})
	}
}

func TestProcess_FailsWithoutSending(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*delivery.Recipient)
		code   string
	}{
		{"consent revoked", func(r *delivery.Recipient) { r.Consented = false }, delivery.CodeConsentRevoked},
		{"over limit after personalization", func(r *delivery.Recipient) {
			r.Values[content.VarFirstName] = strings.Repeat("Ğ", 130)
		}, delivery.CodeInvalidContent},
		{"placeholder left by a value", func(r *delivery.Recipient) {
			r.Values[content.VarFirstName] = "{{city}}"
		}, delivery.CodeInvalidContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(row{})
			tt.mutate(&store.rows[deliveryID].recipient)
			prov := &fakeProvider{}
			if err := newProcessor(store, prov, &fakeLimiter{}).Process(t.Context(), deliveryID); err != nil {
				t.Fatal(err)
			}
			if got := store.row(); got.status != delivery.StatusFailed || got.outcome.ErrorCode != tt.code {
				t.Errorf("row = %+v, want failed(%s)", got, tt.code)
			}
			if len(prov.calls()) != 0 {
				t.Errorf("provider was called")
			}
		})
	}
}

func TestProcess_ProviderErrors(t *testing.T) {
	tests := []struct {
		name        string
		attempts    int // before this claim
		err         error
		wantStatus  delivery.Status
		wantCode    string
		wantRetryIn time.Duration
	}{
		{"5xx retried with backoff", 0, transient(delivery.CodeUnavailable, 0),
			delivery.StatusRetrying, delivery.CodeUnavailable, 500 * time.Millisecond},
		{"backoff grows with attempts", 2, transient(delivery.CodeTimeout, 0),
			delivery.StatusRetrying, delivery.CodeTimeout, 2 * time.Second},
		{"429 waits at least Retry-After", 0, transient(delivery.CodeRateLimited, 7*time.Second),
			delivery.StatusRetrying, delivery.CodeRateLimited, 7 * time.Second},
		{"last attempt exhausts retries", 4, transient(delivery.CodeUnavailable, 0),
			delivery.StatusFailed, delivery.CodeRetriesExhausted, 0},
		{"permanent error is not retried", 0, &delivery.ProviderError{Code: delivery.CodeRejected},
			delivery.StatusFailed, delivery.CodeRejected, 0},
		{"unclassified error is transient", 0, errors.New("boom"),
			delivery.StatusRetrying, delivery.CodeInternal, 500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(row{attempts: tt.attempts})
			prov := &fakeProvider{results: []error{tt.err}}
			if err := newProcessor(store, prov, &fakeLimiter{}).Process(t.Context(), deliveryID); err != nil {
				t.Fatal(err)
			}
			got := store.row()
			if got.status != tt.wantStatus || got.outcome.ErrorCode != tt.wantCode || got.outcome.RetryIn != tt.wantRetryIn {
				t.Errorf("row = %+v, want %s(%s) retry in %v", got, tt.wantStatus, tt.wantCode, tt.wantRetryIn)
			}
			if got.attempts != tt.attempts+1 {
				t.Errorf("attempts = %d, want %d", got.attempts, tt.attempts+1)
			}
		})
	}
}

func TestProcess_RecipientReadErrorRetries(t *testing.T) {
	store := newStore(row{})
	store.recipientErr = errors.New("connection reset")
	prov := &fakeProvider{}
	if err := newProcessor(store, prov, &fakeLimiter{}).Process(t.Context(), deliveryID); err != nil {
		t.Fatal(err)
	}
	if got := store.row(); got.status != delivery.StatusRetrying || got.outcome.ErrorCode != delivery.CodeInternal {
		t.Errorf("row = %+v, want retrying(internal_error)", got)
	}
	if len(prov.calls()) != 0 {
		t.Error("provider was called")
	}
}

func TestProcess_ShutdownReleasesTheAttempt(t *testing.T) {
	t.Run("before the rate limit is granted", func(t *testing.T) {
		store := newStore(row{attempts: 4})
		prov := &fakeProvider{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := newProcessor(store, prov, &fakeLimiter{}).Process(ctx, deliveryID); err != nil {
			t.Fatal(err)
		}
		got := store.row()
		if got.status != delivery.StatusRetrying || got.outcome.ErrorCode != delivery.CodeInterrupted || got.attempts != 4 {
			t.Errorf("row = %+v, want retrying(interrupted) with the attempt given back", got)
		}
		if len(prov.calls()) != 0 {
			t.Error("provider was called")
		}
	})
	t.Run("during the provider call", func(t *testing.T) {
		store := newStore(row{})
		prov := &fakeProvider{block: true}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- newProcessor(store, prov, &fakeLimiter{}).Process(ctx, deliveryID) }()
		for len(prov.calls()) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		// The provider may have accepted the message; the retry reuses the
		// idempotency key, so releasing the attempt cannot cause a double send.
		if got := store.row(); got.status != delivery.StatusRetrying || got.outcome.RetryIn != 0 || got.attempts != 0 {
			t.Errorf("row = %+v, want an immediate retry with the attempt given back", got)
		}
	})
}

func TestProcess_LostClaimIsNotAnError(t *testing.T) {
	store := newStore(row{})
	store.loseClaim = true
	if err := newProcessor(store, &fakeProvider{}, &fakeLimiter{}).Process(t.Context(), deliveryID); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got := store.row(); got.status != delivery.StatusProcessing {
		t.Errorf("stale worker overwrote the new claim: %+v", got)
	}
}

func TestProcess_CompletionCheckFailureIsNotRetried(t *testing.T) {
	store := newStore(row{})
	store.completeErr = errors.New("connection reset")
	if err := newProcessor(store, &fakeProvider{}, &fakeLimiter{}).Process(t.Context(), deliveryID); err != nil {
		t.Fatalf("Process() error = %v, want nil: the outcome is stored and maintenance completes the campaign", err)
	}
	if got := store.row(); got.status != delivery.StatusSent {
		t.Errorf("row = %+v, want sent", got)
	}
}
