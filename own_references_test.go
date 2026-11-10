package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type namedCatalog struct {
	*catalog
	calls int
	bad   bool
}

func (c *namedCatalog) ReferenceNames(_ context.Context, ids []string) (r.References, error) {
	c.calls++
	if c.failed {
		return r.References{}, r.ErrUnavailable
	}
	out := r.References{Items: []r.Reference{}, MissingIDs: []string{}}
	for _, id := range ids {
		if id == "removed" {
			out.MissingIDs = append(out.MissingIDs, id)
		} else {
			out.Items = append(out.Items, r.Reference{ID: id, Kind: "course", Name: "Name of " + id, CanonicalID: id})
		}
	}
	if c.bad {
		out.Items = append(out.Items, r.Reference{ID: "unrequested", Kind: "course", Name: "leak"})
	}
	return out, nil
}
func TestOwnNamesAndValidatedDraftAssociationsSurviveAdmissionExpiry(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	names := &namedCatalog{catalog: f.catalog}
	f.service.Catalog = names
	in := input("draft-course", "autumn", 4)
	// An ordinary draft has no qualification requirement and grants no name access.
	member := r.Principal{Subject: "member"}
	saved, e := f.service.SaveDraft(ctx, member, "", "unverified", in, 0)
	if e != nil {
		t.Fatal(e)
	}
	draft, e := f.service.GetDraft(ctx, member, saved.ID)
	if e != nil || draft.ReferenceState != "unverified" || len(draft.ReferenceIDs) != 0 {
		t.Fatal(draft, e)
	}
	if _, e = f.service.MyReferences(ctx, member, []string{"draft-course"}); !errors.Is(e, r.ErrNotFound) || names.calls != 0 {
		t.Fatal("self-typed ID obtained name", e, names.calls)
	}
	// A saved draft without server proof grants no name access.
	unverifiedDraft := r.Draft{ID: "unverifiedDraft", Revision: 1, Owner: student.Subject, Input: in}
	if e = f.service.Repo.Transaction(ctx, func(tx r.Transaction) error { return tx.SaveDraft(unverifiedDraft) }); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.MyReferences(ctx, student, []string{"draft-course"}); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("unverifiedDraft draft invented proof", e)
	}
	saved, e = f.service.SaveDraft(ctx, student, "unverifiedDraft", "validate", in, 1)
	if e != nil {
		t.Fatal(e)
	}
	draft, e = f.service.GetDraft(ctx, student, saved.ID)
	if e != nil || draft.ReferenceState != "verified" || !slices.Contains(draft.ReferenceIDs, "draft-course") {
		t.Fatal(draft, e)
	}
	f.access.revoked = true
	refs, e := f.service.MyReferences(ctx, student, []string{"draft-course", "autumn"})
	if e != nil || len(refs.Items) != 2 {
		t.Fatal(refs, e)
	}
	// A changed target cannot retain the removed target's proof or authorize a new ID.
	in.TargetID = "new-course"
	saved, e = f.service.SaveDraft(ctx, student, saved.ID, "change", in, saved.Revision)
	if e != nil {
		t.Fatal(e)
	}
	draft, e = f.service.GetDraft(ctx, student, saved.ID)
	if e != nil || draft.ReferenceState != "unverified" || !slices.Equal(draft.ReferenceIDs, []string{"autumn"}) {
		t.Fatal(draft, e)
	}
	for _, id := range []string{"draft-course", "new-course"} {
		if _, e = f.service.MyReferences(ctx, student, []string{id}); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("changed draft proof leaked", id, e)
		}
	}
	// Dependency failures retain text and historical proof, with a visible state.
	f.access.failed = true
	in.Body = "Saved during outage"
	saved, e = f.service.SaveDraft(ctx, student, saved.ID, "outage", in, saved.Revision)
	if e != nil {
		t.Fatal(e)
	}
	draft, e = f.service.GetDraft(ctx, student, saved.ID)
	if e != nil || draft.ReferenceState != "unavailable" || draft.Input.Body != in.Body || !slices.Equal(draft.ReferenceIDs, []string{"autumn"}) {
		t.Fatal(draft, e)
	}
	if _, e = f.service.MyReferences(ctx, student, []string{"autumn"}); e != nil {
		t.Fatal(e)
	}
	f.access.failed = false
	f.access.revoked = false
	saved, e = f.service.SaveDraft(ctx, student, saved.ID, "restore", in, saved.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.MyReferences(ctx, student, []string{"new-course"}); e != nil {
		t.Fatal(e)
	}
	// Foreign-school references are not proved by local admission.
	f.catalog.schools = map[string]string{"foreign": "other"}
	in.TargetID = "foreign"
	saved, e = f.service.SaveDraft(ctx, student, saved.ID, "foreign", in, saved.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.MyReferences(ctx, student, []string{"foreign"}); !errors.Is(e, r.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = f.service.MyReferences(ctx, member, []string{"autumn"}); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("cross-account proof", e)
	}
	if _, e = f.service.MyReferences(ctx, r.Principal{}, []string{"autumn"}); !errors.Is(e, r.ErrUnauthenticated) {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = f.service.SaveDraft(cancelled, student, saved.ID, "cancelled", in, saved.Revision); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	persisted, e := f.service.GetDraft(ctx, student, saved.ID)
	if e != nil || persisted.Revision != saved.Revision {
		t.Fatal("cancelled save mutated", persisted, e)
	}
	names.bad = true
	if _, e = f.service.MyReferences(ctx, student, []string{"autumn"}); !errors.Is(e, r.ErrUnavailable) {
		t.Fatal("unrequested provider data exposed", e)
	}
}
func TestOwnReferenceHTTPDoesNotOpenPublicContent(t *testing.T) {
	f := fixture(t)
	names := &namedCatalog{catalog: f.catalog}
	f.service.Catalog = names
	ctx := context.Background()
	entry := create(t, f, "mine", input("own-course", "autumn", 4))
	other, e := f.service.Create(ctx, r.Principal{Subject: "author"}, "others", input("voted-course", "autumn", 4))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Action(ctx, student, other.ID, "vote", r.Action{Action: "vote", Vote: 1}); e != nil {
		t.Fatal(e)
	}
	f.service.Favorites = favoriteReferences{}
	f.access.revoked = true
	f.access.failed = true
	handler := httpapi.Handler{Service: f.service, Authenticate: func(req *http.Request) (r.Principal, error) {
		return r.Principal{Subject: strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")}, nil
	}}.Routes()
	call := func(path, subject string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if subject != "" {
			req.Header.Set("Authorization", "Bearer "+subject)
		}
		req.Header.Set("X-User-ID", "student")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s %s: %d %s", subject, path, out.Code, out.Body.String())
		}
		return out
	}
	path := "/api/v1/reviews/me/references?ids=own-course,voted-course,teacher-a,removed"
	call(path, "", 401)
	call(path, "member", 404)
	out := call(path, "student", 200)
	var refs r.References
	if e = json.Unmarshal(out.Body.Bytes(), &refs); e != nil || len(refs.Items) != 3 || !slices.Equal(refs.MissingIDs, []string{"removed"}) {
		t.Fatal(refs, e)
	}
	for _, key := range []string{"body", "title", "ratings", "author", "description"} {
		if strings.Contains(out.Body.String(), "\""+key+"\"") {
			t.Fatal("content field", key)
		}
	}
	call("/api/v1/reviews/"+entry.ID, "student", 503)
	call("/api/v1/reviews/me/references?ids=own-course,arbitrary", "student", 404)
	call("/api/v1/reviews/me/references?ids="+strings.Repeat("own-course,", 100)+"own-course", "student", 400)
	if _, e = f.service.Action(ctx, student, other.ID, "unvote", r.Action{Action: "vote", Vote: 0}); e != nil {
		t.Fatal(e)
	}
	call("/api/v1/reviews/me/references?ids=voted-course", "student", 404)
}

type favoriteReferences struct{}

func (favoriteReferences) CourseReferences(_ context.Context, owner string, ids []string) ([]string, error) {
	if owner == student.Subject && slices.Contains(ids, "removed") {
		return []string{"removed"}, nil
	}
	return nil, nil
}
