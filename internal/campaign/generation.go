package campaign

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

const (
	// leaseSlack keeps the lease alive past GenerationTimeout so it never
	// expires while its generation can still save.
	leaseSlack = 15 * time.Second
	// cleanupTimeout bounds saving or releasing after the client left.
	cleanupTimeout = 5 * time.Second
)

// Generation is a draft generation that holds the campaign's lease. Run it
// exactly once.
type Generation struct {
	s       *Service
	id      ID
	token   string
	brief   string
	summary string
}

// BeginGeneration takes the campaign's generation lease, so only one
// generation runs per campaign at a time (CodeGenerationInProgress
// otherwise). The lease is stored, so it holds across API instances, and
// expires on its own if the process dies.
func (s *Service) BeginGeneration(ctx context.Context, id ID) (*Generation, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	var g *Generation
	_, err = s.update(ctx, id, func(_ context.Context, _ AudienceInspector, c Campaign) (*Write, error) {
		now := s.now()
		switch {
		case !canGenerate(c.Status):
			return nil, fail(CodeInvalidState, "drafts can only be generated after the audience is ready and before approval")
		case c.Generating(now):
			return nil, fail(CodeGenerationInProgress, "")
		}
		c.GenerationToken = token
		c.GenerationExpiresAt = now.Add(s.cfg.GenerationTimeout + leaseSlack)
		g = &Generation{s: s, id: id, token: token, brief: c.Brief, summary: strings.Join(segment.Describe(c.Rule), "; ")}
		return &Write{Campaign: c}, nil
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// canGenerate reports whether drafts may be generated in status: only
// before approval. A cancel also leads to drafts_ready, but generation
// must not be a way out of an approval.
func canGenerate(s Status) bool {
	return s == StatusAudienceReady || s == StatusDraftsReady
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generation token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Run generates drafts, streaming field text to onDelta (display only),
// and stores them once they pass validation, moving the campaign to
// drafts_ready. On any failure, including cancellation by the caller, the
// campaign keeps its previous content and status and the lease is
// released.
func (g *Generation) Run(ctx context.Context, onDelta func(FieldDelta)) (Campaign, error) {
	genCtx, cancel := context.WithTimeout(ctx, g.s.cfg.GenerationTimeout)
	defer cancel()

	var x *fieldExtractor
	attempt := 0
	res, err := g.s.gen.GenerateDrafts(genCtx, g.brief, g.summary, func(d Delta) {
		if onDelta == nil {
			return
		}
		if d.Attempt != attempt {
			attempt, x = d.Attempt, newFieldExtractor()
		}
		for _, fd := range x.Feed(d.Text) {
			fd.Attempt = attempt
			onDelta(fd)
		}
	})

	// Saving and releasing must happen even if the client has gone.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cleanupCancel()
	if err != nil {
		g.release(cleanupCtx)
		return Campaign{}, err
	}
	c, err := g.s.update(cleanupCtx, g.id, func(_ context.Context, _ AudienceInspector, c Campaign) (*Write, error) {
		// An expired lease may have let an edit or another generation in;
		// saving now would silently overwrite it.
		if c.GenerationToken != g.token || !c.Generating(g.s.now()) {
			return nil, fail(CodeInvalidState, "the generation lease expired before the drafts were ready")
		}
		if !canGenerate(c.Status) {
			return nil, fail(CodeInvalidState, "")
		}
		from := c.Status
		c.Status, c.Drafts = StatusDraftsReady, res.Drafts
		c.GenerationToken, c.GenerationExpiresAt = "", time.Time{}
		c.Version++
		c.ContentHash = ContentHash(c)
		return &Write{Campaign: c, Events: []Event{{
			Action: ActionDraftsGenerated, Actor: ActorSystem, From: from, To: c.Status, Version: c.Version,
		}}}, nil
	})
	if err != nil {
		g.release(cleanupCtx)
		return Campaign{}, err
	}
	return c, nil
}

// release clears the lease if this generation still holds it.
func (g *Generation) release(ctx context.Context) {
	_, err := g.s.update(ctx, g.id, func(_ context.Context, _ AudienceInspector, c Campaign) (*Write, error) {
		if c.GenerationToken != g.token {
			return nil, nil
		}
		c.GenerationToken, c.GenerationExpiresAt = "", time.Time{}
		return &Write{Campaign: c}, nil
	})
	if err != nil {
		// The lease expires on its own; log so a stuck campaign is explainable.
		g.s.logger.WarnContext(ctx, "release generation lease failed", "campaign_id", string(g.id), "error", err)
	}
}
