package reviews

import (
	"context"
	"slices"
	"time"
)

func adminView(e Entry) AdminEntry {
	in := e.Input
	in.Signed = e.Projection.Identity.Signed
	return AdminEntry{ChangeOrigin: e.ChangeOrigin, ChangeReason: e.ChangeReason, Featured: e.Featured, ID: e.ID, RootID: e.RootID, SchoolID: e.SchoolID, Input: in, AuthorHandle: e.Projection.Identity.Handle, Anonymous: !in.Signed, State: e.State, Revision: e.Revision, CreatedAt: e.CreatedAt, PublishedAt: e.FirstPublishedAt}
}
func (s *Service) auditRead(t Transaction, p Principal, school, subject string, revision int64) error {
	now := s.now()
	return t.AuditRead(Event{ID: hash([]string{"inspect", p.Subject, subject, now.Format(time.RFC3339Nano)}), Type: "reviews.inspected.v1", Subject: subject, SchoolID: school, Actor: p.Subject, Revision: revision, At: now})
}
func (s *Service) AdminGet(ctx context.Context, p Principal, id string) (AdminEntry, error) {
	var out AdminEntry
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(id) {
		return out, ErrInput
	}
	var entry Entry
	err := s.Repo.Transaction(ctx, func(t Transaction) error { var err error; entry, err = t.Entry(id); return err })
	if err != nil {
		return out, err
	}
	if _, err = s.authorize(ctx, p, "moderate", entry.SchoolID); err != nil {
		return out, err
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		var err error
		entry, err = t.Entry(id)
		if err != nil {
			return err
		}
		if entry.State == "withdrawn" || entry.ExpiresAt != nil && !s.now().Before(*entry.ExpiresAt) {
			return ErrNotFound
		}
		if entry.RootID != entry.ID {
			root, err := t.Entry(entry.RootID)
			if err != nil {
				return err
			}
			if root.State == "withdrawn" || root.ExpiresAt != nil && !s.now().Before(*root.ExpiresAt) {
				return ErrNotFound
			}
		}
		entry, err = project(t, p, entry)
		if err != nil {
			return err
		}
		out = adminView(entry)
		return s.auditRead(t, p, entry.SchoolID, "entry/"+entry.ID, entry.Revision)
	})
	return out, err
}
func (s *Service) AdminList(ctx context.Context, p Principal, school, kind, state, after string, limit int) (AdminPage, error) {
	out := AdminPage{Items: []AdminEntry{}}
	if !validID(school) || limit < 1 || limit > 100 || after != "" && !validID(after) || kind != "" && !slices.Contains([]string{"course", "mentor", "reply"}, kind) || state != "" && !slices.Contains([]string{"pending", "published", "hidden", "deleted"}, state) {
		return out, ErrInput
	}
	if _, err := s.authorize(ctx, p, "moderate", school); err != nil {
		return out, err
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		entries, err := t.AdminEntries(school, kind, state, after, limit+1, s.now())
		if err != nil {
			return err
		}
		if len(entries) > limit {
			out.Next = entries[limit-1].ID
			entries = entries[:limit]
		}
		for _, entry := range entries {
			out.Items = append(out.Items, adminView(entry))
		}
		return s.auditRead(t, p, school, "school/"+school+"/entries/"+hash([]string{kind, state, after}), 0)
	})
	return out, err
}

func (s *Service) History(ctx context.Context, p Principal, id string, before int64, limit int) ([]AdminEntry, error) {
	out := []AdminEntry{}
	if before < 0 || limit < 1 || limit > 100 {
		return out, ErrInput
	}
	current, err := s.AdminGet(ctx, p, id)
	if err != nil {
		return out, err
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		live, e := t.Entry(id)
		if e != nil {
			return e
		}
		if live.State == "withdrawn" || live.ExpiresAt != nil && !s.now().Before(*live.ExpiresAt) {
			return ErrNotFound
		}
		if live.RootID != live.ID {
			root, e := t.Entry(live.RootID)
			if e != nil {
				return e
			}
			if root.State == "withdrawn" || root.ExpiresAt != nil && !s.now().Before(*root.ExpiresAt) {
				return ErrNotFound
			}
		}
		entries, e := t.History(id, before, limit)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			entry, e = project(t, p, entry)
			if e != nil {
				return e
			}
			out = append(out, adminView(entry))
		}
		return s.auditRead(t, p, current.SchoolID, "history/"+id, current.Revision)
	})
	return out, err
}
