package reviews

import (
	"context"
	"errors"
	"time"
)

const DefaultMaxDrafts = 200

type ParentSummary struct {
	Entry  Entry
	Handle string
	Closed bool
}

type PersonalContextTransaction interface {
	DraftCount(owner string) (int, error)
	ParentSummaries(ids []string) (map[string]ParentSummary, error)
	OwnComposerContext(owner, kind, parent string, targets, terms []string, now time.Time) (string, []string, error)
}

func (s *Service) maxDrafts() int {
	if s.Config.MaxDrafts <= 0 {
		return DefaultMaxDrafts
	}
	return s.Config.MaxDrafts
}

func (s *Service) sameTarget(ctx context.Context, rootKind, a, b string) bool {
	if a == b {
		return true
	}
	targetKind := "course"
	if rootKind == "mentor" {
		targetKind = "teacher"
	}
	ids, err := s.equivalentIDs(ctx, targetKind, a)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == b {
			return true
		}
	}
	return false
}

func (s *Service) publishReplyDraft(ctx context.Context, p Principal, id, key string, in PublishDraft) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(id) || !validKey(key) || in.Revision < 1 || in.Input.Kind != "reply" || validate(in.Input, false) != nil {
		return out, ErrInput
	}
	rk := hash([]string{p.Subject, "publish_draft", id, key})
	h := hash(in)
	var stored Draft
	var replayed bool
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if cached, ok, e := replay(t, rk, h, s.now()); e != nil {
			return e
		} else if ok {
			out = cached
			replayed = true
			return nil
		}
		d, e := t.Draft(id)
		if e != nil {
			return e
		}
		if d.Owner != p.Subject {
			return ErrNotFound
		}
		if d.Revision != in.Revision {
			return ErrConflict
		}
		if d.Input.Kind != "reply" {
			return ErrInput
		}
		stored = d
		return nil
	})
	if err != nil || replayed {
		return out, err
	}
	replyInput := in.Input
	if replyInput.ParentID == "" {
		replyInput.ParentID = stored.Input.ParentID
	}
	if !validID(replyInput.ParentID) || replyInput.ParentID != stored.Input.ParentID || replyInput.TargetID != stored.Input.TargetID {
		return out, ErrInput
	}
	parent := replyInput.ParentID
	if err := s.requirePublicDisclosure(ctx, parent); err != nil {
		return out, err
	}
	var base, root Entry
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		base, e = s.accessible(t, parent)
		if e != nil {
			return e
		}
		root, e = s.accessible(t, base.RootID)
		return e
	})
	if err != nil {
		return out, err
	}
	if !s.sameTarget(ctx, root.Input.Kind, base.Input.TargetID, replyInput.TargetID) {
		return out, ErrInput
	}
	course, term := "", ""
	if root.Input.Kind == "course" {
		course = root.Input.TargetID
		term = root.Input.TermID
	}
	disclosure, err := s.validateDisclosures(ctx, p, root.SchoolID, course, term, replyInput)
	if err != nil {
		return out, err
	}
	g, err := s.replyGrant(ctx, p, root, disclosure)
	if err != nil {
		return out, err
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock("draft:" + id); e != nil {
			return e
		}
		base, e := s.accessible(t, parent)
		if e != nil {
			return e
		}
		if e = t.Lock(base.RootID); e != nil {
			return e
		}
		if e = t.Lock(rk); e != nil {
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
		draft, e := t.Draft(id)
		if e != nil {
			return e
		}
		if draft.Owner != p.Subject {
			return ErrNotFound
		}
		if draft.Revision != in.Revision {
			return ErrConflict
		}
		if draft.Input.Kind != "reply" || draft.Input.ParentID != parent || draft.Input.TargetID != replyInput.TargetID {
			return ErrInput
		}
		base, e = s.accessible(t, parent)
		if e != nil {
			return e
		}
		current, e := s.accessible(t, root.ID)
		if e != nil {
			return e
		}
		if current.Revision != root.Revision {
			return ErrConflict
		}
		parentIdentity, e := t.Identity(base.RootID, base.Owner)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		identity, e := t.Identity(base.RootID, p.Subject)
		if errors.Is(e, ErrNotFound) {
			identity = Identity{RootID: base.RootID, Subject: p.Subject, Handle: hash([]string{base.RootID, p.Subject})[:16], Signed: replyInput.Signed}
			if e = t.SaveIdentity(identity); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		replyInput.Signed = identity.Signed
		now := s.now()
		entry := Entry{
			ParentAuthorHandle: parentIdentity.Handle,
			ChangeOrigin:       "author",
			TeacherReply:       disclosure.ownCourse,
			DisclosureCourse:   course,
			DisclosureTerm:     term,
			ID:                 rk,
			RootID:             base.RootID,
			ParentID:           parent,
			Owner:              p.Subject,
			SchoolID:           base.SchoolID,
			Input:              replyInput,
			Revision:           1,
			CreatedAt:          now,
			UpdatedAt:          now,
			RuleVersion:        g.RuleVersion,
		}
		state, e := s.check(ctx, entry)
		if e != nil {
			return e
		}
		entry.ReviewRequested = state == "review" || state == "pending"
		if state == "review" {
			state = "published"
		}
		entry.State = state
		if state == "published" {
			entry.FirstPublishedAt = &now
		}
		if e = t.DeleteDraft(id); e != nil {
			return e
		}
		if e = t.SaveEntry(entry); e != nil {
			return e
		}
		if e = s.record(t, rk, h, "replied", p, entry); e != nil {
			return e
		}
		out = result(entry)
		return nil
	})
	return out, err
}

func (s *Service) attachOwnComposerContext(ctx context.Context, p Principal, kind, target, term, parent string, out *Composer) error {
	if p.Subject == "" {
		return nil
	}
	var targets, terms []string
	if kind != "reply" {
		targetKind := "course"
		if kind == "mentor" {
			targetKind = "teacher"
		}
		if ids, err := s.equivalentIDs(ctx, targetKind, target); err == nil {
			targets = ids
		} else {
			targets = []string{target}
		}
		if term != "" {
			if ids, err := s.equivalentIDs(ctx, "term", term); err == nil {
				terms = ids
			} else {
				terms = []string{term}
			}
		}
	}
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(PersonalContextTransaction)
		if !ok {
			return nil
		}
		draftID, entryIDs, err := port.OwnComposerContext(p.Subject, kind, parent, targets, terms, s.now())
		if err != nil {
			return err
		}
		if draftID != "" {
			out.OwnDraftID = draftID
		}
		if len(entryIDs) > 0 {
			out.OwnEntryIDs = entryIDs
		}
		return nil
	})
}
