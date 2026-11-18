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
		stats, err := f.service.ReadStatsPage(ctx, student, "course", id, nil, nil, "", 20)
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
