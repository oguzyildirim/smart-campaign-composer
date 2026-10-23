// Package monitoring is the read model of campaign delivery progress: an
// aggregate snapshot of a campaign's deliveries, and a hub that polls it
// once per campaign for every live viewer.
//
// Snapshots are full state, never increments, so a client that reconnects
// or receives the same snapshot twice can only display correct numbers.
//
// The package has no HTTP or SQL dependencies.
package monitoring

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/oguzyildirim/smart-campaign-composer/internal/campaign"
	"github.com/oguzyildirim/smart-campaign-composer/internal/content"
	"github.com/oguzyildirim/smart-campaign-composer/internal/delivery"
)

// StatusCounts counts deliveries by status.
type StatusCounts struct {
	Queued     int64
	Processing int64
	Sent       int64
	Failed     int64
	Retrying   int64
}

// Total is the number of deliveries counted.
func (c StatusCounts) Total() int64 {
	return c.Queued + c.Processing + c.Sent + c.Failed + c.Retrying
}

// Done is the number of terminal (sent or failed) deliveries.
func (c StatusCounts) Done() int64 { return c.Sent + c.Failed }

// PercentDone is the share of terminal deliveries, 0–100; 0 without
// deliveries.
func (c StatusCounts) PercentDone() float64 {
	if c.Total() == 0 {
		return 0
	}
	return float64(c.Done()) * 100 / float64(c.Total())
}

// Add counts n deliveries in status s. Unknown statuses are ignored.
func (c *StatusCounts) Add(s delivery.Status, n int64) {
	switch s {
	case delivery.StatusQueued:
		c.Queued += n
	case delivery.StatusProcessing:
		c.Processing += n
	case delivery.StatusSent:
		c.Sent += n
	case delivery.StatusFailed:
		c.Failed += n
	case delivery.StatusRetrying:
		c.Retrying += n
	}
}

func (c StatusCounts) plus(o StatusCounts) StatusCounts {
	return StatusCounts{
		Queued:     c.Queued + o.Queued,
		Processing: c.Processing + o.Processing,
		Sent:       c.Sent + o.Sent,
		Failed:     c.Failed + o.Failed,
		Retrying:   c.Retrying + o.Retrying,
	}
}

// ChannelProgress is one channel's deliveries.
type ChannelProgress struct {
	Channel content.Channel
	Counts  StatusCounts
	// RecentSent counts deliveries sent within the snapshot's window.
	RecentSent int64
}

// FailureCount is the number of failed deliveries with one error code.
type FailureCount struct {
	Code  string
	Count int64
}

// Snapshot is a campaign's delivery progress at one moment.
type Snapshot struct {
	CampaignID     campaign.ID
	CampaignStatus campaign.Status
	// Channels lists the campaign's channels in a fixed order (sms, email).
	Channels []ChannelProgress
	// Failures lists failed deliveries by error code, most frequent first.
	Failures []FailureCount
	// Window is the period RecentSent covers.
	Window      time.Duration
	GeneratedAt time.Time
}

// Totals sums the channels' counts.
func (s Snapshot) Totals() StatusCounts {
	var t StatusCounts
	for _, ch := range s.Channels {
		t = t.plus(ch.Counts)
	}
	return t
}

// RecentSent sums the channels' recent sends.
func (s Snapshot) RecentSent() int64 {
	var n int64
	for _, ch := range s.Channels {
		n += ch.RecentSent
	}
	return n
}

// Throughput is the send rate over the window, in messages per second.
func (s Snapshot) Throughput() float64 {
	if s.Window <= 0 {
		return 0
	}
	return float64(s.RecentSent()) / s.Window.Seconds()
}

// PercentDone is the share of terminal deliveries, 0–100. A completed
// campaign without deliveries is 100% done.
func (s Snapshot) PercentDone() float64 {
	t := s.Totals()
	if t.Total() == 0 && s.Completed() {
		return 100
	}
	return t.PercentDone()
}

// Completed reports whether the campaign finished sending: no snapshot
// after this one can differ.
func (s Snapshot) Completed() bool { return s.CampaignStatus == campaign.StatusCompleted }

// SameState reports whether s and o describe the same progress, ignoring
// when they were generated.
func (s Snapshot) SameState(o Snapshot) bool {
	return s.CampaignID == o.CampaignID && s.CampaignStatus == o.CampaignStatus && s.Window == o.Window &&
		slices.Equal(s.Channels, o.Channels) && slices.Equal(s.Failures, o.Failures)
}

// Source reads a campaign's progress in one consistent read. It returns
// campaign.ErrNotFound for an unknown campaign.
type Source interface {
	Progress(ctx context.Context, id campaign.ID, window time.Duration) (Snapshot, error)
}

// Row is one aggregate of a campaign's deliveries: how many have a channel
// and status (and, for failed ones, an error code), and how many of those
// were sent within the window.
type Row struct {
	Channel    content.Channel
	Status     delivery.Status
	ErrorCode  string
	Count      int64
	RecentSent int64
}

// UnknownFailure labels failed deliveries without an error code.
const UnknownFailure = "unknown"

var channelOrder = []content.Channel{content.ChannelSMS, content.ChannelEmail}

// Assemble builds a snapshot from aggregate rows. Channels are the
// campaign's selected channels plus any channel that has deliveries, in
// a fixed order, so a channel never disappears from a dashboard.
func Assemble(id campaign.ID, status campaign.Status, selected []content.Channel, rows []Row, window time.Duration, at time.Time) Snapshot {
	byChannel := map[content.Channel]*ChannelProgress{}
	for _, ch := range selected {
		byChannel[ch] = &ChannelProgress{Channel: ch}
	}
	failures := map[string]int64{}
	for _, r := range rows {
		cp := byChannel[r.Channel]
		if cp == nil {
			cp = &ChannelProgress{Channel: r.Channel}
			byChannel[r.Channel] = cp
		}
		cp.Counts.Add(r.Status, r.Count)
		cp.RecentSent += r.RecentSent
		if r.Status == delivery.StatusFailed {
			code := r.ErrorCode
			if code == "" {
				code = UnknownFailure
			}
			failures[code] += r.Count
		}
	}

	s := Snapshot{CampaignID: id, CampaignStatus: status, Window: window, GeneratedAt: at}
	for _, ch := range channelOrder {
		if cp := byChannel[ch]; cp != nil {
			s.Channels = append(s.Channels, *cp)
		}
	}
	for code, n := range failures {
		s.Failures = append(s.Failures, FailureCount{Code: code, Count: n})
	}
	slices.SortFunc(s.Failures, func(a, b FailureCount) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return strings.Compare(a.Code, b.Code)
	})
	return s
}
