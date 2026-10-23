package campaign

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/segment"
)

// ID is a campaign's UUID in canonical lower-case form.
type ID string

var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseID validates s as a UUID and returns it in canonical form.
func ParseID(s string) (ID, error) {
	s = strings.ToLower(s)
	if !idPattern.MatchString(s) {
		return "", errors.New("campaign id must be a UUID")
	}
	return ID(s), nil
}

// Status is a campaign's lifecycle state. The backend is its only source
// of truth; clients display it.
type Status string

// Statuses, in lifecycle order. Milestone 5 adds sending states after
// approved.
const (
	// StatusDraft: created, no audience yet.
	StatusDraft Status = "draft"
	// StatusAudienceReady: a validated segment and its counts are stored.
	StatusAudienceReady Status = "audience_ready"
	// StatusDraftsReady: validated drafts are stored and can be edited.
	StatusDraftsReady Status = "drafts_ready"
	// StatusApproved: a human approved the exact content; immutable.
	StatusApproved Status = "approved"
)

// transitions lists the legal status changes. drafts_ready → drafts_ready
// is a regeneration, which replaces the drafts.
var transitions = map[Status][]Status{
	StatusDraft:         {StatusAudienceReady},
	StatusAudienceReady: {StatusDraftsReady},
	StatusDraftsReady:   {StatusDraftsReady, StatusApproved},
}

// CanTransition reports whether a campaign may move from one status to
// another.
func CanTransition(from, to Status) bool {
	return slices.Contains(transitions[from], to)
}

// Campaign is the aggregate the service loads, changes, and saves.
type Campaign struct {
	ID     ID
	Brief  string
	Status Status

	// Audience proposal, fixed at creation.
	Rule                segment.Rule
	Explanation         string
	UnsupportedCriteria []string
	Counts              segment.Counts

	// Reviewed content. Drafts is empty before drafts_ready.
	Channels []content.Channel
	Drafts   content.Drafts

	// Version increases with every content, channel, or status change and
	// is the optimistic lock for edits and approval.
	Version int
	// ContentHash binds approval to the content a reviewer saw; see
	// ContentHash.
	ContentHash string

	// A non-empty GenerationToken marks a draft generation in progress
	// until GenerationExpiresAt.
	GenerationToken     string
	GenerationExpiresAt time.Time

	ApprovedBy string
	ApprovedAt time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasDrafts reports whether the campaign has reviewable content.
func (c Campaign) HasDrafts() bool {
	return c.Status == StatusDraftsReady || c.Status == StatusApproved
}

// Generating reports whether a draft generation holds the campaign at now.
func (c Campaign) Generating(now time.Time) bool {
	return c.GenerationToken != "" && now.Before(c.GenerationExpiresAt)
}

// Validation is the content check shown to reviewers: the selected
// channels' fields against the channel rules, with each field's
// worst-case length.
type Validation struct {
	Valid     bool
	Errors    []content.FieldError
	WorstCase content.WorstCase
}

// Validate checks the campaign's content statically (without the
// audience). Approval additionally checks the real audience.
func (c Campaign) Validate() Validation {
	wc, errs := content.ValidateChannels(c.Drafts, c.Channels)
	return Validation{Valid: len(errs) == 0, Errors: errs, WorstCase: wc}
}

// Event actions recorded in the audit log.
const (
	ActionCreated          = "created"
	ActionAudienceProposed = "audience_proposed"
	ActionDraftsGenerated  = "drafts_generated"
	ActionEdited           = "edited"
	ActionApproved         = "approved"
)

// Actors for events not attributed to a named operator. There is no
// authentication, so edits cannot name a person.
const (
	ActorSystem   = "system"
	ActorMarketer = "marketer"
)

// Event is one audit record: who did what, and the status change. It
// never holds message content or recipient data.
type Event struct {
	Action  string
	Actor   string
	From    Status
	To      Status
	Version int
}

// contentHashVersion prefixes the canonical encoding so it can evolve
// without silently matching old hashes.
const contentHashVersion = 1

// hashInput is the canonical encoding: a struct with fixed field order,
// the normalized rule, and channels in canonical order.
type hashInput struct {
	V            int               `json:"v"`
	Rule         segment.Rule      `json:"rule"`
	Channels     []content.Channel `json:"channels"`
	SMSBody      string            `json:"sms_body"`
	EmailSubject string            `json:"email_subject"`
	EmailBody    string            `json:"email_body"`
}

// ContentHash is the hex SHA-256 of everything an approval covers: the
// segment rule, the selected channels, and the message content. Brief,
// counts, version, and status are not part of it.
func ContentHash(c Campaign) string {
	channels := c.Channels
	if channels == nil {
		channels = []content.Channel{}
	}
	conditions := c.Rule.Conditions
	if conditions == nil {
		conditions = []segment.Condition{}
	}
	in := hashInput{
		V:            contentHashVersion,
		Rule:         segment.Rule{Version: c.Rule.Version, Conditions: conditions},
		Channels:     channels,
		SMSBody:      c.Drafts.SMS.Body,
		EmailSubject: c.Drafts.Email.Subject,
		EmailBody:    c.Drafts.Email.Body,
	}
	b, err := json.Marshal(in)
	if err != nil {
		// Only a Rule that did not come from segment.Parse can fail to
		// marshal; hash its error text so such a rule never matches.
		b = []byte(err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
