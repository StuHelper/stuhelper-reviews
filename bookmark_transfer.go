package reviews

import (
	"context"
	"errors"
)

var ErrBookmarksMoved = errors.New("bookmarks_moved")

// BookmarkProvider is the authoritative favorites owner after application composition.
type BookmarkProvider interface {
	SetCourseBookmark(context.Context, string, string, string, bool) error
	CourseBookmarks(context.Context, string, string, int) ([]string, error)
	CourseReferences(context.Context, string, []string) ([]string, error)
}

// ExportBookmarks seals legacy writes and returns an immutable, bounded page.
// Only trusted application composition may call this; it has no HTTP route.
func (s *Service) ExportBookmarks(ctx context.Context, owner, after string, limit int) ([]string, error) {
	if owner == "" || limit < 1 || limit > 100 || after != "" && !validID(after) {
		return nil, ErrInput
	}
	var out []string
	e := s.Repo.Transaction(ctx, func(tx Transaction) error { var e error; out, e = tx.BookmarkTransfer(owner, after, limit); return e })
	return out, e
}
func (s *Service) oldBookmarkReplay(ctx context.Context, p Principal, target, key string, set bool) (Result, bool, error) {
	var result Result
	var found bool
	e := s.Repo.Transaction(ctx, func(tx Transaction) error {
		var e error
		result, found, e = replay(tx, hash([]string{p.Subject, "bookmark", target, key}), hash(set), s.now())
		return e
	})
	return result, found, e
}
