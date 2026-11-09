package reviews

import (
	"context"
	"errors"
	"slices"
)

func (s *Service) CurrentActivity(ctx context.Context, p Principal, id string) (Activity, bool, error) {
	var empty Activity
	target, _, e := ParseActivityID(id)
	if e != nil {
		return empty, false, e
	}
	school, e := s.contentScope(ctx, p, "")
	if errors.Is(e, ErrForbidden) {
		return empty, false, nil
	}
	if e != nil {
		return empty, false, e
	}
	object, e := s.Catalog.Target(ctx, target.Kind, target.ID)
	if errors.Is(e, ErrNotFound) || errors.Is(e, ErrForbidden) {
		return empty, false, nil
	}
	if e != nil {
		return empty, false, e
	}
	if object.SchoolID != school || object.Inactive || object.ID != target.ID || object.Kind != target.Kind {
		return empty, false, nil
	}
	repo, ok := s.Repo.(ActivityReader)
	if !ok {
		return empty, false, ErrUnavailable
	}
	d, e := repo.ReadActivity(ctx, id)
	if errors.Is(e, ErrNotFound) {
		return empty, false, nil
	}
	if e != nil {
		return empty, false, e
	}
	disclosures, ok := s.Repo.(ActivityDisclosureReader)
	if !ok {
		return empty, false, ErrUnavailable
	}
	ids, e := disclosures.ActivityDisclosureEntries(ctx, id, 100)
	if e != nil {
		return empty, false, e
	}

	for _, entryID := range ids {
		var entry Entry
		closed := false
		e = s.Repo.Transaction(ctx, func(t Transaction) error {
			var e error
			entry, e = t.Entry(entryID)
			if e != nil {
				return e
			}
			owners, e := t.ClosedOwners([]string{entry.Owner})
			closed = owners[entry.Owner]
			return e
		})
		if errors.Is(e, ErrNotFound) {
			return empty, false, nil
		}
		if e != nil {
			return empty, false, e
		}
		if !closed {
			_, visible, e := s.disclosedFacts(ctx, entry)
			if e != nil {
				return empty, false, e
			}
			if !visible {
				return empty, false, nil
			}
		}
	}
	// External disclosure checks may overlap a withdrawal; return its latest revision.
	d, e = repo.ReadActivity(ctx, id)
	if errors.Is(e, ErrNotFound) {
		return empty, false, nil
	}
	return d, e == nil, e
}

func (s *Service) activityTeacherIDs(ctx context.Context, school string, ids []string) ([]string, error) {
	if len(ids) > 100 {
		return nil, ErrUnavailable
	}
	out := []string{}
	for _, id := range ids {
		target, e := s.Catalog.Target(ctx, "teacher", id)
		if e != nil {
			return nil, e
		}
		if target.Kind != "teacher" || target.SchoolID != school || !validID(target.ID) {
			return nil, ErrUnavailable
		}
		out = append(out, target.ID)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
