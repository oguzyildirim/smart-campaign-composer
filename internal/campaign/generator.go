package campaign

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/llm"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// SegmentPreviewer validates a raw segment rule and counts its audience.
// *segment.Service implements it; using it means model-proposed rules go
// through exactly the same validator and consent-aware counting as rules
// written by hand.
type SegmentPreviewer interface {
	Preview(ctx context.Context, rule []byte) (segment.Rule, segment.Counts, error)
}

// GeneratorConfig bounds model calls.
type GeneratorConfig struct {
	// SegmentTimeout and DraftsTimeout bound each model call, including
	// the SDK's own retries.
	SegmentTimeout time.Duration
	DraftsTimeout  time.Duration
	// MaxAttempts is the total number of model calls per request: the
	// first call plus repair calls after invalid output.
	MaxAttempts int
}

// SegmentProposal is a validated segment rule with its audience size.
type SegmentProposal struct {
	Rule   segment.Rule
	Counts segment.Counts
	// UnsupportedCriteria lists parts of the brief the rule could not
	// express; the UI must show them so the marketer knows the audience
	// is broader than asked.
	UnsupportedCriteria []string
	Explanation         string
}

// DraftsResult is validated drafts with their worst-case lengths.
type DraftsResult struct {
	Drafts    content.Drafts
	WorstCase content.WorstCase
}

// Delta is a streamed fragment of raw model output, for display only.
// Attempt increases when a repair call starts, so a client can discard
// fragments of a rejected attempt.
type Delta struct {
	Attempt int
	Text    string
}

// Generator turns briefs into validated proposals. It is safe for
// concurrent use.
type Generator struct {
	llm      llm.Client
	segments SegmentPreviewer
	cfg      GeneratorConfig
	logger   *slog.Logger
}

// NewGenerator returns a Generator. MaxAttempts below 1 is treated as 1.
func NewGenerator(client llm.Client, segments SegmentPreviewer, cfg GeneratorConfig, logger *slog.Logger) *Generator {
	cfg.MaxAttempts = max(cfg.MaxAttempts, 1)
	return &Generator{llm: client, segments: segments, cfg: cfg, logger: logger}
}

// ProposeSegment asks the model for a segment rule, validates it, and
// counts its audience. A rule reaches the counter only after it passed
// segment validation.
func (g *Generator) ProposeSegment(ctx context.Context, brief string) (SegmentProposal, error) {
	brief, err := ValidateBrief(brief)
	if err != nil {
		return SegmentProposal{}, err
	}
	call := func(ctx context.Context, _ int, repair *llm.Repair) (string, error) {
		return g.llm.ExtractSegment(ctx, llm.SegmentRequest{Brief: brief, Repair: repair})
	}
	validate := func(ctx context.Context, raw string) (SegmentProposal, []string, error) {
		d, problems := decodeSegmentOutput(raw)
		if len(problems) > 0 {
			return SegmentProposal{}, problems, nil
		}
		if d.rule == nil {
			return SegmentProposal{}, nil, &Error{
				Code:                CodeUnsupportedBrief,
				Message:             messages[CodeUnsupportedBrief],
				UnsupportedCriteria: d.unsupported,
			}
		}
		rule, counts, err := g.segments.Preview(ctx, d.rule)
		if verr, ok := errors.AsType[*segment.ValidationError](err); ok {
			return SegmentProposal{}, ruleProblems(verr), nil
		}
		if err != nil {
			return SegmentProposal{}, nil, fmt.Errorf("preview proposed segment: %w", err)
		}
		return SegmentProposal{
			Rule:                rule,
			Counts:              counts,
			UnsupportedCriteria: d.unsupported,
			Explanation:         d.explanation,
		}, nil, nil
	}
	return generate(ctx, g, "extract_segment", g.cfg.SegmentTimeout, MaxSegmentOutputBytes, call, validate)
}

// GenerateDrafts asks the model for SMS and email drafts and validates
// them. audienceSummary, if non-empty, describes the audience in plain
// words for tone; it is passed to the model as data. onDelta, if non-nil,
// receives raw output fragments as they stream;
// they are capped at MaxDraftsOutputBytes per attempt and are never
// validated, so they must only be displayed.
func (g *Generator) GenerateDrafts(ctx context.Context, brief, audienceSummary string, onDelta func(Delta)) (DraftsResult, error) {
	brief, err := ValidateBrief(brief)
	if err != nil {
		return DraftsResult{}, err
	}
	call := func(ctx context.Context, attempt int, repair *llm.Repair) (string, error) {
		return g.llm.GenerateDrafts(ctx, llm.DraftRequest{Brief: brief, AudienceSummary: audienceSummary, Repair: repair}, cappedDeltas(attempt, onDelta))
	}
	validate := func(_ context.Context, raw string) (DraftsResult, []string, error) {
		res, problems := decodeDrafts(raw)
		return res, problems, nil
	}
	return generate(ctx, g, "generate_drafts", g.cfg.DraftsTimeout, MaxDraftsOutputBytes, call, validate)
}

// cappedDeltas forwards fragments to onDelta until MaxDraftsOutputBytes
// have been forwarded for this attempt.
func cappedDeltas(attempt int, onDelta func(Delta)) func(string) {
	if onDelta == nil {
		return nil
	}
	sent, stopped := 0, false
	return func(text string) {
		if stopped || sent+len(text) > MaxDraftsOutputBytes {
			stopped = true
			return
		}
		sent += len(text)
		onDelta(Delta{Attempt: attempt, Text: text})
	}
}

// generate runs the bounded call → validate → repair loop shared by both
// operations. validate returns problems for repairable output, or an
// error that ends the loop.
func generate[T any](
	ctx context.Context,
	g *Generator,
	op string,
	timeout time.Duration,
	maxBytes int,
	call func(ctx context.Context, attempt int, repair *llm.Repair) (string, error),
	validate func(ctx context.Context, raw string) (T, []string, error),
) (T, error) {
	var zero T
	var repair *llm.Repair
	for attempt := 1; ; attempt++ {
		start := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		raw, err := call(callCtx, attempt, repair)
		cancel()

		var problems []string
		switch {
		case err != nil:
			outcome, gerr := classifyCallError(ctx, op, err)
			g.logAttempt(ctx, op, attempt, start, outcome, nil)
			if gerr != nil {
				return zero, gerr
			}
			problems = []string{"$: the response ended before the JSON object was complete; return a shorter, complete object"}
		case len(raw) > maxBytes:
			problems = []string{fmt.Sprintf("$: the response exceeds %d bytes; return a shorter object", maxBytes)}
		default:
			result, p, verr := validate(ctx, raw)
			if verr != nil {
				outcome := "error"
				if gerr, ok := errors.AsType[*Error](verr); ok {
					outcome = string(gerr.Code)
				}
				g.logAttempt(ctx, op, attempt, start, outcome, nil)
				return zero, verr
			}
			if len(p) == 0 {
				g.logAttempt(ctx, op, attempt, start, "ok", nil)
				return result, nil
			}
			problems = p
		}

		if err == nil {
			g.logAttempt(ctx, op, attempt, start, "invalid_output", problems)
		}
		if attempt >= g.cfg.MaxAttempts {
			return zero, newError(CodeInvalidOutput,
				fmt.Errorf("%s: output rejected after %d attempts: %s", op, attempt, strings.Join(problems, "; ")))
		}
		repair = &llm.Repair{PreviousOutput: truncate(raw, maxBytes), Problems: problems}
	}
}

// classifyCallError maps a failed model call to a log outcome and, unless
// the failure is repairable (incomplete output), the error to return.
// Cancellation by the caller is returned as-is, not as a generation error.
func classifyCallError(ctx context.Context, op string, err error) (string, error) {
	if cerr := ctx.Err(); cerr != nil {
		if errors.Is(cerr, context.DeadlineExceeded) {
			return string(CodeTimeout), newError(CodeTimeout, err)
		}
		return "cancelled", fmt.Errorf("%s: %w", op, cerr)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return string(CodeTimeout), newError(CodeTimeout, err)
	case errors.Is(err, llm.ErrRefused):
		return string(CodeRefused), newError(CodeRefused, err)
	case errors.Is(err, llm.ErrIncomplete):
		return "incomplete", nil
	default:
		return string(CodeUpstreamUnavailable), newError(CodeUpstreamUnavailable, err)
	}
}

// logAttempt records one model call. It never logs the brief or the
// model's output; problems are validator messages, which never echo it.
func (g *Generator) logAttempt(ctx context.Context, op string, attempt int, start time.Time, outcome string, problems []string) {
	level := slog.LevelInfo
	if outcome != "ok" {
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{
		slog.String("operation", op),
		slog.Int("attempt", attempt),
		slog.String("outcome", outcome),
		slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
	}
	if len(problems) > 0 {
		attrs = append(attrs, slog.Any("problems", problems))
	}
	g.logger.LogAttrs(ctx, level, "llm attempt", attrs...)
}

// truncate cuts s to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
