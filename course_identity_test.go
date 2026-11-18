package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/postgres"
	"sync"
	"testing"
)

type courseIdentityCatalog struct {
	*catalog
	merged bool
}

func (c *courseIdentityCatalog) Target(ctx context.Context, kind, id string) (r.Target, error) {
	if c.merged && kind == "course" && id == "old-course" {
		id = "new-course"
	}
	return c.catalog.Target(ctx, kind, id)
}
func (c *courseIdentityCatalog) EquivalentIDs(_ context.Context, kind, id string) ([]string, error) {
	if c.merged && kind == "course" && (id == "old-course" || id == "new-course") {
		return []string{"old-course", "new-course"}, nil
	}
	return []string{id}, nil
}
func (c *courseIdentityCatalog) CanonicalCourses(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = id
		if c.merged && id == "old-course" {
			out[id] = "new-course"
		}
	}
	return out, nil
}

func TestCourseIdentityPreservesHistoryNewWritesAndCanonicalIdempotency(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	port := &courseIdentityCatalog{catalog: f.catalog}
	f.service.Catalog = port
	old := create(t, f, "before-cutover", input("old-course", "autumn", 2))
	port.merged = true
	replay, err := f.service.Create(ctx, student, "before-cutover", input("new-course", "autumn", 2))
	if err != nil || replay.ID != old.ID {
		t.Fatal("pre-cutover submission repeated", replay, err)
	}
	var wg sync.WaitGroup
	results := make(chan r.Result, 6)
	failures := make(chan error, 6)
	for i := range 6 {
		wg.Go(func() {
			id := "old-course"
			if i%2 == 0 {
				id = "new-course"
			}
			result, err := f.service.Create(ctx, student, "same-operation", input(id, "autumn", 4))
			results <- result
			failures <- err
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	newID := ""
	for result := range results {
		if newID == "" {
			newID = result.ID
		}
		if result.ID != newID {
			t.Fatal("duplicate alias submission", result)
		}
	}
	if _, err := f.service.Create(ctx, student, "same-operation", input("new-course", "autumn", 5)); !errors.Is(err, r.ErrConflict) {
		t.Fatal("different payload accepted", err)
	}
	for _, id := range []string{"old-course", "new-course"} {
		page, err := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: id, Limit: 100})
		if err != nil || len(page.Items) != 2 {
			t.Fatal(page, err)
		}
		stats, err := f.service.Stats(ctx, "course", id, nil, nil)
		if err != nil || len(stats.Courses) != 1 || stats.Dimensions[0].Samples != 2 {
			t.Fatal(stats, err)
		}
	}
	repo := f.service.Repo.(*postgres.Repository)
	var before r.Entry
	if err := repo.Transaction(ctx, func(tx r.Transaction) error { var err error; before, err = tx.Entry(old.ID); return err }); err != nil || before.Input.TargetID != "old-course" || len(before.Evidence) == 0 {
		t.Fatal("historical evidence rewritten", before, err)
	}
	// Recreate the service over the same database, as a code rollback/restart
	// does. Both pre-cutover and post-cutover entries remain readable.
	restarted, err := r.New(repo, port, f.access, f.moderation, profiles{}, r.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	restarted.Disclosures = ordinaryDisclosures{}
	for _, id := range []string{old.ID, newID} {
		if _, err := restarted.Get(ctx, student, id); err != nil {
			t.Fatal("write lost across restart", id, err)
		}
	}
	f.access.revoked = true
	if _, err := restarted.List(ctx, student, r.Filter{Kind: "course", TargetID: "old-course", Limit: 20}); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("alias bypassed revoked access", err)
	}
}

type listCatalog struct {
	*courseIdentityCatalog
	aliasReads int
}

func (c *listCatalog) EquivalentIDs(ctx context.Context, kind, id string) ([]string, error) {
	c.aliasReads++
	if c.failed {
		return nil, r.ErrUnavailable
	}
	return c.courseIdentityCatalog.EquivalentIDs(ctx, kind, id)
}
func (c *listCatalog) Target(ctx context.Context, kind, id string) (r.Target, error) {
	if kind == "course" && id == "teacher-a" {
		return r.Target{}, r.ErrNotFound
	}
	return c.courseIdentityCatalog.Target(ctx, kind, id)
}
func TestOwnerRecordsAndMentorDiscussionFilters(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	port := &listCatalog{courseIdentityCatalog: &courseIdentityCatalog{catalog: f.catalog}}
	f.service.Catalog = port
	in := input("course", "autumn", 4)
	in.Signed = true
	course := create(t, f, "signed", in)
	mentor := create(t, f, "mentor", r.Input{Kind: "mentor", TargetID: "teacher-a", Title: "Mentor", Body: "Advice"})
	reply, err := f.service.Reply(ctx, student, mentor.ID, "reply", r.Input{Kind: "reply", TargetID: "teacher-a", Body: "Reply"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"reply", ""} {
		page, err := f.service.List(ctx, student, r.Filter{TargetID: "teacher-a", Kind: kind, Limit: 10})
		found := false
		for _, v := range page.Items {
			found = found || v.ID == reply.ID
		}
		if err != nil || !found {
			t.Errorf("mentor discussion %q: %+v %v", kind, page, err)
		}
	}
	port.failed = true
	port.aliasReads = 0
	f.service.Profiles = nil
	f.access.revoked = true
	for _, id := range []string{"course", "unrelated"} {
		page, err := f.service.List(ctx, student, r.Filter{Owner: student.Subject, TargetID: id, Limit: 10})
		want := 0
		if id == "course" {
			want = 1
		}
		if err != nil || len(page.Items) != want {
			t.Errorf("own filter %s: %+v %v", id, page, err)
		}
	}
	if port.aliasReads != 0 {
		t.Errorf("own filters resolved %d aliases", port.aliasReads)
	}
	v, err := f.service.GetOwn(ctx, student, course.ID)
	if err != nil || v.ID != course.ID || v.Author.Profile != nil || v.Body != in.Body {
		t.Fatal(v, err)
	}
}
