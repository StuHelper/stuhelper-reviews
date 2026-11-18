package reviews

import (
	"context"
	"encoding/json"
	"time"
)

const OutboxChannel = "reviews_outbox"

// OutboxReader is a trusted relay port. The relay serializes each source/subject
// and confirms only after every subscribed consumer durably accepts the event.
// Exclude up to 1000 subjects currently backing off or awaiting manual handling.
type OutboxReader interface {
	PendingOutbox(context.Context, int, []string) ([]Envelope, error)
	ConfirmOutbox(context.Context, string) error
}

// OutboxStats reports unconfirmed outbox backlog count and the oldest pending event timestamp.
type OutboxStats struct {
	PendingCount    int64      `json:"pending_count"`
	OldestPendingAt *time.Time `json:"oldest_pending_at,omitempty"`
}

type OutboxInspector interface {
	OutboxMetrics(context.Context) (OutboxStats, error)
}

func (s *Service) OutboxMetrics(ctx context.Context) (OutboxStats, error) {
	port, ok := s.Repo.(OutboxInspector)
	if !ok {
		return OutboxStats{}, ErrUnavailable
	}
	start := time.Now()
	stats, err := port.OutboxMetrics(ctx)
	s.emitDiagnostic("outbox_metrics", "postgres", start, err)
	return stats, err
}

// Envelope carries either a discovery document or the minimal business revision.
// Consumers select a payload type using Type; no private persistence model is sent.
type Envelope struct {
	SpecVersion string          `json:"specversion"`
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	Subject     string          `json:"subject"`
	Time        time.Time       `json:"time"`
	Data        json.RawMessage `json:"data"`
}
