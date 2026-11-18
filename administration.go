package reviews

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"
)

type AdminFilterTransaction interface {
	AdminEntriesFiltered(AdminFilter) ([]Entry, error)
}

func adminView(e Entry, closed bool) AdminEntry {
	in := e.Input
	in.Signed = e.Projection.Identity.Signed && !closed
	if closed {
		in.Disclosures = nil
	}
	var updatedAt *time.Time
	if !e.UpdatedAt.IsZero() {
		u := e.UpdatedAt
		updatedAt = &u
	}
	reviewRequested := e.ReviewRequested && (e.State == "pending" || e.State == "published")
	return AdminEntry{
		ChangeOrigin:         e.ChangeOrigin,
		ChangeReason:         e.ChangeReason,
		ModerationReasonCode: e.ModerationReasonCode,
		ReviewRequested:      reviewRequested,
		Featured:             e.Featured,
		ID:                   e.ID,
		RootID:               e.RootID,
		ParentID:             e.ParentID,
		SchoolID:             e.SchoolID,
		Input:                in,
		AuthorHandle:         e.Projection.Identity.Handle,
		Anonymous:            !in.Signed,
		State:                e.State,
		Revision:             e.Revision,
		CreatedAt:            e.CreatedAt,
		UpdatedAt:            updatedAt,
		PublishedAt:          e.FirstPublishedAt,
	}
}
func (s *Service) auditRead(t Transaction, p Principal, school, subject string, revision int64) error {
	now := s.now()
	return t.AuditRead(Event{ID: rand.Text(), Type: "reviews.inspected.v1", Subject: subject, SchoolID: school, Actor: p.Subject, Revision: revision, At: now})
}
func (s *Service) AdminGet(ctx context.Context, p Principal, id string) (AdminEntry, error) {
	var out AdminEntry
	err := s.readAdminEntry(ctx, p, id, func(t Transaction, entry Entry, closed bool) error {
		out = adminView(entry, closed)
		return s.auditRead(t, p, entry.SchoolID, "entry/"+id, entry.Revision)
	})
	if err != nil {
		return AdminEntry{}, err
	}
	return out, nil
}

// Authorize outside the transaction, then recheck the discussion under its write lock.
func (s *Service) readAdminEntry(ctx context.Context, p Principal, id string, read func(Transaction, Entry, bool) error) error {
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
		closed, err := t.ClosedOwners([]string{entry.Owner})
		if err != nil {
			return err
		}
		return read(t, entry, closed[entry.Owner])
	})
}
func adminBoolKey(v *bool) string {
	if v == nil {
		return ""
	}
	if *v {
		return "true"
	}
	return "false"
}

func adminCursorScope(f AdminFilter) string {
	return hash([]string{f.SchoolID, f.Kind, f.State, f.TargetID, f.RootID, f.Sort, adminBoolKey(f.ReviewRequested), adminBoolKey(f.Featured)})
}

func decodeAdminCursor(f *AdminFilter) error {
	f.CursorTime = nil
	f.CursorID = ""
	if f.Sort == "" {
		f.Sort = "id"
	}
	if !slices.Contains([]string{"id", "newest", "oldest"}, f.Sort) || len(f.After) > 512 {
		return ErrInput
	}
	if f.After == "" {
		return nil
	}
	if f.Sort == "id" {
		if !validID(f.After) {
			return ErrInput
		}
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(f.After)
	if err != nil {
		return ErrInput
	}
	var cursor pageCursor
	if json.Unmarshal(b, &cursor) != nil || cursor.Time.IsZero() || !validID(cursor.ID) || cursor.Scope != adminCursorScope(*f) {
		return ErrInput
	}
	f.CursorTime = &cursor.Time
	f.CursorID = cursor.ID
	return nil
}

func encodeAdminCursor(f AdminFilter, entry Entry) string {
	if f.Sort == "id" {
		return entry.ID
	}
	cursor := pageCursor{Scope: adminCursorScope(f), Time: entry.CreatedAt.Round(time.Microsecond), ID: entry.ID}
	b, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Service) AdminList(ctx context.Context, p Principal, school, kind, state, after string, limit int) (AdminPage, error) {
	return s.AdminListFiltered(ctx, p, AdminFilter{
		SchoolID: school,
		Kind:     kind,
		State:    state,
		Sort:     "id",
		After:    after,
		Limit:    limit,
	})
}

func (s *Service) AdminListFiltered(ctx context.Context, p Principal, f AdminFilter) (AdminPage, error) {
	out := AdminPage{Items: []AdminEntry{}}
	if !validID(f.SchoolID) || f.Limit < 1 || f.Limit > 100 || (f.Kind != "" && !slices.Contains([]string{"course", "mentor", "reply"}, f.Kind)) || (f.State != "" && !slices.Contains([]string{"pending", "published", "hidden", "deleted"}, f.State)) {
		return out, ErrInput
	}
	for _, id := range []string{f.TargetID, f.RootID} {
		if id != "" && !validID(id) {
			return out, ErrInput
		}
	}
	if err := decodeAdminCursor(&f); err != nil {
		return out, err
	}
	if _, err := s.authorize(ctx, p, "moderate", f.SchoolID); err != nil {
		return out, err
	}
	if f.TargetID != "" {
		targetKind := "course"
		if f.Kind == "mentor" {
			targetKind = "teacher"
		}
		if ids, err := s.equivalentIDs(ctx, targetKind, f.TargetID); err == nil {
			f.EquivalentTargets = ids
		} else {
			f.EquivalentTargets = []string{f.TargetID}
		}
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		now := s.now()
		query := f
		query.Limit = f.Limit + 1
		query.Now = now
		var entries []Entry
		var err error
		if port, ok := t.(AdminFilterTransaction); ok {
			entries, err = port.AdminEntriesFiltered(query)
		} else {
			entries, err = t.AdminEntries(f.SchoolID, f.Kind, f.State, f.After, f.Limit+1, now)
		}
		if err != nil {
			return err
		}
		if len(entries) > f.Limit {
			out.Next = encodeAdminCursor(f, entries[f.Limit-1])
			entries = entries[:f.Limit]
		}
		owners := make([]string, 0, len(entries))
		for _, entry := range entries {
			owners = append(owners, entry.Owner)
		}
		closed, err := t.ClosedOwners(owners)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			out.Items = append(out.Items, adminView(entry, closed[entry.Owner]))
		}
		auditSubject := "school/" + f.SchoolID + "/entries/" + hash([]string{f.Kind, f.State, f.TargetID, f.RootID, f.Sort, adminBoolKey(f.ReviewRequested), adminBoolKey(f.Featured), f.After})
		return s.auditRead(t, p, f.SchoolID, auditSubject, 0)
	})
	return out, err
}

func (s *Service) History(ctx context.Context, p Principal, id string, before int64, limit int) ([]AdminEntry, error) {
	out := []AdminEntry{}
	if before < 0 || limit < 1 || limit > 100 {
		return out, ErrInput
	}
	err := s.readAdminEntry(ctx, p, id, func(t Transaction, current Entry, closed bool) error {
		entries, e := t.History(id, before, limit)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			entry.Projection = current.Projection
			out = append(out, adminView(entry, closed))
		}
		return s.auditRead(t, p, current.SchoolID, "history/"+id, current.Revision)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
