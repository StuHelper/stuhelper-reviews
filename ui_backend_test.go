package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

type summaryCatalog struct {
	*catalog
	aliases map[string]map[string]string
	items   []r.PendingCourse
	ownErr  error
	owner   string
}

func (c *summaryCatalog) SummaryTargets(ctx context.Context, kind string, ids []string) (map[string]r.SummaryReference, error) {
	if c.failed {
		return nil, r.ErrUnavailable
	}
	out := map[string]r.SummaryReference{}
	for _, id := range ids {
		if id == "unmarked" || id == "missing" {
			continue
		}
		target, err := c.Target(ctx, kind, id)
		if err != nil {
			return nil, err
		}
		out[id] = r.SummaryReference{ID: id, CanonicalID: c.aliases[kind][id], SchoolID: target.SchoolID, Kind: kind, Name: id}
	}
	return out, nil
}
func (c *summaryCatalog) SummaryAliases(_ context.Context, _ string, kind string) (map[string]string, error) {
	if c.failed {
		return nil, r.ErrUnavailable
	}
	out := c.aliases[kind]
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}
func (c *summaryCatalog) OwnReviewCourses(_ context.Context, p r.Principal) (r.OwnCourseCandidates, error) {
	owner := p.Subject
	if c.owner != "" {
		owner = c.owner
	}
	return r.OwnCourseCandidates{Owner: owner, SchoolID: "school", Items: c.items}, c.ownErr
}
func uiReviewFixture(t *testing.T) (*fixtureState, *summaryCatalog) {
	f := fixture(t)
	port := &summaryCatalog{catalog: f.catalog, aliases: map[string]map[string]string{}}
	f.service.Catalog = port
	return f, port
}

func TestUISummariesVisibleSamplesTermsTeacherWeightsAndAliases(t *testing.T) {
	f, port := uiReviewFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		create(t, f, fmt.Sprintf("a-%d", i), input("a-course", "autumn", 5))
	}
	low := create(t, f, "b-one", input("b-course", "spring", 1))
	port.aliases["teacher"] = map[string]string{"teacher-b": "teacher-a", "teacher-c": "teacher-a"}
	got, err := f.service.ReadSummaries(ctx, student, "teacher", []string{"teacher-a", "teacher-b"}, true)
	if err != nil || len(got.Items) != 1 {
		t.Fatalf("summary: %+v %v", got, err)
	}
	item := got.Items[0]
	if item.ReviewCount != 4 || item.Dimensions[0].Samples != 4 || item.Dimensions[0].Courses != 2 || item.Dimensions[0].Level != 3 || item.Recommendation != "average" {
		t.Fatalf("teacher course weights/dedup: %+v", item)
	}
	total := 0
	for _, term := range item.Terms {
		total += term.ReviewCount
		if term.TermID == "spring" && term.Recommendation != "unrated" {
			t.Fatal("one-sample term graded")
		}
	}
	if total != 4 || len(item.Distribution) != 5 || item.Distribution[0].Samples != 4 || item.Distribution[0].Weights[4] != 20 {
		t.Fatal("chart sample mismatch", item)
	}
	brief, err := f.service.ReadSummaries(ctx, student, "teacher", []string{"teacher-a"}, false)
	if err != nil || len(brief.Items) != 1 || brief.Items[0].ReviewCount != item.ReviewCount || brief.Items[0].Dimensions != item.Dimensions || brief.Items[0].Recommendation != item.Recommendation || len(brief.Items[0].Distribution) != 0 {
		t.Fatal("brief summary differs from chart summary", brief, err)
	}
	if _, err := f.service.Action(ctx, student, low.ID, "withdraw-low", r.Action{Action: "withdraw", Revision: low.Revision}); err != nil {
		t.Fatal(err)
	}
	got, err = f.service.ReadSummaries(ctx, student, "teacher", []string{"teacher-a"}, true)
	if err != nil || got.Items[0].ReviewCount != 3 || got.Items[0].Recommendation != "strongly_recommended" {
		t.Fatal("withdrawn sample retained", got, err)
	}
	page, err := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "a-course", Limit: 100})
	if err != nil || len(page.Items) != got.Items[0].ReviewCount {
		t.Fatal("list/count mismatch", err)
	}
	port.aliases["course"] = map[string]string{"a-course": "b-course"}
	got, err = f.service.ReadSummaries(ctx, student, "course", []string{"b-course"}, false)
	if err != nil || got.Items[0].ReviewCount != 3 || got.Items[0].Dimensions[0].Courses != 1 {
		t.Fatal("course alias aggregation", got, err)
	}
	raw, _ := json.Marshal(got)
	for _, key := range []string{"\"owner\"", "\"score\"", "\"mean\"", "teacher_ids"} {
		if strings.Contains(string(raw), key) {
			t.Fatal("internal statistic exposed", key)
		}
	}
}

func TestUISummaryPolicyBoundariesAndCurrentAuthorization(t *testing.T) {
	f, port := uiReviewFixture(t)
	ctx := context.Background()
	create(t, f, "one", input("course", "autumn", 5))
	create(t, f, "two", input("course", "autumn", 4))
	got, err := f.service.ReadSummaries(ctx, student, "course", []string{"course"}, true)
	if err != nil || got.Items[0].Recommendation != "unrated" || got.Items[0].Dimensions[0].Level != 0 || got.Items[0].Distribution[0].Weights != [5]int{} {
		t.Fatal("small sample graded", got, err)
	}
	create(t, f, "three", input("course", "autumn", 4))
	create(t, f, "four", input("course", "autumn", 5))
	got, err = f.service.ReadSummaries(ctx, student, "course", []string{"course"}, false)
	if err != nil || got.Items[0].Recommendation != "strongly_recommended" {
		t.Fatal("4.5 threshold", got, err)
	}
	f.service.Config.Recommendations.StronglyRecommendedAt = 4.6
	got, err = f.service.ReadSummaries(ctx, student, "course", []string{"course"}, false)
	if err != nil || got.Items[0].Recommendation != "recommended" {
		t.Fatal("config ignored", got, err)
	}
	port.schools = map[string]string{"other": "other-school"}
	for _, test := range []struct {
		p    r.Principal
		kind string
		ids  []string
		want error
	}{{r.Principal{}, "course", []string{"course"}, r.ErrUnauthenticated}, {student, "course", nil, r.ErrInput}, {student, "mentor", []string{"course"}, r.ErrInput}, {student, "course", []string{"other"}, r.ErrForbidden}, {student, "teacher", []string{"unmarked"}, r.ErrNotFound}, {student, "course", []string{"missing"}, r.ErrNotFound}} {
		if _, err := f.service.ReadSummaries(ctx, test.p, test.kind, test.ids, false); !errors.Is(err, test.want) {
			t.Fatalf("rejection: %v want %v", err, test.want)
		}
	}
	f.access.revoked = true
	if _, err := f.service.ReadSummaries(ctx, student, "course", []string{"course"}, false); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("revocation", err)
	}
	f.access.revoked = false
	port.failed = true
	if _, err := f.service.ReadSummaries(ctx, student, "course", []string{"course"}, false); !errors.Is(err, r.ErrUnavailable) {
		t.Fatal("dependency fabricated empty result", err)
	}
}

func TestUIOwnOverviewPendingOwnershipExpiryAndUnavailable(t *testing.T) {
	f, port := uiReviewFixture(t)
	ctx := context.Background()
	root := create(t, f, "mine", input("course", "autumn", 4))
	port.items = []r.PendingCourse{{CourseID: "course", CourseName: "Course", TermID: "autumn", TermName: "Autumn"}, {CourseID: "pending", CourseName: "Pending", TermID: "autumn", TermName: "Autumn"}, {CourseID: "pending", CourseName: "Pending", TermID: "autumn", TermName: "Autumn"}}
	port.aliases["course"] = map[string]string{"pending-alias": "pending"}
	port.items = append(port.items, r.PendingCourse{CourseID: "pending-alias", CourseName: "Old pending name", TermID: "autumn", TermName: "Old term label"})
	if _, err := f.service.SaveDraft(ctx, student, "", "draft-one", input("pending", "autumn", 3), 0); err != nil {
		t.Fatal(err)
	}
	got, err := f.service.OwnOverview(ctx, student)
	if err != nil || got.ReviewCount != 1 || got.DraftCount != 1 || got.PendingCourses.Count == nil || *got.PendingCourses.Count != 1 || got.PendingCourses.Items[0].CourseID != "pending" {
		t.Fatalf("overview: %+v %v", got, err)
	}
	other, err := f.service.OwnOverview(ctx, r.Principal{Subject: "other"})
	if err != nil || other.ReviewCount != 0 || other.DraftCount != 0 {
		t.Fatal("another account received counts", other, err)
	}
	withdrawn, err := f.service.Action(ctx, student, root.ID, "withdraw-own", r.Action{Action: "withdraw", Revision: root.Revision})
	if err != nil {
		t.Fatal(err)
	}
	got, err = f.service.OwnOverview(ctx, student)
	if err != nil || got.ReviewCount != 0 || got.DraftCount != 2 {
		t.Fatal("withdrawn draft count", got, err)
	}
	f.now = *withdrawn.ExpiresAt
	got, err = f.service.OwnOverview(ctx, student)
	if err != nil || got.DraftCount != 1 {
		t.Fatal("expired draft counted", got, err)
	}
	port.ownErr = r.ErrUnavailable
	got, err = f.service.OwnOverview(ctx, student)
	if err != nil || got.PendingCourses.State != "unavailable" || got.PendingCourses.Count != nil || len(got.PendingCourses.Items) != 0 {
		t.Fatal("unavailable enrollment became zero", got, err)
	}
	port.ownErr = nil
	port.owner = "someone-else"
	if _, err := f.service.OwnOverview(ctx, student); !errors.Is(err, r.ErrUnavailable) {
		t.Fatal("foreign provider owner accepted", err)
	}
	if _, err := f.service.OwnOverview(ctx, r.Principal{}); !errors.Is(err, r.ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestUIReviewHTTPAndUpvotePagination(t *testing.T) {
	f, _ := uiReviewFixture(t)
	ctx := context.Background()
	a := create(t, f, "up-a", input("course", "autumn", 4))
	f.now = f.now.Add(time.Second)
	b := create(t, f, "up-b", input("course", "autumn", 5))
	if _, err := f.service.Action(ctx, r.Principal{Subject: "voter"}, a.ID, "upvote", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	page, err := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "course", Sort: "upvotes", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID || page.Next == "" {
		t.Fatal("top review order", page, err)
	}
	page, err = f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "course", Sort: "upvotes", Limit: 1, After: page.Next})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != b.ID {
		t.Fatal("vote cursor", page, err)
	}
	h := httpapi.Handler{Service: f.service, Authenticate: func(req *http.Request) (r.Principal, error) {
		if req.Header.Get("Authorization") == "Bearer student" {
			return student, nil
		}
		return r.Principal{}, nil
	}}.Routes()
	for _, tc := range []struct {
		path, token string
		status      int
	}{{"/api/v1/reviews/summaries?kind=course&ids=course&include_charts=true", "Bearer student", 200}, {"/api/v1/reviews/summaries?kind=course&ids=course", "", 401}, {"/api/v1/reviews/summaries?kind=teacher&ids=unmarked", "Bearer student", 404}, {"/api/v1/reviews/summaries?kind=course&ids=course&include_charts=wrong", "Bearer student", 400}, {"/api/v1/reviews/me/overview?owner=other", "Bearer student", 400}, {"/api/v1/reviews/me/overview", "Bearer student", 200}, {"/api/v1/reviews/me/overview", "", 401}} {
		req := httptest.NewRequest("GET", tc.path, nil)
		req.Header.Set("Authorization", tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}
