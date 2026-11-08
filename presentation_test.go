package reviews_test

import (
	"context"
	"encoding/json"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"

	"net/http"
	"net/http/httptest"
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
