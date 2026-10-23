package mockllm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm"
)

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func TestClient_GenerateDrafts_StreamsDeterministically(t *testing.T) {
	run := func() (string, []string) {
		var deltas []string
		out, err := New().GenerateDrafts(t.Context(), llm.DraftRequest{Brief: exampleBrief}, func(s string) {
			deltas = append(deltas, s)
		})
		if err != nil {
			t.Fatalf("GenerateDrafts() error = %v", err)
		}
		return out, deltas
	}
	out1, deltas1 := run()
	out2, deltas2 := run()

	if out1 != out2 || strings.Join(deltas1, "|") != strings.Join(deltas2, "|") {
		t.Error("two runs differ")
	}
	if len(deltas1) < 2 {
		t.Errorf("got %d deltas, want the output streamed in several chunks", len(deltas1))
	}
	if strings.Join(deltas1, "") != out1 {
		t.Error("deltas do not reassemble the output")
	}
	if strings.Contains(out1, `\u00`) {
		t.Error("output is HTML-escaped")
	}
}

func TestClient_OutputsAreValid(t *testing.T) {
	// The mock's normal output must pass the real validators, otherwise
	// the no-API-key mode would be unusable.
	d := GenerateDrafts(exampleBrief)
	if _, err := content.Validate(d); err != nil {
		t.Errorf("turkish drafts invalid: %v", err)
	}
	d = GenerateDrafts("Remind cart abandoners, 15% discount, friendly tone.")
	if _, err := content.Validate(d); err != nil {
		t.Errorf("english drafts invalid: %v", err)
	}
}

func TestClient_Script(t *testing.T) {
	c := New(Step{Fault: FaultRefusal}, Step{Fault: FaultAPIError}, Step{Fault: FaultIncomplete}, Step{Output: "raw"})
	ctx := t.Context()

	if _, err := c.ExtractSegment(ctx, llm.SegmentRequest{Brief: "x"}); !errors.Is(err, llm.ErrRefused) {
		t.Errorf("call 1 error = %v, want ErrRefused", err)
	}
	if _, err := c.GenerateDrafts(ctx, llm.DraftRequest{Brief: "x"}, nil); !errors.Is(err, llm.ErrUnavailable) {
		t.Errorf("call 2 error = %v, want ErrUnavailable", err)
	}
	if _, err := c.ExtractSegment(ctx, llm.SegmentRequest{Brief: "x"}); !errors.Is(err, llm.ErrIncomplete) {
		t.Errorf("call 3 error = %v, want ErrIncomplete", err)
	}
	if out, err := c.ExtractSegment(ctx, llm.SegmentRequest{Brief: "x"}); err != nil || out != "raw" {
		t.Errorf("call 4 = %q, %v; want raw output", out, err)
	}
	if out, err := c.ExtractSegment(ctx, llm.SegmentRequest{Brief: exampleBrief}); err != nil || !strings.Contains(out, "İstanbul") {
		t.Errorf("call 5 = %q, %v; want normal output after the script", out, err)
	}
	if got := len(c.Calls()); got != 5 {
		t.Errorf("recorded %d calls, want 5", got)
	}
}

func TestClient_HangStopsOnContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, err := New(Step{Fault: FaultHang}).GenerateDrafts(ctx, llm.DraftRequest{Brief: "x"}, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want DeadlineExceeded", err)
		}
	})
}

func TestClient_CancelledWhileStreaming(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var deltas int
	_, err := New().GenerateDrafts(ctx, llm.DraftRequest{Brief: exampleBrief}, func(string) {
		deltas++
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want Canceled", err)
	}
	if deltas != 1 {
		t.Errorf("got %d deltas after cancel, want 1", deltas)
	}
}
