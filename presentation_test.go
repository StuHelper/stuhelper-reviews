package reviews_test

import (
	"context"
	"encoding/json"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
	"github.com/StuHelper/stuhelper-reviews/postgres"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPresentationMigrationAndHTTPRemovePreviewFieldsPreservingRatingTexts(t *testing.T) {
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
	old := map[string]any{"school_id": "school", "revision": 3, "guest_preview": 80, "member_preview": 160, "preview_ratio": 0.3, "rating_texts": texts}
	raw, e := json.Marshal(old)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = migration.Exec(`INSERT INTO reviews.presentations(school_id,data) VALUES($1,$2)`, "school", string(raw)); e != nil {
		t.Fatal(e)
	}
	if _, e = migration.Exec(`INSERT INTO reviews.presentation_history(school_id,revision,data) VALUES($1,3,$2)`, "school", string(raw)); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e = migration.Exec(postgres.Schema); e != nil {
			t.Fatal(e)
		}
	}
	var clean []byte
	if e = migration.QueryRow(`SELECT data FROM reviews.presentations WHERE school_id='school'`).Scan(&clean); e != nil {
		t.Fatal(e)
	}
	assertNoPreviewKeys(t, clean)
	view, e := f.service.ReadPresentation(ctx, student, "school")
	if e != nil || view.Effective.Revision != 3 || len(view.Effective.RatingTexts) != 8 || view.Effective.RatingTexts["ar-EG"][4][5] != "Five" {
		t.Fatal("migration lost rating configuration", view, e)
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
	// Obsolete configuration cannot silently appear to save successfully.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/reviews/admin/presentation", strings.NewReader(string(raw)))
	req.Header.Set("Idempotency-Key", "old")
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("obsolete configuration accepted", w.Code, w.Body.String())
	}
	settings := view.Effective
	settings.RatingTexts["en-US"][0][5] = "Excellent"
	updated, e := f.service.SavePresentation(ctx, r.Principal{Subject: "admin"}, "rating-update", settings)
	if e != nil || updated.Effective.Revision != 4 || updated.Effective.RatingTexts["en-US"][0][5] != "Excellent" {
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
