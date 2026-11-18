package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

type principalDisclosures struct {
	teacherSubject string
	teacher        *disclosureFixture
}

func (d principalDisclosures) Facts(ctx context.Context, p r.Principal, school, course, term string) (r.DisclosureFacts, error) {
	if p.Subject == d.teacherSubject {
		return d.teacher.Facts(ctx, p, school, course, term)
	}
	return ordinaryDisclosures{}.Facts(ctx, p, school, course, term)
}

func TestRecommendedSortFiltersBoundedRefillAndCoTeachers(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	admin := r.Principal{Subject: "admin"}
	teacher := r.Principal{Subject: "teacher-user"}

	// Create 4 course reviews with distinct rating groups, votes, and featured status:
	// rLow: score=2 (low), 10 upvotes & 15 downvotes (net -5)
	// rMid: score=3 (mid), 9 upvotes & 0 downvotes (net +9)
	// rHigh: score=5 (high), 1 upvote & 0 downvotes (net +1), featured=true
	// rHighPlain: score=4 (high), 0 votes (net 0), co-taught without single TeacherID
	f.now = f.now.Add(time.Minute)
	rLow := create(t, f, "r-low", input("course", "autumn", 2))
	f.now = f.now.Add(time.Minute)
	rMid := create(t, f, "r-mid", input("course", "autumn", 3))
	f.now = f.now.Add(time.Minute)
	inSingle := input("course", "autumn", 5)
	inSingle.TeacherID = "teacher-a"
	rHigh := create(t, f, "r-high", inSingle)
	f.now = f.now.Add(time.Minute)
	rHighPlain := create(t, f, "r-high-plain", input("course", "autumn", 4))

	if _, err := f.service.Apply(ctx, admin, r.Decision{ID: "feat-high", ObjectID: rHigh.ID, Revision: rHigh.Revision, Action: "feature", Reason: "featured quality"}); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if _, err := f.service.Action(ctx, r.Principal{Subject: fmt.Sprintf("up-low-%d", i)}, rLow.ID, "vote", r.Action{Action: "vote", Vote: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 15 {
		if _, err := f.service.Action(ctx, r.Principal{Subject: fmt.Sprintf("down-low-%d", i)}, rLow.ID, "vote", r.Action{Action: "vote", Vote: -1}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 9 {
		if _, err := f.service.Action(ctx, r.Principal{Subject: fmt.Sprintf("up-mid-%d", i)}, rMid.ID, "vote", r.Action{Action: "vote", Vote: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.Action(ctx, r.Principal{Subject: "up-high"}, rHigh.ID, "vote", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}

	// B-09.2: Co-taught review without single TeacherID returns full snapshot TeacherIDs in View.
	coView, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, rHighPlain.ID, r.ViewOptions{IncludeTeacherIDs: true})
	if err != nil || coView.TeacherID != "" || !slices.Equal(coView.TeacherIDs, []string{"teacher-a", "teacher-b"}) {
		t.Fatalf("expected co-teaching teacher_ids [teacher-a teacher-b], got %+v %v", coView, err)
	}
	singleView, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, rHigh.ID, r.ViewOptions{IncludeTeacherIDs: true})
	if err != nil || singleView.TeacherID != "teacher-a" || !slices.Equal(singleView.TeacherIDs, []string{"teacher-a"}) {
		t.Fatalf("expected single teacher_ids [teacher-a], got %+v %v", singleView, err)
	}
	mentor := create(t, f, "mentor-no-teachers", r.Input{Kind: "mentor", TargetID: "teacher-a", Title: "Guide", Body: "Mentor comment"})
	mentorView, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, mentor.ID, r.ViewOptions{IncludeTeacherIDs: true})
	if err != nil || len(mentorView.TeacherIDs) != 0 {
		t.Fatalf("mentor view must omit teacher_ids, got %+v %v", mentorView, err)
	}

	// B-02: sort=recommended orders featured first (rHigh), then net helpfulness (+9 rMid > 0 rHighPlain > -5 rLow) with stable cursor pagination.
	var seen []string
	after := ""
	for range 5 {
		page, err := f.service.List(ctx, r.Principal{Subject: "reader"}, r.Filter{Kind: "course", TargetID: "course", Sort: "recommended", After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			seen = append(seen, item.ID)
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	wantOrder := []string{rHigh.ID, rMid.ID, rHighPlain.ID, rLow.ID}
	if !slices.Equal(seen, wantOrder) {
		t.Fatalf("recommended order mismatch: got %v want %v", seen, wantOrder)
	}

	// Changing filter invalidates cursor.
	firstRec, err := f.service.List(ctx, r.Principal{Subject: "reader"}, r.Filter{Kind: "course", TargetID: "course", Sort: "recommended", Limit: 1})
	if err != nil || firstRec.Next == "" {
		t.Fatal(firstRec, err)
	}
	if _, err := f.service.List(ctx, r.Principal{Subject: "reader"}, r.Filter{Kind: "course", TargetID: "course", Sort: "recommended", RatingGroup: "high", After: firstRec.Next, Limit: 1}); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput when reusing cursor across rating_group, got %v", err)
	}

	// HTTP filters: featured, rating_group, invalid parameters.
	h := httpapi.Handler{
		Service:      f.service,
		Authenticate: func(*http.Request) (r.Principal, error) { return r.Principal{Subject: "reader"}, nil },
	}.Routes()
	getPage := func(query string) r.Page {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/reviews?"+query, nil))
		if rec.Code != 200 {
			t.Fatalf("query %s failed: %d %s", query, rec.Code, rec.Body.String())
		}
		var page r.Page
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	featPage := getPage("kind=course&target_id=course&featured=true")
	if len(featPage.Items) != 1 || featPage.Items[0].ID != rHigh.ID {
		t.Fatalf("featured=true mismatch: %+v", featPage)
	}
	lowPage := getPage("kind=course&target_id=course&rating_group=low")
	if len(lowPage.Items) != 1 || lowPage.Items[0].ID != rLow.ID {
		t.Fatalf("rating_group=low mismatch: %+v", lowPage)
	}
	midPage := getPage("kind=course&target_id=course&rating_group=mid")
	if len(midPage.Items) != 1 || midPage.Items[0].ID != rMid.ID {
		t.Fatalf("rating_group=mid mismatch: %+v", midPage)
	}
	highPage := getPage("kind=course&target_id=course&rating_group=high&sort=recommended")
	if len(highPage.Items) != 2 || highPage.Items[0].ID != rHigh.ID || highPage.Items[1].ID != rHighPlain.ID {
		t.Fatalf("rating_group=high mismatch: %+v", highPage)
	}
	for _, bad := range []string{"featured=yes", "has_teacher_reply=1", "rating_group=extreme"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/reviews?"+bad, nil))
		if rec.Code != 400 {
			t.Fatalf("expected 400 for %s, got %d", bad, rec.Code)
		}
	}

	// B-02 has_teacher_reply + B-09.1 bounded refill when teacher disclosure is revoked!
	facts := factsFor(f, true)
	f.service.Disclosures = principalDisclosures{teacherSubject: teacher.Subject, teacher: facts}
	f.now = f.now.Add(time.Minute)
	tReply1, err := f.service.Reply(ctx, teacher, rLow.ID, "t-reply-1", r.Input{Kind: "reply", TargetID: "course", Body: "Teacher official note 1", Disclosures: []string{"real_name", "teacher"}})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := f.service.Reply(ctx, teacher, rLow.ID, "t-reply-2", r.Input{Kind: "reply", TargetID: "course", Body: "Teacher official note 2", Disclosures: []string{"real_name", "teacher"}}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	visibleStudentReply := addReply(t, f, rLow.ID, "s-reply-3", r.Principal{Subject: "other"})
	_ = tReply1

	withTeacher := getPage("kind=course&target_id=course&has_teacher_reply=true")
	if len(withTeacher.Items) != 1 || withTeacher.Items[0].ID != rLow.ID {
		t.Fatalf("has_teacher_reply=true mismatch: %+v", withTeacher)
	}
	withoutTeacher := getPage("kind=course&target_id=course&has_teacher_reply=false")
	if len(withoutTeacher.Items) != 3 {
		t.Fatalf("has_teacher_reply=false mismatch: %+v", withoutTeacher)
	}

	// Revoke teacher's real_name disclosure:
	// 1) has_teacher_reply=true must no longer match rLow;
	// 2) Listing replies on rLow with sort=oldest&limit=1 encounters 2 hidden teacher replies first,
	//    and bounded refill automatically advances to return visibleStudentReply instead of a false empty page!
	revoked := facts.facts.Attributes["real_name"]
	revoked.MayDisclose = false
	facts.facts.Attributes["real_name"] = revoked

	withTeacherAfterRevoke := getPage("kind=course&target_id=course&has_teacher_reply=true")
	if len(withTeacherAfterRevoke.Items) != 0 {
		t.Fatalf("undisclosed teacher reply leaked via has_teacher_reply=true: %+v", withTeacherAfterRevoke)
	}
	replyPreviewPage := getPage("kind=reply&root_id=" + rLow.ID + "&sort=oldest&limit=1")
	if len(replyPreviewPage.Items) != 1 || replyPreviewPage.Items[0].ID != visibleStudentReply.ID {
		t.Fatalf("expected bounded refill to return visible reply instead of false empty page, got %+v", replyPreviewPage)
	}
}

func TestTeacherIDsRequireExplicitHTTPOption(t *testing.T) {
	f, _ := uiReviewFixture(t)
	root := create(t, f, "teacher-id-options", input("course", "autumn", 4))
	handler := httpapi.Handler{Service: f.service, Authenticate: func(*http.Request) (r.Principal, error) { return student, nil }}.Routes()
	for _, route := range []struct {
		path string
		list bool
	}{
		{"/api/v1/reviews", true}, {"/api/v1/reviews/" + root.ID, false},
		{"/api/v1/reviews/me/entries", true}, {"/api/v1/reviews/me/entries/" + root.ID, false},
	} {
		for _, param := range []string{"", "include_teacher_ids=false", "include_teacher_ids=true", "include_teacher_ids=1", "include_teacher_ids=true&include_teacher_ids=false"} {
			req := httptest.NewRequest(http.MethodGet, route.path+"?"+param, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if param == "include_teacher_ids=1" || param == "include_teacher_ids=true&include_teacher_ids=false" {
				if rec.Code != 400 {
					t.Errorf("%s %s: expected 400, got %d", route.path, param, rec.Code)
				}
				continue
			}
			var view map[string]json.RawMessage
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &view) != nil {
				t.Fatal(route.path, param, rec.Code)
			}
			if route.list {
				var items []map[string]json.RawMessage
				if err := json.Unmarshal(view["items"], &items); err != nil || len(items) != 1 {
					t.Fatal(route.path, len(items), err)
				}
				view = items[0]
			}
			raw, ok := view["teacher_ids"]
			if ok != (param == "include_teacher_ids=true") {
				t.Errorf("%s %s: unexpected teacher_ids presence %v", route.path, param, ok)
			}
			if ok {
				var ids []string
				if json.Unmarshal(raw, &ids) != nil || !slices.Equal(ids, []string{"teacher-a", "teacher-b"}) {
					t.Fatal(route.path, ids)
				}
			}
		}
	}
}
