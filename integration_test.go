package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

type catalog struct{ missing, failed bool }

func (c *catalog) Target(_ context.Context, kind, id string) (r.Target, error) {
	if c.failed {
		return r.Target{}, r.ErrUnavailable
	}
	if id == "missing" {
		return r.Target{}, r.ErrNotFound
	}
	return r.Target{ID: id, Kind: kind, SchoolID: "school"}, nil
}
func (c *catalog) Evidence(_ context.Context, p r.Principal, course, term string) ([]r.Evidence, error) {
	if c.failed {
		return nil, r.ErrUnavailable
	}
	if c.missing || p.Subject == "unregistered" {
		return []r.Evidence{}, nil
	}
	teachers := []string{"teacher-a", "teacher-b"}
	if term == "spring" {
		teachers = []string{"teacher-a", "teacher-c"}
	}
	return []r.Evidence{{ID: "enrollment-" + term, TermID: term, TeacherIDs: teachers, SourceID: "approved", Revision: 2, FetchedAt: time.Now()}}, nil
}

type access struct{ revoked, failed, optional bool }

func (a *access) Authorize(_ context.Context, p r.Principal, action, school string) (r.Grant, error) {
	g := r.Grant{RuleVersion: "rule-1", EnrollmentRequired: !a.optional}
	if a.failed {
		return g, r.ErrUnavailable
	}
	if action == "moderate" {
		if p.Subject != "admin" || school != "school" {
			return g, r.ErrForbidden
		}
		return g, nil
	}
	if a.revoked || p.Subject == "" {
		return g, r.ErrForbidden
	}
	if p.Subject == "member" && action != "vote" && action != "bookmark" {
		return g, r.ErrForbidden
	}
	return g, nil
}

type moderation struct {
	outcome string
	failed  bool
}

func (m *moderation) Check(context.Context, r.Candidate) (string, error) {
	if m.failed {
		return "", r.ErrUnavailable
	}
	if m.outcome == "" {
		return "published", nil
	}
	return m.outcome, nil
}

type profiles struct{}

func (profiles) Lookup(_ context.Context, subject string) (r.Profile, error) {
	return r.Profile{Name: "Platform nickname", Avatar: "https://example.test/avatar.png"}, nil
}

type fixtureState struct {
	service    *r.Service
	catalog    *catalog
	access     *access
	moderation *moderation
	now        time.Time
}

func fixture(t *testing.T) *fixtureState {
	t.Helper()
	repo, _ := database(t)
	c := &catalog{}
	a := &access{}
	m := &moderation{}
	s, e := r.New(repo, c, a, m, profiles{}, r.DefaultConfig())
	if e != nil {
		t.Fatal(e)
	}
	f := &fixtureState{s, c, a, m, time.Now().UTC()}
	s.Clock = func() time.Time { return f.now }
	return f
}

var student = r.Principal{Subject: "student"}

func TestPersonalPendingStatesAndBoundedDraftPages(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.moderation.outcome = "pending"
	pending := create(t, f, "pending", input("course", "autumn", 3))
	mine, err := f.service.List(ctx, student, r.Filter{Owner: student.Subject, Limit: 20})
	if err != nil || len(mine.Items) != 1 || mine.Items[0].ID != pending.ID || mine.Items[0].State != "pending" || mine.Items[0].CanEdit {
		t.Fatal("pending owner status missing", err)
	}
	public, err := f.service.List(ctx, r.Principal{}, r.Filter{Limit: 20})
	if err != nil || len(public.Items) != 0 {
		t.Fatal("pending review exposed")
	}
	f.moderation.outcome = "published"
	published := create(t, f, "public", input("other", "autumn", 5))
	view, err := f.service.Get(ctx, student, published.ID)
	if err != nil || !view.CanEdit {
		t.Fatal("editing capability missing")
	}
	if _, err = f.service.Action(ctx, student, published.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err = f.service.SaveDraft(ctx, student, "", fmt.Sprintf("draft-%d", i), input("course", "autumn", 3), 0); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.service.Drafts(ctx, student, "", 2)
	if err != nil || len(first) != 2 || first[0].ID >= first[1].ID {
		t.Fatal("draft page is not bounded and ordered")
	}
	second, err := f.service.Drafts(ctx, student, first[1].ID, 2)
	if err != nil || len(second) != 2 || second[0].ID <= first[1].ID {
		t.Fatal("draft pagination lost or duplicated a draft")
	}
}

func TestTeacherCourseStatisticsPaginationPreservesOverallWeights(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	create(t, f, "one", input("a-course", "autumn", 1))
	create(t, f, "two", input("b-course", "autumn", 5))
	first, err := f.service.StatsPage(ctx, "teacher", "teacher-a", nil, nil, "", 1)
	if err != nil || len(first.Courses) != 1 || first.Next != "a-course" || first.Dimensions[0].Level != 3 {
		t.Fatal("incorrect first statistics page", err)
	}
	second, err := f.service.StatsPage(ctx, "teacher", "teacher-a", nil, nil, first.Next, 1)
	if err != nil || len(second.Courses) != 1 || second.Courses[0].CourseID != "b-course" || second.Courses[0].Dimensions[0].Level != 5 || second.Dimensions != first.Dimensions {
		t.Fatal("pagination changed equal-course aggregate", err)
	}
}

func input(course, term string, score int) r.Input {
	return r.Input{Kind: "course", TargetID: course, TermID: term, Title: "Useful course", Body: "First line of the course experience.\nSecond line should remain private in previews.", Ratings: [5]int{score, score, score, score, score}}
}
func create(t *testing.T, f *fixtureState, key string, in r.Input) r.Result {
	t.Helper()
	v, e := f.service.Create(context.Background(), student, key, in)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestCourseTermEligibilityAndImmutableTeacherAttribution(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	bad := input("course", "autumn", 4)
	bad.TeacherID = "teacher-c"
	if _, e := f.service.Create(ctx, student, "bad", bad); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("other term teacher accepted")
	}
	v := create(t, f, "all", input("course", "autumn", 4))
	for _, teacher := range []string{"teacher-a", "teacher-b"} {
		s, e := f.service.Stats(ctx, "teacher", teacher, nil, nil)
		if e != nil || s.Dimensions[0].Samples != 1 {
			t.Fatal("co-teacher missing", e)
		}
	}
	s, e := f.service.Stats(ctx, "teacher", "teacher-c", nil, nil)
	if e != nil || s.Dimensions[0].Samples != 0 {
		t.Fatal("cross-term attribution")
	}
	withdrawn, e := f.service.Action(ctx, student, v.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1})
	if e != nil {
		t.Fatal(e)
	}
	changed := input("course", "spring", 5)
	if _, e = f.service.Action(ctx, student, v.ID, "switch", r.Action{Action: "republish", Revision: withdrawn.Revision, Input: &changed}); !errors.Is(e, r.ErrInput) {
		t.Fatal("republish changed attribution")
	}
	f.catalog.missing = true
	if _, e = f.service.Create(ctx, student, "missing-enrollment", input("course", "autumn", 4)); !errors.Is(e, r.ErrEnrollment) {
		t.Fatal("missing evidence allowed")
	}
	mentor := r.Input{Kind: "mentor", TargetID: "teacher-c", Title: "Mentoring", Body: "A freely edited comment without template sections."}
	if _, e = f.service.Create(ctx, student, "mentor", mentor); e != nil {
		t.Fatal("mentor incorrectly requires enrollment", e)
	}
	f.access.optional = true
	if _, e = f.service.Create(ctx, student, "optional", input("course", "autumn", 3)); e != nil {
		t.Fatal(e)
	}
	s, e = f.service.Stats(ctx, "teacher", "teacher-a", nil, nil)
	if e != nil || s.Dimensions[0].Samples != 0 {
		t.Fatal("optional missing evidence invented teachers")
	}
}
func TestAnonymousPreviewAndCurrentAccessCannotLeakIdentity(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	v := create(t, f, "review", input("course", "autumn", 4))
	guest, e := f.service.Get(ctx, r.Principal{}, v.ID)
	if e != nil || !guest.Preview || guest.Title != "" || strings.Contains(guest.Body, "Second line") {
		t.Fatalf("guest preview %+v %v", guest, e)
	}
	b, _ := json.Marshal(guest)
	for _, secret := range []string{"student", "enrollment", "approved", "source_id", "owner"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("private field leaked", secret)
		}
	}
	member, e := f.service.Get(ctx, r.Principal{Subject: "member"}, v.ID)
	if e != nil || member.Title == "" || !member.Preview {
		t.Fatal("member preview", e)
	}
	full, e := f.service.Get(ctx, student, v.ID)
	if e != nil || full.Preview || !strings.Contains(full.Body, "Second line") {
		t.Fatal("full read", e)
	}
	f.access.revoked = true
	after, e := f.service.Get(ctx, student, v.ID)
	if e != nil || !after.Preview {
		t.Fatal("revoked facts reused", e)
	}
	f.access.failed = true
	if _, e = f.service.Get(ctx, student, v.ID); !errors.Is(e, r.ErrUnavailable) {
		t.Fatal("failed dependency became success")
	}
}
func TestConcurrentCreateReplayAndNewReviewsRemainAllowed(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	in := input("course", "autumn", 4)
	var wg sync.WaitGroup
	results := make(chan r.Result, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() { v, e := f.service.Create(ctx, student, "same", in); results <- v; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for v := range results {
		if id != "" && id != v.ID {
			t.Fatal("duplicate effects")
		}
		id = v.ID
	}
	in.Body = "Different payload"
	if _, e := f.service.Create(ctx, student, "same", in); !errors.Is(e, r.ErrConflict) {
		t.Fatal("key reuse accepted")
	}
	create(t, f, "new-review", in)
	list, e := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "course", Limit: 20})
	if e != nil || len(list.Items) != 2 {
		t.Fatalf("multiple reviews: %+v %v", list, e)
	}
}
func TestTeacherEqualCourseWeightsAndBandBoundaries(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	for i := range 9 {
		create(t, f, fmt.Sprintf("many-%d", i), input("course-many", "autumn", 5))
	}
	create(t, f, "one", input("course-one", "spring", 1))
	s, e := f.service.Stats(ctx, "teacher", "teacher-a", nil, nil)
	if e != nil || s.Dimensions[0].Level != 3 || s.Dimensions[0].Samples != 10 || s.Dimensions[0].Courses != 2 {
		t.Fatalf("unequal course weighting: %+v %v", s, e)
	}
	for low := 1; low <= 4; low++ {
		course := fmt.Sprintf("boundary-%d", low)
		create(t, f, course+"a", input(course, "autumn", low))
		create(t, f, course+"b", input(course, "autumn", low+1))
		v, e := f.service.Stats(ctx, "course", course, nil, nil)
		if e != nil || v.Dimensions[0].Level != low+1 {
			t.Fatal("boundary must round upward", v, e)
		}
	}
	create(t, f, "mentor", r.Input{Kind: "mentor", TargetID: "teacher-a", Title: "Guide", Body: "Mentor text"})
	same, e := f.service.Stats(ctx, "teacher", "teacher-a", nil, nil)
	if e != nil || same.Dimensions[0].Samples != 18 {
		t.Fatal("mentor affected score", same, e)
	}
}
func TestWithdrawalWindowExpiryVotesAndPermanentDeletion(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	v := create(t, f, "root", input("course", "autumn", 4))
	reply, e := f.service.Reply(ctx, r.Principal{Subject: "other"}, v.ID, "reply", r.Input{Kind: "reply", TargetID: "course", Body: "Reply"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Action(ctx, r.Principal{Subject: "member"}, v.ID, "vote", r.Action{Action: "vote", Vote: 1}); e != nil {
		t.Fatal(e)
	}
	f.now = f.now.Add(4 * time.Minute)
	withdrawn, e := f.service.Action(ctx, student, v.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1})
	if e != nil || withdrawn.ExpiresAt == nil {
		t.Fatal(e)
	}
	for _, id := range []string{v.ID, reply.ID} {
		if _, e = f.service.Get(ctx, student, id); !errors.Is(e, r.ErrNotFound) {
			t.Fatal("withdrawn discussion visible")
		}
	}
	s, e := f.service.Stats(ctx, "course", "course", nil, nil)
	if e != nil || s.Dimensions[0].Samples != 0 {
		t.Fatal("withdrawn entry counted")
	}
	retry, e := f.service.Action(ctx, student, v.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1})
	if e != nil || !retry.ExpiresAt.Equal(*withdrawn.ExpiresAt) {
		t.Fatal("retry extended expiry")
	}
	f.now = f.now.Add(10 * time.Minute)
	in := input("course", "autumn", 5)
	republished, e := f.service.Action(ctx, student, v.ID, "republish", r.Action{Action: "republish", Revision: withdrawn.Revision, Input: &in})
	if e != nil {
		t.Fatal(e)
	}
	got, e := f.service.Get(ctx, student, v.ID)
	if e != nil || got.Upvotes != 1 {
		t.Fatal("republish lost votes", e)
	}
	if _, e = f.service.Action(ctx, student, v.ID, "again", r.Action{Action: "withdraw", Revision: republished.Revision}); !errors.Is(e, r.ErrWindow) {
		t.Fatal("publish reset window")
	}
	other := create(t, f, "expires", input("course", "spring", 3))
	expired, e := f.service.Action(ctx, student, other.ID, "withdraw-expiry", r.Action{Action: "withdraw", Revision: 1})
	if e != nil {
		t.Fatal(e)
	}
	f.now = *expired.ExpiresAt
	if _, e = f.service.Get(ctx, student, other.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("expired content readable")
	}
	if _, e = f.service.Action(ctx, student, other.ID, "late", r.Action{Action: "republish", Revision: 2, Input: &in}); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("expired draft republished")
	}
	n, e := f.service.Sweep(ctx, 20)
	if e != nil || n != 1 {
		t.Fatal("expiry sweep", n, e)
	}
	n, e = f.service.Sweep(ctx, 20)
	if e != nil || n != 0 {
		t.Fatal("expiry replay", n, e)
	}
	deleted := create(t, f, "delete", input("other-course", "autumn", 4))
	if _, e = f.service.Action(ctx, student, deleted.ID, "delete", r.Action{Action: "delete", Revision: 1}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Get(ctx, student, deleted.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("deleted content readable")
	}
	if e = f.service.Repo.Transaction(ctx, func(tx r.Transaction) error { _, e := tx.Entry(deleted.ID); return e }); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("permanent deletion kept record")
	}
}
func TestDiscussionIdentityVotingDraftConflictsAndModeration(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	v := create(t, f, "root", input("course", "autumn", 4))
	other := r.Principal{Subject: "other"}
	reply, e := f.service.Reply(ctx, other, v.ID, "one", r.Input{Kind: "reply", TargetID: "course", Body: "First", Signed: true})
	if e != nil {
		t.Fatal(e)
	}
	reply2, e := f.service.Reply(ctx, other, v.ID, "two", r.Input{Kind: "reply", TargetID: "course", Body: "Second", Signed: false})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{reply.ID, reply2.ID} {
		got, e := f.service.Get(ctx, student, id)
		if e != nil || got.Author.Anonymous || got.Author.Profile == nil {
			t.Fatal("discussion identity changed", e)
		}
	}
	admin := r.Principal{Subject: "admin"}
	decision := r.Decision{ID: "switch", ObjectID: v.ID, Revision: 1, Action: "identity", Subject: "other", Signed: false, Reason: "privacy request"}
	if _, e = f.service.Apply(ctx, admin, decision); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{reply.ID, reply2.ID} {
		got, e := f.service.Get(ctx, student, id)
		if e != nil || !got.Author.Anonymous || got.Author.Profile != nil {
			t.Fatal("identity switch missed reply", e)
		}
	}
	for i, vote := range []int{1, -1, 0} {
		if _, e = f.service.Action(ctx, other, v.ID, fmt.Sprintf("vote-%d", i), r.Action{Action: "vote", Vote: vote}); e != nil {
			t.Fatal(e)
		}
		got, e := f.service.Get(ctx, other, v.ID)
		if e != nil || got.MyVote != vote || got.Upvotes+got.Downvotes > 1 {
			t.Fatal("vote state", got, e)
		}
	}
	unfinished := r.Input{Kind: "course", TargetID: "course", Body: "Unfinished notes"}
	draft, e := f.service.SaveDraft(ctx, student, "", "draft", unfinished, 0)
	if e != nil {
		t.Fatal(e)
	}
	stored, e := f.service.Drafts(ctx, student, "", 20)
	if e != nil || len(stored) != 1 || stored[0].Input != unfinished {
		t.Fatal("unfinished draft lost", stored, e)
	}
	if _, e = f.service.Create(ctx, student, "unfinished-publish", unfinished); !errors.Is(e, r.ErrInput) {
		t.Fatal("unfinished draft bypassed publish validation", e)
	}
	if _, e = f.service.SaveDraft(ctx, student, draft.ID, "edit", input("course", "autumn", 4), draft.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.SaveDraft(ctx, student, draft.ID, "conflict", input("course", "autumn", 5), draft.Revision); !errors.Is(e, r.ErrConflict) {
		t.Fatal("draft silently overwritten")
	}
	if _, e = f.service.DeleteDraft(ctx, other, draft.ID, "steal", 2); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("other user's draft deleted")
	}
	decision = r.Decision{ID: "hide", ObjectID: v.ID, Revision: 2, Action: "hide", Reason: "reviewing report"}
	if _, e = f.service.Apply(ctx, other, decision); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("unauthorized moderation")
	}
	if _, e = f.service.Apply(ctx, admin, decision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Get(ctx, student, reply.ID); !errors.Is(e, r.ErrNotFound) {
		t.Fatal("hidden root reply visible")
	}
	if _, e = f.service.Apply(ctx, admin, r.Decision{ID: "stale", ObjectID: v.ID, Revision: 1, Action: "restore", Reason: "stale"}); !errors.Is(e, r.ErrConflict) {
		t.Fatal("stale decision applied")
	}
}
func TestStrictHTTPInputsAndUnavailableModerationRollback(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.moderation.failed = true
	if _, e := f.service.Create(ctx, student, "fail", input("course", "autumn", 3)); !errors.Is(e, r.ErrUnavailable) {
		t.Fatal(e)
	}
	list, e := f.service.List(ctx, student, r.Filter{Kind: "course", Limit: 20})
	if e != nil || len(list.Items) != 0 {
		t.Fatal("failed moderation published content")
	}
	h := httpapi.Handler{Service: f.service, Authenticate: func(*http.Request) (r.Principal, error) { return student, nil }}.Routes()
	for _, body := range []string{`{"kind":"course","target_id":"course","term_id":"autumn","title":"T","body":"B","ratings":[1]}`, `{"kind":"course","target_id":"course","term_id":"autumn","title":"T","body":"B","ratings":[1,2,3,4,5],"owner":"admin"}`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/reviews", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "test")
		h.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("malformed input accepted: %d", w.Code)
		}
	}
}
