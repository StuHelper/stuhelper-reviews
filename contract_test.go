package reviews_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type contractCatalog struct {
	*summaryCatalog
}

func (c *contractCatalog) ReferenceNames(_ context.Context, ids []string) (r.References, error) {
	if c.failed {
		return r.References{}, r.ErrUnavailable
	}
	out := r.References{Items: []r.Reference{}, MissingIDs: []string{}}
	for _, id := range ids {
		out.Items = append(out.Items, r.Reference{ID: id, Kind: "course", Name: "Course " + id, CanonicalID: id})
	}
	return out, nil
}

// convertOpenAPIToJSONSchema converts OpenAPI 3.0.3 nullable schemas in-place to JSON Schema unions.
func convertOpenAPIToJSONSchema(v any) any {
	switch node := v.(type) {
	case map[string]any:
		for k, child := range node {
			node[k] = convertOpenAPIToJSONSchema(child)
		}
		if nullable, ok := node["nullable"].(bool); ok {
			delete(node, "nullable")
			if nullable {
				if t, ok := node["type"].(string); ok {
					node["type"] = []any{t, "null"}
				}
				if enumVals, ok := node["enum"].([]any); ok {
					hasNull := false
					for _, ev := range enumVals {
						if ev == nil {
							hasNull = true
							break
						}
					}
					if !hasNull {
						node["enum"] = append(enumVals, nil)
					}
				}
			}
		}
		return node
	case []any:
		for i, child := range node {
			node[i] = convertOpenAPIToJSONSchema(child)
		}
		return node
	default:
		return v
	}
}

func escapeJSONPointer(segment string) string {
	s := strings.ReplaceAll(segment, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

func TestOpenAPISchemasHTTPContractAndDecisionParity(t *testing.T) {
	rawSpec, err := os.ReadFile("contracts/reviews.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var specDoc map[string]any
	if err := json.Unmarshal(rawSpec, &specDoc); err != nil {
		t.Fatal(err)
	}
	convertOpenAPIToJSONSchema(specDoc)

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("urn:reviews-openapi", specDoc); err != nil {
		t.Fatal(err)
	}

	validateJSON := func(uri string, payload []byte) {
		t.Helper()
		schema, err := compiler.Compile(uri)
		if err != nil {
			t.Fatalf("compile %s: %v", uri, err)
		}
		var val any
		if err := json.Unmarshal(payload, &val); err != nil {
			t.Fatalf("unmarshal payload for %s: %v (%s)", uri, err, string(payload))
		}
		if err := schema.Validate(val); err != nil {
			t.Fatalf("schema %s rejected payload %s: %v", uri, string(payload), err)
		}
	}

	// 1. Validate Decision schema bi-directional parity across all 9 actions.
	decisionURI := "urn:reviews-openapi#/components/schemas/Decision"
	editedBody := "Moderator edited body"
	decisions := []r.Decision{
		{ID: "dec-publish", ObjectID: "obj-1", Revision: 1, Action: "publish", Reason: "Approved"},
		{ID: "dec-hide", ObjectID: "obj-1", Revision: 2, Action: "hide", Reason: "Hidden"},
		{ID: "dec-restore", ObjectID: "obj-1", Revision: 3, Action: "restore", Reason: "Restored"},
		{ID: "dec-delete", ObjectID: "obj-1", Revision: 4, Action: "delete", Reason: "Deleted"},
		{ID: "dec-edit", ObjectID: "obj-1", Revision: 5, Action: "edit", Body: &editedBody, Reason: "Redacted PII"},
		{ID: "dec-identity-subject", ObjectID: "obj-1", Revision: 6, Action: "identity", Subject: "student", Signed: false, Reason: "Anonymized"},
		{ID: "dec-identity-handle", ObjectID: "obj-1", Revision: 7, Action: "identity", AuthorHandle: "author", Signed: true, Reason: "Signed handle"},
		{ID: "dec-feature", ObjectID: "obj-1", Revision: 8, Action: "feature", Reason: "Featured review"},
		{ID: "dec-unfeature", ObjectID: "obj-1", Revision: 9, Action: "unfeature", Reason: "Unfeatured review"},
		{ID: "dec-reassign", ObjectID: "obj-1", Revision: 10, Action: "reassign", TeacherID: "teacher-b", EvidenceRevision: "rev-hash", Reason: "Corrected teacher"},
	}
	for _, dec := range decisions {
		raw, err := json.Marshal(dec)
		if err != nil {
			t.Fatal(err)
		}
		validateJSON(decisionURI, raw)
	}
	decisionSchema, err := compiler.Compile(decisionURI)
	if err != nil {
		t.Fatal(err)
	}
	for _, badRaw := range []string{
		`{"id":"d1","object_id":"o1","revision":1,"action":"unknown","reason":"bad"}`,
		`{"id":"d1","object_id":"o1","revision":1,"action":"publish","reason":"ok","extra":true}`,
		`{"id":"d1","object_id":"o1","revision":1,"action":"publish"}`,
	} {
		var badVal any
		_ = json.Unmarshal([]byte(badRaw), &badVal)
		if err := decisionSchema.Validate(badVal); err == nil {
			t.Fatalf("Decision schema accepted invalid payload: %s", badRaw)
		}
	}

	// 2. Verify applyModerationOutcome folding across Create, Reply, and republish.
	f := fixture(t)
	cat := &contractCatalog{
		summaryCatalog: &summaryCatalog{
			catalog:   f.catalog,
			aliases:   map[string]map[string]string{},
			orderKeys: map[string]int{"autumn": 20263},
			items: []r.PendingCourse{
				{CourseID: "pending-course", CourseName: "Pending Course", TermID: "autumn", TermName: "2026 Autumn"},
			},
		},
	}
	f.service.Catalog = cat
	ctx := context.Background()

	admin := r.Principal{Subject: "admin"}
	f.moderation.outcome = "review"
	reviewEntry, err := f.service.Create(ctx, student, "mod-review-create", input("course", "autumn", 4))
	if err != nil || reviewEntry.State != "published" {
		t.Fatalf("expected review outcome to publish, got %+v %v", reviewEntry, err)
	}
	inspected, err := f.service.Inspect(ctx, admin, reviewEntry.ID)
	if err != nil || !inspected.ReviewRequested || !inspected.Visible {
		t.Fatalf("expected ReviewRequested=true & Visible=true, got %+v %v", inspected, err)
	}
	reviewReply, err := f.service.Reply(ctx, r.Principal{Subject: "replier"}, reviewEntry.ID, "mod-review-reply", r.Input{
		Kind: "reply", TargetID: "course", Body: "Reply under post-moderation",
	})
	if err != nil || reviewReply.State != "published" {
		t.Fatalf("expected reply review outcome to publish, got %+v %v", reviewReply, err)
	}
	replyInspected, err := f.service.Inspect(ctx, admin, reviewReply.ID)
	if err != nil || !replyInspected.ReviewRequested {
		t.Fatalf("expected reply ReviewRequested=true, got %+v %v", replyInspected, err)
	}
	withdrawnRes, err := f.service.Action(ctx, student, reviewEntry.ID, "mod-withdraw", r.Action{
		Action: "withdraw", Revision: reviewEntry.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.moderation.outcome = "pending"
	pendingRepub, err := f.service.Action(ctx, student, reviewEntry.ID, "mod-repub-pending", r.Action{
		Action: "republish", Revision: withdrawnRes.Revision, Input: ptrInput(input("course", "autumn", 5)),
	})
	if err != nil || pendingRepub.State != "pending" || pendingRepub.ExpiresAt == nil {
		t.Fatalf("expected pending republish to preserve ExpiresAt, got %+v %v", pendingRepub, err)
	}
	f.moderation.outcome = ""

	// Promote pendingRepub via Apply(publish) to verify runtime Apply + Decision parity.
	publishedRes, err := f.service.Apply(ctx, admin, r.Decision{
		ID: "apply-publish", ObjectID: reviewEntry.ID, Revision: pendingRepub.Revision, Action: "publish", Reason: "Approved pending republish",
	})
	if err != nil || publishedRes.State != "published" || publishedRes.ExpiresAt != nil {
		t.Fatalf("expected Apply(publish) to publish and clear ExpiresAt, got %+v %v", publishedRes, err)
	}

	// 3. Validate all HTTP operations (request bodies + 200 responses + error responses) against OpenAPI.
	currentActor := student
	var authErr error
	handler := httpapi.Handler{
		Service: f.service,
		Authenticate: func(*http.Request) (r.Principal, error) {
			return currentActor, authErr
		},
	}.Routes()

	covered := map[string]bool{}
	callAndValidate := func(actor r.Principal, method, pathTemplate, actualPath, idempotencyKey string, reqBody any, wantStatus int) []byte {
		t.Helper()
		currentActor = actor
		opKey := method + " " + pathTemplate
		ptrBase := "urn:reviews-openapi#/paths/" + escapeJSONPointer(pathTemplate) + "/" + strings.ToLower(method)

		var bodyReader *bytes.Reader
		if reqBody != nil {
			rawReq, err := json.Marshal(reqBody)
			if err != nil {
				t.Fatal(err)
			}
			validateJSON(ptrBase+"/requestBody/content/application~1json/schema", rawReq)
			bodyReader = bytes.NewReader(rawReq)
		} else {
			bodyReader = bytes.NewReader(nil)
		}

		req := httptest.NewRequest(method, actualPath, bodyReader)
		if reqBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Fatalf("%s (%s) status=%d want=%d body=%s", opKey, actualPath, w.Code, wantStatus, w.Body.String())
		}
		resURI := ptrBase + "/responses/" + strconv.Itoa(wantStatus) + "/content/application~1json/schema"
		validateJSON(resURI, w.Body.Bytes())
		if wantStatus == http.StatusOK {
			covered[opKey] = true
		}
		return w.Body.Bytes()
	}

	// Create root review via HTTP
	var created r.Result
	_ = json.Unmarshal(callAndValidate(
		student, "POST", "/api/v1/reviews", "/api/v1/reviews", "http-create-1",
		input("course", "autumn", 5), http.StatusOK,
	), &created)

	// Reply via HTTP
	_ = callAndValidate(
		r.Principal{Subject: "voter"}, "POST", "/api/v1/reviews/{id}/replies",
		"/api/v1/reviews/"+created.ID+"/replies", "http-reply-1",
		r.Input{Kind: "reply", TargetID: "course", Body: "Helpful course review"}, http.StatusOK,
	)

	// Vote action via HTTP
	_ = callAndValidate(
		r.Principal{Subject: "voter"}, "POST", "/api/v1/reviews/{id}/actions",
		"/api/v1/reviews/"+created.ID+"/actions", "http-vote-1",
		r.Action{Action: "vote", Vote: 1}, http.StatusOK,
	)

	// Public & personal reads
	_ = callAndValidate(student, "GET", "/api/v1/reviews", "/api/v1/reviews?kind=course&target_id=course&include_reply_preview=true", "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/{id}", "/api/v1/reviews/"+created.ID, "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/statistics", "/api/v1/reviews/statistics?kind=course&target_id=course", "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/compose", "/api/v1/reviews/compose?kind=course&target_id=course&term_id=autumn", "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/me/entries", "/api/v1/reviews/me/entries", "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/me/entries/{id}", "/api/v1/reviews/me/entries/"+created.ID, "", nil, http.StatusOK)
	_ = callAndValidate(r.Principal{Subject: "voter"}, "GET", "/api/v1/reviews/me/votes", "/api/v1/reviews/me/votes", "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/me/references", "/api/v1/reviews/me/references?ids=course", "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/summaries", "/api/v1/reviews/summaries?kind=course&ids=course&include_charts=true", "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/me/overview", "/api/v1/reviews/me/overview", "", nil, http.StatusOK)

	// Draft lifecycle (save, list, get, delete, save+publish)
	var savedDraft r.Result
	_ = json.Unmarshal(callAndValidate(
		student, "POST", "/api/v1/reviews/me/drafts", "/api/v1/reviews/me/drafts", "http-draft-1",
		map[string]any{"revision": 0, "input": input("course", "autumn", 4)}, http.StatusOK,
	), &savedDraft)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/me/drafts", "/api/v1/reviews/me/drafts", "", nil, http.StatusOK)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/me/drafts/{id}", "/api/v1/reviews/me/drafts/"+savedDraft.ID, "", nil, http.StatusOK)
	_ = callAndValidate(
		student, "DELETE", "/api/v1/reviews/me/drafts/{id}", "/api/v1/reviews/me/drafts/"+savedDraft.ID, "http-draft-del-1",
		map[string]any{"revision": savedDraft.Revision}, http.StatusOK,
	)

	var draftToPub r.Result
	_ = json.Unmarshal(callAndValidate(
		student, "POST", "/api/v1/reviews/me/drafts", "/api/v1/reviews/me/drafts", "http-draft-2",
		map[string]any{"revision": 0, "input": input("course", "spring", 4)}, http.StatusOK,
	), &draftToPub)
	_ = callAndValidate(
		student, "POST", "/api/v1/reviews/me/drafts/{id}/publish", "/api/v1/reviews/me/drafts/"+draftToPub.ID+"/publish", "http-draft-pub-2",
		r.PublishDraft{Revision: draftToPub.Revision, Input: input("course", "spring", 4)}, http.StatusOK,
	)

	// Admin & presentation routes
	_ = callAndValidate(admin, "POST", "/api/v1/reviews/admin/entries/batch-approve", "/api/v1/reviews/admin/entries/batch-approve", "contract-batch", r.AdminBatchApproveRequest{
		SchoolID: "school", Reason: "approved", Items: []r.AdminBatchItem{{ObjectID: created.ID, Revision: created.Revision}},
	}, http.StatusOK)
	_ = callAndValidate(admin, "GET", "/api/v1/reviews/admin/entries", "/api/v1/reviews/admin/entries?school_id=school", "", nil, http.StatusOK)
	_ = callAndValidate(admin, "GET", "/api/v1/reviews/admin/entries/{id}", "/api/v1/reviews/admin/entries/"+created.ID, "", nil, http.StatusOK)
	_ = callAndValidate(admin, "GET", "/api/v1/reviews/admin/entries/{id}/history", "/api/v1/reviews/admin/entries/"+created.ID+"/history", "", nil, http.StatusOK)
	_ = callAndValidate(
		admin, "POST", "/api/v1/reviews/admin/entries/{id}/attribution", "/api/v1/reviews/admin/entries/"+created.ID+"/attribution", "http-attribution-preview",
		map[string]any{"teacher_id": "teacher-b"}, http.StatusOK,
	)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/presentation", "/api/v1/reviews/presentation?school_id=school", "", nil, http.StatusOK)
	_ = callAndValidate(admin, "GET", "/api/v1/reviews/admin/presentation", "/api/v1/reviews/admin/presentation?school_id=school", "", nil, http.StatusOK)
	_ = callAndValidate(
		admin, "PUT", "/api/v1/reviews/admin/presentation", "/api/v1/reviews/admin/presentation", "http-pres-put-1",
		r.Presentation{SchoolID: "school", Revision: 0, RatingTexts: map[string][][]string{}}, http.StatusOK,
	)

	// Error status responses (400, 401, 403, 404, 409, 503) schema validation
	_ = callAndValidate(student, "GET", "/api/v1/reviews", "/api/v1/reviews?limit=999", "", nil, http.StatusBadRequest)
	_ = callAndValidate(r.Principal{}, "GET", "/api/v1/reviews", "/api/v1/reviews", "", nil, http.StatusUnauthorized)
	_ = callAndValidate(r.Principal{Subject: "member"}, "GET", "/api/v1/reviews", "/api/v1/reviews", "", nil, http.StatusForbidden)
	_ = callAndValidate(student, "GET", "/api/v1/reviews/{id}", "/api/v1/reviews/nonexistent-entry-id", "", nil, http.StatusNotFound)
	_ = callAndValidate(
		admin, "PUT", "/api/v1/reviews/admin/presentation", "/api/v1/reviews/admin/presentation", "http-pres-stale",
		r.Presentation{SchoolID: "school", Revision: 0, RatingTexts: map[string][][]string{}}, http.StatusConflict,
	)
	authErr = r.ErrUnavailable
	_ = callAndValidate(student, "GET", "/api/v1/reviews", "/api/v1/reviews", "", nil, http.StatusServiceUnavailable)
	authErr = nil
	_ = time.Second

	// Ensure every operation declared in OpenAPI paths was exercised on HTTP 200.
	pathsMap := specDoc["paths"].(map[string]any)
	totalOps := 0
	for p, rawOps := range pathsMap {
		for m := range rawOps.(map[string]any) {
			totalOps++
			key := strings.ToUpper(m) + " " + p
			if !covered[key] {
				t.Fatalf("OpenAPI operation %s was not exercised in contract test", key)
			}
		}
	}
	if totalOps != len(covered) {
		t.Fatalf("expected %d OpenAPI operations covered, got %d", totalOps, len(covered))
	}
}

func ptrInput(v r.Input) *r.Input {
	return &v
}
