package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/auditquery"
	"github.com/StuHelper/stuhelper-reviews/postgres"
	"sync"
	"testing"
	"time"
)

func TestClosureRetainsAnonymousVotes(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	entry := create(t, f, "entry", input("course", "autumn", 4))
	voter := r.Principal{Subject: "voter"}
	other := r.Principal{Subject: "other"}
	for _, p := range []r.Principal{voter, other} {
		if _, e := f.service.Action(ctx, p, entry.ID, "vote", r.Action{Action: "vote", Vote: 1}); e != nil {
			t.Fatal(e)
		}

	}
	for range 2 {
		if e := f.service.AnonymizeOwner(ctx, voter.Subject, "11111111-1111-4111-8111-111111111111"); e != nil {
			t.Fatal(e)
		}
	}
	view, e := f.service.Get(ctx, other, entry.ID)
	if e != nil || view.Upvotes != 2 || view.MyVote != 1 {
		t.Fatal("anonymous vote no longer counted", view, e)
	}
	votes, e := f.service.MyVotes(ctx, voter, "", 20)
	if e != nil || len(votes) != 0 {
		t.Fatal("votes still associated with closed account", votes, e)
	}

	if _, e = f.service.Action(ctx, voter, entry.ID, "vote", r.Action{Action: "vote", Vote: 1}); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("closed vote replay accepted", e)
	}
	db := f.service.Repo.(*postgres.Repository).DB
	var n int

	if e = db.QueryRow(`SELECT count(*) FROM reviews.audit WHERE data->>'Actor'=$1 AND data->>'Type'='reviews.voted.v1'`, voter.Subject).Scan(&n); e != nil || n != 0 {
		t.Fatal("vote audit still identifies account", n, e)
	}
}

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

func TestPublicAuditPrivacyRuntimePrivilegesAndFullAccountClosure(t *testing.T) {
	f := fixture(t)
	repo, migration := database(t)
	f.service.Repo = repo
	ctx := context.Background()
	facts := factsFor(f, false)
	f.service.Disclosures = facts

	// 1. A-01: Anonymous review, reply, and active voter must never leak real Subject in ReadAudit,
	// while moderator inspections and decisions must retain moderator Actor.
	anonAuthor := r.Principal{Subject: "anon-student"}
	anonInput := input("course", "autumn", 4)
	anonInput.Signed = false
	anonEntry, err := f.service.Create(ctx, anonAuthor, "anon-create", anonInput)
	if err != nil {
		t.Fatal(err)
	}
	activeVoter := r.Principal{Subject: "active-voter"}
	if _, err = f.service.Action(ctx, activeVoter, anonEntry.ID, "anon-vote", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	admin := r.Principal{Subject: "admin"}
	if _, err = f.service.AdminGet(ctx, admin, anonEntry.ID); err != nil {
		t.Fatal(err)
	}

	scopes := []auditquery.Scope{{School: "*", Group: "*", Module: "reviews"}}
	batch, err := repo.ReadAudit(ctx, auditquery.Request{
		Scopes: scopes,
		Target: "entry/" + anonEntry.ID,
		Until:  f.now.Add(time.Hour),
		Limit:  50,
	})
	if err != nil || len(batch.Records) == 0 {
		t.Fatalf("expected audit records for entry, got %+v err=%v", batch, err)
	}
	foundAdmin := false
	for _, rec := range batch.Records {
		if rec.Actor == anonAuthor.Subject || rec.Actor == activeVoter.Subject {
			t.Fatalf("ReadAudit leaked private user Subject: %+v", rec)
		}
		if rec.Action == "reviews.inspected.v1" && rec.Actor == admin.Subject {
			foundAdmin = true
		}
	}
	if !foundAdmin {
		t.Fatalf("expected moderator Actor in ReadAudit, got %+v", batch.Records)
	}
	for _, who := range []string{anonAuthor.Subject, activeVoter.Subject} {
		byActor, err := repo.ReadAudit(ctx, auditquery.Request{
			Scopes: scopes,
			Actor:  who,
			Until:  f.now.Add(time.Hour),
			Limit:  50,
		})
		if err != nil || len(byActor.Records) != 0 {
			t.Fatalf("ReadAudit Actor filter matched private user %s: %+v err=%v", who, byActor, err)
		}
	}

	// 2. A-13 & A-01: Signed author with ordinary draft, withdrawn draft, published signed entry,
	// and a reply under another user's root closes their account.
	closingUser := r.Principal{Subject: "closing-user"}
	draftRes, err := f.service.SaveDraft(ctx, closingUser, "", "draft-1", input("course", "autumn", 3), 0)
	if err != nil {
		t.Fatal(err)
	}
	withdrawnPost, err := f.service.Create(ctx, closingUser, "to-withdraw", input("other", "autumn", 4))
	if err != nil {
		t.Fatal(err)
	}
	withdrawnReply, err := f.service.Reply(ctx, closingUser, withdrawnPost.ID, "reply-to-withdraw", r.Input{
		Kind: "reply", TargetID: "other", Body: "Withdrawn with its root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if withdrawnPost.ID >= withdrawnReply.ID {
		t.Fatal("fixture must exercise root-first deletion order")
	}
	if _, err = f.service.Action(ctx, closingUser, withdrawnReply.ID, "withdraw-reply", r.Action{Action: "withdraw", Revision: withdrawnReply.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Action(ctx, closingUser, withdrawnPost.ID, "withdraw-it", r.Action{Action: "withdraw", Revision: withdrawnPost.Revision}); err != nil {
		t.Fatal(err)
	}
	signedIn := input("course", "spring", 5)
	signedIn.Signed = true
	signedIn.Disclosures = []string{"department"}
	signedPost, err := f.service.Create(ctx, closingUser, "signed-post", signedIn)
	if err != nil {
		t.Fatal(err)
	}
	replyRes, err := f.service.Reply(ctx, closingUser, anonEntry.ID, "closing-reply", r.Input{
		Kind:     "reply",
		TargetID: "course",
		Body:     "Reply from soon-closed user",
		Signed:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	replyAdminBefore, err := f.service.AdminGet(ctx, admin, replyRes.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A-11: Open a ReadAudit snapshot before account closures; closures and 0-row maintenance updates
	// must NOT invalidate active ReadAudit or DiscoverySnapshot cursors.
	page1, err := repo.ReadAudit(ctx, auditquery.Request{
		Scopes: scopes,
		Until:  f.now.Add(time.Hour),
		Limit:  1,
	})
	if err != nil || len(page1.Records) != 1 || page1.Snapshot == "" {
		t.Fatalf("failed to open ReadAudit snapshot: %+v err=%v", page1, err)
	}
	discPage1, err := repo.DiscoverySnapshot(ctx, "", 1)
	if err != nil || discPage1.Next == "" {
		t.Fatalf("failed to open DiscoverySnapshot cursor: %+v err=%v", discPage1, err)
	}

	if err = f.service.AnonymizeOwner(ctx, "never-wrote-anything", "33333333-3333-4333-8333-333333333333"); err != nil {
		t.Fatal(err)
	}
	if err = f.service.AnonymizeOwner(ctx, closingUser.Subject, "44444444-4444-4444-8444-444444444444"); err != nil {
		t.Fatal(err)
	}
	if _, err = migration.Exec(`UPDATE reviews.audit SET reason = reason WHERE id = 'nonexistent'`); err != nil {
		t.Fatal(err)
	}
	if _, err = migration.Exec(`UPDATE reviews.discovery_history SET data = data WHERE id = 'nonexistent'`); err != nil {
		t.Fatal(err)
	}

	r0 := page1.Records[0]
	if _, err = repo.ReadAudit(ctx, auditquery.Request{
		Scopes:   scopes,
		Until:    f.now.Add(time.Hour),
		Snapshot: page1.Snapshot,
		Before:   &auditquery.Position{Time: r0.CreatedAt, Source: r0.Source, ID: r0.ID},
		Limit:    10,
	}); err != nil {
		t.Fatalf("ReadAudit snapshot invalidated by account closure or 0-row update: %v", err)
	}
	if _, err = repo.DiscoverySnapshot(ctx, discPage1.Next, 10); err != nil {
		t.Fatalf("DiscoverySnapshot cursor invalidated by 0-row update: %v", err)
	}

	// Closed author must not appear in ReadAudit for their historical post.
	closedAudit, err := repo.ReadAudit(ctx, auditquery.Request{
		Scopes: scopes,
		Target: "entry/" + signedPost.ID,
		Until:  f.now.Add(time.Hour),
		Limit:  50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range closedAudit.Records {
		if rec.Actor == closingUser.Subject {
			t.Fatalf("ReadAudit leaked closed owner Subject: %+v", rec)
		}
	}

	var remainingWithdrawn int
	if err := migration.QueryRow(`SELECT count(*) FROM reviews.entries WHERE id IN ($1, $2)`, withdrawnPost.ID, withdrawnReply.ID).Scan(&remainingWithdrawn); err != nil || remainingWithdrawn != 0 {
		t.Fatalf("withdrawn root and reply remaining after closure: %d, err=%v", remainingWithdrawn, err)
	}

	// Ordinary and withdrawn drafts must be deleted and new draft writes blocked.
	if _, err = f.service.GetDraft(ctx, closingUser, draftRes.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatalf("ordinary draft survived closure: %v", err)
	}
	if _, err = f.service.GetDraft(ctx, closingUser, withdrawnPost.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatalf("withdrawn draft survived closure: %v", err)
	}
	remainingDrafts, err := f.service.Drafts(ctx, closingUser, "", 20)
	if err != nil || len(remainingDrafts) != 0 {
		t.Fatalf("closed user still has drafts: %+v err=%v", remainingDrafts, err)
	}
	if _, err = f.service.SaveDraft(ctx, closingUser, "", "post-close-draft", input("course", "autumn", 3), 0); !errors.Is(err, r.ErrForbidden) {
		t.Fatalf("closed user allowed to SaveDraft: %v", err)
	}

	// AdminGet, AdminList, and History must hide Signed and Disclosures for closed owners.
	adminEntry, err := f.service.AdminGet(ctx, admin, signedPost.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !adminEntry.Anonymous || adminEntry.Input.Signed || len(adminEntry.Input.Disclosures) != 0 {
		t.Fatalf("AdminGet leaked closed owner signed/disclosures state: %+v", adminEntry)
	}
	adminList, err := f.service.AdminList(ctx, admin, "school", "course", "published", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range adminList.Items {
		if item.ID == signedPost.ID && (!item.Anonymous || item.Input.Signed || len(item.Input.Disclosures) != 0) {
			t.Fatalf("AdminList leaked closed owner signed/disclosures state: %+v", item)
		}
	}
	hist, err := f.service.History(ctx, admin, signedPost.ID, 0, 20)
	if err != nil || len(hist) == 0 {
		t.Fatalf("History failed: %+v err=%v", hist, err)
	}
	for _, item := range hist {
		if !item.Anonymous || item.Input.Signed || len(item.Input.Disclosures) != 0 {
			t.Fatalf("History leaked closed owner signed/disclosures state: %+v", item)
		}
	}

	// Moderator cannot switch a closed user's reply under another user's root back to Signed: true.
	otherRoot, err := f.service.AdminGet(ctx, admin, anonEntry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Apply(ctx, admin, r.Decision{
		ID:           "unmask-closed-reply",
		ObjectID:     anonEntry.ID,
		Revision:     otherRoot.Revision,
		Action:       "identity",
		AuthorHandle: replyAdminBefore.AuthorHandle,
		Signed:       true,
		Reason:       "Attempt to unmask closed reply author",
	}); !errors.Is(err, r.ErrForbidden) {
		t.Fatalf("moderator unmasked closed user's reply: %v", err)
	}

	// 3. A-11: CheckRuntime must reject UPDATE/DELETE/TRUNCATE on audit and history tables.
	if err = repo.CheckRuntime(ctx); err != nil {
		t.Fatalf("CheckRuntime failed on valid runtime role: %v", err)
	}
	var runtimeRole string
	if err = repo.DB.QueryRow(`SELECT current_user`).Scan(&runtimeRole); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"reviews.audit", "reviews.discovery_history", "reviews.activity_history"} {
		if _, err = migration.Exec(`GRANT DELETE ON ` + tbl + ` TO ` + runtimeRole); err != nil {
			t.Fatal(err)
		}
		if err = repo.CheckRuntime(ctx); !errors.Is(err, r.ErrUnavailable) {
			t.Fatalf("CheckRuntime accepted runtime role with DELETE on %s: %v", tbl, err)
		}
		if _, err = migration.Exec(`REVOKE UPDATE,DELETE,TRUNCATE ON ` + tbl + ` FROM ` + runtimeRole); err != nil {
			t.Fatal(err)
		}
		if _, err = migration.Exec(`GRANT UPDATE(data) ON ` + tbl + ` TO ` + runtimeRole); err != nil {
			t.Fatal(err)
		}
		if err = repo.CheckRuntime(ctx); !errors.Is(err, r.ErrUnavailable) {
			t.Errorf("CheckRuntime accepted column UPDATE on %s: %v", tbl, err)
		}
		if _, err = migration.Exec(`REVOKE UPDATE(data) ON ` + tbl + ` FROM ` + runtimeRole); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.CheckRuntime(ctx); err != nil {
		t.Fatalf("CheckRuntime failed after revoking excess privileges: %v", err)
	}
}

func TestLegacyAuthorAuditAndClosureReceipts(t *testing.T) {
	f := fixture(t)
	repo, db := database(t)
	f.service.Repo = repo
	ctx := context.Background()
	root := create(t, f, "legacy", input("course", "autumn", 3))
	voter := r.Principal{Subject: "legacy-voter"}
	if _, err := f.service.Action(ctx, voter, root.ID, "legacy", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE reviews.audit SET data=jsonb_set(data,'{Actor}',to_jsonb((CASE WHEN audit_action='reviews.voted.v1' THEN 'legacy-voter' ELSE 'student' END)::text)) WHERE audit_action IN ('reviews.created.v1','reviews.voted.v1'); ALTER TABLE reviews.replays DROP COLUMN owner`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.Exec(postgres.Schema); err != nil {
			t.Fatal(err)
		}
	}
	var private, receipts int
	if err := db.QueryRow(`SELECT count(*) FROM reviews.audit WHERE audit_actor IN ('student','legacy-voter') OR data->>'Actor' IN ('student','legacy-voter')`).Scan(&private); err != nil || private != 0 {
		t.Errorf("identifying audit rows %d err=%v", private, err)
	}
	if err := f.service.AnonymizeOwner(ctx, voter.Subject, "00000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM reviews.replays WHERE key IN(SELECT id FROM reviews.audit WHERE audit_action='reviews.voted.v1')`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal(receipts, err)
	}
}
