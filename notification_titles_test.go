package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"testing"
)

func TestNotificationTitlesRespectScopePublicationAndRootNames(t *testing.T) {
	f, _ := uiReviewFixture(t)
	ctx := context.Background()
	in := input("course", "autumn", 3)
	in.Title = "Readable review title"
	root := create(t, f, "root", in)
	reply := addReply(t, f, root.ID, "private reply body", student)
	got, err := f.service.NotificationTitles(ctx, student, []string{root.ID, reply.ID, "missing"})
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	for _, item := range got {
		if item.Title != in.Title {
			t.Fatal("reply body exposed", item)
		}
	}
	f.access.revoked = true
	got, err = f.service.NotificationTitles(ctx, student, []string{root.ID})
	if err != nil || len(got) != 0 {
		t.Fatal("revoked name", got, err)
	}
	f.access.revoked = false
	f.access.failed = true
	if _, err = f.service.NotificationTitles(ctx, student, []string{root.ID}); !errors.Is(err, r.ErrUnavailable) {
		t.Fatal("dependency failure", err)
	}
	f.access.failed = false
	if _, err = f.service.NotificationTitles(ctx, r.Principal{}, []string{root.ID}); !errors.Is(err, r.ErrUnauthenticated) {
		t.Fatal("anonymous", err)
	}
	for _, ids := range [][]string{nil, make([]string, 101), {"bad/id"}} {
		if _, err = f.service.NotificationTitles(ctx, student, ids); !errors.Is(err, r.ErrInput) {
			t.Fatal("bounds", err)
		}
	}
	if _, err = f.service.Action(ctx, student, root.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	got, err = f.service.NotificationTitles(ctx, student, []string{root.ID, reply.ID})
	if err != nil || len(got) != 0 {
		t.Fatal("withdrawn title", got, err)
	}
}
