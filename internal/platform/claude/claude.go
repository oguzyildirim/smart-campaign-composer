// Package claude adapts the Claude API (anthropic-sdk-go) to llm.Client.
//
// Every call uses structured outputs (output_config.format with a JSON
// schema), a fixed system prompt, and the brief as delimited data. The
// adapter checks the stop reason before reading content and maps failures
// to the llm package's errors. It logs model, latency, token usage, stop
// reason, and outcome — never the API key, the brief, or generated text.
package claude

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/oguzyildirim/smart-campaign-composer/internal/llm"
)

// Per-call settings. Extraction is a small classification-like task, so it
// runs at low effort; drafts are short creative writing, so medium. Both
// leave thinking at the model default (adaptive), and max_tokens leaves
// room for thinking on top of the small JSON answer.
const (
	segmentEffort    = anthropic.OutputConfigEffortLow
	segmentMaxTokens = 8000
	draftsEffort     = anthropic.OutputConfigEffortMedium
	draftsMaxTokens  = 16000
)

// Config configures the adapter.
type Config struct {
	APIKey string
	// Model is the model id, e.g. claude-sonnet-5-5 (see config).
	Model string
	// MaxRetries is the SDK's retry count for 408/409/429/5xx and
	// connection errors. The caller's context deadline bounds the total.
	MaxRetries int
	// BaseURL overrides the API endpoint; tests point it at httptest.
	BaseURL string
}

// Client calls the Claude API. It is safe for concurrent use.
type Client struct {
	api    anthropic.Client
	model  string
	logger *slog.Logger
}

var _ llm.Client = (*Client)(nil)

// New returns a Client. Credentials come only from cfg: environment
// variables and on-disk profiles the SDK would otherwise read are ignored.
func New(cfg Config, logger *slog.Logger) *Client {
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithHTTPClient(&http.Client{}), // deadlines come from the request context
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(cfg.MaxRetries),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Client{api: anthropic.NewClient(opts...), model: cfg.Model, logger: logger}
}

// ExtractSegment implements llm.Client with a single non-streaming call.
func (c *Client) ExtractSegment(ctx context.Context, req llm.SegmentRequest) (string, error) {
	const op = "extract_segment"
	params := c.params(segmentSystemPrompt, segmentUserMessage(req), segmentSchema, segmentEffort, segmentMaxTokens)
	start := time.Now()
	msg, err := c.api.Messages.New(ctx, params)
	if err != nil {
		return "", c.fail(ctx, op, start, err)
	}
	return c.finish(ctx, op, start, msg)
}

// GenerateDrafts implements llm.Client with a streaming call, forwarding
// text deltas to onDelta as they arrive.
func (c *Client) GenerateDrafts(ctx context.Context, req llm.DraftRequest, onDelta func(string)) (string, error) {
	const op = "generate_drafts"
	params := c.params(draftsSystemPrompt, draftsUserMessage(req), draftsSchema, draftsEffort, draftsMaxTokens)
	start := time.Now()
	stream := c.api.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	var msg anthropic.Message
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return "", c.fail(ctx, op, start, err)
		}
		if onDelta == nil {
			continue
		}
		if delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			if text, ok := delta.Delta.AsAny().(anthropic.TextDelta); ok {
				onDelta(text.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return "", c.fail(ctx, op, start, err)
	}
	return c.finish(ctx, op, start, &msg)
}

func (c *Client) params(system, user string, schema map[string]any, effort anthropic.OutputConfigEffort, maxTokens int64) anthropic.MessageNewParams {
	return anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: effort,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	}
}

// finish checks the stop reason before reading content: a refusal or a
// truncated response is an error even if it carries text.
func (c *Client) finish(ctx context.Context, op string, start time.Time, msg *anthropic.Message) (string, error) {
	var err error
	switch msg.StopReason {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
	case anthropic.StopReasonRefusal:
		err = fmt.Errorf("claude %s: %w", op, llm.ErrRefused)
	default: // max_tokens, model_context_window_exceeded, or anything unexpected
		err = fmt.Errorf("claude %s: stop reason %q: %w", op, msg.StopReason, llm.ErrIncomplete)
	}

	var text strings.Builder
	if err == nil {
		for _, block := range msg.Content {
			if b, ok := block.AsAny().(anthropic.TextBlock); ok {
				text.WriteString(b.Text)
			}
		}
		if text.Len() == 0 {
			err = fmt.Errorf("claude %s: no text content: %w", op, llm.ErrIncomplete)
		}
	}

	outcome := "ok"
	switch {
	case errors.Is(err, llm.ErrRefused):
		outcome = "refused"
	case err != nil:
		outcome = "incomplete"
	}
	c.logger.LogAttrs(ctx, levelFor(err), "claude call",
		slog.String("operation", op),
		slog.String("model", c.model),
		slog.String("outcome", outcome),
		slog.String("stop_reason", string(msg.StopReason)),
		slog.Int64("input_tokens", msg.Usage.InputTokens),
		slog.Int64("output_tokens", msg.Usage.OutputTokens),
		slog.Float64("duration_ms", ms(start)),
	)
	if err != nil {
		return "", err
	}
	return text.String(), nil
}

// fail maps a transport or API error. Context errors are returned wrapped
// so the caller can tell cancellation and deadlines apart; everything else
// is llm.ErrUnavailable. The SDK error text is not propagated because it
// includes the raw response body.
func (c *Client) fail(ctx context.Context, op string, start time.Time, err error) error {
	attrs := []slog.Attr{
		slog.String("operation", op),
		slog.String("model", c.model),
		slog.Float64("duration_ms", ms(start)),
	}
	var out error
	apiErr, isAPI := errors.AsType[*anthropic.Error](err)
	switch {
	case ctx.Err() != nil:
		out = fmt.Errorf("claude %s: %w", op, ctx.Err())
		attrs = append(attrs, slog.String("outcome", "context_done"))
	case isAPI:
		out = fmt.Errorf("claude %s: status %d (request id %q): %w", op, apiErr.StatusCode, apiErr.RequestID, llm.ErrUnavailable)
		attrs = append(attrs, slog.String("outcome", "api_error"),
			slog.Int("status", apiErr.StatusCode), slog.String("request_id", apiErr.RequestID))
	default:
		out = fmt.Errorf("claude %s: %w: %w", op, llm.ErrUnavailable, err)
		attrs = append(attrs, slog.String("outcome", "transport_error"))
	}
	c.logger.LogAttrs(ctx, slog.LevelWarn, "claude call", attrs...)
	return out
}

func levelFor(err error) slog.Level {
	if err != nil {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

func ms(start time.Time) float64 { return float64(time.Since(start).Microseconds()) / 1000 }
