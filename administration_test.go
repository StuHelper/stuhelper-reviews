package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	r "github.com/StuHelper/stuhelper-reviews"
)

func TestModerationReadIsScopedAuditedAndUsesDiscussionHandles(t *testing.T) {
	f := fixture(t)
	repo, migration := database(t)
	f.service.Repo = repo
	ctx := context.Background()
	f.moderation.outcome = "pending"
	root := create(t, f, "pending", input("course", "autumn", 3))
	if _, err := f.service.AdminGet(ctx, student, root.ID); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("author gained moderation access", err)
	}
	var reads int
	if err := migration.QueryRow(`SELECT count(*) FROM reviews.audit WHERE data->>'Type'='reviews.inspected.v1'`).Scan(&reads); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatal("denied read returned a successful audit")
	}
	admin := r.Principal{Subject: "admin"}
	view, err := f.service.AdminGet(ctx, admin, root.ID)
	if err != nil || view.State != "pending" || view.Input.Body == "" || view.AuthorHandle != "author" {
		t.Fatal(view, err)
	}
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"owner", "evidence", "source_id", "student"} {
		if strings.Contains(string(b), key) {
			t.Fatal("private identity leaked to review console", key)
		}
	}
	if err = migration.QueryRow(`SELECT count(*) FROM reviews.audit WHERE data->>'Type'='reviews.inspected.v1'`).Scan(&reads); err != nil || reads != 1 {
		t.Fatal("moderation read was not audited", reads, err)
	}
	if _, err = f.service.AdminList(ctx, admin, "different-school", "", "", "", 20); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("other school leaked", err)
	}
	page, err := f.service.AdminList(ctx, admin, "school", "course", "pending", "", 20)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if _, err = f.service.Apply(ctx, admin, r.Decision{ID: "publish", ObjectID: root.ID, Revision: 1, Action: "publish", Reason: "Approved"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Apply(ctx, admin, r.Decision{ID: "identity", ObjectID: root.ID, Revision: 2, Action: "identity", AuthorHandle: view.AuthorHandle, Signed: true, Reason: "Change public display"}); err != nil {
		t.Fatal(err)
	}
	public, err := f.service.Get(ctx, student, root.ID)
	if err != nil || public.Author.Anonymous || public.Author.Profile == nil {
		t.Fatal("handle did not resolve within discussion", public, err)
	}
	if _, err = f.service.Action(ctx, student, root.ID, "withdraw", r.Action{Action: "withdraw", Revision: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.AdminGet(ctx, admin, root.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("private withdrawn draft exposed", err)
	}
}

func TestAdministrativeReadsAuditEverySuccessfulCall(t *testing.T) {
	f := fixture(t)
	repo, db := database(t)
	f.service.Repo = repo
	ctx := context.Background()
	entry := create(t, f, "root", input("course", "autumn", 4))
	admin := r.Principal{Subject: "admin"}
	count := func(subject string) int {
		var n int
		err := db.QueryRow(`SELECT count(*) FROM reviews.audit WHERE data->>'Type'='reviews.inspected.v1' AND data->>'Subject'=$1`, subject+"/"+entry.ID).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := f.service.History(ctx, admin, entry.ID, 0, 10); err != nil {
		t.Fatal(err)
	}
	if count("history") != 1 || count("entry") != 0 {
		t.Error("history must have one purpose-specific audit")
	}
	for range 2 {
		if _, err := f.service.AdminGet(ctx, admin, entry.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := count("entry"); n != 2 {
		t.Fatalf("two same-clock reads produced %d audits", n)
	}
}
