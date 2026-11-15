package reviews

import (
	"context"
	"errors"
)

type PendingCourse struct {
	CourseID   string `json:"course_id"`
	CourseName string `json:"course_name"`
	TermID     string `json:"term_id"`
	TermName   string `json:"term_name"`
}
type PendingCourses struct {
	State  string          `json:"state"`
	Reason string          `json:"reason"`
	Count  *int            `json:"count"`
	Items  []PendingCourse `json:"items"`
}
type OwnOverview struct {
	ReviewCount    int64          `json:"review_count"`
	DraftCount     int64          `json:"draft_count"`
	PendingCourses PendingCourses `json:"pending_courses"`
}

// OwnReviewCourses must enforce the current source authorization, not read a
// stale enrollment table when the owner source is missing or inaccessible.
type OwnReviewCourses interface {
	OwnReviewCourses(context.Context, Principal) (OwnCourseCandidates, error)
}
type OwnCourseCandidates struct {
	Owner, SchoolID string
	Items           []PendingCourse
}

func (s *Service) OwnOverview(ctx context.Context, p Principal) (OwnOverview, error) {
	var out OwnOverview
	if p.Subject == "" {
		return out, ErrUnauthenticated
	}
	err := s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(SummaryTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out, err = port.OwnReviewOverview(p.Subject, s.now())
		return err
	})
	if err != nil {
		return out, err
	}
	out.PendingCourses = PendingCourses{State: "unavailable", Reason: "enrollment_unavailable", Items: []PendingCourse{}}
	school, err := s.contentScope(ctx, p, "")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return OwnOverview{}, err
		}
		return out, nil
	}
	courses, ok := s.Catalog.(OwnReviewCourses)
	refs, refsOK := s.Catalog.(SummaryCatalog)
	if !ok || !refsOK {
		return out, nil
	}
	candidates, err := courses.OwnReviewCourses(ctx, p)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return OwnOverview{}, err
		}
		return out, nil
	}
	if candidates.Owner != p.Subject || candidates.SchoolID != school || len(candidates.Items) > 2000 {
		return OwnOverview{}, ErrUnavailable
	}
	for _, row := range candidates.Items {
		if !validID(row.CourseID) || !validID(row.TermID) || !textOK(row.CourseName, 256) || !textOK(row.TermName, 256) {
			return OwnOverview{}, ErrUnavailable
		}
	}
	if len(candidates.Items) == 0 {
		count := 0
		out.PendingCourses = PendingCourses{State: "available", Count: &count, Items: []PendingCourse{}}
		return out, nil
	}
	courseAliases, err := refs.SummaryAliases(ctx, school, "course")
	if err != nil {
		return OwnOverview{}, err
	}
	termAliases, err := refs.SummaryAliases(ctx, school, "term")
	if err != nil {
		return OwnOverview{}, err
	}
	err = s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(SummaryTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out.PendingCourses, err = port.PendingReviewCourses(p.Subject, candidates.Items, courseAliases, termAliases, s.now())
		return err
	})
	return out, err
}
