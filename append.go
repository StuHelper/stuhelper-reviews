package reviews

import (
	"context"
	"slices"
	"strings"
	"time"
)

func (s *Service) validateAppendTarget(t Transaction, p Principal, entry Entry, revision int64, now time.Time) error {
	if entry.Owner != p.Subject {
		return ErrNotFound
	}
	if entry.ExpiresAt != nil && !now.Before(*entry.ExpiresAt) {
		return ErrNotFound
	}
	closed, err := t.ClosedOwners([]string{p.Subject})
	if err != nil {
		return err
	}
	if closed[p.Subject] {
		return ErrForbidden
	}
	if entry.ID != entry.RootID || (entry.Input.Kind != "course" && entry.Input.Kind != "mentor") {
		return ErrInput
	}
	if entry.Revision != revision || entry.State != "published" || len(entry.Appends) >= s.maxAppends() {
		return ErrConflict
	}
	if s.Config.AppendCooldown > 0 && len(entry.Appends) > 0 {
		last := entry.Appends[len(entry.Appends)-1]
		if now.Before(last.CreatedAt.Add(s.Config.AppendCooldown)) {
			return ErrConflict
		}
	}
	return nil
}

func (s *Service) appendEntry(ctx context.Context, p Principal, id, key string, in Action) (Result, error) {
	var out Result
	if in.Input != nil || in.Vote != 0 || in.Revision < 1 || strings.TrimSpace(in.Body) == "" || !textOK(in.Body, 4000) {
		return out, ErrInput
	}
	rk := hash([]string{p.Subject, "append", id, key})
	h := hash(in)
	var snapshot Entry
	var replayed bool
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		cached, ok, err := replay(t, rk, h, s.now())
		if err != nil {
			return err
		}
		if ok {
			out = cached
			replayed = true
			return nil
		}
		entry, err := t.Entry(id)
		if err != nil {
			return err
		}
		if err = s.validateAppendTarget(t, p, entry, in.Revision, s.now()); err != nil {
			return err
		}
		snapshot = entry
		return nil
	})
	if err != nil || replayed {
		return out, err
	}
	targetKind := "course"
	publishAction := "publish_course"
	if snapshot.Input.Kind == "mentor" {
		targetKind = "teacher"
		publishAction = "publish_mentor"
	}
	target, err := s.catalogTarget(ctx, targetKind, snapshot.Input.TargetID)
	if err != nil {
		return out, err
	}
	if target.Inactive || target.SchoolID != snapshot.SchoolID {
		return out, ErrForbidden
	}
	grant, err := s.authorize(ctx, p, publishAction, snapshot.SchoolID)
	if err != nil {
		return out, err
	}
	outcome, err := s.check(ctx, Entry{
		ID:       snapshot.ID,
		SchoolID: snapshot.SchoolID,
		Owner:    p.Subject,
		Input:    Input{Kind: snapshot.Input.Kind, Title: snapshot.Input.Title, Body: in.Body},
		Revision: snapshot.Revision + 1,
	})
	if err != nil {
		return out, err
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(rk); err != nil {
			return err
		}
		now := s.now()
		cached, ok, err := replay(t, rk, h, now)
		if err != nil {
			return err
		}
		if ok {
			out = cached
			return nil
		}
		entry, err := t.Entry(id)
		if err != nil {
			return err
		}
		if err = t.Lock(entry.RootID); err != nil {
			return err
		}
		entry, err = t.Entry(id)
		if err != nil {
			return err
		}
		if err = t.Lock("closure-owner:" + p.Subject); err != nil {
			return err
		}
		now = s.now()
		if err = s.validateAppendTarget(t, p, entry, in.Revision, now); err != nil {
			return err
		}
		appendState := "published"
		if outcome == "pending" {
			appendState = "pending"
		}
		if outcome == "review" || outcome == "pending" {
			entry.ReviewRequested = true
		}
		appendID := hash([]string{"append-item", entry.ID, rk})[:24]
		entry.Appends = append(slices.Clone(entry.Appends), Append{
			ID:        appendID,
			Body:      in.Body,
			State:     appendState,
			CreatedAt: now.Round(time.Microsecond),
		})
		entry.ChangeOrigin = "author"
		entry.ChangeReason = ""
		entry.RuleVersion = grant.RuleVersion
		entry.Revision++
		entry.UpdatedAt = now
		if err = t.SaveEntry(entry); err != nil {
			return err
		}
		if err = s.record(t, rk, h, "appended", p, entry); err != nil {
			return err
		}
		out = result(entry)
		return nil
	})
	return out, err
}
