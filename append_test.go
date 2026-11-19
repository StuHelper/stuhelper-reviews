package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

func TestPostWindowAppendPreservesAttributionSamplesAndModeration(t *testing.T) {
	f, _ := uiReviewFixture(t)
	f.service.Config.AppendCooldown = 24 * time.Hour
	f.service.Config.MaxAppends = 2
	ctx := context.Background()

	orig := input("course", "autumn", 4)
	orig.TeacherID = "teacher-a"
	root := create(t, f, "append-root", orig)
	if _, err := f.service.Action(ctx, r.Principal{Subject: "voter"}, root.ID, "vote-root", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	reply := addReply(t, f, root.ID, "child-reply", r.Principal{Subject: "other"})

	// Advance past the 5-minute edit window: withdraw fails, append succeeds.
	f.now = f.now.Add(30 * 24 * time.Hour)
	if _, err := f.service.Action(ctx, student, root.ID, "late-withdraw", r.Action{Action: "withdraw", Revision: root.Revision}); !errors.Is(err, r.ErrWindow) {
		t.Fatalf("expected ErrWindow, got %v", err)
	}

	firstPublishedView, err := f.service.Get(ctx, student, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, _ := json.Marshal(firstPublishedView)
	if strings.Contains(string(firstRaw), `"appends"`) {
		t.Fatalf("legacy view without appends must omit appends field: %s", string(firstRaw))
	}

	// Reject malformed/unauthorized/reply appends.
	if _, err := f.service.Action(ctx, student, root.ID, "empty-append", r.Action{Action: "append", Revision: root.Revision, Body: "   "}); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput on blank body, got %v", err)
	}
	if _, err := f.service.Action(ctx, r.Principal{Subject: "other"}, reply.ID, "reply-append", r.Action{Action: "append", Revision: reply.Revision, Body: "Follow-up on reply"}); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput on reply append, got %v", err)
	}
	if _, err := f.service.Action(ctx, r.Principal{Subject: "other"}, root.ID, "stranger-append", r.Action{Action: "append", Revision: root.Revision, Body: "Hijack"}); !errors.Is(err, r.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on non-owner append, got %v", err)
	}

	// Blocked moderation does not mutate entry.
	f.moderation.outcome = "blocked"
	if _, err := f.service.Action(ctx, student, root.ID, "blocked-append", r.Action{Action: "append", Revision: root.Revision, Body: "Blocked content"}); !errors.Is(err, r.ErrForbidden) {
		t.Fatalf("expected ErrForbidden on blocked append, got %v", err)
	}

	// Published append via HTTP handler preserves original text, ratings, teacher attribution, publish time, votes, and sample count.
	f.moderation.outcome = "published"
	handler := httpapi.Handler{
		Service:      f.service,
		Authenticate: func(*http.Request) (r.Principal, error) { return student, nil },
	}.Routes()
	req := httptest.NewRequest("POST", "/api/v1/reviews/"+root.ID+"/actions", strings.NewReader(fmt.Sprintf(`{"action":"append","revision":%d,"body":"期末出分更新：给分公正，考题贴合讲义。"}`, root.Revision)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "append-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 on append, got %d: %s", rec.Code, rec.Body.String())
	}
	var appended r.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &appended); err != nil || appended.Revision != root.Revision+1 || appended.State != "published" {
		t.Fatalf("unexpected append result: %+v %v", appended, err)
	}

	// Idempotent replay returns identical result; different payload with same key conflicts.
	replayRes, err := f.service.Action(ctx, student, root.ID, "append-1", r.Action{Action: "append", Revision: root.Revision, Body: "期末出分更新：给分公正，考题贴合讲义。"})
	if err != nil || replayRes != appended {
		t.Fatalf("expected idempotent replay match, got %+v %v", replayRes, err)
	}
	if _, err := f.service.Action(ctx, student, root.ID, "append-1", r.Action{Action: "append", Revision: root.Revision, Body: "Changed payload"}); !errors.Is(err, r.ErrConflict) {
		t.Fatalf("expected ErrConflict on key reuse with different body, got %v", err)
	}

	// Cooldown blocks immediate second append before 24h.
	f.now = f.now.Add(2 * time.Hour)
	if _, err := f.service.Action(ctx, student, root.ID, "append-cooldown", r.Action{Action: "append", Revision: appended.Revision, Body: "Too soon"}); !errors.Is(err, r.ErrConflict) {
		t.Fatalf("expected ErrConflict during cooldown, got %v", err)
	}

	// Verify public view, original fields, and sample count invariance.
	afterFirst, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, root.ID, r.ViewOptions{IncludeAppends: true})
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.Body != orig.Body || afterFirst.Ratings != orig.Ratings || afterFirst.TeacherID != "teacher-a" || !afterFirst.PublishedAt.Equal(*firstPublishedView.PublishedAt) || afterFirst.Upvotes != 1 {
		t.Fatalf("append mutated original fields: %+v", afterFirst)
	}
	if len(afterFirst.Appends) != 1 || afterFirst.Appends[0].Body != "期末出分更新：给分公正，考题贴合讲义。" || afterFirst.Appends[0].State != "published" {
		t.Fatalf("unexpected public appends: %+v", afterFirst.Appends)
	}
	summaries, err := f.service.ReadSummaries(ctx, student, "course", []string{"course"}, false)
	if err != nil || len(summaries.Items) != 1 || summaries.Items[0].ReviewCount != 1 {
		t.Fatalf("append polluted review sample count: %+v %v", summaries, err)
	}

	// Second append hitting pending moderation stays hidden from public until moderator publishes, while keeping root visible.
	f.now = f.now.Add(25 * time.Hour)
	f.moderation.outcome = "pending"
	secondRes, err := f.service.Action(ctx, student, root.ID, "append-2", r.Action{Action: "append", Revision: appended.Revision, Body: "二轮复查补充说明。"})
	if err != nil || secondRes.State != "published" {
		t.Fatalf("expected root to stay published on pending append, got %+v %v", secondRes, err)
	}
	publicDuringPending, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, root.ID, r.ViewOptions{IncludeAppends: true})
	if err != nil || len(publicDuringPending.Appends) != 1 {
		t.Fatalf("pending append leaked to public view: %+v %v", publicDuringPending, err)
	}
	ownDuringPending, err := f.service.GetWithOptions(ctx, student, root.ID, r.ViewOptions{Own: true, IncludeAppends: true})
	if err != nil || len(ownDuringPending.Appends) != 2 || ownDuringPending.Appends[1].State != "pending" {
		t.Fatalf("owner view missing pending append: %+v %v", ownDuringPending, err)
	}
	admin := r.Principal{Subject: "admin"}
	inspect, err := f.service.Inspect(ctx, admin, root.ID)
	if err != nil || inspect.ReviewRequested || len(inspect.PendingAppendIDs) != 1 {
		t.Fatalf("expected ReviewRequested=true on pending append, got %+v %v", inspect, err)
	}
	adminView, err := f.service.AdminGet(ctx, admin, root.ID)
	if err != nil || len(adminView.Appends) != 2 || adminView.Appends[1].State != "pending" {
		t.Fatalf("admin view missing pending append: %+v %v", adminView, err)
	}

	// MaxAppends=2 blocks a third append.
	f.now = f.now.Add(25 * time.Hour)
	f.moderation.outcome = "published"
	if _, err := f.service.Action(ctx, student, root.ID, "append-3", r.Action{Action: "append", Revision: secondRes.Revision, Body: "Exceeds max"}); !errors.Is(err, r.ErrConflict) {
		t.Fatalf("expected ErrConflict when MaxAppends reached, got %v", err)
	}

	// Moderator approves pending append via Apply(Action: "publish").
	publishedRes, err := f.service.Apply(ctx, admin, r.Decision{
		ID:       "approve-append",
		ObjectID: root.ID,
		Revision: secondRes.Revision,
		Action:   "approve_append", AppendID: adminView.Appends[1].ID,
		Reason: "approved follow-up",
	})
	if err != nil {
		t.Fatal(err)
	}
	publicAfterApprove, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, root.ID, r.ViewOptions{IncludeAppends: true})
	if err != nil || len(publicAfterApprove.Appends) != 2 || !slices.Equal([]string{publicAfterApprove.Appends[0].State, publicAfterApprove.Appends[1].State}, []string{"published", "published"}) {
		t.Fatalf("approved append not visible publicly: %+v %v", publicAfterApprove, err)
	}
	inspectAfter, err := f.service.Inspect(ctx, admin, root.ID)
	if err != nil || inspectAfter.ReviewRequested || inspectAfter.Revision != publishedRes.Revision {
		t.Fatalf("expected ReviewRequested cleared after publish, got %+v %v", inspectAfter, err)
	}

	// Closed owner cannot append to mentor review either.
	mentor := create(t, f, "mentor-root", r.Input{Kind: "mentor", TargetID: "teacher-a", Title: "Guide", Body: "Initial mentor review"})
	if err := f.service.AnonymizeOwner(ctx, student.Subject, "00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Action(ctx, student, mentor.ID, "closed-append", r.Action{Action: "append", Revision: mentor.Revision, Body: "After closure"}); !errors.Is(err, r.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for closed owner append, got %v", err)
	}
}

func TestAppendLimitMatchesContract(t *testing.T) {
	f := fixture(t)
	cfg := r.DefaultConfig()
	cfg.MaxAppends = 11
	if _, err := r.New(f.service.Repo, f.catalog, f.access, f.moderation, profiles{}, cfg); !errors.Is(err, r.ErrInput) {
		t.Fatalf("limit 11 accepted: %v", err)
	}
	cfg.MaxAppends = 10
	if _, err := r.New(f.service.Repo, f.catalog, f.access, f.moderation, profiles{}, cfg); err != nil {
		t.Fatalf("limit 10: %v", err)
	}
	ctx := context.Background()
	root := create(t, f, "limit", input("course", "autumn", 4))
	f.service.Config.MaxAppends = 11
	revision := root.Revision
	for i := range 10 {
		got, err := f.service.Action(ctx, student, root.ID, fmt.Sprintf("append-%d", i), r.Action{Action: "append", Revision: revision, Body: "later note"})
		if err != nil {
			t.Fatal(err)
		}
		revision = got.Revision
	}
	_, err := f.service.Action(ctx, student, root.ID, "over-limit", r.Action{Action: "append", Revision: revision, Body: "another note"})
	if !errors.Is(err, r.ErrConflict) {
		t.Fatalf("eleventh append: %v", err)
	}
	got, err := f.service.GetWithOptions(ctx, student, root.ID, r.ViewOptions{IncludeAppends: true})
	if err != nil || len(got.Appends) != 10 || got.Revision != revision {
		t.Fatalf("retained %d revision %d: %v", len(got.Appends), got.Revision, err)
	}
}

type beforeAppendCommit struct{ run func() error }

func (m beforeAppendCommit) Check(context.Context, r.Candidate) (string, error) {
	return "published", m.run()
}
func TestAppendRechecksAccountAfterModeration(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	root := create(t, f, "closure", input("course", "autumn", 4))
	f.service.Moderation = beforeAppendCommit{func() error {
		return f.service.AnonymizeOwner(ctx, student.Subject, "00000000-0000-4000-8000-000000000021")
	}}
	_, err := f.service.Action(ctx, student, root.ID, "append-after-close", r.Action{Action: "append", Revision: root.Revision, Body: "text"})
	if !errors.Is(err, r.ErrForbidden) {
		t.Fatalf("closed author: %v", err)
	}
	view, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, root.ID, r.ViewOptions{IncludeAppends: true})
	if err != nil || view.Revision != root.Revision || len(view.Appends) != 0 {
		t.Fatalf("entry changed after closure: %+v %v", view, err)
	}
}

func TestAppendsAreModeratedIndependentlyUnderSchoolPolicy(t *testing.T) {
	f, _ := uiReviewFixture(t)
	f.service.Config.AppendCooldown = 0
	f.service.Config.MaxAppends = 10
	f.moderation.outcome = "review"
	ctx := context.Background()
	root := create(t, f, "root-review", input("course", "autumn", 4))
	rev := root.Revision
	for i, mode := range []string{"review", "pending", "published"} {
		f.moderation.outcome = mode
		f.now = f.now.Add(25 * time.Hour)
		item, err := f.service.Action(ctx, student, root.ID, fmt.Sprintf("item-%d", i), r.Action{Action: "append", Revision: rev, Body: mode})
		if err != nil {
			t.Fatal(err)
		}
		rev = item.Revision
	}
	admin := r.Principal{Subject: "admin"}
	visibleBodies := func() []string {
		t.Helper()
		v, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, root.ID, r.ViewOptions{IncludeAppends: true})
		if err != nil {
			t.Fatal(err)
		}
		var bodies []string
		for _, a := range v.Appends {
			bodies = append(bodies, a.Body)
			if a.ReviewRequested {
				t.Fatal("private queue flag in public view")
			}
		}
		return bodies
	}
	if !slices.Equal(visibleBodies(), []string{"review", "published"}) {
		t.Fatal("prepublication visibility")
	}
	decided, err := f.service.Apply(ctx, admin, r.Decision{ID: "parent", ObjectID: root.ID, Revision: rev, Action: "publish", Reason: "parent only"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.service.Inspect(ctx, admin, root.ID)
	if err != nil || len(pending.PendingAppendIDs) != 2 || pending.ReviewRequested {
		t.Fatalf("independent queue: %+v %v", pending, err)
	}
	reject := r.Decision{ID: "reject", ObjectID: root.ID, AppendID: pending.PendingAppendIDs[0], Revision: decided.Revision, Action: "reject_append", Reason: "reject one"}
	decided, err = f.service.Apply(ctx, admin, reject)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.Apply(ctx, admin, reject)
	if err != nil || replay.Revision != decided.Revision {
		t.Fatal("replayed decision", err)
	}
	if !slices.Equal(visibleBodies(), []string{"published"}) {
		t.Fatal("rejected append is still public")
	}
	_, err = f.service.Apply(ctx, admin, r.Decision{ID: "accept", ObjectID: root.ID, AppendID: pending.PendingAppendIDs[1], Revision: decided.Revision, Action: "approve_append", Reason: "accept another"})
	if err != nil || !slices.Equal(visibleBodies(), []string{"pending", "published"}) {
		t.Fatal("individual approval", err)
	}
}
