package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"testing"
)

func TestCatalogReadOnlyKeepsHistoryAndRejectsNewPublicWrites(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	in := input("course", "autumn", 4)
	root := create(t, f, "before-cutoff", in)
	voter := r.Principal{Subject: "voter"}
	if _, err := f.service.Action(ctx, voter, root.ID, "before-vote", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	f.catalog.readOnly = map[string]bool{"course": true}
	view, err := f.service.Get(ctx, student, root.ID)
	if err != nil || view.Body != in.Body || view.Upvotes != 1 {
		t.Fatal("existing review disappeared", view, err)
	}
	stats, err := f.service.Stats(ctx, "course", "course", nil, nil)
	if err != nil || stats.Dimensions[0].Samples != 1 {
		t.Fatal("existing rating disappeared", stats, err)
	}
	for _, term := range []string{"", "autumn"} {
		composer, err := f.service.Composer(ctx, student, "course", "course", term, "")
		if err != nil || composer.Allowed || composer.State != "forbidden" {
			t.Fatal("readonly composer", composer, err)
		}
	}
	if _, err := f.service.Create(ctx, student, "after-cutoff", in); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("readonly publication accepted", err)
	}
	if _, err := f.service.Reply(ctx, voter, root.ID, "after-reply", r.Input{Kind: "reply", TargetID: "course", Body: "new reply"}); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("readonly reply accepted", err)
	}
	if _, err := f.service.Action(ctx, voter, root.ID, "after-vote", r.Action{Action: "vote", Vote: -1}); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("readonly vote accepted", err)
	}
	view, err = f.service.Get(ctx, student, root.ID)
	if err != nil || view.Upvotes != 1 || view.Downvotes != 0 {
		t.Fatal("rejected vote changed state", view, err)
	}
	page, err := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "course", Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("rejected write persisted", page, err)
	}
	// Catalog evidence is still available for an authorized attribution review.
	if _, err = f.service.PreviewAttribution(ctx, r.Principal{Subject: "admin"}, root.ID, ""); err != nil {
		t.Fatal("maintenance evidence blocked", err)
	}
	withdrawn, err := f.service.Action(ctx, student, root.ID, "withdraw", r.Action{Action: "withdraw", Revision: root.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Action(ctx, student, root.ID, "republish", r.Action{Action: "republish", Revision: withdrawn.Revision, Input: &in}); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("readonly republish accepted", err)
	}
	f.catalog.failed = true
	if _, err = f.service.Action(ctx, voter, root.ID, "remove-own-vote", r.Action{Action: "vote", Vote: 0}); err != nil {
		t.Fatal("own cancellation blocked", err)
	}
	votes, err := f.service.MyVotes(ctx, voter, "", 100)
	if err != nil || len(votes) != 0 {
		t.Fatal("own vote remained", votes, err)
	}
}

func TestCatalogReadOnlyTermRejectsNewReviewOnCurrentCourse(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.catalog.readOnly = map[string]bool{"autumn": true}
	if _, err := f.service.Create(ctx, student, "old-term", input("course", "autumn", 4)); !errors.Is(err, r.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := f.service.Create(ctx, student, "recent-term", input("course", "spring", 4)); err != nil {
		t.Fatal(err)
	}
	page, err := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "course", Limit: 100})
	if err != nil || len(page.Items) != 1 || page.Items[0].TermID != "spring" {
		t.Fatal(page, err)
	}
}
