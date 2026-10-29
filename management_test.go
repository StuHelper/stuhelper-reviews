package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"testing"
)

func TestPresentationConfigAndAttributionChangesAreScopedAndVersioned(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	admin := r.Principal{Subject: "admin"}
	settings := r.Presentation{SchoolID: "school", GuestPreview: 5, MemberPreview: 10, PreviewRatio: 0.2, RatingTexts: map[string][][]string{}}
	if _, err := f.service.SavePresentation(ctx, student, "denied", settings); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("student configured previews", err)
	}
	result, err := f.service.SavePresentation(ctx, admin, "settings", settings)
	if err != nil || result.Effective.Revision != 1 {
		t.Fatal(result, err)
	}
	if _, err = f.service.SavePresentation(ctx, admin, "settings", settings); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.SavePresentation(ctx, admin, "stale", settings); !errors.Is(err, r.ErrConflict) {
		t.Fatal(err)
	}
	root := create(t, f, "review", input("course", "autumn", 3))
	view, err := f.service.Get(ctx, r.Principal{}, root.ID)
	if err != nil || len([]rune(view.Body)) > 5 {
		t.Fatal(view, err)
	}
	preview, err := f.service.PreviewAttribution(ctx, admin, root.ID, "teacher-b")
	if err != nil || len(preview.TeacherIDs) != 1 {
		t.Fatal(preview, err)
	}
	if _, err = f.service.Apply(ctx, admin, r.Decision{ID: "wrong-evidence", ObjectID: root.ID, Revision: 1, Action: "reassign", TeacherID: "teacher-b", EvidenceRevision: "stale", Reason: "Source corrected"}); !errors.Is(err, r.ErrConflict) {
		t.Fatal(err)
	}
	applied, err := f.service.Apply(ctx, admin, r.Decision{ID: "reassign", ObjectID: root.ID, Revision: 1, Action: "reassign", TeacherID: "teacher-b", EvidenceRevision: preview.EvidenceRevision, Reason: "Source corrected"})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := f.service.Stats(ctx, "teacher", "teacher-a", nil, nil)
	if err != nil || stats.Dimensions[0].Samples != 0 {
		t.Fatal("old teacher retained attribution", stats, err)
	}
	stats, err = f.service.Stats(ctx, "teacher", "teacher-b", nil, nil)
	if err != nil || stats.Dimensions[0].Samples != 1 {
		t.Fatal(stats, err)
	}
	if _, err = f.service.Apply(ctx, admin, r.Decision{ID: "feature", ObjectID: root.ID, Revision: applied.Revision, Action: "feature", Reason: "Reviewed quality"}); err != nil {
		t.Fatal(err)
	}
	history, e := f.service.History(ctx, admin, root.ID, 0, 20)
	if e != nil || len(history) != 3 || history[0].ChangeReason != "Reviewed quality" || history[2].Input.TeacherID != "" {
		t.Fatal("history lost original attribution or reason", history, e)
	}
	if _, e = f.service.History(ctx, student, root.ID, 0, 20); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("student read moderation history", e)
	}
	view, err = f.service.Get(ctx, student, root.ID)
	if err != nil || !view.Featured {
		t.Fatal(view, err)
	}
}

func TestWithdrawnContentCannotExposeEarlierBodiesThroughHistory(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	entry := create(t, f, "root", input("course", "autumn", 3))
	if _, err := f.service.Action(ctx, student, entry.ID, "withdraw", r.Action{Action: "withdraw", Revision: entry.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.History(ctx, r.Principal{Subject: "admin"}, entry.ID, 0, 20); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("withdrawn history exposed", err)
	}
}
