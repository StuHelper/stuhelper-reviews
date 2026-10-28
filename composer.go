package reviews

import (
	"context"
	"errors"
	"slices"
)

// Composer reports current service decisions; clients do not reconstruct eligibility rules.
func (s *Service) Composer(ctx context.Context, p Principal, kind, target, term, parent string) (Composer, error) {
	out := Composer{Requirements: []Requirement{}, TeacherIDs: []string{}, Identity: "choice"}
	if !slices.Contains([]string{"course", "mentor", "reply"}, kind) || !validID(target) || (term != "" && !validID(term)) || (parent != "" && !validID(parent)) {
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
	if kind == "reply" {
		var base Entry
		var identity Identity
		if parent == "" {
			return out, ErrInput
		}
		err := s.Repo.Transaction(ctx, func(t Transaction) error {
			var err error
			base, err = s.accessible(t, parent)
			if err != nil {
				return err
			}
			if base.Input.TargetID != target {
				return ErrInput
			}
			identity, err = t.Identity(base.RootID, p.Subject)
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		})
		if err != nil {
			return out, err
		}
		if identity.Subject != "" {
			out.Identity = "anonymous"
			if identity.Signed {
				out.Identity = "signed"
			}
		}
		grant, checkErr = s.authorize(ctx, p, "reply", base.SchoolID)
	} else if kind == "course" && term == "" {
		object, err := s.Catalog.Target(ctx, "course", target)
		if err != nil {
			return out, err
		}
		grant, checkErr = s.authorize(ctx, p, "publish_course", object.SchoolID)
		if checkErr == nil {
			out.State = "choose_term"
		}
	} else {
		_, _, out.TeacherIDs, grant, checkErr = s.prepare(ctx, p, Input{Kind: kind, TargetID: target, TermID: term})
		if out.TeacherIDs == nil {
			out.TeacherIDs = []string{}
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
