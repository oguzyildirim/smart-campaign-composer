package delivery

import (
	"math/rand/v2"
	"testing"
	"time"
)

var testPolicy = RetryPolicy{BaseDelay: time.Second, Factor: 2, MaxDelay: 60 * time.Second}

func TestRetryPolicy_Ceiling(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second}, // 64s capped
		{40, 60 * time.Second},
		{1000, 60 * time.Second}, // no overflow
		{0, time.Second},
	}
	for _, tt := range tests {
		if got := testPolicy.Ceiling(tt.attempt); got != tt.want {
			t.Errorf("Ceiling(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestRetryPolicy_BackoffFullJitter(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for attempt := 1; attempt <= 8; attempt++ {
		ceiling := testPolicy.Ceiling(attempt)
		var lo, hi time.Duration = ceiling, 0
		for range 2000 {
			d := testPolicy.Backoff(attempt, rng.Float64())
			if d < 0 || d > ceiling {
				t.Fatalf("Backoff(%d) = %v, outside [0, %v]", attempt, d, ceiling)
			}
			lo, hi = min(lo, d), max(hi, d)
		}
		// Full jitter spreads over the whole range, not just near the top.
		if lo > ceiling/10 || hi < ceiling*9/10 {
			t.Errorf("attempt %d: jitter range [%v, %v] does not cover [0, %v]", attempt, lo, hi, ceiling)
		}
	}
	if got := testPolicy.Backoff(3, 0.5); got != 2*time.Second {
		t.Errorf("Backoff(3, 0.5) = %v, want 2s", got)
	}
}

func TestRetryPolicy_DelayRespectsRetryAfter(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter time.Duration
		rnd        float64
		want       time.Duration
	}{
		{"no header: backoff", 0, 0.5, 2 * time.Second},
		{"retry-after above backoff wins", 10 * time.Second, 0.5, 10 * time.Second},
		{"retry-after below backoff: backoff", time.Second, 1, 4 * time.Second},
		{"huge retry-after capped", 24 * time.Hour, 0.5, 60 * time.Second},
		{"negative retry-after ignored", -time.Second, 0.25, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := testPolicy.Delay(3, tt.retryAfter, tt.rnd); got != tt.want {
				t.Errorf("Delay = %v, want %v", got, tt.want)
			}
		})
	}
}
