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

	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
)

type aliasSummaryCatalog struct {
	*summaryCatalog
}

func (c *aliasSummaryCatalog) EquivalentIDs(_ context.Context, kind, id string) ([]string, error) {
	out := []string{id}
	for alias, canonical := range c.aliases[kind] {
		if id == alias || id == canonical {
			out = append(out, alias, canonical)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func (c *aliasSummaryCatalog) CanonicalCourses(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if canonical := c.aliases["course"][id]; canonical != "" {
			out[id] = canonical
		} else {
			out[id] = id
		}
	}
	return out, nil
}

func (c *aliasSummaryCatalog) HasTaught(ctx context.Context, teachers []string, course string) (bool, error) {
	if canonical := c.aliases["course"][course]; canonical != "" {
		course = canonical
	}
	return c.catalog.HasTaught(ctx, teachers, course)
}

func TestComposerOwnAssociationsReplyDraftsAndParentSummary(t *testing.T) {
	f, port := uiReviewFixture(t)
	aliasPort := &aliasSummaryCatalog{summaryCatalog: port}
	f.service.Catalog = aliasPort
	ctx := context.Background()

	// 1. B-07: Alias-aware own_draft_id in Composer and draft_id in OwnOverview.pending_courses.
	port.aliases["course"] = map[string]string{"legacy-course": "course"}
	port.items = []r.PendingCourse{
		{CourseID: "course", CourseName: "Algorithms", TermID: "autumn", TermName: "2025 Autumn"},
		{CourseID: "other-course", CourseName: "Systems", TermID: "autumn", TermName: "2025 Autumn"},
	}
	draftIn := r.Input{Kind: "course", TargetID: "legacy-course", TermID: "autumn", Title: "Half written", Body: "Draft notes"}
	courseDraft, err := f.service.SaveDraft(ctx, student, "", "course-draft-1", draftIn, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Another user's draft on other-course must never leak into student's overview or composer.
	if _, err := f.service.SaveDraft(ctx, r.Principal{Subject: "other-student"}, "", "other-draft", r.Input{Kind: "course", TargetID: "other-course", TermID: "autumn", Body: "Other"}, 0); err != nil {
		t.Fatal(err)
	}

	overview, err := f.service.OwnOverview(ctx, student)
	if err != nil || overview.PendingCourses.State != "available" || len(overview.PendingCourses.Items) != 2 {
		t.Fatalf("unexpected overview: %+v %v", overview, err)
	}
	var algoItem, sysItem r.PendingCourse
	for _, item := range overview.PendingCourses.Items {
		if item.CourseID == "course" {
			algoItem = item
		} else if item.CourseID == "other-course" {
			sysItem = item
		}
	}
	if algoItem.DraftID != courseDraft.ID || sysItem.DraftID != "" {
		t.Fatalf("pending course draft_id mismatch: algo=%+v sys=%+v", algoItem, sysItem)
	}
	rawSys, _ := json.Marshal(sysItem)
	if strings.Contains(string(rawSys), "draft_id") {
		t.Fatalf("pending course without draft should omit draft_id: %s", string(rawSys))
	}

	comp, err := f.service.Composer(ctx, student, "course", "course", "autumn", "")
	if err != nil || comp.OwnDraftID != courseDraft.ID || len(comp.OwnEntryIDs) != 0 {
		t.Fatalf("composer missing own_draft_id: %+v %v", comp, err)
	}

	// Publish the course draft; Composer now reports own_entry_ids and clears own_draft_id.
	pubCourseIn := input("legacy-course", "autumn", 5)
	publishedRoot, err := f.service.PublishDraft(ctx, student, courseDraft.ID, "pub-course-draft", r.PublishDraft{Revision: courseDraft.Revision, Input: pubCourseIn})
	if err != nil {
		t.Fatal(err)
	}
	compAfterPub, err := f.service.Composer(ctx, student, "course", "course", "autumn", "")
	if err != nil || compAfterPub.OwnDraftID != "" || !slices.Equal(compAfterPub.OwnEntryIDs, []string{publishedRoot.ID}) {
		t.Fatalf("composer after publish mismatch: %+v %v", compAfterPub, err)
	}

	// 2. B-08.2: Reply draft save, update, Composer own_draft_id, atomic PublishDraft, and per-user MaxDrafts cap.
	teacher := r.Principal{Subject: "teacher-user"}
	facts := factsFor(f, true)
	f.service.Disclosures = principalDisclosuresForDrafts{teacherSubject: teacher.Subject, teacher: facts}

	// Missing parent_id on reply draft is rejected; parent_id on course draft is rejected.
	if _, err := f.service.SaveDraft(ctx, teacher, "", "bad-reply-draft", r.Input{Kind: "reply", TargetID: "course", Body: "No parent"}, 0); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput for reply draft without parent_id, got %v", err)
	}
	if _, err := f.service.SaveDraft(ctx, student, "", "bad-course-parent", r.Input{Kind: "course", TargetID: "course", ParentID: publishedRoot.ID, Body: "Bad"}, 0); !errors.Is(err, r.ErrInput) {
		t.Fatalf("expected ErrInput for course draft with parent_id, got %v", err)
	}

	replyDraftIn := r.Input{
		Kind:        "reply",
		TargetID:    "course",
		ParentID:    publishedRoot.ID,
		Body:        "Teacher draft v1",
		Disclosures: []string{"real_name", "teacher"},
	}
	replyDraft, err := f.service.SaveDraft(ctx, teacher, "", "teacher-reply-draft", replyDraftIn, 0)
	if err != nil {
		t.Fatal(err)
	}
	gotDraft, err := f.service.GetDraft(ctx, teacher, replyDraft.ID)
	if err != nil || gotDraft.ParentID != publishedRoot.ID || gotDraft.RootID != publishedRoot.ID || gotDraft.Withdrawn {
		t.Fatalf("unexpected reply draft view: %+v %v", gotDraft, err)
	}
	replyComp, err := f.service.Composer(ctx, teacher, "reply", "course", "", publishedRoot.ID)
	if err != nil || replyComp.OwnDraftID != replyDraft.ID {
		t.Fatalf("reply composer missing own_draft_id: %+v %v", replyComp, err)
	}

	// Enforce MaxDrafts cap on new drafts while still allowing updates to existing drafts.
	f.service.Config.MaxDrafts = 2
	secondDraft, err := f.service.SaveDraft(ctx, teacher, "", "teacher-draft-2", r.Input{Kind: "mentor", TargetID: "teacher-a", Body: "Second draft"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = secondDraft
	if _, err := f.service.SaveDraft(ctx, teacher, "", "teacher-draft-3", r.Input{Kind: "mentor", TargetID: "teacher-a", Body: "Exceeds limit"}, 0); !errors.Is(err, r.ErrConflict) {
		t.Fatalf("expected ErrConflict when MaxDrafts exceeded, got %v", err)
	}
	replyDraftIn.Body = "Teacher official response final"
	replyDraftUpdated, err := f.service.SaveDraft(ctx, teacher, replyDraft.ID, "teacher-reply-update", replyDraftIn, replyDraft.Revision)
	if err != nil {
		t.Fatalf("updating existing draft at MaxDrafts cap should succeed: %v", err)
	}

	// Publish reply draft via HTTP endpoint and verify atomic removal + idempotent replay.
	h := httpapi.Handler{
		Service:      f.service,
		Authenticate: func(*http.Request) (r.Principal, error) { return teacher, nil },
	}.Routes()
	pubBody := fmt.Sprintf(`{"revision":%d,"input":{"kind":"reply","target_id":"course","parent_id":%q,"title":"","body":"Teacher official response final","ratings":[0,0,0,0,0],"signed":true,"disclosures":["real_name","teacher"]}}`, replyDraftUpdated.Revision, publishedRoot.ID)
	pubRec := httptest.NewRecorder()
	pubReq := httptest.NewRequest("POST", "/api/v1/reviews/me/drafts/"+replyDraft.ID+"/publish", strings.NewReader(pubBody))
	pubReq.Header.Set("Idempotency-Key", "pub-reply-1")
	h.ServeHTTP(pubRec, pubReq)
	if pubRec.Code != 200 {
		t.Fatalf("publish reply draft failed: %d %s", pubRec.Code, pubRec.Body.String())
	}
	var teacherReply r.Result
	if err := json.Unmarshal(pubRec.Body.Bytes(), &teacherReply); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.GetDraft(ctx, teacher, replyDraft.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatalf("published reply draft remained in drafts: %v", err)
	}
	// Replay returns identical result.
	replayRec := httptest.NewRecorder()
	replayReq := httptest.NewRequest("POST", "/api/v1/reviews/me/drafts/"+replyDraft.ID+"/publish", strings.NewReader(pubBody))
	replayReq.Header.Set("Idempotency-Key", "pub-reply-1")
	h.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != 200 || replayRec.Body.String() != pubRec.Body.String() {
		t.Fatalf("reply draft publish replay mismatch: %d %s", replayRec.Code, replayRec.Body.String())
	}

	// 3. B-08.1: Nested reply parent_author_handle & parent_visible across deletion and teacher disclosure revocation.
	replierA := r.Principal{Subject: "replier-a"}
	replierB := r.Principal{Subject: "replier-b"}
	r1, err := f.service.Reply(ctx, replierA, publishedRoot.ID, "r1", r.Input{Kind: "reply", TargetID: "course", Body: "SecretParentBodyToDelete"})
	if err != nil {
		t.Fatal(err)
	}
	r1View, err := f.service.Get(ctx, student, r1.ID)
	if err != nil || r1View.ParentVisible == nil || !*r1View.ParentVisible || r1View.ParentAuthorHandle != "author" {
		t.Fatalf("expected direct reply parent_visible=true & handle=author, got %+v %v", r1View, err)
	}
	r2, err := f.service.Reply(ctx, replierB, r1.ID, "r2", r.Input{Kind: "reply", TargetID: "course", Body: "Child reply to r1"})
	if err != nil {
		t.Fatal(err)
	}
	childToTeacher, err := f.service.Reply(ctx, replierB, teacherReply.ID, "r-to-teacher", r.Input{Kind: "reply", TargetID: "course", Body: "Thanks professor"})
	if err != nil {
		t.Fatal(err)
	}

	r2Before, err := f.service.Get(ctx, student, r2.ID)
	if err != nil || r2Before.ParentVisible == nil || !*r2Before.ParentVisible || r2Before.ParentAuthorHandle != r1View.Author.Handle {
		t.Fatalf("expected nested reply parent_visible=true & handle=%s, got %+v %v", r1View.Author.Handle, r2Before, err)
	}

	// Delete r1 within the 5-minute window: r2 stays visible, parent_visible becomes false, deleted body never leaks.
	if _, err := f.service.Action(ctx, replierA, r1.ID, "del-r1", r.Action{Action: "delete", Revision: r1.Revision}); err != nil {
		t.Fatal(err)
	}
	r2AfterDelete, err := f.service.Get(ctx, student, r2.ID)
	if err != nil || r2AfterDelete.ParentVisible == nil || *r2AfterDelete.ParentVisible || r2AfterDelete.ParentAuthorHandle != r1View.Author.Handle {
		t.Fatalf("expected nested reply parent_visible=false after parent delete, got %+v %v", r2AfterDelete, err)
	}
	rawR2, _ := json.Marshal(r2AfterDelete)
	if strings.Contains(string(rawR2), "SecretParentBodyToDelete") {
		t.Fatalf("deleted parent body leaked: %s", string(rawR2))
	}

	// Revoke teacher's real_name disclosure: childToTeacher reports parent_visible=false!
	revoked := facts.facts.Attributes["real_name"]
	revoked.MayDisclose = false
	facts.facts.Attributes["real_name"] = revoked
	childAfterRevoke, err := f.service.Get(ctx, student, childToTeacher.ID)
	if err != nil || childAfterRevoke.ParentVisible == nil || *childAfterRevoke.ParentVisible {
		t.Fatalf("expected parent_visible=false when teacher parent disclosure revoked, got %+v %v", childAfterRevoke, err)
	}
}

type principalDisclosuresForDrafts struct {
	teacherSubject string
	teacher        *disclosureFixture
}

func (d principalDisclosuresForDrafts) Facts(ctx context.Context, p r.Principal, school, course, term string) (r.DisclosureFacts, error) {
	if p.Subject == d.teacherSubject {
		return d.teacher.Facts(ctx, p, school, course, term)
	}
	return ordinaryDisclosures{}.Facts(ctx, p, school, course, term)
}
