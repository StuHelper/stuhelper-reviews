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
	for attempt := 0; attempt < 2; attempt++ {
		ids, err := disclosures.ActivityDisclosureEntries(ctx, id, 100)
		if err != nil {
			return empty, false, err
		}
		var candidates []Entry
		if len(ids) > 0 {
			err = s.Repo.Transaction(ctx, func(t Transaction) error {
				var err error
				candidates, err = t.ActivityDisclosureCandidates(ids)
				return err
			})
			if err != nil {
				return empty, false, err
			}
		}
		reader := s.withReadCache()
		visible := true
		for _, entry := range candidates {
			_, visible, err = reader.disclosedFacts(ctx, entry)
			if err != nil {
				return empty, false, err
			}
			if !visible {
				break
			}
		}
		latest, err := repo.ReadActivity(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return empty, false, nil
		}
		if err != nil {
			return empty, false, err
		}
		if latest.Revision == d.Revision {
			if !visible {
				return empty, false, nil
			}
			return latest, true, nil
		}
		// Recheck the candidates belonging to the newer counts.
		d = latest
	}
	return empty, false, ErrUnavailable
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
