package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContentHTTPReadAdmissionAndOwnRecordException(t *testing.T) {
	f := fixture(t)
	entry := create(t, f, "read-gate", input("course", "autumn", 4))
	mentor := create(t, f, "mentor-read-gate", r.Input{Kind: "mentor", TargetID: "teacher-a", Title: "Mentor", Body: "Restricted mentor comment"})
	h := httpapi.Handler{Service: f.service, Authenticate: func(req *http.Request) (r.Principal, error) {
		return r.Principal{Subject: strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")}, nil
	}}.Routes()
	call := func(path, who string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if who != "" {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("X-User-ID", "student")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != status {
			t.Fatalf("%s %s status=%d want=%d body=%s", who, path, out.Code, status, out.Body.String())
		}
		if status != 200 {
			for _, key := range []string{"\"body\"", "\"items\"", "\"dimensions\"", "\"teacher_ids\"", "\"rating_texts\""} {
				if strings.Contains(out.Body.String(), key) {
					t.Fatal("denied response contains content", key)
				}
			}
		}
		return out
	}
	paths := []string{"/api/v1/reviews?limit=20", "/api/v1/reviews?kind=course&target_id=missing", "/api/v1/reviews/" + entry.ID, "/api/v1/reviews/statistics?kind=course&target_id=course", "/api/v1/reviews/compose?kind=course&target_id=course&term_id=autumn", "/api/v1/reviews/presentation?school_id=school"}
	paths = append(paths, "/api/v1/reviews/"+mentor.ID, "/api/v1/reviews?kind=mentor&target_id=teacher-a")
	for _, path := range paths {
		call(path, "", 401)
		call(path, "member", 403)
	}
	for _, path := range []string{paths[0], paths[2], paths[3], paths[4], paths[5], paths[6], paths[7]} {
		call(path, "student", 200)
	}
	f.access.revoked = true
	for _, path := range paths {
		call(path, "student", 403)
	}
	own := call("/api/v1/reviews/me/entries/"+entry.ID, "student", 200)
	if !strings.Contains(own.Body.String(), "Second line") {
		t.Fatal("own full record lost")
	}
	call("/api/v1/reviews/me/entries?limit=20", "student", 200)
	call("/api/v1/reviews/me/entries/"+entry.ID, "member", 404)
	f.access.failed = true
	for _, path := range paths {
		call(path, "student", 503)
	}
	call("/api/v1/reviews/me/entries/"+entry.ID, "student", 200)
}
func TestContentListsStatisticsAndComposerRespectServedSchool(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	local := create(t, f, "local", input("course", "autumn", 4))
	f.catalog.schools = map[string]string{"foreign": "other", "foreign-term": "other"}
	in := input("course", "autumn", 3)
	in.TargetID = "foreign"
	in.TermID = "foreign-term"
	foreign := create(t, f, "foreign", in)
	page, e := f.service.List(ctx, r.Principal{Subject: "reader"}, r.Filter{Limit: 20})
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != local.ID {
		t.Fatal("list exposed another school", page, e)
	}
	if _, e = f.service.Get(ctx, student, foreign.ID); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("foreign detail exposed", e)
	}
	if _, e = f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "foreign", Limit: 20}); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("foreign filtered list exposed", e)
	}
	if _, e = f.service.ReadStatsPage(ctx, student, "course", "foreign", nil, nil, "", 20); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("foreign statistics exposed", e)
	}
	if _, e = f.service.Composer(ctx, student, "course", "foreign", "autumn", ""); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("foreign composer exposed", e)
	}
	stats, e := f.service.ReadStatsPage(ctx, student, "teacher", "teacher-a", nil, nil, "", 20)
	if e != nil || stats.Dimensions[0].Samples != 1 {
		t.Fatal("teacher aggregates included rows from another school", stats, e)
	}
	own, e := f.service.GetOwn(ctx, student, foreign.ID)
	if e != nil || own.ID != foreign.ID {
		t.Fatal("own historical school record lost", e)
	}
}
