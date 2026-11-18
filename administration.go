package reviews

import (
	"context"
	"crypto/rand"
	"slices"
)

func adminView(e Entry) AdminEntry {
	in := e.Input
	in.Signed = e.Projection.Identity.Signed
	return AdminEntry{ChangeOrigin: e.ChangeOrigin, ChangeReason: e.ChangeReason, Featured: e.Featured, ID: e.ID, RootID: e.RootID, SchoolID: e.SchoolID, Input: in, AuthorHandle: e.Projection.Identity.Handle, Anonymous: !in.Signed, State: e.State, Revision: e.Revision, CreatedAt: e.CreatedAt, PublishedAt: e.FirstPublishedAt}
}
func (s *Service) auditRead(t Transaction, p Principal, school, subject string, revision int64) error {
	now := s.now()
	return t.AuditRead(Event{ID: rand.Text(), Type: "reviews.inspected.v1", Subject: subject, SchoolID: school, Actor: p.Subject, Revision: revision, At: now})
}
func (s *Service) AdminGet(ctx context.Context, p Principal, id string) (AdminEntry, error) {
	var out AdminEntry
	err := s.readAdminEntry(ctx, p, id, func(t Transaction, entry Entry) error {
		out = adminView(entry)
		return s.auditRead(t, p, entry.SchoolID, "entry/"+id, entry.Revision)
	})
	if err != nil {
		return AdminEntry{}, err
	}
	return out, nil
}

// Authorize outside the transaction, then recheck the discussion under its write lock.
func (s *Service) readAdminEntry(ctx context.Context, p Principal, id string, read func(Transaction, Entry) error) error {
	if p.Subject == "" {
		return ErrForbidden
	}
	if !validID(id) {
		return ErrInput
	}
	var prior Entry
	err := s.Repo.Transaction(ctx, func(t Transaction) error { var err error; prior, err = t.Entry(id); return err })
	if err != nil {
		return err
	}
	if _, err = s.authorize(ctx, p, "moderate", prior.SchoolID); err != nil {
		return err
	}
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(prior.RootID); err != nil {
			return err
		}
		entry, err := t.Entry(id)
		if err != nil {
			return err
		}
		if entry.RootID != prior.RootID || entry.SchoolID != prior.SchoolID {
			return ErrConflict
		}
		if entry.State == "withdrawn" || entry.ExpiresAt != nil && !s.now().Before(*entry.ExpiresAt) {
			return ErrNotFound
		}
		if entry.ID != entry.RootID {
			root, err := t.Entry(entry.RootID)
			if err != nil {
				return err
			}
			if root.State == "withdrawn" || root.ExpiresAt != nil && !s.now().Before(*root.ExpiresAt) {
				return ErrNotFound
			}
		}
		identity, err := t.Identity(entry.RootID, entry.Owner)
		if err != nil {
			return err
		}
		entry.Projection = &Projection{Identity: identity}
		return read(t, entry)
	})
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
	err := s.readAdminEntry(ctx, p, id, func(t Transaction, current Entry) error {
		entries, e := t.History(id, before, limit)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			entry.Projection = current.Projection
			out = append(out, adminView(entry))
		}
		return s.auditRead(t, p, current.SchoolID, "history/"+id, current.Revision)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
