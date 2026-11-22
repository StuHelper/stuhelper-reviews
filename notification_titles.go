package reviews

import (
	"context"
	"errors"
	"strings"
	"time"
)

// NotificationTitle is current authorized metadata for rendering an inbox.
// RootID identifies the root review, whose title is also used for replies.
type NotificationTitle struct{ ID, RootID, Title string }
type NotificationCandidate struct {
	Entry Entry
	Root  Entry
}
type NotificationTitleTransaction interface {
	NotificationCandidates([]string, string, time.Time) ([]NotificationCandidate, error)
}

func (s *Service) NotificationTitles(ctx context.Context, p Principal, ids []string) ([]NotificationTitle, error) {
	s = s.withReadCache()
	out := []NotificationTitle{}
	if len(ids) < 1 || len(ids) > 100 {
		return nil, ErrInput
	}
	for _, id := range ids {
		if !validID(id) {
			return nil, ErrInput
		}
	}
	school, err := s.contentScope(ctx, p, "")
	if errors.Is(err, ErrForbidden) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []NotificationCandidate
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(NotificationTitleTransaction)
		if !ok {
			return ErrUnavailable
		}
		var e error
		rows, e = port.NotificationCandidates(ids, school, s.now())
		return e
	})
	if err != nil {
		return nil, err
	}
	catalog, ok := s.Catalog.(SummaryCatalog)
	if !ok {
		return nil, ErrUnavailable
	}
	refs := map[string]SummaryReference{}
	for _, kind := range []string{"course", "teacher"} {
		targets := []string{}
		seen := map[string]bool{}
		for _, row := range rows {
			if (kind == "course") != (row.Root.Input.Kind == "course") {
				continue
			}
			id := row.Root.Input.TargetID
			if !seen[id] {
				targets = append(targets, id)
				seen[id] = true
			}
		}
		if len(targets) == 0 {
			continue
		}
		values, e := s.catalogSummaryTargets(ctx, catalog, kind, targets)
		if e != nil {
			return nil, e
		}
		for id, ref := range values {
			refs[id] = ref
		}
	}
	owners := []string{}
	for _, row := range rows {
		owners = append(owners, row.Entry.Owner)
	}
	var closed map[string]bool
	err = s.Repo.Transaction(ctx, func(t Transaction) error { var e error; closed, e = t.ClosedOwners(owners); return e })
	if err != nil {
		return nil, err
	}
	authorized := map[string]NotificationCandidate{}
	for _, row := range rows {
		ref, ok := refs[row.Root.Input.TargetID]
		if !ok || ref.Inactive || ref.SchoolID != school {
			continue
		}
		if !closed[row.Entry.Owner] {
			_, public := s.disclosed(ctx, row.Entry)
			if !public && row.Entry.Owner != p.Subject {
				continue
			}
		}
		title := strings.TrimSpace(row.Root.Input.Title)
		if title == "" {
			title = ref.Name
		}
		authorized[row.Entry.ID] = row
		out = append(out, NotificationTitle{ID: row.Entry.ID, RootID: row.Root.ID, Title: title})
	}
	// Authorization may call other modules. Re-read publication and revisions
	// afterwards, dropping edits, unlisting, deletion, and expired roots.
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(NotificationTitleTransaction)
		if !ok {
			return ErrUnavailable
		}
		var e error
		rows, e = port.NotificationCandidates(ids, school, s.now())
		return e
	})
	if err != nil {
		return nil, err
	}
	current := map[string]bool{}
	for _, row := range rows {
		old, ok := authorized[row.Entry.ID]
		if ok && old.Entry.Revision == row.Entry.Revision && old.Root.Revision == row.Root.Revision {
			current[row.Entry.ID] = true
		}
	}
	filtered := []NotificationTitle{}
	for _, item := range out {
		if current[item.ID] {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}
