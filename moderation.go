package reviews

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Apply executes a moderation decision after authorization for this object/school.
// A decision ID and content revision fence every retry and stale delivery.
func (s *Service) Apply(ctx context.Context, p Principal, d Decision) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(d.ID) || !validID(d.ObjectID) || d.Revision < 1 || !textOK(d.Reason, 2000) || strings.TrimSpace(d.Reason) == "" || !slices.Contains([]string{"publish", "hide", "restore", "delete", "edit", "identity"}, d.Action) {
		return out, ErrInput
	}
	rk := hash([]string{"moderation", d.ID})
	h := hash(d)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		entry, e := t.Entry(d.ObjectID)
		if e != nil {
			return e
		}
		if _, e = s.authorize(ctx, p, "moderate", entry.SchoolID); e != nil {
			return e
		}
		if e = t.Lock(entry.RootID); e != nil {
			return e
		}
		entry, e = t.Entry(d.ObjectID)
		if e != nil {
			return e
		}
		cached, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = cached
			return nil
		}
		if entry.Revision != d.Revision {
			return ErrConflict
		}
		if entry.ExpiresAt != nil && !s.now().Before(*entry.ExpiresAt) {
			return ErrNotFound
		}
		switch d.Action {
		case "publish":
			if entry.State != "pending" {
				return ErrConflict
			}
			entry.State = "published"
			entry.ExpiresAt = nil
			if entry.FirstPublishedAt == nil {
				now := s.now()
				entry.FirstPublishedAt = &now
			}
		case "hide":
			if entry.State != "published" {
				return ErrConflict
			}
			entry.State = "hidden"
		case "restore":
			if entry.State != "hidden" && entry.State != "deleted" {
				return ErrConflict
			}
			entry.State = "published"
			entry.ExpiresAt = nil
			if entry.FirstPublishedAt == nil {
				now := s.now()
				entry.FirstPublishedAt = &now
			}
		case "delete":
			if entry.State == "withdrawn" {
				return ErrConflict
			}
			entry.State = "deleted"
		case "edit":
			if d.Body == nil || !textOK(*d.Body, 16000) || strings.TrimSpace(*d.Body) == "" {
				return ErrInput
			}
			if entry.State == "withdrawn" {
				return ErrConflict
			}
			entry.Input.Body = *d.Body
		case "identity":
			if (d.Subject == "" && d.AuthorHandle == "") || (d.Subject != "" && d.AuthorHandle != "") {
				return ErrInput
			}
			var identity Identity
			var e error
			if d.AuthorHandle != "" {
				identity, e = t.IdentityByHandle(entry.RootID, d.AuthorHandle)
			} else {
				identity, e = t.Identity(entry.RootID, d.Subject)
			}
			if errors.Is(e, ErrNotFound) {
				return ErrNotFound
			}
			if e != nil {
				return e
			}
			identity.Signed = d.Signed
			if e = t.SaveIdentity(identity); e != nil {
				return e
			}
		}
		entry.Revision++
		entry.UpdatedAt = s.now()
		if e = t.SaveEntry(entry); e != nil {
			return e
		}
		if e = s.record(t, rk, h, "moderated", p, entry); e != nil {
			return e
		}
		out = result(entry)
		return nil
	})
	return out, err
}
