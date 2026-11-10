package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"slices"
	"testing"
	"time"
)

type ordinaryDisclosures struct{}

func (ordinaryDisclosures) Facts(context.Context, r.Principal, string, string, string) (r.DisclosureFacts, error) {
	return r.DisclosureFacts{StudentVerified: true, ValidUntil: time.Now().Add(365 * 24 * time.Hour)}, nil
}

type disclosureFixture struct {
	facts r.DisclosureFacts
	err   error
}

func (d *disclosureFixture) Facts(context.Context, r.Principal, string, string, string) (r.DisclosureFacts, error) {
	return d.facts, d.err
}
func (c *catalog) HasTaught(_ context.Context, teachers []string, course string) (bool, error) {
	if c.failed {
		return false, r.ErrUnavailable
	}
	return course == "course" && slices.Contains(teachers, "teacher-a"), nil
}
func factsFor(f *fixtureState, teacher bool) *disclosureFixture {
	until := f.now.Add(48 * time.Hour)
	facts := r.DisclosureFacts{StudentVerified: !teacher, TeacherVerified: teacher, ValidUntil: until, Attributes: map[string]r.DisclosureFact{}}
	if teacher {
		facts.TeacherIDs = []string{"teacher-a"}
	}
	for k, v := range map[string]string{"real_name": "Synthetic Name", "gender": "female", "department": "Synthetic College", "teacher": "verified", "organization": "Synthetic Department", "enrollment": "enrolled"} {
		facts.Attributes[k] = r.DisclosureFact{Value: v, Source: "trusted-fixture", Revision: "1", ValidUntil: until, MayDisclose: true}
	}
	return &disclosureFixture{facts: facts}
}
func TestVoluntaryDisclosuresRequireFreshAuthorizedFacts(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	facts := factsFor(f, false)
	f.service.Disclosures = facts
	in := input("course", "autumn", 3)
	in.Disclosures = []string{"real_name", "department"}
	created := create(t, f, "selected", in)
	view, err := f.service.Get(ctx, student, created.ID)
	if err != nil || len(view.Author.Disclosures) != 2 {
		t.Fatal(view, err)
	}
	for _, tag := range view.Author.Disclosures {
		if tag.Key == "gender" || tag.Key == "teacher" {
			t.Fatal("unselected or unverified attribute published")
		}
	}
	denied := facts.facts.Attributes["real_name"]
	denied.MayDisclose = false
	facts.facts.Attributes["real_name"] = denied
	if _, err = f.service.Create(ctx, student, "denied", in); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("unauthorized field accepted", err)
	}
	view, err = f.service.Get(ctx, student, created.ID)
	if err != nil || len(view.Author.Disclosures) != 1 || view.Author.Disclosures[0].Key != "department" {
		t.Fatal("revoked field remained", view, err)
	}
	facts.err = r.ErrUnavailable
	view, err = f.service.Get(ctx, student, created.ID)
	if err != nil || len(view.Author.Disclosures) != 0 {
		t.Fatal("cached private values leaked", view, err)
	}
}
func TestTeacherReplyRequiresRealNameAcrossTermsAndAllowsIndependentTagsElsewhere(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	root := create(t, f, "course", input("course", "spring", 3))
	facts := factsFor(f, true)
	f.service.Disclosures = facts
	teacher := r.Principal{Subject: "teacher-account"}
	in := r.Input{Kind: "reply", TargetID: "course", Body: "Teacher response"}
	composer, err := f.service.Composer(ctx, teacher, "reply", "course", "", root.ID)
	if err != nil || !composer.TeacherReply || !composer.Allowed {
		t.Fatal(composer, err)
	}
	if _, err = f.service.Reply(ctx, teacher, root.ID, "anonymous", in); !errors.Is(err, r.ErrForbidden) {
		t.Fatal("teacher bypassed mandatory name", err)
	}
	in.Disclosures = []string{"real_name", "teacher"}
	reply, err := f.service.Reply(ctx, teacher, root.ID, "named", in)
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.service.Get(ctx, student, reply.ID)
	if err != nil || len(view.Author.Disclosures) != 2 {
		t.Fatal(view, err)
	}
	revoked := facts.facts.Attributes["real_name"]
	revoked.MayDisclose = false
	facts.facts.Attributes["real_name"] = revoked
	if _, err = f.service.Get(ctx, student, reply.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("required name was silently removed", err)
	}
	if _, err = f.service.Action(ctx, student, reply.ID, "hidden-vote", r.Action{Action: "vote", Vote: 1}); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("hidden reply accepted vote", err)
	}
	if _, err = f.service.Reply(ctx, student, reply.ID, "hidden-reply", r.Input{Kind: "reply", TargetID: "course", Body: "Reply to hidden teacher"}); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("hidden reply accepted reply", err)
	}
	if _, err = f.service.Composer(ctx, student, "reply", "course", "", reply.ID); !errors.Is(err, r.ErrNotFound) {
		t.Fatal("hidden reply exposed composer", err)
	}
	if _, err = f.service.GetOwn(ctx, teacher, reply.ID); err != nil {
		t.Fatal("author lost private record", err)
	}
	facts.facts.Attributes["real_name"] = r.DisclosureFact{Value: "Synthetic Name", Source: "trusted", Revision: "2", ValidUntil: f.now.Add(time.Hour), MayDisclose: true}
	other := create(t, f, "other-course", input("unrelated-course", "autumn", 3))
	in.TargetID = "unrelated-course"
	in.Disclosures = []string{"teacher"}
	independent, err := f.service.Reply(ctx, teacher, other.ID, "badge-only", in)
	if err != nil {
		t.Fatal(err)
	}
	view, err = f.service.Get(ctx, student, independent.ID)
	if err != nil || len(view.Author.Disclosures) != 1 || view.Author.Disclosures[0].Key != "teacher" {
		t.Fatal(view, err)
	}
	f.catalog.failed = true
	if _, err = f.service.Reply(ctx, teacher, root.ID, "unknown-teaching", r.Input{Kind: "reply", TargetID: "course", Body: "Cannot silently fall back"}); !errors.Is(err, r.ErrUnavailable) {
		t.Fatal(err)
	}
}
