package reviews_test

import (
	"encoding/json"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyReviewHTTPShapeAndExplicitPreviews(t *testing.T) {
	f := fixture(t)
	root := create(t, f, "compat", input("course", "autumn", 3))
	addReply(t, f, root.ID, "reply", student)
	repo := &previewCountingRepo{Repository: f.service.Repo}
	f.service.Repo = repo
	h := httpapi.Handler{Service: f.service, Authenticate: func(*http.Request) (r.Principal, error) { return student, nil }}
	for _, path := range []string{"/api/v1/reviews", "/api/v1/reviews/me/entries"} {
		for _, param := range []string{"", "&include_reply_preview=false", "&include_reply_preview=true", "&include_reply_preview=1", "&include_reply_preview=true&include_reply_preview=false"} {
			before := repo.batches
			rec := httptest.NewRecorder()
			h.Routes().ServeHTTP(rec, httptest.NewRequest("GET", path+"?kind=course"+param, nil))
			if param == "&include_reply_preview=1" || param == "&include_reply_preview=true&include_reply_preview=false" {
				if rec.Code != 400 {
					t.Fatal(param, rec.Code)
				}
				continue
			}
			var page r.Page
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			want := param == "&include_reply_preview=true"
			v := page.Items[0]
			if (v.ReplyCount != nil) != want || (v.ReplyPreview != nil) != want {
				t.Fatal("legacy shape", param, v)
			}
			if !want && repo.batches != before {
				t.Fatal("legacy request fetched previews")
			}
			if want && (*v.ReplyCount != 1 || len(*v.ReplyPreview) != 1) {
				t.Fatal(v)
			}
			if path == "/api/v1/reviews" && (param == "" || param == "&include_reply_preview=true") {
				if dir := os.Getenv("STUHELPER_B2_RESPONSE_DIR"); dir != "" {
					if err := os.WriteFile(filepath.Join(dir, map[bool]string{true: "reviews-expanded.json", false: "reviews-legacy.json"}[param != ""]), rec.Body.Bytes(), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}
