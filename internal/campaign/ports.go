package campaign

import (
	"context"
	"errors"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// Store persists campaigns and their audit events.
type Store interface {
	// Create inserts c (ID and timestamps are assigned) and its events in
	// one transaction.
	Create(ctx context.Context, c Campaign, events []Event) (Campaign, error)
	// Get returns the campaign or ErrNotFound.
	Get(ctx context.Context, id ID) (Campaign, error)
	// List returns up to limit campaigns, newest first.
	List(ctx context.Context, limit int) ([]Campaign, error)
	// Update locks the campaign, passes it to fn, and persists fn's Write
	// together with its events in one transaction. A nil Write persists
	// nothing. fn's error aborts the transaction and is returned as is.
	// Update returns the campaign as stored afterwards, or ErrNotFound.
	Update(ctx context.Context, id ID, fn UpdateFunc) (Campaign, error)
}

// UpdateFunc decides a change to a locked campaign. audience reads the
// audience inside the same transaction.
type UpdateFunc func(ctx context.Context, audience AudienceInspector, c Campaign) (*Write, error)

// Write is a change decided by an UpdateFunc.
type Write struct {
	Campaign Campaign
	Events   []Event
	// Deliveries, when set, creates the campaign's deliveries and queues
	// them in the same transaction.
	Deliveries *DeliveryPlan
}

// DeliveryPlan materializes an approved campaign's audience: one delivery
// per user matching Predicate who granted consent for the channel, for
// each channel, with its queue message in the outbox. Existing deliveries
// are kept, so applying a plan twice creates nothing new.
type DeliveryPlan struct {
	Predicate   segment.Predicate
	Channels    []content.Channel
	MaxAttempts int
}

// Store errors.
var (
	ErrNotFound = errors.New("campaign not found")
	// ErrConflict means the stored version changed under the lock, which
	// only a store bug or a manual edit can cause.
	ErrConflict = errors.New("campaign version changed concurrently")
)

// AudienceInspector reads a channel's actual audience: users matching the
// predicate who granted consent for that channel. Implementations own the
// consent filter.
type AudienceInspector interface {
	InspectAudience(ctx context.Context, p segment.Predicate, ch content.Channel, samples int) (ChannelAudience, error)
}

// ChannelAudience describes one channel's consented recipients.
type ChannelAudience struct {
	Recipients int64
	// Longest maps each variable to its longest value, in runes, among the
	// recipients.
	Longest map[content.Variable]int
	// Samples holds up to the requested number of recipients, including
	// the one with the longest first name.
	Samples []Recipient
}

// Recipient is a fake user's personalization data. It never leaves the
// API except in previews, and is never logged.
type Recipient struct {
	UserID int64
	Values map[content.Variable]string
}

// AIGenerator produces validated proposals. *Generator implements it.
type AIGenerator interface {
	ProposeSegment(ctx context.Context, brief string) (SegmentProposal, error)
	GenerateDrafts(ctx context.Context, brief, audienceSummary string, onDelta func(Delta)) (DraftsResult, error)
}
