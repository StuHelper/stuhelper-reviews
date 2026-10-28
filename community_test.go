package reviews_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/postgres"
)

func TestDraftPublicationIsAtomicAndRecoverable(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	draft, err := f.service.SaveDraft(ctx, student, "", "notes", input("course", "autumn", 3), 0)
	if err != nil {
		t.Fatal(err)
	}
	request := r.PublishDraft{Revision: draft.Revision, Input: input("course", "autumn", 4)}
	if _, err = f.service.PublishDraft(ctx, r.Principal{Subject: "other"}, draft.ID, "publish", request); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("another user published draft", err)
	}
	f.moderation.failed = true
	if _, err = f.service.PublishDraft(ctx, student, draft.ID, "publish", request); !errors.Is(err, r.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = f.service.GetDraft(ctx, student, draft.ID); err != nil {
		t.Fatal("failed publication removed draft", err)
	}
	f.moderation.failed = false
	var wg sync.WaitGroup
	results := make(chan r.Result, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			v, e := f.service.PublishDraft(ctx, student, draft.ID, "publish", request)
			results <- v
			errs <- e
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for v := range results {
		if id != "" && id != v.ID {
			t.Fatal("duplicate publication")
		}
		id = v.ID
	}
	if _, err = f.service.GetDraft(ctx, student, draft.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("published draft remained", err)
	}
	page, err := f.service.List(ctx, student, r.Filter{Owner: student.Subject, Limit: 100})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != id {
		t.Fatal(page, err)
	}
	changed := request
	changed.Input.Body = "Changed after retry"
	if _, err = f.service.PublishDraft(ctx, student, draft.ID, "publish", changed); !errors.Is(err, r.ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	other, err := f.service.SaveDraft(ctx, student, "", "other", request.Input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.SaveDraft(ctx, student, other.ID, "edit", request.Input, other.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.PublishDraft(ctx, student, other.ID, "stale", r.PublishDraft{Revision: other.Revision, Input: request.Input}); !errors.Is(err, r.ErrConflict) {
		t.Fatal("stale draft published", err)
	}
	if _, err = f.service.GetDraft(ctx, student, other.ID); err != nil {
		t.Fatal("conflict removed draft", err)
	}
}

func TestWithdrawnDraftSaveKeepsExpiryAndAttribution(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	original := input("course", "autumn", 4)
	root := create(t, f, "root", original)
	f.now = f.now.Add(4 * time.Minute)
	withdrawn, err := f.service.Action(ctx, student, root.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	expires := *withdrawn.ExpiresAt
	f.now = f.now.Add(time.Hour)
	edit := original
	edit.Body = "New notes"
	edit.Title = ""
	edit.Ratings = [5]int{}
	saved, err := f.service.Action(ctx, student, root.ID, "save", r.Action{Action: "save_withdrawn", Revision: withdrawn.Revision, Input: &edit})
	if err != nil || saved.ExpiresAt == nil || !saved.ExpiresAt.Equal(expires) || saved.State != "withdrawn" {
		t.Fatal(saved, err)
	}
	draft, err := f.service.GetDraft(ctx, student, root.ID)
	if err != nil || draft.Input.Body != edit.Body || !draft.Withdrawn {
		t.Fatal(draft, err)
	}
	if _, err = f.service.GetDraft(ctx, r.Principal{Subject: "other"}, root.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("draft leaked", err)
	}
	wrong := edit
	wrong.TermID = "spring"
	if _, err = f.service.Action(ctx, student, root.ID, "reassign", r.Action{Action: "save_withdrawn", Revision: saved.Revision, Input: &wrong}); !errors.Is(err, r.ErrInput) {
		t.Fatal("attribution changed", err)
	}
	if _, err = f.service.Get(ctx, student, root.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("saving draft published it", err)
	}
	f.now = expires
	if _, err = f.service.GetDraft(ctx, student, root.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("expired draft readable", err)
	}
	if n, err := f.service.Sweep(ctx, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestChronologicalPaginationTeacherFilterAndPrivateDetails(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	ids := []string{}
	for i := 0; i < 4; i++ {
		f.now = f.now.Add(time.Minute)
		in := input("course", "autumn", 3)
		in.TeacherID = "teacher-a"
		if i == 1 {
			in.TeacherID = "teacher-b"
		}
		ids = append(ids, create(t, f, fmt.Sprint(i), in).ID)
	}
	filter := r.Filter{Kind: "course", TargetID: "course", TeacherID: "teacher-a", Sort: "newest", Limit: 2}
	first, err := f.service.List(ctx, student, filter)
	if err != nil || len(first.Items) != 2 || first.Items[0].ID != ids[3] || first.Items[1].ID != ids[2] || first.Next == "" {
		t.Fatal(first, err)
	}
	f.now = f.now.Add(time.Minute)
	create(t, f, "newer", input("course", "autumn", 2))
	filter.After = first.Next
	second, err := f.service.List(ctx, student, filter)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != ids[0] || second.Next != "" {
		t.Fatal(second, err)
	}
	filter.TeacherID = "teacher-b"
	if _, err = f.service.List(ctx, student, filter); !errors.Is(err, r.ErrInput) {
		t.Fatal("cursor from different filter accepted", err)
	}
	filter = r.Filter{Kind: "course", TargetID: "course", Sort: "oldest", Limit: 100}
	oldest, err := f.service.List(ctx, student, filter)
	if err != nil || oldest.Items[0].ID != ids[0] {
		t.Fatal(oldest, err)
	}
	f.moderation.outcome = "pending"
	pending := create(t, f, "pending", input("course", "autumn", 5))
	if _, err = f.service.Get(ctx, student, pending.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("pending became public")
	}
	own, err := f.service.GetOwn(ctx, student, pending.ID)
	if err != nil || own.State != "pending" || own.CanEdit {
		t.Fatal(own, err)
	}
	if _, err = f.service.GetOwn(ctx, r.Principal{Subject: "other"}, pending.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("private pending entry leaked")
	}
}

func TestComposerUsesCurrentFactsAndDiscussionIdentity(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	guest, err := f.service.Composer(ctx, r.Principal{}, "course", "course", "autumn", "")
	if err != nil || guest.Allowed || guest.State != "unauthenticated" {
		t.Fatal(guest, err)
	}
	choice, err := f.service.Composer(ctx, student, "course", "course", "", "")
	if err != nil || choice.State != "choose_term" || choice.Allowed {
		t.Fatal(choice, err)
	}
	fall, err := f.service.Composer(ctx, student, "course", "course", "autumn", "")
	if err != nil || !fall.Allowed || !slices.Equal(fall.TeacherIDs, []string{"teacher-a", "teacher-b"}) {
		t.Fatal(fall, err)
	}
	f.catalog.missing = true
	missing, err := f.service.Composer(ctx, student, "course", "course", "autumn", "")
	if err != nil || missing.State != "enrollment_required" || missing.Allowed {
		t.Fatal(missing, err)
	}
	mentor, err := f.service.Composer(ctx, student, "mentor", "teacher", "", "")
	if err != nil || !mentor.Allowed {
		t.Fatal("mentor incorrectly required enrollment", mentor, err)
	}
	f.catalog.missing = false
	in := input("course", "autumn", 3)
	in.Signed = true
	root := create(t, f, "root", in)
	reply, err := f.service.Composer(ctx, student, "reply", "course", "", root.ID)
	if err != nil || reply.Identity != "signed" || !reply.Allowed {
		t.Fatal(reply, err)
	}
	f.access.revoked = true
	revoked, err := f.service.Composer(ctx, student, "mentor", "teacher", "", "")
	if err != nil || revoked.Allowed || revoked.State != "forbidden" {
		t.Fatal(revoked, err)
	}
}

func TestCreationTimeMigrationPreservesExistingData(t *testing.T) {
	repo, migration := database(t)
	ctx := context.Background()
	f := fixture(t)
	f.service.Repo = repo
	root := create(t, f, "legacy", input("course", "autumn", 3))
	if _, err := migration.Exec(`ALTER TABLE reviews.entries DROP COLUMN created_at CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.Exec(postgres.Schema); err != nil {
		t.Fatal(err)
	}
	var at time.Time
	if err := migration.QueryRow(`SELECT created_at FROM reviews.entries WHERE id=$1`, root.ID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(f.now.Round(time.Microsecond)) {
		t.Fatal("migration changed creation time", at, f.now)
	}
	got, err := f.service.Get(ctx, student, root.ID)
	if err != nil || got.Body != input("course", "autumn", 3).Body {
		t.Fatal("migration lost entry", got, err)
	}
}

func TestPrivateReplyListRespectsWithdrawnRoot(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	root := create(t, f, "root", input("course", "autumn", 3))
	other := r.Principal{Subject: "other"}
	f.moderation.outcome = "pending"
	reply, err := f.service.Reply(ctx, other, root.ID, "reply", r.Input{Kind: "reply", TargetID: "course", Body: "Waiting for moderation"})
	if err != nil {
		t.Fatal(err)
	}
	filter := r.Filter{Owner: other.Subject, Limit: 100}
	page, err := f.service.List(ctx, other, filter)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != reply.ID {
		t.Fatal(page, err)
	}
	if _, err = f.service.Action(ctx, student, root.ID, "withdraw", r.Action{Action: "withdraw", Revision: root.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.GetOwn(ctx, other, reply.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("reply detail survived root withdrawal", err)
	}
	page, err = f.service.List(ctx, other, filter)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("reply list survived root withdrawal", page, err)
	}
}
