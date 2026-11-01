package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"testing"
)

func TestBookmarkTransferSealsLegacyWritesAndRetainsOriginalReplay(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	if _, e := f.service.SetBookmark(ctx, student, "course", "old", true); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		ids, e := f.service.ExportBookmarks(ctx, student.Subject, "", 100)
		if e != nil || len(ids) != 1 || ids[0] != "course" {
			t.Fatal(ids, e)
		}
	}
	if _, e := f.service.SetBookmark(ctx, student, "late", "late", true); !errors.Is(e, r.ErrUnavailable) {
		t.Fatal("sealed source accepted a write", e)
	}
	if _, e := f.service.Bookmarks(ctx, student, "", 20); !errors.Is(e, r.ErrUnavailable) {
		t.Fatal("sealed source returned stale truth", e)
	}
	if _, e := f.service.MyReferences(ctx, student, []string{"course"}); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("sealed row granted name access", e)
	}
	if _, e := f.service.ExportBookmarks(ctx, "", "", 100); !errors.Is(e, r.ErrInput) {
		t.Fatal(e)
	}
	if _, e := f.service.ExportBookmarks(ctx, student.Subject, "", 101); !errors.Is(e, r.ErrInput) {
		t.Fatal(e)
	}
}
