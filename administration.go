package reviews

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"
)

type AdminFilterTransaction interface {
	AdminEntriesFiltered(AdminFilter) ([]Entry, error)
}

func adminView(e Entry) AdminEntry {
	in := e.Input
	in.Signed = e.Projection.Identity.Signed
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
		for _, entry := range entries {
			out.Items = append(out.Items, adminView(entry))
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
