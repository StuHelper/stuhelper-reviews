package reviews_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

type globalConfigAccess struct {
	base r.Access
}

func (g globalConfigAccess) Authorize(ctx context.Context, p r.Principal, action, school string) (r.Grant, error) {
	if action == "configure" && p.Subject == "global-admin" && school == "*" {
		return r.Grant{SchoolID: "*", RuleVersion: "rule-1"}, nil
	}
	return g.base.Authorize(ctx, p, action, school)
}

func TestDefaultRatingTextsCoverEightLocalesAndPresentationFallback(t *testing.T) {
	wantDims := [5]string{"recommendation", "content_quality", "difficulty", "workload", "grading"}
	if r.RatingDimensionKeys != wantDims {
		t.Fatalf("unexpected dimension order: %v", r.RatingDimensionKeys)
	}
	wantLocales := [8]string{"zh-CN", "en-US", "ja-JP", "ko-KR", "fr-FR", "es-ES", "ru-RU", "ar-EG"}
	if r.SupportedLocales != wantLocales {
		t.Fatalf("unexpected supported locales: %v", r.SupportedLocales)
	}

	defaults := r.DefaultRatingTexts()
	if len(defaults) != 8 {
		t.Fatalf("expected 8 locales, got %d", len(defaults))
	}
	for _, locale := range r.SupportedLocales {
		dims, ok := defaults[locale]
		if !ok || len(dims) != 5 {
			t.Fatalf("locale %s missing 5 dimensions: %v", locale, dims)
		}
		for d, scores := range dims {
			if len(scores) != 6 {
				t.Fatalf("locale %s dim %d expected 6 labels, got %d", locale, d, len(scores))
			}
			for s, label := range scores {
				if strings.TrimSpace(label) == "" || len([]rune(label)) > 100 {
					t.Fatalf("locale %s dim %d score %d invalid label %q", locale, d, s, label)
				}
			}
		}
	}
	hasArabic := false
	for _, ch := range defaults["ar-EG"][0][5] {
		if unicode.Is(unicode.Arabic, ch) {
			hasArabic = true
			break
		}
	}
	if !hasArabic {
		t.Fatalf("expected Arabic script in ar-EG default label, got %q", defaults["ar-EG"][0][5])
	}

	// Mutating a returned copy must not pollute subsequent DefaultRatingTexts() calls.
	defaults["zh-CN"][0][5] = "MUTATED"
	fresh := r.DefaultRatingTexts()
	if fresh["zh-CN"][0][5] == "MUTATED" {
		t.Fatal("DefaultRatingTexts shared mutable backing slice")
	}

	f := fixture(t)
	repo, _ := database(t)
	f.service.Repo = repo
	ctx := context.Background()

	// Unconfigured school returns source=default with full 8-locale template.
	unconfigured, err := f.service.ReadPresentation(ctx, student, "school")
	if err != nil {
		t.Fatal(err)
	}
	if unconfigured.Source != "default" || unconfigured.Configured != nil {
		t.Fatalf("expected default unconfigured presentation, got %+v", unconfigured)
	}
	if len(unconfigured.Effective.RatingTexts) != 8 || unconfigured.Effective.RatingTexts["ar-EG"][2][5] != fresh["ar-EG"][2][5] {
		t.Fatalf("unexpected default effective rating_texts: %+v", unconfigured.Effective)
	}

	// HTTP endpoint returns 8-locale effective rating_texts on 200, and leaks nothing on 401.
	unauthHandler := httpapi.Handler{
		Service:      f.service,
		Authenticate: func(*http.Request) (r.Principal, error) { return r.Principal{}, nil },
	}.Routes()
	w := httptest.NewRecorder()
	unauthHandler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/reviews/presentation?school_id=school", nil))
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "rating_texts") {
		t.Fatalf("unauthorized presentation leaked rating_texts: %d %s", w.Code, w.Body.String())
	}

	authHandler := httpapi.Handler{
		Service:      f.service,
		Authenticate: func(*http.Request) (r.Principal, error) { return student, nil },
	}.Routes()
	w = httptest.NewRecorder()
	authHandler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/reviews/presentation?school_id=school", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body.String())
	}
	var httpView r.PresentationView
	if err := json.Unmarshal(w.Body.Bytes(), &httpView); err != nil {
		t.Fatal(err)
	}
	if httpView.Source != "default" || len(httpView.Effective.RatingTexts) != 8 {
		t.Fatalf("unexpected HTTP presentation view: %+v", httpView)
	}

	// Admin can initialize from DefaultRatingTexts(), customize, and save directly.
	f.service.Access = globalConfigAccess{base: f.service.Access}
	admin := r.Principal{Subject: "admin"}
	globalAdmin := r.Principal{Subject: "global-admin"}
	globalCustom := r.DefaultRatingTexts()
	globalCustom["zh-CN"][0][5] = "全局强烈推荐"
	if _, err := f.service.SavePresentation(ctx, globalAdmin, "global-init", r.Presentation{
		SchoolID:    "*",
		RatingTexts: globalCustom,
	}); err != nil {
		t.Fatalf("SavePresentation rejected DefaultRatingTexts template: %v", err)
	}

	// School with empty RatingTexts inherits global custom texts for Effective while keeping Configured empty.
	schoolSaved, err := f.service.SavePresentation(ctx, admin, "school-empty", r.Presentation{
		SchoolID:    "school",
		RatingTexts: map[string][][]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if schoolSaved.Source != "school" || schoolSaved.Configured == nil || len(schoolSaved.Configured.RatingTexts) != 0 {
		t.Fatalf("expected empty school configured map, got %+v", schoolSaved)
	}
	if schoolSaved.Effective.RatingTexts["zh-CN"][0][5] != "全局强烈推荐" {
		t.Fatalf("expected school effective texts to inherit global override, got %q", schoolSaved.Effective.RatingTexts["zh-CN"][0][5])
	}
}
