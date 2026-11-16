package reviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	r "github.com/StuHelper/stuhelper-reviews"
	"github.com/StuHelper/stuhelper-reviews/httpapi"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func addReply(t *testing.T, f *fixtureState, parent, key string, p r.Principal) r.Result {
	t.Helper()
	v, err := f.service.Reply(context.Background(), p, parent, key, r.Input{Kind: "reply", TargetID: "course", Body: key})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestReplyPreviewsAreBoundedOrderedAndMatchVisibleReplies(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	root := create(t, f, "root", input("course", "autumn", 3))
	empty := create(t, f, "empty", input("other", "autumn", 3))
	ids := []string{}
	for i := range 5 {
		f.now = f.now.Add(time.Second)
		parent := root.ID
		if i == 1 {
			parent = ids[0]
		}
		ids = append(ids, addReply(t, f, parent, fmt.Sprintf("reply-%d", i), student).ID)
	}
	hidden := addReply(t, f, root.ID, "withdrawn", student)
	if _, err := f.service.Action(ctx, student, hidden.ID, "withdraw", r.Action{Action: "withdraw", Revision: hidden.Revision}); err != nil {
		t.Fatal(err)
	}
	f.moderation.outcome = "pending"
	addReply(t, f, root.ID, "pending", student)
	f.moderation.outcome = "published"
	page, err := f.service.List(ctx, student, r.Filter{IncludeReplyPreview: true, Kind: "course", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range page.Items {
		if v.ReplyCount == nil || v.ReplyPreview == nil {
			t.Fatal("missing preview", v)
		}
		if v.ID == empty.ID {
			if *v.ReplyCount != 0 || len(*v.ReplyPreview) != 0 {
				t.Fatal(v)
			}
			continue
		}
		if *v.ReplyCount != 5 || len(*v.ReplyPreview) != r.ReplyPreviewLimit {
			t.Fatal("count or cap", v)
		}
		actual := []string{}
		for _, reply := range *v.ReplyPreview {
			actual = append(actual, reply.ID)
			if reply.ReplyCount != nil || reply.ReplyPreview != nil || !reply.Author.Anonymous || reply.Author.Profile != nil {
				t.Fatal("recursive preview or identity", reply)
			}
		}
		if !slices.Equal(actual, ids[:3]) {
			t.Fatal("chronological nested replies", actual, ids)
		}
	}
	handler := httpapi.Handler{Service: f.service, Authenticate: func(*http.Request) (r.Principal, error) { return student, nil }}
	rec := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/reviews?kind=course&limit=1&include_reply_preview=true", nil))
	var first r.Page
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.Next == "" {
		t.Fatal("HTTP pagination", rec.Code, rec.Body.String())
	}
	second, err := f.service.List(ctx, student, r.Filter{IncludeReplyPreview: true, Kind: "course", After: first.Next, Limit: 1})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatal("pagination changed", second, err)
	}
	f.access.revoked = true
	if _, err := f.service.List(ctx, student, r.Filter{IncludeReplyPreview: true, Kind: "course", Limit: 20}); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("revoked reader", err)
	}
	mine, err := f.service.List(ctx, student, r.Filter{IncludeReplyPreview: true, Owner: student.Subject, Kind: "course", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range mine.Items {
		if v.ReplyCount != nil || v.ReplyPreview != nil {
			t.Fatal("other authors exposed through own receipt")
		}
	}
}
func TestReplyPreviewsRecheckMandatoryTeacherDisclosure(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	root := create(t, f, "root", input("course", "autumn", 3))
	facts := factsFor(f, true)
	f.service.Disclosures = facts
	teacher := r.Principal{Subject: "teacher-account"}
	teacherReply, err := f.service.Reply(ctx, teacher, root.ID, "teacher", r.Input{Kind: "reply", TargetID: "course", Body: "Synthetic teacher response", Disclosures: []string{"real_name", "teacher"}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		page, e := f.service.List(ctx, student, r.Filter{IncludeReplyPreview: true, Kind: "course", Limit: 20})
		if e != nil || len(page.Items) != 1 || page.Items[0].ReplyCount == nil || *page.Items[0].ReplyCount != want || len(*page.Items[0].ReplyPreview) != want {
			t.Fatal("current visibility", page, e)
		}
	}

	check(1)
	value := facts.facts.Attributes["real_name"]
	value.MayDisclose = false
	facts.facts.Attributes["real_name"] = value
	check(0)
	facts.err = r.ErrUnavailable
	check(0)
	facts.err = nil
	value.MayDisclose = true
	facts.facts.Attributes["real_name"] = value
	check(1)
	if _, err = f.service.Action(ctx, teacher, teacherReply.ID, "withdraw", r.Action{Action: "withdraw", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	check(0)
	if _, err = f.service.Action(ctx, student, root.ID, "root-withdraw", r.Action{Action: "withdraw", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	page, err := f.service.List(ctx, student, r.Filter{IncludeReplyPreview: true, Kind: "course", Limit: 20})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("withdrawn root", page, err)
	}
}

type previewCountingRepo struct {
	r.Repository
	batches, candidates, entries int
}
type previewCountingTx struct {
	r.Transaction
	counts *previewCountingRepo
}

func (c *previewCountingRepo) Transaction(ctx context.Context, run func(r.Transaction) error) error {
	return c.Repository.Transaction(ctx, func(tx r.Transaction) error { return run(&previewCountingTx{tx, c}) })
}
func (t *previewCountingTx) Entry(id string) (r.Entry, error) {
	t.counts.entries++
	return t.Transaction.Entry(id)
}
func (t *previewCountingTx) ReplyDisclosureCandidates(ids []string, viewer string, now time.Time) ([]r.Entry, error) {
	t.counts.candidates++
	return t.Transaction.(r.ReplyPreviewTransaction).ReplyDisclosureCandidates(ids, viewer, now)
}
func (t *previewCountingTx) ReplyPreviews(ids []string, viewer string, now time.Time, allowed []r.DiscussionAuthor) (r.ReplyBatch, error) {
	t.counts.batches++
	return t.Transaction.(r.ReplyPreviewTransaction).ReplyPreviews(ids, viewer, now, allowed)
}
func TestReplyPreviewPageUsesOneBatchForOneHundredRoots(t *testing.T) {
	f := fixture(t)
	for i := range 100 {
		root := create(t, f, fmt.Sprintf("root-%d", i), input("course", "autumn", 3))
		addReply(t, f, root.ID, fmt.Sprintf("reply-%d", i), student)
	}
	repo := &previewCountingRepo{Repository: f.service.Repo}
	f.service.Repo = repo
	page, err := f.service.List(context.Background(), student, r.Filter{IncludeReplyPreview: true, Kind: "course", Limit: 100})
	if err != nil || len(page.Items) != 100 {
		t.Fatal(len(page.Items), err)
	}
	if repo.batches != 1 || repo.candidates != 1 || repo.entries != 0 {
		t.Fatal("per-item queries", repo.batches, repo.candidates, repo.entries)
	}
	for _, v := range page.Items {
		if v.ReplyCount == nil || *v.ReplyCount != 1 || len(*v.ReplyPreview) != 1 {
			t.Fatal("wrong batch", v)
		}
	}
}
