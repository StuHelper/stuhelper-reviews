package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"sync"
	"testing"
)

func TestClosedOwnerKeepsContentWithoutProfileOrDisclosures(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	in := input("course", "autumn", 4)
	in.Signed = true
	entry := create(t, f, "signed", in)
	before, e := f.service.Get(ctx, r.Principal{Subject: "reader"}, entry.ID)
	if e != nil || before.Author.Profile == nil || before.Author.Anonymous {
		t.Fatal(before, e)
	}
	id := "11111111-1111-4111-8111-111111111111"
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- f.service.AnonymizeOwner(ctx, student.Subject, id) })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	after, e := f.service.Get(ctx, r.Principal{Subject: "reader"}, entry.ID)
	if e != nil || after.Body != before.Body || after.Author.Profile != nil || !after.Author.Anonymous || len(after.Author.Disclosures) != 0 {
		t.Fatal(after, e)
	}
	if _, e = f.service.Create(ctx, student, "late", input("other", "autumn", 3)); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("post-closure publication", e)
	}
	if e = f.service.AnonymizeOwner(ctx, student.Subject, "22222222-2222-4222-8222-222222222222"); !errors.Is(e, r.ErrConflict) {
		t.Fatal(e)
	}
}

type closureModerationGate struct{ entered, release chan struct{} }

func (g closureModerationGate) Check(ctx context.Context, _ r.Candidate) (string, error) {
	close(g.entered)
	select {
	case <-g.release:
		return "published", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func TestClosureRejectsPublicationAlreadyWaitingForExternalModeration(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	gate := closureModerationGate{make(chan struct{}), make(chan struct{})}
	f.service.Moderation = gate
	done := make(chan error, 1)
	go func() { _, e := f.service.Create(ctx, student, "in-flight", input("course", "autumn", 4)); done <- e }()
	<-gate.entered
	if e := f.service.AnonymizeOwner(ctx, student.Subject, "11111111-1111-4111-8111-111111111111"); e != nil {
		t.Fatal(e)
	}
	close(gate.release)
	if e := <-done; !errors.Is(e, r.ErrForbidden) {
		t.Fatal("publication survived closure", e)
	}
	page, e := f.service.List(ctx, r.Principal{Subject: "reader"}, r.Filter{Limit: 20})
	if e != nil || len(page.Items) != 0 {
		t.Fatal("late content persisted", page, e)
	}
}
