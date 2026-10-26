package reviews

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Inspect is an in-process metadata interface for moderation; there is no public HTTP route.
func (s *Service) Inspect(ctx context.Context, p Principal, id string) (Inspection, error) {
	var out Inspection
	if p.Subject == "" {
		return out, ErrForbidden
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		entry, e := t.Entry(id)
		if e != nil {
			return e
		}
		isVisible := visible(entry, s.now())
		if entry.RootID != entry.ID {
			root, e := t.Entry(entry.RootID)
			if e != nil {
				return e
			}
			isVisible = isVisible && visible(root, s.now())
		}
		if !isVisible && entry.Owner != p.Subject {
			if _, e = s.authorize(ctx, p, "moderate", entry.SchoolID); e != nil {
				return ErrNotFound
			}
		}
		out = Inspection{entry.ID, entry.Input.Kind, entry.SchoolID, entry.Owner, entry.Revision, isVisible}
		return nil
	})
	return out, err
}

// Handoff sends current pending/review-marked revisions to moderation, using the durable outbox ID.
func (s *Service) Handoff(ctx context.Context, limit int, deliver func(context.Context, string, Inspection) error) (int, error) {
	if limit < 1 || limit > 100 || deliver == nil {
		return 0, ErrInput
	}
	var events []DeliveryEvent
	err := s.Repo.Transaction(ctx, func(t Transaction) error { var e error; events, e = t.Events(s.now(), limit); return e })
	if err != nil {
		return 0, err
	}
	done := 0
	for _, event := range events {
		var entry Entry
		err = s.Repo.Transaction(ctx, func(t Transaction) error {
			var e error
			entry, e = t.Entry(strings.TrimPrefix(event.Subject, "entry/"))
			return e
		})
		if err != nil && !errors.Is(err, ErrNotFound) {
			return done, err
		}
		var deliveryErr error
		if err == nil && entry.ReviewRequested && entry.Revision == event.Data.Revision && (entry.ExpiresAt == nil || s.now().Before(*entry.ExpiresAt)) && (entry.State == "published" || entry.State == "pending") {
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			deliveryErr = deliver(callCtx, event.ID, Inspection{entry.ID, entry.Input.Kind, entry.SchoolID, entry.Owner, entry.Revision, entry.State == "published"})
			cancel()
		}
		err = s.Repo.Transaction(ctx, func(t Transaction) error {
			if deliveryErr == nil {
				done++
				return t.CompleteEvent(event.ID)
			}
			first := s.now()
			if event.FirstFailure != nil {
				first = *event.FirstFailure
			}
			delay := 5 * time.Second
			for i := 0; i < event.Attempts && delay < time.Hour; i++ {
				delay *= 2
			}
			if delay > time.Hour {
				delay = time.Hour
			}
			return t.FailEvent(event.ID, event.Attempts+1, first, s.now().Add(delay), s.now().Sub(first) >= 24*time.Hour)
		})
		if err != nil {
			return done, err
		}
	}
	return done, nil
}
