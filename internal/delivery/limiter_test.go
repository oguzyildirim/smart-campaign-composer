package delivery

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
)

// scriptedBucket returns the scripted waits in order, then grants.
type scriptedBucket struct {
	mu       sync.Mutex
	waits    []time.Duration
	calls    int
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	err      error
}

func (b *scriptedBucket) Take(_ context.Context, _ content.Channel) (time.Duration, error) {
	n := b.inFlight.Add(1)
	defer b.inFlight.Add(-1)
	for {
		seen := b.maxSeen.Load()
		if n <= seen || b.maxSeen.CompareAndSwap(seen, n) {
			break
		}
	}
	time.Sleep(time.Millisecond) // widen the window for overlapping calls
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return 0, b.err
	}
	if len(b.waits) == 0 {
		return 0, nil
	}
	w := b.waits[0]
	b.waits = b.waits[1:]
	return w, nil
}

func TestLimiter_WaitsUntilGranted(t *testing.T) {
	b := &scriptedBucket{waits: []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}}
	l := NewLimiter(b)
	start := time.Now()
	if err := l.Wait(t.Context(), content.ChannelSMS); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if b.calls != 3 {
		t.Errorf("bucket calls = %d, want 3", b.calls)
	}
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Errorf("Wait() returned after %v, before the reported waits", elapsed)
	}
}

func TestLimiter_StopsOnCancellation(t *testing.T) {
	b := &scriptedBucket{waits: []time.Duration{time.Hour}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := NewLimiter(b).Wait(ctx, content.ChannelSMS); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want deadline exceeded", err)
	}
}

func TestLimiter_PropagatesBucketErrors(t *testing.T) {
	b := &scriptedBucket{err: errors.New("db down")}
	if err := NewLimiter(b).Wait(t.Context(), content.ChannelEmail); err == nil {
		t.Fatal("Wait() error = nil, want the bucket error")
	}
}

func TestLimiter_OneBucketRequestPerChannelAtATime(t *testing.T) {
	b := &scriptedBucket{}
	l := NewLimiter(b)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := l.Wait(t.Context(), content.ChannelSMS); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := b.maxSeen.Load(); got != 1 {
		t.Errorf("concurrent bucket requests for one channel = %d, want 1", got)
	}
}
