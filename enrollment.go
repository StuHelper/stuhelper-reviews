package reviews

import (
	"context"
	"errors"
	"slices"
)

type PublicCourseTeachers interface {
	CourseTermTeachers(context.Context, string, string) ([]string, error)
}

type PublicCourseTerms interface {
	CourseOfferingTerms(context.Context, string) ([]CourseOfferingTerm, error)
}

func (s *Service) reviewEnrollment(ctx context.Context, p Principal, course, term string) ([]Evidence, string, error) {
	evidence, err := s.catalogEvidenceFor(ctx, "prepare_evidence", p, course, term)
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if err != nil {
		if !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrForbidden) {
			return nil, "", err
		}
		return nil, "unknown", nil
	}
	if len(evidence) > 1000 {
		return nil, "unknown", nil
	}
	for _, v := range evidence {
		if !validID(v.TermID) || (term != "" && v.TermID != term) || v.ID == "" || v.SourceID == "" || v.Revision < 1 || v.FetchedAt.IsZero() {
			return nil, "unknown", nil
		}
	}
	if len(evidence) > 0 {
		return evidence, "enrolled", nil
	}
	return nil, "not_enrolled", nil
}

func (s *Service) courseTeachers(ctx context.Context, course, term string) ([]string, error) {
	port, ok := s.Catalog.(PublicCourseTeachers)
	if !ok {
		return nil, ErrUnavailable
	}
	call, cancel := s.externalCtx(ctx)
	defer cancel()
	ids, err := port.CourseTermTeachers(call, course, term)
	if errors.Is(err, ErrInput) {
		return nil, inputFieldErr("term_not_offered", "term_id")
	}
	if err != nil {
		return nil, wrapExternalErr(ctx, err)
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	for _, id := range ids {
		if !validID(id) {
			return nil, ErrUnavailable
		}
	}
	return slices.Compact(ids), nil
}

func (s *Service) courseComposerCandidates(ctx context.Context, p Principal, course, term string) ([]ComposerTerm, []ComposerTeacher, error) {
	port, ok := s.Catalog.(PublicCourseTerms)
	if !ok {
		return nil, nil, ErrUnavailable
	}
	call, cancel := s.externalCtx(ctx)
	terms, err := port.CourseOfferingTerms(call, course)
	cancel()
	if err != nil {
		return nil, nil, wrapExternalErr(ctx, err)
	}
	var ids []string
	if term != "" {
		if !slices.ContainsFunc(terms, func(candidate CourseOfferingTerm) bool { return candidate.ID == term }) {
			return nil, nil, inputFieldErr("term_not_offered", "term_id")
		}
		ids, err = s.courseTeachers(ctx, course, term)
		if err != nil {
			return nil, nil, err
		}
	}
	evidence, _, err := s.reviewEnrollment(ctx, p, course, "")
	if err != nil {
		return nil, nil, err
	}
	enrolledTerms, enrolledTeachers := map[string]bool{}, map[string]bool{}
	for _, row := range evidence {
		enrolledTerms[row.TermID] = true
		if row.TermID == term {
			for _, id := range row.TeacherIDs {
				enrolledTeachers[id] = true
			}
		}
	}
	termOptions := make([]ComposerTerm, 0, len(terms))
	for _, option := range terms {
		termOptions = append(termOptions, ComposerTerm{CourseOfferingTerm: option, Enrolled: enrolledTerms[option.ID]})
	}
	teacherOptions := make([]ComposerTeacher, 0, len(ids))
	for _, id := range ids {
		teacherOptions = append(teacherOptions, ComposerTeacher{ID: id, Enrolled: enrolledTeachers[id]})
	}
	return termOptions, teacherOptions, nil
}
