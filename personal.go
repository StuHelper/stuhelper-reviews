package reviews

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

func (s *Service) SaveDraft(ctx context.Context, p Principal, id, key string, in Input, revision int64) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validKey(key) || in.Kind == "reply" || validate(in, true) != nil || revision < 0 {
		return out, ErrInput
	}
	if len(in.Disclosures) > 0 {
		kind := "course"
		course := in.TargetID
		if in.Kind == "mentor" {
			kind = "teacher"
			course = ""
		}
		target, err := s.Catalog.Target(ctx, kind, in.TargetID)
		if err != nil {
			return out, err
		}
		if _, err = s.validateDisclosures(ctx, p, target.SchoolID, course, in.TermID, in); err != nil {
			return out, err
		}
	}
	if id == "" {
		id = hash([]string{p.Subject, "draft", key})
	} else if !validID(id) {
		return out, ErrInput
	}
	rk := hash([]string{p.Subject, "save_draft", id, key})
	h := hash(struct {
		Input    Input
		Revision int64
	}{in, revision})
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock("draft:" + id); e != nil {
			return e
		}
		r, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = r
			return nil
		}
		old, e := t.Draft(id)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		if e == nil && old.Owner != p.Subject {
			return ErrNotFound
		}
		if old.Revision != revision {
			return ErrConflict
		}
		v := Draft{ID: id, Revision: old.Revision + 1, Owner: p.Subject, Input: in, UpdatedAt: s.now()}
		if e = t.SaveDraft(v); e != nil {
			return e
		}
		out = Result{ID: id, State: "draft", Revision: v.Revision}
		return t.SaveReplay(rk, Replay{Digest: h, Result: out, Until: s.now().Add(24 * time.Hour), ObjectID: id})
	})
	return out, err
}
func (s *Service) GetDraft(ctx context.Context, p Principal, id string) (DraftView, error) {
	var out DraftView
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(id) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		draft, err := t.Draft(id)
		if err == nil {
			if draft.Owner != p.Subject {
				return ErrNotFound
			}
			out = DraftView{ID: draft.ID, Revision: draft.Revision, Input: draft.Input}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		entry, err := t.Entry(id)
		if err != nil {
			return err
		}
		if entry.Owner != p.Subject || entry.State != "withdrawn" || entry.ExpiresAt == nil || !s.now().Before(*entry.ExpiresAt) {
			return ErrNotFound
		}
		identity, err := t.Identity(entry.RootID, p.Subject)
		if err != nil {
			return err
		}
		entry.Input.Signed = identity.Signed
		out = DraftView{ID: entry.ID, Revision: entry.Revision, Input: entry.Input, ExpiresAt: entry.ExpiresAt, Withdrawn: true, RootID: entry.RootID, ParentID: entry.ParentID}
		return nil
	})
	return out, err
}
func (s *Service) DeleteDraft(ctx context.Context, p Principal, id, key string, revision int64) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(id) || !validKey(key) {
		return out, ErrInput
	}
	rk := hash([]string{p.Subject, "delete_draft", id, key})
	h := hash(revision)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock("draft:" + id); e != nil {
			return e
		}
		r, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = r
			return nil
		}
		draft, e := t.Draft(id)
		if e != nil {
			return e
		}
		if draft.Owner != p.Subject {
			return ErrNotFound
		}
		if draft.Revision != revision {
			return ErrConflict
		}
		if e = t.DeleteDraft(id); e != nil {
			return e
		}
		out = Result{ID: id, State: "deleted", Revision: revision + 1}
		return t.SaveReplay(rk, Replay{Digest: h, Result: out, Until: s.now().Add(24 * time.Hour), ObjectID: id})
	})
	return out, err
}
func (s *Service) Drafts(ctx context.Context, p Principal, after string, limit int) ([]DraftView, error) {
	if p.Subject == "" {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	out := []DraftView{}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		ordinary, e := t.Drafts(p.Subject, after, limit)
		if e != nil {
			return e
		}
		withdrawn, e := t.Withdrawals(p.Subject, after, limit)
		if e != nil {
			return e
		}
		for _, v := range ordinary {
			out = append(out, DraftView{ID: v.ID, Revision: v.Revision, Input: v.Input})
		}
		for _, v := range withdrawn {
			if v.ExpiresAt != nil && s.now().Before(*v.ExpiresAt) {
				out = append(out, DraftView{ID: v.ID, Revision: v.Revision, Input: v.Input, ExpiresAt: v.ExpiresAt, Withdrawn: true})
			}
		}
		return nil
	})
	slices.SortFunc(out, func(a, b DraftView) int { return strings.Compare(a.ID, b.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, err
}
func (s *Service) SetBookmark(ctx context.Context, p Principal, target, key string, set bool) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(target) || !validKey(key) {
		return out, ErrInput
	}
	obj, e := s.Catalog.Target(ctx, "course", target)
	if e != nil {
		return out, e
	}
	if _, e = s.authorize(ctx, p, "bookmark", obj.SchoolID); e != nil {
		return out, e
	}
	rk := hash([]string{p.Subject, "bookmark", target, key})
	h := hash(set)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(rk); e != nil {
			return e
		}
		r, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = r
			return nil
		}
		if e = t.Bookmark(p.Subject, target, set); e != nil {
			return e
		}
		out = Result{ID: target, State: "unmarked"}
		if set {
			out.State = "bookmarked"
		}
		return t.SaveReplay(rk, Replay{Digest: h, Result: out, Until: s.now().Add(24 * time.Hour), ObjectID: target})
	})
	return out, err
}
func (s *Service) Bookmarks(ctx context.Context, p Principal, after string, limit int) ([]string, error) {
	if p.Subject == "" {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	var out []string
	err := s.Repo.Transaction(ctx, func(t Transaction) error { var e error; out, e = t.Bookmarks(p.Subject, after, limit); return e })
	return out, err
}
func (s *Service) MyVotes(ctx context.Context, p Principal, after string, limit int) ([]View, error) {
	if p.Subject == "" {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	entries := []Entry{}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		rows, e := t.Voted(p.Subject, after, limit, s.now())
		if e != nil {
			return e
		}
		for _, entry := range rows {
			if _, e = s.accessible(t, entry.ID); errors.Is(e, ErrNotFound) {
				continue
			} else if e != nil {
				return e
			}
			v, e := project(t, p, entry)
			if e != nil {
				return e
			}
			entries = append(entries, v)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.render(ctx, p, entries)
}

// Sweep is bounded and idempotent; read paths independently enforce expiry.
func (s *Service) Sweep(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrInput
	}
	count := 0
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		entries, e := t.Expired(s.now(), limit)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if e = t.Lock(entry.RootID); e != nil {
				return e
			}
			current, e := t.Entry(entry.ID)
			if errors.Is(e, ErrNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			if current.ExpiresAt == nil || s.now().Before(*current.ExpiresAt) {
				continue
			}
			if e = t.DeleteEntry(current); e != nil {
				return e
			}
			if e = t.Event(Event{ID: hash([]string{"expire", entry.ID}), Type: "reviews.expired.v1", Subject: "entry/" + entry.ID, SchoolID: entry.SchoolID, Revision: entry.Revision + 1, At: s.now()}); e != nil {
				return e
			}
			count++
		}
		return nil
	})
	return count, err
}
