package reviews

import (
	"context"
	"errors"
	"time"
)

// DiagnosticEvent exposes strictly desensitized operational telemetry.
// It never carries user subjects, school IDs, query parameters, SQL text, or review bodies.
type DiagnosticEvent struct {
	Operation  string
	Dependency string
	Category   string
	SQLState   string
	Duration   time.Duration
}

type DiagnosticHook func(DiagnosticEvent)

func ClassifyDiagnosticCategory(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrUnauthenticated):
		return "unauthenticated"
	case errors.Is(err, ErrInput):
		return "invalid_input"
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrEnrollment):
		return "forbidden"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrWindow):
		return "conflict"
	default:
		return "unavailable"
	}
}

func SanitizeSQLState(code string) string {
	if len(code) != 5 {
		return ""
	}
	for i := 0; i < 5; i++ {
		c := code[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return ""
		}
	}
	return code
}

func (s *Service) emitDiagnostic(op, dep string, start time.Time, err error) {
	if err == nil || s == nil || s.OnDiagnostic == nil {
		return
	}
	s.OnDiagnostic(DiagnosticEvent{
		Operation:  op,
		Dependency: dep,
		Category:   ClassifyDiagnosticCategory(err),
		Duration:   time.Since(start),
	})
}

func (s *Service) catalogTargetFor(ctx context.Context, op, kind, id string) (Target, error) {
	if s.Catalog == nil {
		err := ErrUnavailable
		s.emitDiagnostic(op, "catalog", time.Now(), err)
		return Target{}, err
	}
	start := time.Now()
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	out, err := s.Catalog.Target(callCtx, kind, id)
	s.emitDiagnostic(op, "catalog", start, err)
	return out, wrapExternalErr(ctx, err)
}

func (s *Service) catalogEvidenceFor(ctx context.Context, op string, p Principal, course, term string) ([]Evidence, error) {
	if s.Catalog == nil {
		err := ErrUnavailable
		s.emitDiagnostic(op, "catalog", time.Now(), err)
		return nil, err
	}
	start := time.Now()
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	out, err := s.Catalog.Evidence(callCtx, p, course, term)
	s.emitDiagnostic(op, "catalog", start, err)
	return out, wrapExternalErr(ctx, err)
}
