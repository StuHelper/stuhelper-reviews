package reviews

import (
	"context"
	"errors"
	"slices"
	"time"
)

const ReplyPreviewLimit = 3

type DiscussionAuthor struct{ RootID, Owner string }
type ReplyBatch struct {
	Counts  map[string]int
	Entries []Entry
}

// ReplyPreviewTransaction queries all roots of one page together. Candidates
// carry only the facts needed to recheck mandatory teacher disclosure.
type ReplyPreviewTransaction interface {
	ReplyDisclosureCandidates([]string, string, time.Time) ([]Entry, error)
	ReplyPreviews([]string, string, time.Time, []DiscussionAuthor) (ReplyBatch, error)
}

func (s *Service) addReplyPreviews(ctx context.Context, p Principal, out *Page, school string, own bool) error {
	if own {
		scope, err := s.contentScope(ctx, p, "")
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrUnauthenticated) {
			return nil
		}
		if err != nil {
			return err
		}
		school = scope
	}
	roots := []string{}
	for _, v := range out.Items {
		if v.ID == v.RootID && v.State == "published" && v.SchoolID == school {
			roots = append(roots, v.ID)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	var candidates []Entry
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(ReplyPreviewTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		candidates, err = port.ReplyDisclosureCandidates(roots, p.Subject, s.now())
		return err
	})
	if err != nil {
		return err
	}
	allowed := []DiscussionAuthor{}
	// The same teacher may have replied many times in a discussion. Repository
	// grouping makes the current mandatory-disclosure check once per author/root.
	for _, entry := range candidates {
		_, public := s.disclosed(ctx, entry)
		if public {
			allowed = append(allowed, DiscussionAuthor{entry.RootID, entry.Owner})
		}
	}
	var batch ReplyBatch
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(ReplyPreviewTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		batch, err = port.ReplyPreviews(roots, p.Subject, s.now(), allowed)
		return err
	})
	if err != nil {
		return err
	}
	views, err := s.render(ctx, p, batch.Entries, false, school, false)
	if err != nil {
		return err
	}
	byRoot := map[string][]View{}
	for _, v := range views {
		byRoot[v.RootID] = append(byRoot[v.RootID], v)
	}
	for i := range out.Items {
		v := &out.Items[i]
		if !slices.Contains(roots, v.ID) {
			continue
		}
		count := batch.Counts[v.ID]
		preview := byRoot[v.ID]
		if preview == nil {
			preview = []View{}
		}
		v.ReplyCount = &count
		v.ReplyPreview = &preview
	}
	return nil
}
