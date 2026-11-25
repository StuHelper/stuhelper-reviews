package reviews

import (
	"context"
	"errors"
	"slices"
)

type PublicCourseTeachers interface {
	CourseTermTeachers(context.Context, string, string) ([]string, error)
}

func (s *Service) reviewEnrollment(ctx context.Context, p Principal, course, term string) ([]Evidence, []string, string, error) {
	evidence, err := s.catalogEvidenceFor(ctx, "prepare_evidence", p, course, term)
	if ctx.Err() != nil {
		return nil, nil, "", ctx.Err()
	}
	status := "not_enrolled"
	if err != nil {
		if !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrForbidden) {
			return nil, nil, "", err
		}
		status = "unknown"
		evidence = nil
	}
	if len(evidence) > 1000 {
		return nil, nil, "", ErrUnavailable
	}
	if len(evidence) > 0 {
		ids := []string{}
		for _, v := range evidence {
			if v.TermID != term || v.ID == "" || v.SourceID == "" || v.Revision < 1 || v.FetchedAt.IsZero() {
				return nil, nil, "", ErrUnavailable
			}
			ids = append(ids, v.TeacherIDs...)
		}
		slices.Sort(ids)
		return evidence, slices.Compact(ids), "enrolled", nil
	}
	port, ok := s.Catalog.(PublicCourseTeachers)
	if !ok {
		return nil, nil, "", ErrUnavailable
	}
	call, cancel := s.externalCtx(ctx)
	defer cancel()
	ids, err := port.CourseTermTeachers(call, course, term)
	return nil, ids, status, wrapExternalErr(ctx, err)
}
