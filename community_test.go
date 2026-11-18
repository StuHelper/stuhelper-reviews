package reviews_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/StuHelper/stuhelper-reviews/postgres"
	"slices"
	"sync"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
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
	if !errors.Is(err, r.ErrUnauthenticated) || guest.Allowed {
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
	if !errors.Is(err, r.ErrForbidden) || revoked.Allowed {
		t.Fatal(revoked, err)
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

func TestSoftDeletePreservesHistoryDraftPaginationAndIsolatedSweep(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	admin := r.Principal{Subject: "admin"}

	// 1. A-02 & A-05.1: Publish -> reply & vote -> withdraw -> republish into "pending" -> moderator "delete".
	rootIn := input("course", "autumn", 4)
	rootIn.Signed = false
	root := create(t, f, "soft-delete-root", rootIn)
	other := r.Principal{Subject: "other-replier"}
	reply, err := f.service.Reply(ctx, other, root.ID, "child-reply", r.Input{Kind: "reply", TargetID: "course", Body: "Child reply", Signed: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Action(ctx, other, root.ID, "upvote-root", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	// Moderator toggles reply author's discussion identity to Signed: true, then reply author withdraws reply to test A-05.3 fields.
	if _, err = f.service.Apply(ctx, admin, r.Decision{ID: "toggle-sign", ObjectID: root.ID, Revision: root.Revision, Action: "identity", Subject: other.Subject, Signed: true, Reason: "Verify signed"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	withdrawnReply, err := f.service.Action(ctx, other, reply.ID, "withdraw-reply", r.Action{Action: "withdraw", Revision: reply.Revision})
	if err != nil {
		t.Fatal(err)
	}

	// A-05.3: Drafts list must include RootID, ParentID, synced Signed, and reject invalid after cursor.
	if _, err = f.service.Drafts(ctx, other, "invalid cursor!", 10); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput on invalid after cursor, got %v", err)
	}
	replyDrafts, err := f.service.Drafts(ctx, other, "", 10)
	if err != nil || len(replyDrafts) != 1 {
		t.Fatalf("expected 1 withdrawn reply draft, got %+v err=%v", replyDrafts, err)
	}
	if replyDrafts[0].RootID != root.ID || replyDrafts[0].ParentID != root.ID || !replyDrafts[0].Input.Signed {
		t.Fatalf("Drafts omitted RootID/ParentID/Signed: %+v", replyDrafts[0])
	}
	_ = withdrawnReply

	// Withdraw root, then republish into pending moderation.
	currentRoot, err := f.service.Get(ctx, student, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	withdrawnRoot, err := f.service.Action(ctx, student, root.ID, "withdraw-root", r.Action{Action: "withdraw", Revision: currentRoot.Revision})
	if err != nil {
		t.Fatal(err)
	}
	f.moderation.outcome = "pending"
	repubIn := rootIn
	repubIn.Body = "Republished waiting for human review"
	pendingRoot, err := f.service.Action(ctx, student, root.ID, "republish-pending", r.Action{Action: "republish", Revision: withdrawnRoot.Revision, Input: &repubIn})
	if err != nil || pendingRoot.State != "pending" {
		t.Fatalf("expected pending state, got %+v err=%v", pendingRoot, err)
	}

	// A-05.1: Pending entry must NOT appear in Drafts.
	studentDrafts, err := f.service.Drafts(ctx, student, "", 10)
	if err != nil || len(studentDrafts) != 0 {
		t.Fatalf("pending republished entry leaked into Drafts: %+v err=%v", studentDrafts, err)
	}

	// Moderator soft-deletes the pending entry; advance past original 24h withdrawal expiry and run Sweep.
	deletedRes, err := f.service.Apply(ctx, admin, r.Decision{ID: "mod-soft-delete", ObjectID: root.ID, Revision: pendingRoot.Revision, Action: "delete", Reason: "Policy violation"})
	if err != nil || deletedRes.State != "deleted" || deletedRes.ExpiresAt != nil {
		t.Fatalf("moderator delete kept ExpiresAt or wrong state: %+v err=%v", deletedRes, err)
	}
	f.now = f.now.Add(25 * time.Hour)
	if _, err = f.service.Sweep(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// Soft-deleted entry, its moderation history, and the ability to restore (preserving votes) must survive Sweep!
	adminGot, err := f.service.AdminGet(ctx, admin, root.ID)
	if err != nil || adminGot.State != "deleted" {
		t.Fatalf("soft-deleted entry vanished or became unreadable after 24h: %+v err=%v", adminGot, err)
	}
	hist, err := f.service.History(ctx, admin, root.ID, 0, 20)
	if err != nil || len(hist) < 4 {
		t.Fatalf("moderation history lost after Sweep: %+v err=%v", hist, err)
	}
	restored, err := f.service.Apply(ctx, admin, r.Decision{ID: "mod-restore", ObjectID: root.ID, Revision: deletedRes.Revision, Action: "restore", Reason: "Appeal accepted"})
	if err != nil || restored.State != "published" {
		t.Fatalf("restore failed after 24h: %+v err=%v", restored, err)
	}
	restoredPublic, err := f.service.Get(ctx, student, root.ID)
	if err != nil || restoredPublic.Upvotes != 1 {
		t.Fatalf("votes lost on soft-deleted entry: %+v err=%v", restoredPublic, err)
	}

	// 2. A-05.2: Expired unswept withdrawals with smaller IDs must NOT starve unexpired withdrawals in Drafts(limit=2).
	f.moderation.outcome = "published"
	author2 := r.Principal{Subject: "draft-pager"}
	createdIDs := make([]string, 0, 4)
	for i := range 4 {
		res, err := f.service.Create(ctx, author2, fmt.Sprintf("w-%d", i), input(fmt.Sprintf("c-%d", i), "autumn", 4))
		if err != nil {
			t.Fatal(err)
		}
		createdIDs = append(createdIDs, res.ID)
	}
	slices.Sort(createdIDs)
	// Withdraw the two lexicographically smallest IDs first and let them expire WITHOUT running Sweep.
	for _, id := range createdIDs[:2] {
		if _, err = f.service.Action(ctx, author2, id, "wd-"+id, r.Action{Action: "withdraw", Revision: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Withdraw the 3rd ID 2 hours later so it remains unexpired when the first 2 expire.
	f.now = f.now.Add(2 * time.Minute)
	laterPost, err := f.service.Create(ctx, author2, "w-fresh", input("c-fresh", "autumn", 5))
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(23*time.Hour + 59*time.Minute)
	// Wait: laterPost is past 5m edit window if we advance 23h59m first; create and withdraw a fresh post whose ID > createdIDs[1]!
	_ = laterPost
	var freshID string
	for attempt := range 50 {
		cand, err := f.service.Create(ctx, author2, fmt.Sprintf("fresh-cand-%d", attempt), input(fmt.Sprintf("fresh-course-%d", attempt), "autumn", 5))
		if err != nil {
			t.Fatal(err)
		}
		if cand.ID > createdIDs[1] {
			freshID = cand.ID
			if _, err = f.service.Action(ctx, author2, freshID, "wd-fresh", r.Action{Action: "withdraw", Revision: 1}); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if freshID == "" {
		t.Fatal("failed to generate ID after expired prefix")
	}
	// Advance 2 minutes so createdIDs[:2] are expired (>= 24h) while freshID has ~23h58m remaining!
	f.now = f.now.Add(2 * time.Minute)
	pagedDrafts, err := f.service.Drafts(ctx, author2, "", 2)
	if err != nil || len(pagedDrafts) != 1 || pagedDrafts[0].ID != freshID {
		t.Fatalf("expired unswept rows starved fresh withdrawn draft: got %+v err=%v (expected [%s])", pagedDrafts, err, freshID)
	}

	// 3. A-09: Concurrent Sweep across multiple expired roots and concurrent Create/Vote must complete without deadlock or batch loss.
	for i := range 12 {
		p := r.Principal{Subject: fmt.Sprintf("sweeper-%d", i)}
		res, err := f.service.Create(ctx, p, fmt.Sprintf("exp-%d", i), input(fmt.Sprintf("sweep-c-%d", i%3), "autumn", 4))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.service.Action(ctx, p, res.ID, fmt.Sprintf("exp-wd-%d", i), r.Action{Action: "withdraw", Revision: 1}); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Second)
	}
	f.now = f.now.Add(25 * time.Hour)
	var wg sync.WaitGroup
	sweepErrs := make(chan error, 8)
	for g := range 4 {
		wg.Go(func() {
			_, err := f.service.Sweep(ctx, 10)
			sweepErrs <- err
		})
		wg.Go(func() {
			p := r.Principal{Subject: fmt.Sprintf("concurrent-writer-%d", g)}
			_, err := f.service.Create(ctx, p, fmt.Sprintf("conc-%d", g), input(fmt.Sprintf("sweep-c-%d", g%3), "autumn", 5))
			sweepErrs <- err
		})
	}
	wg.Wait()
	close(sweepErrs)
	for err := range sweepErrs {
		if err != nil {
			t.Fatalf("concurrent Sweep/Create failed: %v", err)
		}
	}
	if _, err = f.service.Sweep(ctx, 100); err != nil {
		t.Fatal(err)
	}
	remaining, err := f.service.Sweep(ctx, 100)
	if err != nil || remaining != 0 {
		t.Fatalf("expected all expired entries swept, remaining=%d err=%v", remaining, err)
	}
}

func TestLegacySoftDeletionRemainsRestorable(t *testing.T) {
	f := fixture(t)
	repo, db := database(t)
	f.service.Repo = repo
	ctx := context.Background()
	root := create(t, f, "legacy", input("course", "autumn", 4))
	admin := r.Principal{Subject: "admin"}
	deleted, err := f.service.Apply(ctx, admin, r.Decision{ID: "delete", ObjectID: root.ID, Revision: 1, Action: "delete", Reason: "Removed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE reviews.entries SET expires_at=now()-interval '2 days',data=jsonb_set(data,'{expires_at}',to_jsonb(now()-interval '2 days')) WHERE id=$1`, root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(postgres.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Sweep(ctx, 10); err != nil {
		t.Fatal(err)
	}
	restored, err := f.service.Apply(ctx, admin, r.Decision{ID: "restore", ObjectID: root.ID, Revision: deleted.Revision, Action: "restore", Reason: "Retained history"})
	if err != nil || restored.State != "published" {
		t.Fatal(restored, err)
	}
}

type failedExpiryEvent struct {
	r.Transaction
	subject string
}

func (t failedExpiryEvent) Event(e r.Event) error {
	if e.Type == "reviews.expired.v1" && e.Subject == t.subject {
		return r.ErrUnavailable
	}
	return t.Transaction.Event(e)
}

type failingSweepRepo struct {
	r.Repository
	subject string
}

func (repo failingSweepRepo) Transaction(ctx context.Context, fn func(r.Transaction) error) error {
	return repo.Repository.Transaction(ctx, func(t r.Transaction) error { return fn(failedExpiryEvent{t, repo.subject}) })
}
func TestSweepFailureRollsBackOnlyItsDiscussion(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	repo, db := database(t)
	f.service.Repo = repo
	var ids []string
	for _, key := range []string{"first", "second"} {
		entry := create(t, f, key, input("course", "spring", 3))
		if _, err := f.service.Action(ctx, student, entry.ID, "withdraw", r.Action{Action: "withdraw", Revision: entry.Revision}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, entry.ID)
		f.now = f.now.Add(time.Minute)
	}
	f.now = f.now.Add(25 * time.Hour)
	f.service.Repo = failingSweepRepo{repo, "entry/" + ids[1]}
	n, err := f.service.Sweep(ctx, 2)
	if !errors.Is(err, r.ErrUnavailable) {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("completed %d want 1", n)
	}
	for i, id := range ids {
		var exists, event bool
		if err = db.QueryRow(`SELECT EXISTS(SELECT FROM reviews.entries WHERE id=$1),EXISTS(SELECT FROM reviews.audit WHERE audit_target='entry/'||$1 AND audit_action='reviews.expired.v1')`, id).Scan(&exists, &event); err != nil {
			t.Fatal(err)
		}
		if exists != (i == 1) || event != (i == 0) {
			t.Errorf("discussion %d exists=%t expired event=%t", i, exists, event)
		}
	}
	f.service.Repo = repo
	n, err = f.service.Sweep(ctx, 2)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

type inactiveCourseCatalog struct{ *summaryCatalog }

func (c inactiveCourseCatalog) Target(ctx context.Context, kind, id string) (r.Target, error) {
	t, err := c.summaryCatalog.Target(ctx, kind, id)
	if err == nil && id == "inactive-course" {
		t.Inactive = true
	}
	return t, err
}

func TestComposerReportsInactiveTargetAndDisclosureOutage(t *testing.T) {
	f, port := uiReviewFixture(t)
	ctx := context.Background()
	// 4. A-15.3: Composer rejects inactive course even when term is empty, and surfaces disclosure provider failure.
	f.service.Catalog = inactiveCourseCatalog{port}
	compInactive, err := f.service.Composer(ctx, student, "course", "inactive-course", "", "")
	if err != nil || compInactive.State != "forbidden" || compInactive.Allowed {
		t.Fatalf("Composer returned %+v err=%v for inactive course without term", compInactive, err)
	}
	facts := factsFor(f, false)
	facts.err = r.ErrUnavailable
	f.service.Disclosures = facts
	compDiscDown, err := f.service.Composer(ctx, student, "course", "course", "autumn", "")
	if err != nil || compDiscDown.State != "unavailable" || compDiscDown.Allowed {
		t.Fatalf("Composer swallowed disclosure failure: %+v err=%v", compDiscDown, err)
	}

}
