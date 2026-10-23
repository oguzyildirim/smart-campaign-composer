package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
)

// SSE event names of the draft generation stream.
const (
	eventDelta = "delta"
	eventDone  = "done"
	eventError = "error"
)

// DeltaEvent is a fragment of one field's text, for display only. A new
// Attempt means a repair call started; discard text of earlier attempts.
type DeltaEvent struct {
	Attempt int                 `json:"attempt"`
	Field   campaign.DraftField `json:"field"`
	Text    string              `json:"text"`
}

// DoneEvent carries the validated, stored drafts.
type DoneEvent struct {
	Status      campaign.Status    `json:"status"`
	Drafts      DraftsBody         `json:"drafts"`
	Validation  ValidationResponse `json:"validation"`
	Version     int                `json:"version"`
	ContentHash string             `json:"content_hash"`
}

// StreamErrorEvent reports a failed generation; the campaign is unchanged.
type StreamErrorEvent struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// generate streams draft generation as Server-Sent Events over a POST
// response (read with fetch, not EventSource, so a dropped connection
// never silently starts another paid model call). Errors before the
// stream starts (unknown campaign, wrong state, generation already
// running) are ordinary JSON responses. Closing the connection cancels
// the request context and with it the model call.
func (h campaignHandler) generate(w http.ResponseWriter, r *http.Request) {
	id, ok := h.campaignID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	gen, err := h.svc.BeginGeneration(ctx, id)
	if err != nil {
		h.writeError(w, r, "start draft generation", err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)
	w.WriteHeader(http.StatusOK)
	stream := newSSEWriter(w)
	_ = stream.comment("generating") // flushes headers so the client sees the stream start

	stopHeartbeat := h.startHeartbeat(ctx, stream)
	c, err := gen.Run(ctx, func(d campaign.FieldDelta) {
		_ = stream.event(eventDelta, DeltaEvent{Attempt: d.Attempt, Field: d.Field, Text: d.Text})
	})
	stopHeartbeat()

	if err != nil {
		// After a cancellation the client is gone and reads nothing.
		_, body := h.failure(ctx, "draft generation", err, slog.String("campaign_id", string(id)))
		if !errors.Is(err, context.Canceled) {
			_ = stream.event(eventError, StreamErrorEvent{Code: body.Code, Message: body.Error})
		}
		return
	}
	h.logger.InfoContext(ctx, "drafts generated",
		"request_id", middleware.GetReqID(ctx), "campaign_id", string(id), "version", c.Version)
	_ = stream.event(eventDone, DoneEvent{
		Status:      c.Status,
		Drafts:      *draftsBody(c.Drafts),
		Validation:  validationResponse(c.Validate()),
		Version:     c.Version,
		ContentHash: c.ContentHash,
	})
}

// startHeartbeat writes a comment every h.heartbeat until the returned
// function is called; it returns after the goroutine has stopped.
func (h campaignHandler) startHeartbeat(ctx context.Context, stream *sseWriter) func() {
	if h.heartbeat <= 0 {
		return func() {}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		ticker := time.NewTicker(h.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = stream.comment("heartbeat")
			}
		}
	})
	return func() {
		close(stop)
		wg.Wait()
	}
}

// sseWriter writes Server-Sent Events and flushes each one. It is safe for
// concurrent use; after the first write error it writes nothing more.
type sseWriter struct {
	mu  sync.Mutex
	w   io.Writer
	rc  *http.ResponseController
	err error
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	return &sseWriter{w: w, rc: http.NewResponseController(w)}
}

// event writes one event. data is JSON-encoded on a single line, so it
// can never inject event fields.
func (s *sseWriter) event(name string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", name, err)
	}
	return s.write("event: " + name + "\ndata: " + string(payload) + "\n\n")
}

// comment writes an SSE comment line, which clients ignore.
func (s *sseWriter) comment(text string) error {
	return s.write(": " + text + "\n\n")
}

func (s *sseWriter) write(chunk string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if _, err := io.WriteString(s.w, chunk); err != nil {
		s.err = err
		return err
	}
	if err := s.rc.Flush(); err != nil {
		s.err = err
		return err
	}
	return nil
}
