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

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

type coTaughtReviewCatalog struct {
	*catalog
	teachers []string
}

func (c coTaughtReviewCatalog) CourseTermTeachers(context.Context, string, string) ([]string, error) {
	return c.teachers, nil
}

func TestMultiTeacherStatisticsGradesAndCreateReplay(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	catalog := &coTaughtReviewCatalog{f.catalog, []string{"teacher-a", "teacher-b", "teacher-c"}}
	f.service.Catalog = catalog
	in := input("course", "autumn", 4)
	in.TeacherIDs = []string{"teacher-c", "teacher-a"}
	in.Grade = &r.Grade{Scale: "percent", Value: json.RawMessage("92")}
	first := create(t, f, "multi", in)
	view, err := f.service.GetWithOptions(ctx, r.Principal{Subject: "reader"}, first.ID, r.ViewOptions{IncludeTeacherIDs: true})
	wantTeachers := []string{"teacher-a", "teacher-c"}
	if err != nil || view.Grade == nil || view.Grade.Scale != "percent" || string(view.Grade.Value) != "92" ||
		!slices.Equal(view.TeacherIDs, wantTeachers) || !slices.Equal(view.AttributedTeacherIDs, wantTeachers) {
		t.Fatal(view, err)
	}
	if _, err := f.service.Get(ctx, r.Principal{Subject: "member"}, first.ID); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("ineligible reader obtained a published review", err)
	}
	unspecified, err := f.service.Create(ctx, r.Principal{Subject: "another-author"}, "unspecified", input("course", "autumn", 2))
	if err != nil {
		t.Fatal(err)
	}
	view, err = f.service.GetWithOptions(ctx, student, unspecified.ID, r.ViewOptions{IncludeTeacherIDs: true})
	if err != nil || len(view.TeacherIDs) != 0 || view.Grade != nil || !slices.Equal(view.AttributedTeacherIDs, catalog.teachers) {
		t.Fatal(view, err)
	}
	for _, check := range []struct {
		kind, id string
		samples  int64
	}{{"course", "course", 2}, {"teacher", "teacher-a", 2}, {"teacher", "teacher-b", 1}, {"teacher", "teacher-c", 2}} {
		stats, err := f.service.ReadStatsPage(ctx, student, check.kind, check.id, nil, nil, "", 20)
		if err != nil || int64(stats.Dimensions[0].Samples) != check.samples {
			t.Fatalf("%s %s: %+v, %v", check.kind, check.id, stats.Dimensions[0], err)
		}
	}

	// The original result still replays after the public assignment changes.
	catalog.teachers = []string{"teacher-b"}
	in.TeacherIDs = wantTeachers
	if repeated, err := f.service.Create(ctx, student, "multi", in); err != nil || repeated != first {
		t.Fatal(repeated, err)
	}
	in.Grade = &r.Grade{Scale: "percent", Value: json.RawMessage("93")}
	if _, err := f.service.Create(ctx, student, "multi", in); !errors.Is(err, r.ErrConflict) {
		t.Fatal("changed grade reused an idempotency key", err)
	}
}

func TestReviewGradeAndTeacherSelectionHTTPValidation(t *testing.T) {
	f := fixture(t)
	h := httpapi.Handler{Service: f.service, Authenticate: func(*http.Request) (r.Principal, error) { return student, nil }}.Routes()
	for i, tc := range []struct {
		fields, subcode string
		status          int
	}{
		{`"teacher_id":"teacher-a"`, "malformed_json", 400},
		{`"teacher_ids":null`, "invalid_teacher_ids", 400},
		{`"teacher_ids":["teacher-a","teacher-a"]`, "invalid_teacher_ids", 400},
		{`"teacher_ids":["teacher-a","teacher-c"]`, "teacher_not_candidate", 403},
		{`"grade":null`, "invalid_grade", 400},
		{`"grade":{"scale":"percent","value":1.5}`, "invalid_grade", 400},
		{`"grade":{"scale":"percent","value":101}`, "invalid_grade", 400},
		{`"grade":{"scale":"percent","value":-1}`, "invalid_grade", 400},
		{`"grade":{"scale":"percent","value":null}`, "invalid_grade", 400},
		{`"grade":{"scale":"percent","value":"92"}`, "invalid_grade", 400},
		{`"grade":{"scale":"five_level","value":"A"}`, "invalid_grade", 400},
		{`"grade":{"scale":"two_level","value":"good"}`, "invalid_grade", 400},
		{`"grade":{"scale":"withdrawn","value":null}`, "invalid_grade", 400},
		{`"grade":{"scale":"withdrawn","other":true}`, "invalid_grade", 400},
		{`"grade":{"scale":"unknown","value":0}`, "invalid_grade", 400},
	} {
		body := `{"kind":"course","target_id":"course","term_id":"autumn","title":"Review","body":"Body","ratings":[4,4,4,4,4],"signed":false,` + tc.fields + "}"
		req := httptest.NewRequest(http.MethodPost, "/api/v1/reviews", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", fmt.Sprintf("validation-%d", i))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var payload httpErrorPayload
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || rec.Code != tc.status || payload.Subcode != tc.subcode {
			t.Fatalf("case %d: %d %+v %v", i, rec.Code, payload, err)
		}
	}
	page, err := f.service.List(context.Background(), student, r.Filter{Owner: student.Subject, Limit: 20})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("an invalid submission persisted", page, err)
	}
}

func TestReviewDraftWithdrawalAndHistoryRetainGradeAndSelection(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	admin := r.Principal{Subject: "admin"}
	in := input("course", "autumn", 4)
	bad := in
	bad.TeacherIDs = []string{"teacher-c"}
	if _, err := f.service.SaveDraft(ctx, student, "", "draft-outside-offering", bad, 0); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("draft accepted a noncandidate", err)
	}
	in.TeacherIDs = []string{"teacher-a", "teacher-b"}
	in.Grade = &r.Grade{Scale: "five_level", Value: json.RawMessage(`"good"`)}
	draft, err := f.service.SaveDraft(ctx, student, "", "my-draft", in, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := f.service.GetDraft(ctx, student, draft.ID)
	if err != nil || !slices.Equal(saved.Input.TeacherIDs, in.TeacherIDs) || saved.Input.Grade == nil || string(saved.Input.Grade.Value) != `"good"` {
		t.Fatal(saved, err)
	}
	pub, err := f.service.PublishDraft(ctx, student, draft.ID, "publish-grade-draft", r.PublishDraft{Revision: draft.Revision, Input: in})
	if err != nil {
		t.Fatal(err)
	}
	withdrawn, err := f.service.Action(ctx, student, pub.ID, "withdraw-grade", r.Action{Action: "withdraw", Revision: pub.Revision})
	if err != nil {
		t.Fatal(err)
	}
	in.Grade = &r.Grade{Scale: "withdrawn"}
	edited, err := f.service.Action(ctx, student, pub.ID, "draft-update", r.Action{Action: "save_withdrawn", Revision: withdrawn.Revision, Input: &in})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := f.service.GetDraft(ctx, student, pub.ID)
	if err != nil || !reopened.Withdrawn || reopened.Input.Grade == nil || reopened.Input.Grade.Scale != "withdrawn" || !slices.Equal(reopened.Input.TeacherIDs, in.TeacherIDs) {
		t.Fatal(reopened, err)
	}
	changed := in
	changed.TeacherIDs = []string{"teacher-a"}
	if _, err := f.service.Action(ctx, student, pub.ID, "change-teachers", r.Action{Action: "republish", Revision: edited.Revision, Input: &changed}); !errors.Is(err, r.ErrInput) {
		t.Fatal("republication changed attribution", err)
	}
	if _, err := f.service.Action(ctx, student, pub.ID, "republish-grade", r.Action{Action: "republish", Revision: edited.Revision, Input: &in}); err != nil {
		t.Fatal(err)
	}
	history, err := f.service.History(ctx, admin, pub.ID, 0, 20)
	if err != nil || len(history) != 4 || history[0].Input.Grade == nil || history[0].Input.Grade.Scale != "withdrawn" ||
		history[3].Input.Grade == nil || history[3].Input.Grade.Scale != "five_level" || !slices.Equal(history[3].Input.TeacherIDs, in.TeacherIDs) {
		t.Fatal(history, err)
	}
}
