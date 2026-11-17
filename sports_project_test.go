package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"slices"
	"testing"
)

type sportsCatalog struct{ *catalog }

func (c sportsCatalog) Target(ctx context.Context, kind, id string) (r.Target, error) {
	target, err := c.catalog.Target(ctx, kind, id)
	if err == nil && id == "project" {
		target.TeacherRequired = true
	}
	return target, err
}
func TestSportsProjectRequiresOwnTeacherAndKeepsAttribution(t *testing.T) {
	f := fixture(t)
	f.service.Catalog = sportsCatalog{f.catalog}
	ctx := context.Background()
	composer, err := f.service.Composer(ctx, student, "course", "project", "autumn", "")
	if err != nil || !composer.Allowed || !composer.TeacherRequired || !slices.Equal(composer.TeacherIDs, []string{"teacher-a", "teacher-b"}) {
		t.Fatal(composer, err)
	}
	in := input("project", "autumn", 4)
	if _, err = f.service.Create(ctx, student, "missing-teacher", in); !errors.Is(err, r.ErrInput) {
		t.Fatal("teacherless project review", err)
	}
	in.TeacherID = "teacher-c"
	if _, err = f.service.Create(ctx, student, "wrong-teacher", in); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("foreign teacher", err)
	}
	in.TeacherID = "teacher-a"
	f.access.optional = true
	f.catalog.missing = true
	if _, err = f.service.Create(ctx, student, "no-enrollment", in); !errors.Is(err, r.ErrEnrollment) {
		t.Fatal("project eligibility relaxed", err)
	}
	f.catalog.missing = false
	result, err := f.service.Create(ctx, student, "publish-project", in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.Create(ctx, student, "publish-project", in)
	if err != nil || result.ID != replay.ID {
		t.Fatal(result, replay, err)
	}
	entry, err := f.service.Get(ctx, student, result.ID)
	if err != nil || entry.TargetID != "project" || entry.TeacherID != "teacher-a" {
		t.Fatal(entry, err)
	}
	page, err := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "project", TeacherID: "teacher-a", Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	page, err = f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "project", TeacherID: "teacher-b", Limit: 100})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("other teacher received score", page, err)
	}
	in.TeacherID = ""
	if _, err = f.service.Action(ctx, student, result.ID, "edit-missing-teacher", r.Action{Action: "edit", Revision: result.Revision, Input: &in}); !errors.Is(err, r.ErrInput) {
		t.Fatal("edit removed teacher", err)
	}
	// Rejected creates and edits leave the single published project/teacher entry.
	page, err = f.service.List(ctx, student, r.Filter{Owner: student.Subject, Limit: 100})
	if err != nil || len(page.Items) != 1 || page.Items[0].Revision != result.Revision {
		t.Fatal(page, err)
	}
}
