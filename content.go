package reviews

import (
	"context"
	"time"
)

func (s *Service) contentScope(ctx context.Context, p Principal, school string) (string, error) {
	if p.Subject == "" {
		return "", ErrUnauthenticated
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	grant, e := s.authorize(callCtx, p, "read_full", school)
	if e != nil {
		return "", e
	}
	if !validID(grant.SchoolID) || school != "" && school != grant.SchoolID {
		return "", ErrUnavailable
	}
	return grant.SchoolID, nil
}
func (s *Service) ReadStatsPage(ctx context.Context, p Principal, kind, target string, from, until *time.Time, after string, limit int) (Statistics, error) {
	school, e := s.contentScope(ctx, p, "")
	if e != nil {
		return Statistics{}, e
	}
	object, e := s.Catalog.Target(ctx, kind, target)
	if e != nil {
		return Statistics{}, e
	}
	if object.SchoolID != school {
		return Statistics{}, ErrForbidden
	}
	return s.statsPage(ctx, kind, target, from, until, after, limit, school)
}
func (s *Service) ReadPresentation(ctx context.Context, p Principal, school string) (PresentationView, error) {
	scope, e := s.contentScope(ctx, p, school)
	if e != nil {
		return PresentationView{}, e
	}
	return s.Presentation(ctx, scope)
}
