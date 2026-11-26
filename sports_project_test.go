package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"slices"
	"testing"
	"time"
)

type sportsCatalog struct{ *catalog }

func TestSportsProjectUsesPublicTeachersAndKeepsAttribution(t *testing.T) {
	f := fixture(t)
	f.service.Catalog = sportsCatalog{f.catalog}
	ctx := context.Background()
	composer, err := f.service.Composer(ctx, student, "course", "project", "autumn", "")
	if err != nil || !composer.Allowed || !slices.Equal(composer.Teachers, []r.ComposerTeacher{{ID: "teacher-a", Enrolled: true}, {ID: "teacher-b", Enrolled: true}}) {
		t.Fatal(composer, err)
	}
	in := input("project", "autumn", 4)
	unspecified, err := f.service.Create(ctx, student, "missing-teacher", in)
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.service.GetWithOptions(ctx, student, unspecified.ID, r.ViewOptions{IncludeTeacherIDs: true})
	if err != nil || view.EnrollmentStatus != "enrolled" || !slices.Equal(view.AttributedTeacherIDs, []string{"teacher-a", "teacher-b"}) {
		t.Fatal(view, err)
	}
	in.TeacherIDs = []string{"teacher-c"}
	if _, err = f.service.Create(ctx, student, "wrong-teacher", in); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("foreign teacher", err)
	}
	in.TeacherIDs = []string{"teacher-a"}

	f.catalog.missing = true
	result, err := f.service.Create(ctx, student, "publish-project", in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.Create(ctx, student, "publish-project", in)
	if err != nil || result.ID != replay.ID {
		t.Fatal(result, replay, err)
	}
	entry, err := f.service.Get(ctx, student, result.ID)
	if err != nil || entry.TargetID != "project" || !slices.Equal(entry.TeacherIDs, []string{"teacher-a"}) {
		t.Fatal(entry, err)
	}
	page, err := f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "project", TeacherID: "teacher-a", Limit: 100})
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	page, err = f.service.List(ctx, student, r.Filter{Kind: "course", TargetID: "project", TeacherID: "teacher-b", Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("other teacher received score", page, err)
	}
	in.TeacherIDs = nil
	if _, err = f.service.Action(ctx, student, result.ID, "edit-missing-teacher", r.Action{Action: "edit", Revision: result.Revision, Input: &in}); !errors.Is(err, r.ErrInput) {
		t.Fatal("edit removed teacher", err)
	}
	// Rejected creates and edits leave the single published project/teacher entry.
	page, err = f.service.List(ctx, student, r.Filter{Owner: student.Subject, Limit: 100})
	if err != nil || len(page.Items) != 2 || page.Items[0].Revision != result.Revision {
		t.Fatal(page, err)
	}
}

type mergedSportsCatalog struct{ sportsCatalog }

func (c mergedSportsCatalog) CourseTermTeachers(context.Context, string, string) ([]string, error) {
	return []string{"current-teacher", "teacher-b"}, nil
}

func (c mergedSportsCatalog) EquivalentIDs(_ context.Context, kind, id string) ([]string, error) {
	if kind == "teacher" && (id == "current-teacher" || id == "teacher-a") {
		return []string{"current-teacher", "teacher-a"}, nil
	}
	return []string{id}, nil
}
func (c mergedSportsCatalog) CanonicalCourses(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = id
	}
	return out, nil
}
func TestModeratedProjectTeacherAndPreviewAudit(t *testing.T) {
	f := fixture(t)
	repo, db := database(t)
	f.service.Repo = repo
	f.service.Catalog = mergedSportsCatalog{sportsCatalog{f.catalog}}
	ctx := context.Background()
	admin := r.Principal{Subject: "admin"}
	in := input("project", "autumn", 4)
	in.TeacherIDs = []string{"teacher-a"}
	root := create(t, f, "project", in)
	preview, err := f.service.PreviewAttribution(ctx, admin, root.ID, []string{"current-teacher"})
	if err != nil || !slices.Equal(preview.TeacherIDs, []string{"current-teacher"}) {
		t.Fatal(preview, err)
	}
	var reads int
	if err := db.QueryRow(`SELECT count(*) FROM reviews.audit WHERE data->>'Type'='reviews.inspected.v1' AND data->>'Subject'=$1`, "attribution/"+root.ID).Scan(&reads); err != nil || reads != 1 {
		t.Fatal(reads, err)
	}
	d := r.Decision{ID: "correct", ObjectID: root.ID, Revision: 1, Action: "reassign", Reason: "Updated source", EvidenceRevision: preview.EvidenceRevision}
	d.TeacherIDs = []string{"current-teacher"}
	if _, err = f.service.Apply(ctx, admin, d); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.GetOwn(ctx, student, root.ID)
	if err != nil || !slices.Equal(after.TeacherIDs, d.TeacherIDs) || after.Revision != 2 {
		t.Fatal(after, err)
	}
	if _, err = db.Exec(`UPDATE reviews.entries SET state='pending',expires_at=now(),data=data||jsonb_build_object('state','pending','expires_at',now()) WHERE id=$1`, root.ID); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Hour)
	if _, err = f.service.PreviewAttribution(ctx, admin, root.ID, []string{"current-teacher"}); !errors.Is(err, r.ErrNotFound) {
		t.Fatal(err)
	}

}
