package reviews

import (
	"context"
	"errors"
	"slices"
	"time"
)

type Reference struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	CanonicalID string `json:"canonical_id,omitempty"`
}
type References struct {
	Items      []Reference `json:"items"`
	MissingIDs []string    `json:"missing_ids"`
}

// NameCatalog is a minimal module port, called only after local ownership checks.
type NameCatalog interface {
	ReferenceNames(context.Context, []string) (References, error)
}

func (s *Service) MyReferences(ctx context.Context, p Principal, ids []string) (References, error) {
	if p.Subject == "" {
		return References{}, ErrUnauthenticated
	}
	if len(ids) < 1 || len(ids) > 100 {
		return References{}, ErrInput
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for _, id := range ids {
		if !validID(id) {
			return References{}, ErrInput
		}
	}
	var owned []string
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		owned, e = t.OwnedReferences(p.Subject, ids, s.now())
		if e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		return References{}, err
	}
	if !slices.Equal(owned, ids) && s.BookmarksProvider != nil {
		remaining := []string{}
		for _, id := range ids {
			if !slices.Contains(owned, id) {
				remaining = append(remaining, id)
			}
		}
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		extra, e := s.BookmarksProvider.CourseReferences(call, p.Subject, remaining)
		cancel()
		if e != nil {
			return References{}, e
		}
		for _, id := range extra {
			if !slices.Contains(remaining, id) {
				return References{}, ErrUnavailable
			}
		}
		owned = append(owned, extra...)
		slices.Sort(owned)
		owned = slices.Compact(owned)
	}
	if !slices.Equal(owned, ids) {
		return References{}, ErrNotFound
	}
	port, ok := s.Catalog.(NameCatalog)
	if !ok {
		return References{}, ErrUnavailable
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := port.ReferenceNames(call, ids)
	if err != nil {
		return References{}, err
	}
	seen := map[string]bool{}
	for _, item := range out.Items {
		if !slices.Contains(ids, item.ID) || seen[item.ID] || !validID(item.Kind) || !textOK(item.Name, 500) || item.Name == "" || item.CanonicalID != "" && !validID(item.CanonicalID) {
			return References{}, ErrUnavailable
		}
		seen[item.ID] = true
	}
	for _, id := range out.MissingIDs {
		if !slices.Contains(ids, id) || seen[id] {
			return References{}, ErrUnavailable
		}
		seen[id] = true
	}
	if len(seen) != len(ids) {
		return References{}, ErrUnavailable
	}
	return out, nil
}
func draftReferenceIDs(in Input) []string {
	out := []string{in.TargetID}
	if in.TermID != "" {
		out = append(out, in.TermID)
	}
	if in.TeacherID != "" {
		out = append(out, in.TeacherID)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Failure to validate a new association does not discard private draft text.
// Previously proved IDs are retained only while they remain in this draft.
func (s *Service) verifyDraftReferences(ctx context.Context, p Principal, in Input) ([]string, string) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	school, err := s.contentScope(call, p, "")
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return nil, "unverified"
		}
		return nil, "unavailable"
	}
	if s.Catalog == nil {
		return nil, "unavailable"
	}
	kind := "course"
	if in.Kind == "mentor" {
		kind = "teacher"
	}
	pairs := []struct{ kind, id string }{{kind, in.TargetID}, {"term", in.TermID}, {"teacher", in.TeacherID}}
	ids := []string{}
	state := "verified"
	for _, pair := range pairs {
		if pair.id == "" {
			continue
		}
		target, e := s.Catalog.Target(call, pair.kind, pair.id)
		if e != nil {
			if !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrInput) && !errors.Is(e, ErrForbidden) {
				state = "unavailable"
			} else if state != "unavailable" {
				state = "unverified"
			}
			continue
		}
		if target.ID != pair.id || target.Kind != pair.kind || target.SchoolID != school {
			if state != "unavailable" {
				state = "unverified"
			}
			continue
		}
		ids = append(ids, pair.id)
	}
	return ids, state
}
func mergeDraftReferences(old, verified []string, in Input, state string) ([]string, string) {
	current := draftReferenceIDs(in)
	trusted := []string{}
	for _, id := range current {
		if slices.Contains(verified, id) || slices.Contains(old, id) {
			trusted = append(trusted, id)
		}
	}
	if len(trusted) == len(current) {
		state = "verified"
	}
	return trusted, state
}
func draftView(d Draft) DraftView {
	ids, state := mergeDraftReferences(d.ReferenceIDs, nil, d.Input, d.ReferenceState)
	if state == "" {
		state = "unverified"
	}
	return DraftView{ID: d.ID, Revision: d.Revision, Input: d.Input, ReferenceIDs: ids, ReferenceState: state}
}
