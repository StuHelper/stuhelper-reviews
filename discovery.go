package reviews

import (
	"context"
	"errors"
	"time"
)

// DiscoveryDocument is the public search projection. It deliberately has no
// body, identity, evidence, origin, payment or rating fields.
type DiscoveryTarget struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	Kind   string `json:"kind"`
}
type DiscoveryDocument struct {
	Target     DiscoveryTarget `json:"target"`
	Revision   int64           `json:"revision"`
	Title      string          `json:"title"`
	SchoolID   string          `json:"school_id"`
	Visible    bool            `json:"visible"`
	CourseID   string          `json:"course_id,omitempty"`
	TeacherIDs []string        `json:"teacher_ids,omitempty"`
	At         time.Time       `json:"at"`
}
type DiscoveryPage struct {
	Items []DiscoveryDocument
	Next  string
}

// DiscoverySource is for trusted rebuild jobs, not an unauthenticated HTTP route.
type DiscoverySource interface {
	DiscoverySnapshot(context.Context, string, int) (DiscoveryPage, error)
}

// DiscoveryCurrent applies the same current read admission and school scope as
// public review reads. Revocation hides results; dependency failures propagate.
func (s *Service) DiscoveryCurrent(ctx context.Context, p Principal, ids []string) ([]DiscoveryDocument, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return nil, ErrInput
	}
	for _, id := range ids {
		if !validID(id) {
			return nil, ErrInput
		}
	}
	out := []DiscoveryDocument{}
	school, e := s.contentScope(ctx, p, "")
	if errors.Is(e, ErrForbidden) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	e = s.Repo.Transaction(ctx, func(t Transaction) error { var e error; out, e = t.DiscoveryCurrent(school, ids, s.now()); return e })
	return out, e
}

// DiscoveryVotePage maps directly to search's current-user Votes result.
type DiscoveryVote struct {
	Target DiscoveryTarget `json:"target"`
	Choice string          `json:"choice"`
}
type DiscoveryVotePage struct {
	Items      []DiscoveryVote `json:"items"`
	NextCursor string          `json:"next_cursor"`
}

// DiscoveryMyVotes is distinct from MyVotes' own-action receipts: each target
// must still be readable. The cursor advances over inspected, filtered rows.
func (s *Service) DiscoveryMyVotes(ctx context.Context, p Principal, cursor string, limit int) (DiscoveryVotePage, error) {
	out := DiscoveryVotePage{Items: []DiscoveryVote{}}
	if limit < 1 || limit > 100 || cursor != "" && !validID(cursor) {
		return out, ErrInput
	}
	school, e := s.contentScope(ctx, p, "")
	if errors.Is(e, ErrForbidden) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	var entries []Entry
	e = s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		entries, e = t.Voted(p.Subject, cursor, limit+1, s.now())
		return e
	})
	if e != nil {
		return out, e
	}
	if len(entries) > limit {
		entries = entries[:limit]
		out.NextCursor = entries[len(entries)-1].ID
	}
	for _, entry := range entries {
		if entry.SchoolID != school {
			continue
		}
		view, e := s.Get(ctx, p, entry.ID)
		if errors.Is(e, ErrNotFound) || errors.Is(e, ErrForbidden) {
			continue
		}
		if e != nil {
			return DiscoveryVotePage{}, e
		}
		choice := ""
		if view.MyVote == 1 {
			choice = "up"
		}
		if view.MyVote == -1 {
			choice = "down"
		}
		if choice == "" {
			continue
		}
		out.Items = append(out.Items, DiscoveryVote{DiscoveryTarget{Source: "reviews", ID: entry.ID, Kind: "review"}, choice})
	}
	return out, nil
}
