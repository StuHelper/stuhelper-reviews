package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/postgres"
	"reflect"
	"testing"
	"time"
)

func TestDiscoveryCourseLifecycleCurrentAdmissionAndAttribution(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	repo := f.service.Repo.(*postgres.Repository)
	in := input("course", "autumn", 4)
	in.TeacherID = "teacher-a"
	item := create(t, f, "discovery-course", in)
	current, e := f.service.DiscoveryCurrent(ctx, student, []string{item.ID, "missing"})
	if e != nil || len(current) != 1 || current[0].Revision != item.Revision || current[0].CourseID != "course" || !reflect.DeepEqual(current[0].TeacherIDs, []string{"teacher-a"}) {
		t.Fatal(current, e)
	}
	f.access.revoked = true
	current, e = f.service.DiscoveryCurrent(ctx, student, []string{item.ID})
	if e != nil || len(current) != 0 {
		t.Fatal(current, e)
	}
	f.access.revoked = false
	f.access.failed = true
	if _, e = f.service.DiscoveryCurrent(ctx, student, []string{item.ID}); !errors.Is(e, r.ErrUnavailable) {
		t.Fatal(e)
	}
	f.access.failed = false
	if _, e = f.service.DiscoveryCurrent(ctx, r.Principal{}, []string{item.ID}); !errors.Is(e, r.ErrUnauthenticated) {
		t.Fatal(e)
	}
	f.catalog.schools = map[string]string{"elsewhere": "elsewhere", "other-term": "elsewhere"}
	other := create(t, f, "other-school", input("elsewhere", "other-term", 4))
	current, e = f.service.DiscoveryCurrent(ctx, student, []string{other.ID})
	if e != nil || len(current) != 0 {
		t.Fatal(current, e)
	}
	// Withdrawal and replacement affect only the new version; tombstones omit titles.
	withdrawn, e := f.service.Action(ctx, student, item.ID, "withdraw", r.Action{Action: "withdraw", Revision: item.Revision})
	if e != nil {
		t.Fatal(e)
	}
	current, e = f.service.DiscoveryCurrent(ctx, student, []string{item.ID})
	if e != nil || len(current) != 0 {
		t.Fatal(current, e)
	}
	in.Title = "New public title"
	item, e = f.service.Action(ctx, student, item.ID, "republish", r.Action{Action: "republish", Revision: withdrawn.Revision, Input: &in})
	if e != nil {
		t.Fatal(e)
	}
	admin := r.Principal{Subject: "admin"}
	for _, action := range []string{"hide", "restore", "delete"} {
		item, e = f.service.Apply(ctx, admin, r.Decision{ID: "discovery-" + action, ObjectID: item.ID, Revision: item.Revision, Action: action, Reason: "private moderation reason"})
		if e != nil {
			t.Fatal(e)
		}
	}
	events, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil {
		t.Fatal(e)
	}
	var states []bool
	var titles []string
	for _, event := range events {
		if event.Type == "reviews.discovery.v1" && event.Subject == item.ID {
			var d r.DiscoveryDocument
			if e = json.Unmarshal(event.Data, &d); e != nil {
				t.Fatal(e)
			}
			states = append(states, d.Visible)
			titles = append(titles, d.Title)
		}
	}
	if !reflect.DeepEqual(states, []bool{true, false, true, false, true, false}) || titles[2] != "New public title" {
		t.Fatal(states, titles)
	}
	expired := create(t, f, "expired", input("course", "autumn", 3))
	if _, e = f.service.Action(ctx, student, expired.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1}); e != nil {
		t.Fatal(e)
	}
	f.now = f.now.Add(f.service.Config.DraftLifetime + time.Second)
	if _, e = f.service.Sweep(ctx, 100); e != nil {
		t.Fatal(e)
	}
	snapshot, e := repo.DiscoverySnapshot(ctx, "", 100)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, d := range snapshot.Items {
		if d.Target.ID == expired.ID {
			found = true
			if d.Visible || d.Revision != 3 || d.Title != "" {
				t.Fatal(d)
			}
		}
	}
	if !found {
		t.Fatal("expiry lost tombstone")
	}
	// Pending material never contributes its private title to search.
	f.moderation.outcome = "pending"
	pending := create(t, f, "pending-discovery", input("course", "autumn", 3))
	snapshot, e = repo.DiscoverySnapshot(ctx, "", 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range snapshot.Items {
		if d.Target.ID == pending.ID && (d.Visible || d.Title != "") {
			t.Fatal(d)
		}
	}
}

func TestDiscoveryMyVotesCurrentPermissionsPaginationAndCancellation(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	voter := r.Principal{Subject: "voter"}
	first := create(t, f, "vote-first", input("course", "autumn", 4))
	second := create(t, f, "vote-second", input("course", "spring", 4))
	for i, item := range []r.Result{first, second} {
		choice := 1
		if i == 1 {
			choice = -1
		}
		if _, e := f.service.Action(ctx, voter, item.ID, "vote", r.Action{Action: "vote", Vote: choice}); e != nil {
			t.Fatal(e)
		}
	}
	page, e := f.service.DiscoveryMyVotes(ctx, voter, "", 1)
	if e != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal(page, e)
	}
	next, e := f.service.DiscoveryMyVotes(ctx, voter, page.NextCursor, 1)
	if e != nil || len(next.Items) != 1 || next.Items[0].Target.ID == page.Items[0].Target.ID || next.NextCursor != "" {
		t.Fatal(next, e)
	}
	f.access.revoked = true
	hidden, e := f.service.DiscoveryMyVotes(ctx, voter, "", 100)
	if e != nil || len(hidden.Items) != 0 {
		t.Fatal(hidden, e)
	}
	receipts, e := f.service.MyVotes(ctx, voter, "", 100)
	if e != nil || len(receipts) != 2 {
		t.Fatal("own receipts changed", receipts, e)
	}
	f.access.revoked = false
	if _, e = f.service.Action(ctx, voter, first.ID, "cancel", r.Action{Action: "vote", Vote: 0}); e != nil {
		t.Fatal(e)
	}
	page, e = f.service.DiscoveryMyVotes(ctx, voter, "", 100)
	if e != nil || len(page.Items) != 1 || page.Items[0].Choice != "down" {
		t.Fatal(page, e)
	}
	if _, e = f.service.Action(ctx, student, second.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1}); e != nil {
		t.Fatal(e)
	}
	page, e = f.service.DiscoveryMyVotes(ctx, voter, "", 100)
	if e != nil || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
	page, e = f.service.DiscoveryMyVotes(ctx, r.Principal{Subject: "other"}, "", 100)
	if e != nil || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
	if _, e = f.service.DiscoveryMyVotes(ctx, r.Principal{}, "", 1); !errors.Is(e, r.ErrUnauthenticated) {
		t.Fatal(e)
	}
}

func TestDiscoveryRelayInspectionUsesCurrentModerationRequest(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	admin := r.Principal{Subject: "admin"}
	ordinary := create(t, f, "ordinary", input("course", "autumn", 4))
	inspect, e := f.service.Inspect(ctx, admin, ordinary.ID)
	if e != nil || inspect.ReviewRequested {
		t.Fatal(inspect, e)
	}
	f.moderation.outcome = "review"
	review := create(t, f, "needs-review", input("course", "autumn", 4))
	inspect, e = f.service.Inspect(ctx, admin, review.ID)
	if e != nil || !inspect.ReviewRequested || inspect.Revision != review.Revision {
		t.Fatal(inspect, e)
	}
	if _, e = f.service.Action(ctx, student, review.ID, "withdraw", r.Action{Action: "withdraw", Revision: review.Revision}); e != nil {
		t.Fatal(e)
	}
	inspect, e = f.service.Inspect(ctx, admin, review.ID)
	if e != nil || inspect.ReviewRequested {
		t.Fatal(inspect, e)
	}
}
