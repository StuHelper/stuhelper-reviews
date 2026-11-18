package reviews_test

import (
	"context"
	"encoding/json"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPresentationHTTPPreservesRatingTexts(t *testing.T) {
	f := fixture(t)
	repo, migration := database(t)
	f.service.Repo = repo
	ctx := context.Background()
	texts := map[string][][]string{}
	for _, locale := range []string{"zh-CN", "en-US", "ja-JP", "ko-KR", "fr-FR", "es-ES", "ru-RU", "ar-EG"} {
		dimensions := [][]string{}
		for i := 0; i < 5; i++ {
			dimensions = append(dimensions, []string{"None", "One", "Two", "Three", "Four", "Five"})
		}
		texts[locale] = dimensions
	}
	_, e := f.service.SavePresentation(ctx, r.Principal{Subject: "admin"}, "initial", r.Presentation{SchoolID: "school", RatingTexts: texts})
	if e != nil {
		t.Fatal(e)
	}
	view, e := f.service.ReadPresentation(ctx, student, "school")
	if e != nil || view.Effective.Revision != 1 || len(view.Effective.RatingTexts) != 8 || view.Effective.RatingTexts["ar-EG"][4][5] != "Five" {
		t.Fatal("rating configuration missing", view, e)
	}
	entry := create(t, f, "no-preview", input("course", "autumn", 4))
	h := httpapi.Handler{Service: f.service, Authenticate: func(*http.Request) (r.Principal, error) { return r.Principal{Subject: "admin"}, nil }}.Routes()
	for _, path := range []string{"/api/v1/reviews/" + entry.ID, "/api/v1/reviews", "/api/v1/reviews/presentation?school_id=school", "/api/v1/reviews/admin/presentation?school_id=school"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		assertNoPreviewKeys(t, w.Body.Bytes())
	}
	// Unknown fields must be rejected.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/reviews/admin/presentation", strings.NewReader(`{"school_id":"school","revision":1,"unknown":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "unknown")
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("unknown field accepted", w.Code, w.Body.String())
	}
	settings := view.Effective
	settings.RatingTexts["en-US"][0][5] = "Excellent"
	updated, e := f.service.SavePresentation(ctx, r.Principal{Subject: "admin"}, "rating-update", settings)
	if e != nil || updated.Effective.Revision != 2 || updated.Effective.RatingTexts["en-US"][0][5] != "Excellent" {
		t.Fatal("rating update failed", updated, e)
	}
	var history int
	if e = migration.QueryRow(`SELECT count(*) FROM reviews.presentation_history WHERE school_id='school'`).Scan(&history); e != nil || history != 2 {
		t.Fatal("history lost", history, e)
	}
}

type globalPresentationAccess struct{ *access }

func (a globalPresentationAccess) Authorize(ctx context.Context, p r.Principal, action, school string) (r.Grant, error) {
	if action == "configure" && p.Subject == "global-admin" && school == "*" {
		return r.Grant{SchoolID: "*", RuleVersion: "rule-1"}, nil
	}
	if action == "read_full" && p.Subject == "student" {
		return r.Grant{SchoolID: "school", RuleVersion: "rule-1", EnrollmentRequired: true}, nil
	}
	return a.access.Authorize(ctx, p, action, school)
}

func assertNoPreviewKeys(t *testing.T, raw []byte) {
	t.Helper()
	var value any
	if e := json.Unmarshal(raw, &value); e != nil {
		t.Fatal(e)
	}
	var walk func(any)
	walk = func(v any) {
		switch item := v.(type) {
		case map[string]any:
			for key, child := range item {
				switch key {
				case "preview", "guest_preview", "member_preview", "preview_ratio":
					t.Fatalf("retired field returned: %s", key)
				}
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(value)
}

func TestDecisionContractAndPresentationReplay(t *testing.T) {
	f := fixture(t)
	f.service.Access = globalPresentationAccess{f.access}
	ctx := context.Background()
	admin := r.Principal{Subject: "admin"}
	globalAdmin := r.Principal{Subject: "global-admin"}

	// 1. A-15.1: Global "*" presentation must report source="global", and idempotent replay must return original revision.
	firstGlobal, err := f.service.SavePresentation(ctx, globalAdmin, "global-k1", r.Presentation{SchoolID: "*", Revision: 0, RatingTexts: map[string][][]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if firstGlobal.Source != "global" || firstGlobal.Effective.Revision != 1 {
		t.Fatalf("expected global source and revision 1, got %+v", firstGlobal)
	}
	secondGlobal, err := f.service.SavePresentation(ctx, globalAdmin, "global-k2", r.Presentation{SchoolID: "*", Revision: 1, RatingTexts: map[string][][]string{}})
	if err != nil || secondGlobal.Effective.Revision != 2 {
		t.Fatalf("expected revision 2, got %+v err=%v", secondGlobal, err)
	}
	replayedFirst, err := f.service.SavePresentation(ctx, globalAdmin, "global-k1", r.Presentation{SchoolID: "*", Revision: 0, RatingTexts: map[string][][]string{}})
	if err != nil || replayedFirst.Effective.Revision != 1 {
		t.Fatalf("SavePresentation replay returned latest revision instead of original: %+v err=%v", replayedFirst, err)
	}

	// 2. A-06.2/3/4: HTTP nil Service/Access safety, invalid/empty/cross-school school_id status codes, and attribution header/body validation.
	nilHandler := httpapi.Handler{Authenticate: func(*http.Request) (r.Principal, error) { return admin, nil }}.Routes()
	wNil := httptest.NewRecorder()
	nilHandler.ServeHTTP(wNil, httptest.NewRequest("GET", "/api/v1/reviews/admin/presentation?school_id=school", nil))
	if wNil.Code != 503 {
		t.Fatalf("expected 503 for nil Service, got %d", wNil.Code)
	}

	h := httpapi.Handler{Service: f.service, Authenticate: func(req *http.Request) (r.Principal, error) {
		who := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		return r.Principal{Subject: who}, nil
	}}.Routes()

	for _, tc := range []struct {
		method, path, token, key, body string
		want                           int
	}{
		{"GET", "/api/v1/reviews/presentation", "student", "", "", 400},
		{"GET", "/api/v1/reviews/presentation?school_id=bad!", "student", "", "", 400},
		{"GET", "/api/v1/reviews/presentation?school_id=other-school", "student", "", "", 403},
		{"GET", "/api/v1/reviews/admin/presentation", "student", "", "", 400},
		{"GET", "/api/v1/reviews/admin/presentation?school_id=bad!", "admin", "", "", 400},
		{"PUT", "/api/v1/reviews/admin/presentation?school_id=", "admin", "empty-school", `{"school_id":"school","revision":0,"rating_texts":{}}`, 400},
		{"PUT", "/api/v1/reviews/admin/presentation?school_id=school&school_id=other", "admin", "ambiguous-school", `{"school_id":"school","revision":0,"rating_texts":{}}`, 400},
		{"PUT", "/api/v1/reviews/admin/presentation?school_id=other", "admin", "mismatch-q", `{"school_id":"school","revision":0,"rating_texts":{}}`, 400},
		{"POST", "/api/v1/reviews/admin/entries/entry1/attribution", "admin", "", `{"teacher_id":"teacher-a"}`, 400},
		{"POST", "/api/v1/reviews/admin/entries/entry1/attribution", "admin", "attr-k1", `{}`, 400},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		if tc.key != "" {
			req.Header.Set("Idempotency-Key", tc.key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s %s: got %d want %d body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	unchanged, err := f.service.AdminPresentation(ctx, admin, "school")
	if err != nil || unchanged.Configured != nil {
		t.Fatalf("rejected PUT wrote school override: %+v %v", unchanged, err)
	}
	savedAccess := f.service.Access
	f.service.Access = nil
	req := httptest.NewRequest("GET", "/api/v1/reviews/admin/presentation?school_id=school", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("missing authorization service: %d", rec.Code)
	}
	f.service.Access = savedAccess

	// 5. A-06.1: Validate Decision struct with feature/unfeature/reassign and attribution/handle fields against OpenAPI Decision schema.
	rawSpec, err := os.ReadFile("contracts/reviews.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var specDoc map[string]any
	if err = json.Unmarshal(rawSpec, &specDoc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource("urn:decision", specDoc); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("urn:decision#/components/schemas/Decision")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"feature", "unfeature", "reassign", "identity"} {
		sample := r.Decision{
			ID:               "dec-1",
			ObjectID:         "obj-1",
			Revision:         2,
			Action:           action,
			AuthorHandle:     "author",
			TeacherID:        "teacher-a",
			EvidenceRevision: "ev-rev-1",
			Signed:           true,
			Reason:           "Valid moderation decision",
		}
		encoded, _ := json.Marshal(sample)
		var decoded any
		_ = json.Unmarshal(encoded, &decoded)
		if err = compiled.Validate(decoded); err != nil {
			t.Fatalf("OpenAPI Decision schema rejected %s decision: %v", action, err)
		}
	}
}
