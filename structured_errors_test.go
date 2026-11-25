package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

type httpErrorPayload struct {
	Code            string `json:"code"`
	Subcode         string `json:"subcode"`
	Field           string `json:"field"`
	CurrentRevision *int64 `json:"current_revision"`
}

func TestStructuredErrorSubcodesFieldsAndConflictRevisions(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	currentCaller := student
	h := httpapi.Handler{
		Service:      f.service,
		Authenticate: func(*http.Request) (r.Principal, error) { return currentCaller, nil },
	}.Routes()

	send := func(method, path, body, key string) (int, httpErrorPayload) {
		t.Helper()
		var reader *strings.Reader
		if body != "" {
			reader = strings.NewReader(body)
		} else {
			reader = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, reader)
		if reader != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out httpErrorPayload
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode error payload (%d %s): %v", rec.Code, rec.Body.String(), err)
		}
		return rec.Code, out
	}

	// 1. Unverified campus admission -> 403 forbidden + admission_required.
	currentCaller = r.Principal{Subject: "member"}
	status, payload := send("GET", "/api/v1/reviews?limit=20", "", "")
	if status != 403 || payload.Code != "forbidden" || payload.Subcode != "admission_required" {
		t.Fatalf("expected admission_required, got %d %+v", status, payload)
	}
	currentCaller = student

	// 3. Invalid ratings -> 400 invalid_input + invalid_ratings + field=ratings.
	status, payload = send("POST", "/api/v1/reviews", `{"kind":"course","target_id":"course","term_id":"autumn","title":"Course","body":"Body","ratings":[0,5,5,5,5],"signed":false}`, "zero-rating")
	if status != 400 || payload.Code != "invalid_input" || payload.Subcode != "invalid_ratings" || payload.Field != "ratings" {
		t.Fatalf("expected invalid_ratings on ratings, got %d %+v", status, payload)
	}

	// 4. Teacher not in caller's enrollment -> 403 forbidden + teacher_not_enrolled + field=teacher_id.
	status, payload = send("POST", "/api/v1/reviews", `{"kind":"course","target_id":"course","term_id":"autumn","teacher_id":"teacher-c","title":"Course","body":"Body","ratings":[4,4,4,4,4],"signed":false}`, "wrong-teacher")
	if status != 403 || payload.Code != "forbidden" || payload.Subcode != "teacher_not_enrolled" || payload.Field != "teacher_id" {
		t.Fatalf("expected teacher_not_enrolled on teacher_id, got %d %+v", status, payload)
	}

	// 5. Automated moderation block -> 403 forbidden + moderation_blocked + field=body (and errors.Is preserves ErrForbidden).
	f.moderation.outcome = "blocked"
	_, directErr := f.service.Create(ctx, student, "direct-blocked", input("course", "autumn", 4))
	if !errors.Is(directErr, r.ErrForbidden) {
		t.Fatalf("errors.Is compatibility broken: %v", directErr)
	}
	status, payload = send("POST", "/api/v1/reviews", `{"kind":"course","target_id":"course","term_id":"autumn","title":"Course","body":"Blocked","ratings":[4,4,4,4,4],"signed":false}`, "http-blocked")
	if status != 403 || payload.Code != "forbidden" || payload.Subcode != "moderation_blocked" || payload.Field != "body" {
		t.Fatalf("expected moderation_blocked on body, got %d %+v", status, payload)
	}
	f.moderation.outcome = "published"

	// 6. Verified teacher replying to own course without real_name & teacher disclosures -> 403 + teacher_disclosure_required + field=disclosures.
	root := create(t, f, "err-root", input("course", "autumn", 4))
	teacher := r.Principal{Subject: "teacher-account"}
	facts := factsFor(f, true)
	f.service.Disclosures = principalDisclosuresForErrors{teacherSubject: teacher.Subject, teacher: facts}
	currentCaller = teacher
	status, payload = send("POST", "/api/v1/reviews/"+root.ID+"/replies", `{"kind":"reply","target_id":"course","title":"","body":"Official reply","ratings":[0,0,0,0,0],"signed":false}`, "missing-real-name")
	if status != 403 || payload.Code != "forbidden" || payload.Subcode != "teacher_disclosure_required" || payload.Field != "disclosures" {
		t.Fatalf("expected teacher_disclosure_required on disclosures, got %d %+v", status, payload)
	}
	currentCaller = student

	// 7. Multi-device draft conflict -> 409 conflict + revision_conflict + field=revision + current_revision=2;
	//    meanwhile non-owner gets 404 without leaking current_revision!
	draft1, err := f.service.SaveDraft(ctx, student, "", "draft-conflict", input("course", "autumn", 3), 0)
	if err != nil {
		t.Fatal(err)
	}
	draft2, err := f.service.SaveDraft(ctx, student, draft1.ID, "draft-conflict-2", input("course", "autumn", 4), draft1.Revision)
	if err != nil || draft2.Revision != 2 {
		t.Fatal(draft2, err)
	}
	status, payload = send("POST", "/api/v1/reviews/me/drafts", `{"id":"`+draft1.ID+`","revision":1,"input":{"kind":"course","target_id":"course","term_id":"autumn","title":"Stale","body":"Stale","ratings":[5,5,5,5,5],"signed":false}}`, "draft-stale")
	if status != 409 || payload.Code != "conflict" || payload.Subcode != "revision_conflict" || payload.Field != "revision" || payload.CurrentRevision == nil || *payload.CurrentRevision != 2 {
		t.Fatalf("expected revision_conflict with current_revision=2, got %d %+v", status, payload)
	}
	currentCaller = r.Principal{Subject: "other-student"}
	status, payload = send("POST", "/api/v1/reviews/me/drafts", `{"id":"`+draft1.ID+`","revision":1,"input":{"kind":"course","target_id":"course","term_id":"autumn","title":"Probe","body":"Probe","ratings":[5,5,5,5,5],"signed":false}}`, "draft-probe")
	if status != 404 || payload.CurrentRevision != nil {
		t.Fatalf("non-owner draft probe leaked revision: %d %+v", status, payload)
	}
	currentCaller = student

	// 8. Idempotency key conflict -> 409 conflict + idempotency_conflict + field=idempotency_key.
	status, payload = send("POST", "/api/v1/reviews", `{"kind":"course","target_id":"course","term_id":"autumn","title":"Changed","body":"Changed","ratings":[2,2,2,2,2],"signed":false}`, "err-root")
	if status != 409 || payload.Code != "conflict" || payload.Subcode != "idempotency_conflict" || payload.Field != "idempotency_key" {
		t.Fatalf("expected idempotency_conflict, got %d %+v", status, payload)
	}
}

type principalDisclosuresForErrors struct {
	teacherSubject string
	teacher        *disclosureFixture
}

func (d principalDisclosuresForErrors) Facts(ctx context.Context, p r.Principal, school, course, term string) (r.DisclosureFacts, error) {
	if p.Subject == d.teacherSubject {
		return d.teacher.Facts(ctx, p, school, course, term)
	}
	return ordinaryDisclosures{}.Facts(ctx, p, school, course, term)
}
