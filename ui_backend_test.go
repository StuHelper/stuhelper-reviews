package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/StuHelper/stuhelper-reviews/postgres"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/auditquery"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

type summaryCatalog struct {
	orderKeys map[string]int
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
		out[id] = r.SummaryReference{ID: id, CanonicalID: c.aliases[kind][id], SchoolID: target.SchoolID, Kind: kind, Name: id, TermOrderKey: c.orderKeys[id]}
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

func TestSummaryTermsUseCatalogOrderInsteadOfDisplayNames(t *testing.T) {
	f, port := uiReviewFixture(t)
	port.orderKeys = map[string]int{"z-old": 20233, "a-new": 20243}
	for _, term := range []string{"z-old", "a-new", "unknown"} {
		create(t, f, "term-"+term, input("course", term, 3))
	}
	out, err := f.service.ReadSummaries(context.Background(), student, "course", []string{"course"}, true)
	if err != nil || len(out.Items) != 1 {
		t.Fatal(out, err)
	}
	terms := out.Items[0].Terms
	if len(terms) != 3 || terms[0].TermID != "a-new" || terms[0].TermOrderKey != 20243 || terms[1].TermID != "z-old" || terms[2].TermOrderKey != 0 {
		t.Fatal("structured summary order", terms)
	}
}

func TestSummariesUpvoteAndAuditUseIndexesWithoutFullScan(t *testing.T) {
	f, port := uiReviewFixture(t)
	repo, migration := database(t)
	f.service.Repo = repo
	ctx := context.Background()

	// Seed 80 reviews across distinct courses so planner prefers indexes whenever sargable.
	port.aliases["course"] = map[string]string{"c-0-legacy": "c-0"}
	port.aliases["teacher"] = map[string]string{"teacher-b": "teacher-special"}
	var firstID string
	for i := range 80 {
		cid := fmt.Sprintf("c-%d", i)
		in := input(cid, "autumn", (i%5)+1)
		if i == 0 {
			in.TargetID = "c-0-legacy"
			in.TeacherID = "teacher-b"
		} else {
			in.TeacherID = "teacher-a"
		}
		p := r.Principal{Subject: fmt.Sprintf("author-%d", i)}
		res, err := f.service.Create(ctx, p, fmt.Sprintf("seed-%d", i), in)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstID = res.ID
		}
	}

	// Verify sync_entry_upvotes trigger across upvote (+1), flip to downvote (-1), flip back (+1), cancel (0), and account closure.
	voter1 := r.Principal{Subject: "v-1"}
	voter2 := r.Principal{Subject: "v-2"}
	if _, err := f.service.Action(ctx, voter1, firstID, "v1-up", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Action(ctx, voter2, firstID, "v2-up", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	gotView, err := f.service.Get(ctx, student, firstID)
	if err != nil || gotView.Upvotes != 2 {
		t.Fatalf("expected 2 upvotes, got %+v err=%v", gotView, err)
	}
	if _, err := f.service.Action(ctx, voter1, firstID, "v1-down", r.Action{Action: "vote", Vote: -1}); err != nil {
		t.Fatal(err)
	}
	gotView, err = f.service.Get(ctx, student, firstID)
	if err != nil || gotView.Upvotes != 1 || gotView.Downvotes != 1 {
		t.Fatalf("expected 1 upvote and 1 downvote after flip, got %+v err=%v", gotView, err)
	}
	if _, err := f.service.Action(ctx, voter1, firstID, "v1-cancel", r.Action{Action: "vote", Vote: 0}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnonymizeOwner(ctx, voter2.Subject, "55555555-5555-4555-8555-555555555555"); err != nil {
		t.Fatal(err)
	}

	admin := r.Principal{Subject: "admin"}
	if _, err := f.service.AdminGet(ctx, admin, firstID); err != nil {
		t.Fatal(err)
	}

	repo.DB.SetMaxOpenConns(1)
	if _, err := migration.Exec(`ANALYZE reviews.entries, reviews.votes, reviews.audit; SELECT pg_stat_reset();`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB.Exec(`SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}

	indexScans := func(indexName string) int64 {
		t.Helper()
		if _, err := repo.DB.Exec(`SELECT pg_stat_force_next_flush()`); err != nil {
			t.Fatal(err)
		}
		var n int64
		if err := migration.QueryRow(`SELECT COALESCE(idx_scan, 0) FROM pg_stat_user_indexes WHERE schemaname='reviews' AND indexrelname=$1`, indexName).Scan(&n); err != nil {
			return 0
		}
		return n
	}

	// A-03: Course summaries hit reviews_listing.
	beforeListing := indexScans("reviews_listing")
	sumCourse, err := f.service.ReadSummaries(ctx, student, "course", []string{"c-0"}, false)
	if err != nil || len(sumCourse.Items) != 1 || sumCourse.Items[0].ReviewCount != 1 {
		t.Fatalf("unexpected course summary: %+v err=%v", sumCourse, err)
	}
	if afterListing := indexScans("reviews_listing"); afterListing <= beforeListing {
		t.Fatalf("ReadSummaries(course) failed to use reviews_listing index (before=%d after=%d)", beforeListing, afterListing)
	}

	// A-03: Teacher summaries hit reviews_teachers GIN index.
	beforeTeachers := indexScans("reviews_teachers")
	sumTeacher, err := f.service.ReadSummaries(ctx, student, "teacher", []string{"teacher-special"}, false)
	if err != nil || len(sumTeacher.Items) != 1 || sumTeacher.Items[0].ReviewCount != 1 {
		t.Fatalf("unexpected teacher summary: %+v err=%v", sumTeacher, err)
	}
	if afterTeachers := indexScans("reviews_teachers"); afterTeachers <= beforeTeachers {
		t.Fatalf("ReadSummaries(teacher) failed to use reviews_teachers GIN index (before=%d after=%d)", beforeTeachers, afterTeachers)
	}

	// A-14.1: School-wide and target-scoped sort=upvotes hit composite upvote indexes.
	beforeUpSchool := indexScans("reviews_upvotes_school")
	topSchool, err := f.service.List(ctx, student, r.Filter{Sort: "upvotes", Limit: 5})
	if err != nil || len(topSchool.Items) != 5 || topSchool.Items[0].ID != firstID || topSchool.Items[0].Upvotes != 1 {
		t.Fatalf("unexpected school-wide top reviews: %+v err=%v", topSchool, err)
	}
	if afterUpSchool := indexScans("reviews_upvotes_school"); afterUpSchool <= beforeUpSchool {
		t.Fatalf("List(sort=upvotes) school-wide failed to use reviews_upvotes_school (before=%d after=%d)", beforeUpSchool, afterUpSchool)
	}

	beforeUpTarget := indexScans("reviews_upvotes_target")
	topCourse, err := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "c-0-legacy", Sort: "upvotes", Limit: 5})
	if err != nil || len(topCourse.Items) != 1 || topCourse.Items[0].ID != firstID {
		t.Fatalf("unexpected target top reviews: %+v err=%v", topCourse, err)
	}
	if afterUpTarget := indexScans("reviews_upvotes_target"); afterUpTarget <= beforeUpTarget {
		t.Fatalf("List(sort=upvotes) target failed to use reviews_upvotes_target (before=%d after=%d)", beforeUpTarget, afterUpTarget)
	}

	// A-14.2: ReadAudit by Actor and by Target hit dedicated indexes.
	scopes := []auditquery.Scope{{School: "*", Group: "*", Module: "reviews"}}
	beforeAuditActor := indexScans("audit_actor_order")
	auditByActor, err := repo.ReadAudit(ctx, auditquery.Request{Scopes: scopes, Actor: admin.Subject, Until: f.now.Add(time.Hour), Limit: 10})
	if err != nil || len(auditByActor.Records) == 0 {
		t.Fatalf("ReadAudit by Actor failed: %+v err=%v", auditByActor, err)
	}
	if afterAuditActor := indexScans("audit_actor_order"); afterAuditActor <= beforeAuditActor {
		t.Fatalf("ReadAudit(Actor) failed to use audit_actor_order (before=%d after=%d)", beforeAuditActor, afterAuditActor)
	}

	beforeAuditTarget := indexScans("audit_target_order")
	auditByTarget, err := repo.ReadAudit(ctx, auditquery.Request{Scopes: scopes, Target: "entry/" + firstID, Until: f.now.Add(time.Hour), Limit: 10})
	if err != nil || len(auditByTarget.Records) == 0 {
		t.Fatalf("ReadAudit by Target failed: %+v err=%v", auditByTarget, err)
	}
	if afterAuditTarget := indexScans("audit_target_order"); afterAuditTarget <= beforeAuditTarget {
		t.Fatalf("ReadAudit(Target) failed to use audit_target_order (before=%d after=%d)", beforeAuditTarget, afterAuditTarget)
	}

	// A-14.3: Bounded PruneReplays and PruneOutbox only remove expired/confirmed rows.
	if _, err := repo.PruneReplays(ctx, time.Time{}, 10); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput on zero time PruneReplays, got %v", err)
	}
	if _, err := repo.PruneOutbox(ctx, f.now, 1001); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput on over-limit PruneOutbox, got %v", err)
	}
	prunedBeforeExpiry, err := repo.PruneReplays(ctx, f.now.Add(time.Hour), 50)
	if err != nil || prunedBeforeExpiry != 0 {
		t.Fatalf("active replays pruned prematurely: n=%d err=%v", prunedBeforeExpiry, err)
	}
	prunedAfterExpiry, err := repo.PruneReplays(ctx, f.now.Add(25*time.Hour), 25)
	if err != nil || prunedAfterExpiry != 25 {
		t.Fatalf("expected bounded 25 expired replays pruned, got n=%d err=%v", prunedAfterExpiry, err)
	}

	pendingOutbox, err := repo.PendingOutbox(ctx, 10, nil)
	if err != nil || len(pendingOutbox) < 4 {
		t.Fatalf("expected pending outbox events, got %d err=%v", len(pendingOutbox), err)
	}
	for _, ev := range pendingOutbox[:3] {
		if err := repo.ConfirmOutbox(ctx, ev.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Unconfirmed rows must never be deleted by PruneOutbox; confirmed rows older than cutoff are boundedly deleted.
	prunedOutbox, err := outboxPruner(t, migration).PruneOutbox(ctx, time.Now().UTC().Add(time.Minute), 2)
	if err != nil || prunedOutbox != 2 {
		t.Fatalf("expected bounded 2 confirmed outbox rows pruned, got n=%d err=%v", prunedOutbox, err)
	}
	stillPending, err := repo.PendingOutbox(ctx, 100, nil)
	if err != nil || len(stillPending) == 0 {
		t.Fatalf("unconfirmed outbox events were lost: %d err=%v", len(stillPending), err)
	}
}

func TestExistingVotesAndReplayDeadlinesSurviveUpgrade(t *testing.T) {
	f := fixture(t)
	repo, db := database(t)
	f.service.Repo = repo
	ctx := context.Background()
	entry := create(t, f, "old", input("course", "autumn", 4))
	if _, err := f.service.Action(ctx, r.Principal{Subject: "voter"}, entry.ID, "old", r.Action{Action: "vote", Vote: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER sync_entry_upvotes ON reviews.votes; ALTER TABLE reviews.entries DROP COLUMN upvotes; ALTER TABLE reviews.replays DROP COLUMN expires_at`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.Exec(postgres.Schema); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.service.List(ctx, student, r.Filter{Sort: "upvotes", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Upvotes != 1 {
		t.Errorf("legacy counted vote: %+v %v", page, err)
	}
	n, err := repo.PruneReplays(ctx, f.now.Add(25*time.Hour), 10)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
}

type summaryQueryRecorder struct {
	r.Repository
	seen r.SummaryQuery
}
type summaryRecorderTx struct {
	r.Transaction
	r.SummaryTransaction
	rec *summaryQueryRecorder
}

func (t summaryRecorderTx) SummaryAggregates(q r.SummaryQuery) ([]r.SummaryAggregate, error) {
	t.rec.seen = q
	return t.SummaryTransaction.SummaryAggregates(q)
}
func (rec *summaryQueryRecorder) Transaction(ctx context.Context, fn func(r.Transaction) error) error {
	return rec.Repository.Transaction(ctx, func(t r.Transaction) error { return fn(summaryRecorderTx{t, t.(r.SummaryTransaction), rec}) })
}

func TestTeacherSummaryBindsOnlyRequestedAliases(t *testing.T) {
	f, port := uiReviewFixture(t)
	ctx := context.Background()
	create(t, f, "old", input("old-course", "old-term", 1))
	create(t, f, "new", input("new-course", "autumn", 5))
	port.aliases = map[string]map[string]string{"course": {"old-course": "new-course", "other-course": "other"}, "term": {"old-term": "autumn"}, "teacher": {"old-a": "teacher-a", "other-old": "teacher-b"}}
	rec := &summaryQueryRecorder{Repository: f.service.Repo}
	f.service.Repo = rec
	for _, charts := range []bool{false, true} {
		result, err := f.service.ReadSummaries(ctx, student, "teacher", []string{"teacher-a"}, charts)
		if err != nil || len(result.Items) != 1 || result.Items[0].Dimensions[0].Courses != 1 || result.Items[0].ReviewCount != 2 {
			t.Fatal(result, err)
		}
		if charts && (len(result.Items[0].Terms) != 1 || result.Items[0].Terms[0].TermID != "autumn") {
			t.Fatal(result)
		}
		if len(rec.seen.TargetAliases) != 1 || len(rec.seen.CourseAliases) != 0 || len(rec.seen.TermAliases) != 0 {
			t.Error("bound unrelated alias dictionaries to targeted SQL")
		}
	}
}

type boundedSummaryCatalog struct{ *summaryCatalog }

func (c boundedSummaryCatalog) EquivalentIDs(_ context.Context, kind, id string) ([]string, error) {
	out := []string{id}
	for alias, canonical := range c.aliases[kind] {
		if canonical == id {
			out = append(out, alias)
		}
	}
	return out, nil
}
func (c boundedSummaryCatalog) CanonicalCourses(_ context.Context, ids []string) (map[string]string, error) {
	result := map[string]string{}
	for _, id := range ids {
		result[id] = id
		if mapped := c.aliases["course"][id]; mapped != "" {
			result[id] = mapped
		}
	}
	return result, nil
}
func (c boundedSummaryCatalog) SummaryAliases(context.Context, string, string) (map[string]string, error) {
	return nil, r.ErrUnavailable
}
func TestTargetedSummaryUsesScopedCatalogLookups(t *testing.T) {
	f, port := uiReviewFixture(t)
	ctx := context.Background()
	create(t, f, "old-c", input("old-course", "old-term", 4))
	port.aliases = map[string]map[string]string{"teacher": {"teacher-a": "new-a"}, "course": {"old-course": "new-course"}, "term": {"old-term": "new-term"}}
	f.service.Catalog = boundedSummaryCatalog{port}
	for _, tc := range []struct{ kind, id string }{{"teacher", "new-a"}, {"course", "new-course"}} {
		result, err := f.service.ReadSummaries(ctx, student, tc.kind, []string{tc.id}, true)
		if err != nil || len(result.Items) != 1 || result.Items[0].ReviewCount != 1 || len(result.Items[0].Terms) != 1 || result.Items[0].Terms[0].TermID != "new-term" {
			t.Fatal(result, err)
		}
	}
}

func TestOwnOverviewKeepsCountsOnAliasFailure(t *testing.T) {
	f, port := uiReviewFixture(t)
	ctx := context.Background()
	// 3. A-15.2: OwnOverview preserves review_count and draft_count when SummaryAliases fails.
	create(t, f, "ov-mine", input("course", "autumn", 4))
	if _, err := f.service.SaveDraft(ctx, student, "", "ov-draft", input("other", "autumn", 3), 0); err != nil {
		t.Fatal(err)
	}
	port.items = []r.PendingCourse{{CourseID: "pending-c", CourseName: "Pending", TermID: "autumn", TermName: "Autumn"}}
	port.failed = true
	overview, err := f.service.OwnOverview(ctx, student)
	if err != nil || overview.ReviewCount != 1 || overview.DraftCount != 1 || overview.PendingCourses.State != "unavailable" {
		t.Fatalf("OwnOverview discarded local counts on alias outage: %+v err=%v", overview, err)
	}

	port.failed = false
	port.ownErr = context.DeadlineExceeded
	overview, err = f.service.OwnOverview(ctx, student)
	if err != nil || overview.ReviewCount != 1 || overview.DraftCount != 1 || overview.PendingCourses.Count != nil || overview.PendingCourses.State != "unavailable" {
		t.Fatal(overview, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = f.service.OwnOverview(canceled, student); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

}
