package reviews

import (
	"context"
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

func replyDraftForPublish(t Transaction, p Principal, id string, revision int64, in Input) (Draft, error) {
	d, err := t.Draft(id)
	if err != nil {
		return Draft{}, err
	}
	if d.Owner != p.Subject {
		return Draft{}, ErrNotFound
	}
	if d.Revision != revision {
		return Draft{}, revisionConflictErr(d.Revision)
	}
	if d.Input.Kind != "reply" || !validID(d.Input.ParentID) || (in.ParentID != "" && d.Input.ParentID != in.ParentID) || d.Input.TargetID != in.TargetID {
		return Draft{}, ErrInput
	}
	return d, nil
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
