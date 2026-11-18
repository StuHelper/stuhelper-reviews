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
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/reviews/admin/entries?school_id=school&include_moderation_details=true&"+query, nil))
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
	unauthReq.Header.Set("Content-Type", "application/json")
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
	overLimitReq.Header.Set("Content-Type", "application/json")
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
	batchReq.Header.Set("Content-Type", "application/json")
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
	replayReq.Header.Set("Content-Type", "application/json")
	replayReq.Header.Set("Idempotency-Key", "batch-1")
	h.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != 200 || replayRec.Body.String() != batchRec.Body.String() {
		t.Fatalf("batch replay mismatch: %d %s", replayRec.Code, replayRec.Body.String())
	}
	conflictRec := httptest.NewRecorder()
	conflictReq := httptest.NewRequest("POST", "/api/v1/reviews/admin/entries/batch-approve", strings.NewReader(`{"school_id":"school","reason":"different","items":[{"object_id":"missing-entry","revision":1}]}`))
	conflictReq.Header.Set("Content-Type", "application/json")
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

type clockOnDiscussionLock struct {
	r.Repository
	root    string
	advance func()
}

func (repo clockOnDiscussionLock) Transaction(ctx context.Context, fn func(r.Transaction) error) error {
	return repo.Repository.Transaction(ctx, func(tx r.Transaction) error {
		return fn(clockOnLockTx{tx, repo.root, repo.advance})
	})
}

type clockOnLockTx struct {
	r.Transaction
	root    string
	advance func()
}

func (tx clockOnLockTx) Lock(key string) error {
	if err := tx.Transaction.Lock(key); err != nil {
		return err
	}
	if key == tx.root {
		tx.advance()
	}
	return nil
}

func TestBatchApproveChecksExpiryAfterDiscussionLock(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	root := create(t, f, "batch-expiry", input("course", "autumn", 4))
	f.moderation.outcome = "pending"
	reply, err := f.service.Reply(ctx, r.Principal{Subject: "reader"}, root.ID, "pending-reply", r.Input{Kind: "reply", TargetID: "course", Body: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	withdrawn, err := f.service.Action(ctx, student, root.ID, "withdraw", r.Action{Action: "withdraw", Revision: root.Revision})
	if err != nil || withdrawn.ExpiresAt == nil {
		t.Fatalf("withdraw: %+v %v", withdrawn, err)
	}
	in := input("course", "autumn", 4)
	pending, err := f.service.Action(ctx, student, root.ID, "republish", r.Action{Action: "republish", Revision: withdrawn.Revision, Input: &in})
	if err != nil || pending.State != "pending" {
		t.Fatalf("republish: %+v %v", pending, err)
	}
	f.now = withdrawn.ExpiresAt.Add(-time.Second)
	repo := f.service.Repo
	f.service.Repo = clockOnDiscussionLock{repo, root.ID, func() { f.now = *withdrawn.ExpiresAt }}
	req := r.AdminBatchApproveRequest{SchoolID: "school", Reason: "eligible", Items: []r.AdminBatchItem{{ObjectID: root.ID, Revision: pending.Revision}, {ObjectID: reply.ID, Revision: reply.Revision}}}
	result, err := f.service.BatchApprove(ctx, r.Principal{Subject: "admin"}, "expires-on-lock", req)
	if err != nil || len(result.Items) != 2 {
		t.Fatalf("batch: %+v %v", result, err)
	}
	for _, item := range result.Items {
		if item.Status != "not_found" {
			t.Fatalf("expired result: %+v", item)
		}
	}
	err = repo.Transaction(ctx, func(tx r.Transaction) error {
		for _, item := range req.Items {
			entry, err := tx.Entry(item.ObjectID)
			if err != nil {
				return err
			}
			if entry.State != "pending" || entry.Revision != item.Revision {
				t.Fatalf("changed expired entry: %s rev %d", entry.State, entry.Revision)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentBatchApprovalsShareOwnersAndActivity(t *testing.T) {
	f := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f.moderation.outcome = "pending"
	batches := make([]r.AdminBatchApproveRequest, 2)
	for batch := range batches {
		req := r.AdminBatchApproveRequest{SchoolID: "school", Reason: "approved"}
		for i := range 4 {
			n := i
			if batch == 1 {
				n = 3 - i
			}
			owner := r.Principal{Subject: fmt.Sprintf("author-%d", n%2)}
			entry, err := f.service.Create(ctx, owner, fmt.Sprintf("batch-%d-item-%d", batch, i), input(fmt.Sprintf("course-%d", n), "autumn", 4))
			if err != nil {
				t.Fatal(err)
			}
			req.Items = append(req.Items, r.AdminBatchItem{ObjectID: entry.ID, Revision: entry.Revision})
		}
		batches[batch] = req
	}
	start := make(chan struct{})
	finished := make(chan error, len(batches))
	for i, req := range batches {
		go func() {
			<-start
			out, err := f.service.BatchApprove(ctx, r.Principal{Subject: "admin"}, fmt.Sprintf("approve-%d", i), req)
			if err == nil && len(out.Items) != len(req.Items) {
				err = fmt.Errorf("batch %d returned %d items", i, len(out.Items))
			}
			for _, item := range out.Items {
				if item.Status != "approved" {
					err = fmt.Errorf("batch %d: %+v", i, item)
				}
			}
			finished <- err
		}()
	}
	close(start)
	for range batches {
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}
	for _, req := range batches {
		for _, item := range req.Items {
			view, err := f.service.Get(ctx, student, item.ObjectID)
			if err != nil || view.State != "published" || view.Revision != item.Revision+1 {
				t.Fatalf("entry after batch: %+v %v", view, err)
			}
		}
	}
}

func TestHTTPModerationDetailsRequireExplicitSelection(t *testing.T) {
	f := fixture(t)
	f.moderation.outcome = "pending"
	entry := create(t, f, "explicit-details", input("course", "autumn", 4))
	actor := student
	h := httpapi.Handler{Service: f.service, Authenticate: func(*http.Request) (r.Principal, error) { return actor, nil }}.Routes()
	for _, tc := range []struct {
		path  string
		admin bool
	}{
		{"/api/v1/reviews/me/entries/" + entry.ID, false},
		{"/api/v1/reviews/me/entries?limit=5", false},
		{"/api/v1/reviews/admin/entries/" + entry.ID, true},
		{"/api/v1/reviews/admin/entries?school_id=school", true},
		{"/api/v1/reviews/admin/entries/" + entry.ID + "/history", true},
	} {
		if tc.admin {
			actor = r.Principal{Subject: "admin"}
		}
		for _, selected := range []bool{false, true} {
			req := httptest.NewRequest("GET", tc.path, nil)
			if selected {
				q := req.URL.Query()
				q.Set("include_moderation_details", "true")
				req.URL.RawQuery = q.Encode()
			}
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != 200 {
				t.Fatalf("%s: %d %s", tc.path, res.Code, res.Body.String())
			}
			if has := strings.Contains(res.Body.String(), `"moderation_reason_code":"pending_review"`); has != selected {
				t.Fatalf("%s selection %v returned reason %v", tc.path, selected, has)
			}
			if tc.admin && strings.Contains(res.Body.String(), `"updated_at"`) != selected {
				t.Fatalf("%s selection %v unexpected update time", tc.path, selected)
			}
		}
	}
}
