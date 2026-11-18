package reviews

import (
	"context"
	"errors"
	"time"
)

const defaultExternalTimeout = 3 * time.Second

func (s *Service) externalTimeout() time.Duration {
	if s != nil && s.Config.ExternalTimeout > 0 {
		return s.Config.ExternalTimeout
	}
	return defaultExternalTimeout
}

func (s *Service) externalCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.externalTimeout())
}

func wrapExternalErr(parent context.Context, err error) error {
	if err == nil {
		return nil
	}
	if parent.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return ErrUnavailable
	}
	return err
}

func (s *Service) catalogTarget(ctx context.Context, kind, id string) (Target, error) {
	if s.Catalog == nil {
		return Target{}, ErrUnavailable
	}
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := s.Catalog.Target(callCtx, kind, id)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) catalogEvidence(ctx context.Context, p Principal, course, term string) ([]Evidence, error) {
	if s.Catalog == nil {
		return nil, ErrUnavailable
	}
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := s.Catalog.Evidence(callCtx, p, course, term)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) catalogHasTaught(ctx context.Context, port TeachingCatalog, teachers []string, course string) (bool, error) {
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := port.HasTaught(callCtx, teachers, course)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) catalogEquivalentIDs(ctx context.Context, port ReferenceCatalog, kind, id string) ([]string, error) {
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := port.EquivalentIDs(callCtx, kind, id)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) catalogCanonicalCourses(ctx context.Context, port ReferenceCatalog, ids []string) (map[string]string, error) {
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := port.CanonicalCourses(callCtx, ids)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) catalogSummaryTargets(ctx context.Context, port SummaryCatalog, kind string, ids []string) (map[string]SummaryReference, error) {
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := port.SummaryTargets(callCtx, kind, ids)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) catalogSummaryAliases(ctx context.Context, port SummaryCatalog, school, kind string) (map[string]string, error) {
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := port.SummaryAliases(callCtx, school, kind)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) catalogOwnReviewCourses(ctx context.Context, port OwnReviewCourses, p Principal) (OwnCourseCandidates, error) {
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := port.OwnReviewCourses(callCtx, p)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) disclosureFacts(ctx context.Context, p Principal, school, course, term string) (DisclosureFacts, error) {
	if s.Disclosures == nil {
		return DisclosureFacts{}, ErrUnavailable
	}
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := s.Disclosures.Facts(callCtx, p, school, course, term)
	return v, wrapExternalErr(ctx, err)
}

func (s *Service) lookupProfile(ctx context.Context, owner string) (Profile, error) {
	if s.Profiles == nil {
		return Profile{}, ErrUnavailable
	}
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	v, err := s.Profiles.Lookup(callCtx, owner)
	return v, wrapExternalErr(ctx, err)
}
