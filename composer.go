package reviews

import (
	"context"
	"errors"
)

// Composer exposes decisions and consent options from current server facts.
func (s *Service) Composer(ctx context.Context, p Principal, kind, target, term, parent string) (Composer, error) {
	out := Composer{Requirements: []Requirement{}, TeacherIDs: []string{}, Identity: "choice", Disclosures: []DisclosureOption{}}
	if !validID(target) || (term != "" && !validID(term)) || (parent != "" && !validID(parent)) || (kind != "course" && kind != "mentor" && kind != "reply") {
		return out, ErrInput
	}
	if p.Subject == "" {
		out.State = "unauthenticated"
		out.Requirements = append(out.Requirements, Requirement{"login", "missing"})
		return out, nil
	}
	out.Requirements = append(out.Requirements, Requirement{"login", "satisfied"})
	var grant Grant
	var checkErr error
	var school, course string
	if kind == "reply" {
		var root Entry
		var identity Identity
		if parent == "" {
			return out, ErrInput
		}
		if err := s.requirePublicDisclosure(ctx, parent); err != nil {
			return out, err
		}
		err := s.Repo.Transaction(ctx, func(t Transaction) error {
			base, e := s.accessible(t, parent)
			if e != nil {
				return e
			}
			root, e = s.accessible(t, base.RootID)
			if e != nil {
				return e
			}
			if base.Input.TargetID != target {
				return ErrInput
			}
			identity, e = t.Identity(base.RootID, p.Subject)
			if errors.Is(e, ErrNotFound) {
				return nil
			}
			return e
		})
		if err != nil {
			return out, err
		}
		school = root.SchoolID
		if identity.Subject != "" {
			out.Identity = "anonymous"
			if identity.Signed {
				out.Identity = "signed"
			}
		}
		if root.Input.Kind == "course" {
			course = root.Input.TargetID
			term = root.Input.TermID
		}
		disclosure, err := s.disclosureContext(ctx, p, school, course, term, true)
		if err != nil {
			out.State = "unavailable"
			return out, nil
		}
		out.Disclosures = disclosure.options
		out.TeacherReply = disclosure.ownCourse
		if !disclosure.canDiscloseRequired() {
			out.State = "disclosure_required"
			return out, nil
		}
		grant, checkErr = s.replyGrant(ctx, p, root, disclosure)
	} else {
		targetKind := "course"
		if kind == "mentor" {
			targetKind = "teacher"
		} else {
			course = target
		}
		object, err := s.Catalog.Target(ctx, targetKind, target)
		if err != nil {
			return out, err
		}
		school = object.SchoolID
		if kind == "course" && term == "" {
			grant, checkErr = s.authorize(ctx, p, "publish_course", school)
			if checkErr == nil {
				out.State = "choose_term"
			}
		} else {
			_, _, out.TeacherIDs, grant, checkErr = s.prepare(ctx, p, Input{Kind: kind, TargetID: target, TermID: term})
			if out.TeacherIDs == nil {
				out.TeacherIDs = []string{}
			}
		}
		if disclosure, err := s.disclosureContext(ctx, p, school, course, term, false); err == nil {
			out.Disclosures = disclosure.options
		}
	}
	out.RuleVersion = grant.RuleVersion
	out.Requirements = append(out.Requirements, grant.Requirements...)
	switch {
	case checkErr == nil:
		if out.State != "choose_term" {
			out.State = "ready"
			out.Allowed = true
		}
	case errors.Is(checkErr, ErrEnrollment):
		out.State = "enrollment_required"
	case errors.Is(checkErr, ErrForbidden):
		out.State = "forbidden"
	case errors.Is(checkErr, ErrUnavailable):
		out.State = "unavailable"
	default:
		return out, checkErr
	}
	return out, nil
}
