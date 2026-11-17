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
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

func TestAuthorModerationReasonCodesAdminFiltersAndBatchApprove(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	admin := r.Principal{Subject: "admin"}

	// 1. Pending entry gets pending_review reason code in owner views only.
	f.now = f.now.Add(time.Minute)
	f.moderation.outcome = "pending"
	pending := create(t, f, "mod-pending", input("course", "autumn", 4))

	ownPending, err := f.service.GetOwn(ctx, student, pending.ID)
	if err != nil || ownPending.State != "pending" || ownPending.ModerationReasonCode != "pending_review" {
		t.Fatalf("expected owner pending_review code, got %+v %v", ownPending, err)
	}

	// 2. Post-publication review ("review") sets ReviewRequested=true while publicly visible without reason code.
	f.now = f.now.Add(time.Minute)
	f.moderation.outcome = "review"
	postReview := create(t, f, "mod-review", input("course", "autumn", 5))
	publicView, err := f.service.Get(ctx, r.Principal{Subject: "reader"}, postReview.ID)
	if err != nil || publicView.ModerationReasonCode != "" {
		t.Fatalf("public view must not expose moderation_reason_code: %+v %v", publicView, err)
	}

	// 3. Ordinary published entry + child reply + featured state + hide with structured ReasonCode vs confidential Reason.
	f.now = f.now.Add(time.Minute)
	f.moderation.outcome = "published"
	otherCourse := create(t, f, "mod-other", input("other-course", "autumn", 3))
	f.now = f.now.Add(time.Minute)
	childReply, err := f.service.Reply(ctx, r.Principal{Subject: "replier"}, otherCourse.ID, "mod-reply", r.Input{Kind: "reply", TargetID: "other-course", Body: "Reply on other-course"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Apply(ctx, admin, r.Decision{ID: "feat-other", ObjectID: otherCourse.ID, Revision: otherCourse.Revision, Action: "feature", Reason: "featured"}); err != nil {
		t.Fatal(err)
	}
	// Reject unknown reason_code.
	if _, err := f.service.Apply(ctx, admin, r.Decision{ID: "bad-code", ObjectID: otherCourse.ID, Revision: 2, Action: "hide", Reason: "note", ReasonCode: "unknown_code"}); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput for invalid reason_code, got %v", err)
	}

	f.now = f.now.Add(time.Minute)
	confidentialNote := "Internal ticket #9482 reported by anonymous informant"
	hiddenRes, err := f.service.Apply(ctx, admin, r.Decision{
		ID:         "hide-with-code",
		ObjectID:   otherCourse.ID,
		Revision:   2,
		Action:     "hide",
		Reason:     confidentialNote,
		ReasonCode: "privacy_leak",
	})
	if err != nil {
		t.Fatal(err)
	}

	ownHidden, err := f.service.GetOwn(ctx, student, otherCourse.ID)
	if err != nil || ownHidden.State != "hidden" || ownHidden.ModerationReasonCode != "privacy_leak" {
		t.Fatalf("expected privacy_leak reason code for owner, got %+v %v", ownHidden, err)
	}
	rawOwn, _ := json.Marshal(ownHidden)
	if strings.Contains(string(rawOwn), confidentialNote) || strings.Contains(string(rawOwn), "9482") {
		t.Fatalf("confidential moderator note leaked to author: %s", string(rawOwn))
	}
	minePage, err := f.service.List(ctx, student, r.Filter{Owner: student.Subject, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	foundHidden := false
	for _, item := range minePage.Items {
		if item.ID == otherCourse.ID && item.ModerationReasonCode == "privacy_leak" {
			foundHidden = true
		}
	}
	if !foundHidden {
		t.Fatalf("mine list missing moderation_reason_code: %+v", minePage.Items)
	}

	// Restore clears moderation_reason_code.
	restored, err := f.service.Apply(ctx, admin, r.Decision{
		ID:       "restore-other",
		ObjectID: otherCourse.ID,
		Revision: hiddenRes.Revision,
		Action:   "restore",
		Reason:   "redacted personal info",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = restored
	ownRestored, err := f.service.GetOwn(ctx, student, otherCourse.ID)
	if err != nil || ownRestored.ModerationReasonCode != "" {
		t.Fatalf("restore should clear moderation_reason_code, got %+v %v", ownRestored, err)
	}

	// 4. AdminEntry fields (parent_id, updated_at, review_requested) & HTTP AdminList filters/sorting.
	replyAdmin, err := f.service.AdminGet(ctx, admin, childReply.ID)
	if err != nil || replyAdmin.ParentID != otherCourse.ID || replyAdmin.UpdatedAt == nil {
		t.Fatalf("admin entry missing parent_id or updated_at: %+v %v", replyAdmin, err)
	}

	currentCaller := admin
	h := httpapi.Handler{
		Service:      f.service,
		Authenticate: func(*http.Request) (r.Principal, error) { return currentCaller, nil },
	}.Routes()

	getAdmin := func(query string) r.AdminPage {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/reviews/admin/entries?school_id=school&"+query, nil))
		if rec.Code != 200 {
			t.Fatalf("admin query %s failed: %d %s", query, rec.Code, rec.Body.String())
		}
		var page r.AdminPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	// Filter review_requested=true sorted newest first: postReview then pending.
	reqQueue := getAdmin("review_requested=true&sort=newest&limit=1")
	if len(reqQueue.Items) != 1 || reqQueue.Items[0].ID != postReview.ID || !reqQueue.Items[0].ReviewRequested || reqQueue.Next == "" {
		t.Fatalf("unexpected first review_requested page: %+v", reqQueue)
	}
	reqQueue2 := getAdmin("review_requested=true&sort=newest&limit=1&after=" + reqQueue.Next)
	if len(reqQueue2.Items) != 1 || reqQueue2.Items[0].ID != pending.ID || !reqQueue2.Items[0].ReviewRequested || reqQueue2.Items[0].ModerationReasonCode != "pending_review" {
		t.Fatalf("unexpected second review_requested page: %+v", reqQueue2)
	}
	// Cursor bound to filter scope: changing featured rejects cursor.
	badCursorRec := httptest.NewRecorder()
	h.ServeHTTP(badCursorRec, httptest.NewRequest("GET", "/api/v1/reviews/admin/entries?school_id=school&review_requested=true&featured=true&sort=newest&after="+reqQueue.Next, nil))
	if badCursorRec.Code != 400 {
		t.Fatalf("expected 400 on cross-filter admin cursor, got %d", badCursorRec.Code)
	}

	// Filter by target_id, root_id, featured.
	byRoot := getAdmin("root_id=" + otherCourse.ID + "&sort=oldest")
	if len(byRoot.Items) != 2 || !slices.Equal([]string{byRoot.Items[0].ID, byRoot.Items[1].ID}, []string{otherCourse.ID, childReply.ID}) {
		t.Fatalf("root_id filter mismatch: %+v", byRoot)
	}
	byFeatured := getAdmin("featured=true&target_id=other-course")
	if len(byFeatured.Items) != 1 || byFeatured.Items[0].ID != otherCourse.ID {
		t.Fatalf("featured+target_id filter mismatch: %+v", byFeatured)
	}

	// 5. Batch approve via HTTP: cross-school entry, stale revision, missing entry, pending entry, and post-publication review_requested entry.
	f.catalog.schools = map[string]string{"foreign-course": "foreign-school", "foreign-term": "foreign-school"}
	foreignIn := input("foreign-course", "foreign-term", 4)
	f.moderation.outcome = "pending"
	foreignPending := create(t, f, "foreign-pending", foreignIn)

	// Reject >20 items and non-admin caller.
	currentCaller = student
	unauthRec := httptest.NewRecorder()
	unauthReq := httptest.NewRequest("POST", "/api/v1/reviews/admin/entries/batch-approve", strings.NewReader(`{"school_id":"school","reason":"batch","items":[{"object_id":"x","revision":1}]}`))
	unauthReq.Header.Set("Idempotency-Key", "unauth-batch")
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != 403 {
		t.Fatalf("expected 403 for student batch approve, got %d", unauthRec.Code)
	}
	currentCaller = admin

	tooMany := make([]string, 0, 21)
	for i := range 21 {
		tooMany = append(tooMany, fmt.Sprintf(`{"object_id":"id-%d","revision":1}`, i))
	}
	overLimitRec := httptest.NewRecorder()
	overLimitReq := httptest.NewRequest("POST", "/api/v1/reviews/admin/entries/batch-approve", strings.NewReader(`{"school_id":"school","reason":"batch","items":[`+strings.Join(tooMany, ",")+`]}`))
	overLimitReq.Header.Set("Idempotency-Key", "over-limit")
	h.ServeHTTP(overLimitRec, overLimitReq)
	if overLimitRec.Code != 400 {
		t.Fatalf("expected 400 for >20 batch items, got %d", overLimitRec.Code)
	}

	batchPayload := fmt.Sprintf(`{"school_id":"school","reason":"巡检批量放行","items":[
		{"object_id":%q,"revision":%d},
		{"object_id":%q,"revision":%d},
		{"object_id":%q,"revision":999},
		{"object_id":"missing-entry","revision":1},
		{"object_id":%q,"revision":%d}
	]}`, pending.ID, pending.Revision, postReview.ID, postReview.Revision, otherCourse.ID, foreignPending.ID, foreignPending.Revision)

	batchRec := httptest.NewRecorder()
	batchReq := httptest.NewRequest("POST", "/api/v1/reviews/admin/entries/batch-approve", strings.NewReader(batchPayload))
	batchReq.Header.Set("Idempotency-Key", "batch-1")
	h.ServeHTTP(batchRec, batchReq)
	if batchRec.Code != 200 {
		t.Fatalf("batch approve failed: %d %s", batchRec.Code, batchRec.Body.String())
	}
	var batchOut r.AdminBatchApproveResponse
	if err := json.Unmarshal(batchRec.Body.Bytes(), &batchOut); err != nil || len(batchOut.Items) != 5 {
		t.Fatalf("unexpected batch response: %+v %v", batchOut, err)
	}
	gotStatuses := []string{batchOut.Items[0].Status, batchOut.Items[1].Status, batchOut.Items[2].Status, batchOut.Items[3].Status, batchOut.Items[4].Status}
	wantStatuses := []string{"approved", "approved", "conflict", "not_found", "forbidden"}
	if !slices.Equal(gotStatuses, wantStatuses) {
		t.Fatalf("batch statuses mismatch: got %v want %v", gotStatuses, wantStatuses)
	}

	// Idempotent replay returns exact same response; modified payload conflicts.
	replayRec := httptest.NewRecorder()
	replayReq := httptest.NewRequest("POST", "/api/v1/reviews/admin/entries/batch-approve", strings.NewReader(batchPayload))
	replayReq.Header.Set("Idempotency-Key", "batch-1")
	h.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != 200 || replayRec.Body.String() != batchRec.Body.String() {
		t.Fatalf("batch replay mismatch: %d %s", replayRec.Code, replayRec.Body.String())
	}
	conflictRec := httptest.NewRecorder()
	conflictReq := httptest.NewRequest("POST", "/api/v1/reviews/admin/entries/batch-approve", strings.NewReader(`{"school_id":"school","reason":"different","items":[{"object_id":"missing-entry","revision":1}]}`))
	conflictReq.Header.Set("Idempotency-Key", "batch-1")
	h.ServeHTTP(conflictRec, conflictReq)
	if conflictRec.Code != 409 {
		t.Fatalf("expected 409 on batch idempotency conflict, got %d", conflictRec.Code)
	}

	// Review queue is now empty for school, and pending entry is publicly visible with cleared reason code.
	emptyQueue := getAdmin("review_requested=true")
	if len(emptyQueue.Items) != 0 {
		t.Fatalf("expected empty review_requested queue after batch approve, got %+v", emptyQueue)
	}
	nowPublic, err := f.service.Get(ctx, r.Principal{Subject: "reader"}, pending.ID)
	if err != nil || nowPublic.State != "published" {
		t.Fatalf("batch-approved pending entry not public: %+v %v", nowPublic, err)
	}
	ownNowPublic, err := f.service.GetOwn(ctx, student, pending.ID)
	if err != nil || ownNowPublic.ModerationReasonCode != "" {
		t.Fatalf("approved entry kept moderation_reason_code: %+v %v", ownNowPublic, err)
	}
}
