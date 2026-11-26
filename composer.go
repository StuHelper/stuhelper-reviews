package reviews

import (
	"context"
	"errors"
)

// Composer exposes decisions and consent options from current server facts.
func (s *Service) Composer(ctx context.Context, p Principal, kind, target, term, parent string) (Composer, error) {
	scope, err := s.contentScope(ctx, p, "")
	if err != nil {
		return Composer{}, err
	}
	out := Composer{Requirements: []Requirement{}, Terms: []ComposerTerm{}, Teachers: []ComposerTeacher{}, Identity: "choice", Disclosures: []DisclosureOption{}}
	if !validID(target) || (term != "" && !validID(term)) || (parent != "" && !validID(parent)) || (kind != "course" && kind != "mentor" && kind != "reply") {
		return out, ErrInput
	}
	out.Requirements = append(out.Requirements, Requirement{"login", "satisfied"})
	var grant Grant
	var checkErr error
	var school, course string
	if kind == "reply" {
		var base, root Entry
		var identity Identity
		if parent == "" {
			return out, ErrInput
		}
		if err := s.requirePublicDisclosure(ctx, parent); err != nil {
			return out, err
		}
		err := s.Repo.Transaction(ctx, func(t Transaction) error {
			var e error
			base, e = s.accessible(t, parent)
			if e != nil {
				return e
			}
			root, e = s.accessible(t, base.RootID)
			if e != nil {
				return e
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
		if !s.sameTarget(ctx, root.Input.Kind, base.Input.TargetID, target) {
			return out, ErrInput
		}
		school = root.SchoolID
		if school != scope {
			return Composer{}, ErrForbidden
		}
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
			if err := s.attachOwnComposerContext(ctx, p, kind, target, term, parent, &out); err != nil {
				return Composer{}, err
			}
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
		object, err := s.catalogTarget(ctx, targetKind, target)
		if err != nil {
			return out, err
		}
		school = object.SchoolID
		if school != scope {
			return Composer{}, ErrForbidden
		}
		if object.Inactive {
			checkErr = ErrForbidden
		} else {
			grant, checkErr = s.authorize(ctx, p, publishAction(kind), school)
		}
		if checkErr == nil && kind == "course" {
			terms, teachers, err := s.courseComposerCandidates(ctx, p, object.ID, term)
			checkErr = err
			if err == nil {
				out.Terms, out.Teachers = terms, teachers
				if term == "" {
					out.State = "choose_term"
				}
			}
		}
		if checkErr == nil {
			disclosure, err := s.disclosureContext(ctx, p, school, course, term, false)
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if err != nil {
				checkErr = err
			} else {
				out.Disclosures = disclosure.options
			}
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
	case errors.Is(checkErr, ErrForbidden):
		out.State = "forbidden"
	case errors.Is(checkErr, ErrUnavailable):
		out.State = "unavailable"
	default:
		return out, checkErr
	}
	if err := s.attachOwnComposerContext(ctx, p, kind, target, term, parent, &out); err != nil {
		return Composer{}, err
	}
	return out, nil
}
