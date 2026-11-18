package reviews

import (
	"context"
	"errors"
	"time"
)

func (s *Service) contentScope(ctx context.Context, p Principal, school string) (string, error) {
	if p.Subject == "" {
		return "", ErrUnauthenticated
	}
	if school != "" && !validID(school) {
		return "", ErrInput
	}
	grant, e := s.authorize(ctx, p, "read_full", school)
	if e != nil {
		if errors.Is(e, ErrForbidden) {
			return "", forbiddenErr("admission_required", "")
		}
		return "", e
	}
	if !validID(grant.SchoolID) {
		return "", ErrUnavailable
	}
	if school != "" && school != grant.SchoolID {
		return "", ErrForbidden
	}
	return grant.SchoolID, nil
}
func (s *Service) ReadStatsPage(ctx context.Context, p Principal, kind, target string, from, until *time.Time, after string, limit int) (Statistics, error) {
	school, e := s.contentScope(ctx, p, "")
	if e != nil {
		return Statistics{}, e
	}
	object, e := s.catalogTarget(ctx, kind, target)
	if e != nil {
		return Statistics{}, e
	}
	if object.SchoolID != school {
		return Statistics{}, ErrForbidden
	}
	return s.statsPage(ctx, kind, target, from, until, after, limit, school)
}
func (s *Service) ReadPresentation(ctx context.Context, p Principal, school string) (PresentationView, error) {
	if !validID(school) {
		return PresentationView{}, ErrInput
	}
	scope, e := s.contentScope(ctx, p, school)
	if e != nil {
		return PresentationView{}, e
	}
	return s.Presentation(ctx, scope)
}
