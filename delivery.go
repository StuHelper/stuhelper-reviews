package reviews

import (
	"context"
)

// Inspect is an in-process metadata interface for moderation; there is no public HTTP route.
func (s *Service) Inspect(ctx context.Context, p Principal, id string) (Inspection, error) {
	var out Inspection
	if p.Subject == "" {
		return out, ErrForbidden
	}
	var entry Entry
	var rootVisible bool
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		entry, e = t.Entry(id)
		if e != nil {
			return e
		}
		rootVisible = true
		if entry.RootID != entry.ID {
			root, e := t.Entry(entry.RootID)
			if e != nil {
				return e
			}
			rootVisible = visible(root, s.now())
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	var closed map[string]bool
	if err := s.Repo.Transaction(ctx, func(t Transaction) error { var e error; closed, e = t.ClosedOwners([]string{entry.Owner}); return e }); err != nil {
		return Inspection{}, err
	}
	disclosed := true
	if !closed[entry.Owner] {
		_, disclosed = s.disclosed(ctx, entry)
	}
	isVisible := visible(entry, s.now()) && rootVisible && disclosed
	if !isVisible && entry.Owner != p.Subject {
		if _, err = s.authorize(ctx, p, "moderate", entry.SchoolID); err != nil {
			return out, ErrNotFound
		}
	}
	return Inspection{ID: entry.ID, Kind: entry.Input.Kind, SchoolID: entry.SchoolID, Owner: entry.Owner, Revision: entry.Revision, Visible: isVisible, ReviewRequested: entry.ReviewRequested && (entry.State == "published" || entry.State == "pending") && (entry.ExpiresAt == nil || s.now().Before(*entry.ExpiresAt))}, nil
}
