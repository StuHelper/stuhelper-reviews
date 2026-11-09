package reviews_test

import (
	"context"
	"fmt"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/postgres"
	"sync"
	"testing"
	"time"
)

func TestActivityReviewsVotesRepliesAndOriginalDayCorrections(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	repo := f.service.Repo.(*postgres.Repository)
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	f.now = day.Add(12 * time.Hour)
	root := create(t, f, "activity-root", input("course", "autumn", 4))
	assert := func(day time.Time, reviews, interactions int64) r.Activity {
		t.Helper()
		d, e := repo.ReadActivity(ctx, r.ActivityID("course", "course", day))
		if e != nil || d.Metrics["reviews"] != reviews || d.Metrics["interactions"] != interactions {
			t.Fatal(d, e)
		}
		return d
	}
	assert(day, 1, 0)
	reply, e := f.service.Reply(ctx, r.Principal{Subject: "reply-owner"}, root.ID, "reply", r.Input{Kind: "reply", TargetID: "course", Body: "private reply"})
	if e != nil {
		t.Fatal(e)
	}
	voter := r.Principal{Subject: "voter"}
	if _, e = f.service.Action(ctx, voter, root.ID, "vote-up", r.Action{Action: "vote", Vote: 1}); e != nil {
		t.Fatal(e)
	}
	before := assert(day, 1, 2)
	if _, e = f.service.Action(ctx, voter, root.ID, "vote-down", r.Action{Action: "vote", Vote: -1}); e != nil {
		t.Fatal(e)
	}
	if d := assert(day, 1, 2); d.Revision != before.Revision {
		t.Fatal("vote flip double-counted", d)
	}
	root, e = f.service.Action(ctx, student, root.ID, "withdraw", r.Action{Action: "withdraw", Revision: root.Revision})
	if e != nil {
		t.Fatal(e)
	}
	if d := assert(day, 0, 0); d.Visible {
		t.Fatal(d)
	}
	in := input("course", "autumn", 4)
	root, e = f.service.Action(ctx, student, root.ID, "republish", r.Action{Action: "republish", Revision: root.Revision, Input: &in})
	if e != nil {
		t.Fatal(e)
	}
	assert(day, 1, 2)
	f.now = f.now.Add(24 * time.Hour)
	if _, e = f.service.Action(ctx, voter, root.ID, "cancel-next-day", r.Action{Action: "vote", Vote: 0}); e != nil {
		t.Fatal(e)
	}
	assert(day, 1, 1)
	if _, e = f.service.Action(ctx, voter, root.ID, "recast", r.Action{Action: "vote", Vote: 1}); e != nil {
		t.Fatal(e)
	}
	assert(day.AddDate(0, 0, 1), 0, 1)
	admin := r.Principal{Subject: "admin"}
	root, e = f.service.Apply(ctx, admin, r.Decision{ID: "hide-activity", ObjectID: root.ID, Revision: root.Revision, Action: "hide", Reason: "reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	assert(day, 0, 0)
	assert(day.AddDate(0, 0, 1), 0, 0)
	root, e = f.service.Apply(ctx, admin, r.Decision{ID: "restore-activity", ObjectID: root.ID, Revision: root.Revision, Action: "restore", Reason: "reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	assert(day, 1, 1)
	assert(day.AddDate(0, 0, 1), 0, 1)
	// Hiding a single reply removes its own contribution without discarding the review.
	if _, e = f.service.Apply(ctx, admin, r.Decision{ID: "hide-reply", ObjectID: reply.ID, Revision: reply.Revision, Action: "hide", Reason: "reviewed"}); e != nil {
		t.Fatal(e)
	}
	assert(day, 1, 0)
	if _, e = f.service.Apply(ctx, admin, r.Decision{ID: "delete-root", ObjectID: root.ID, Revision: root.Revision, Action: "delete", Reason: "reviewed"}); e != nil {
		t.Fatal(e)
	}
	assert(day, 0, 0)
	assert(day.AddDate(0, 0, 1), 0, 0)
	teacher, e := repo.ReadActivity(ctx, r.ActivityID("teacher", "teacher-a", day))
	if e != nil || teacher.Visible || teacher.Metrics["reviews"] != 0 || teacher.Metrics["interactions"] != 0 {
		t.Fatal(teacher, e)
	}
	if _, ok, e := f.service.CurrentActivity(ctx, student, r.ActivityID("course", "course", day)); e != nil || !ok {
		t.Fatal("zero snapshot should remain readable", ok, e)
	}
	f.access.revoked = true
	if _, ok, e := f.service.CurrentActivity(ctx, student, r.ActivityID("course", "course", day)); e != nil || ok {
		t.Fatal(ok, e)
	}
}

func TestActivityConcurrentReviewsAndVotesDoNotLoseCounts(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	repo := f.service.Repo.(*postgres.Repository)
	day := f.now.UTC().Truncate(24 * time.Hour)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			_, e := f.service.Create(ctx, r.Principal{Subject: fmt.Sprintf("author-%d", i)}, "create", input("course", "autumn", 4))
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	d, e := repo.ReadActivity(ctx, r.ActivityID("course", "course", day))
	if e != nil || d.Metrics["reviews"] != 8 || d.Revision != 8 {
		t.Fatal(d, e)
	}
	root := create(t, f, "voting-root", input("course", "autumn", 4))
	errs = make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			_, e := f.service.Action(ctx, r.Principal{Subject: fmt.Sprintf("voter-%d", i)}, root.ID, "vote", r.Action{Action: "vote", Vote: 1})
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	d, e = repo.ReadActivity(ctx, d.ID)
	if e != nil || d.Metrics["reviews"] != 9 || d.Metrics["interactions"] != 8 {
		t.Fatal(d, e)
	}
}

func TestActivityHidesBucketsWithUndisclosableTeacherReplies(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	root := create(t, f, "teacher-root", input("course", "spring", 3))
	facts := factsFor(f, true)
	f.service.Disclosures = facts
	_, e := f.service.Reply(ctx, r.Principal{Subject: "teacher-account"}, root.ID, "reply", r.Input{Kind: "reply", TargetID: "course", Body: "Teacher response", Disclosures: []string{"real_name", "teacher"}})
	if e != nil {
		t.Fatal(e)
	}
	id := r.ActivityID("course", "course", f.now)
	if d, ok, e := f.service.CurrentActivity(ctx, student, id); e != nil || !ok || d.Metrics["interactions"] != 1 {
		t.Fatal(d, ok, e)
	}
	facts.err = r.ErrUnavailable
	if _, _, e = f.service.CurrentActivity(ctx, student, id); e == nil {
		t.Fatal("disclosure outage became a zero contribution")
	}
	facts.err = nil
	_, e = f.service.Reply(ctx, r.Principal{Subject: "teacher-account"}, root.ID, "second-reply", r.Input{Kind: "reply", TargetID: "course", Body: "Second response", Disclosures: []string{"real_name", "teacher"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Repo.(r.ActivityDisclosureReader).ActivityDisclosureEntries(ctx, id, 1); e == nil {
		t.Fatal("unbounded disclosure reads")
	}
	revoked := facts.facts.Attributes["real_name"]
	revoked.MayDisclose = false
	facts.facts.Attributes["real_name"] = revoked
	if _, ok, e := f.service.CurrentActivity(ctx, student, id); e != nil || ok {
		t.Fatal("hidden teacher reply contributed publicly", ok, e)
	}
}

type activityCanonicalCatalog struct{ *catalog }

func (c activityCanonicalCatalog) Target(ctx context.Context, kind, id string) (r.Target, error) {
	if kind == "course" && id == "course-alias" {
		id = "course"
	}
	return c.catalog.Target(ctx, kind, id)
}
func TestActivityReviewUsesCanonicalCourseFact(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.service.Catalog = activityCanonicalCatalog{f.catalog}
	create(t, f, "canonical", input("course-alias", "autumn", 4))
	repo := f.service.Repo.(*postgres.Repository)
	d, e := repo.ReadActivity(ctx, r.ActivityID("course", "course", f.now))
	if e != nil || d.Metrics["reviews"] != 1 {
		t.Fatal(d, e)
	}
	if _, e = repo.ReadActivity(ctx, r.ActivityID("course", "course-alias", f.now)); e == nil {
		t.Fatal("alias counted separately")
	}
}
