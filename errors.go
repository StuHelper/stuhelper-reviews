package reviews

import "errors"

// ErrorDetail carries a stable machine-readable subcode, optional form/parameter
// field name, and optional current server revision while preserving sentinel errors.Is checks.
type ErrorDetail struct {
	Base            error
	Subcode         string
	Field           string
	CurrentRevision *int64
}

func (e ErrorDetail) Error() string {
	if e.Base != nil {
		return e.Base.Error()
	}
	return "unavailable"
}

func (e ErrorDetail) Unwrap() error {
	return e.Base
}

func NewErrorDetail(base error, subcode, field string, currentRevision *int64) error {
	var existing ErrorDetail
	if errors.As(base, &existing) {
		return base
	}
	var rev *int64
	if currentRevision != nil {
		v := *currentRevision
		rev = &v
	}
	return ErrorDetail{
		Base:            base,
		Subcode:         subcode,
		Field:           field,
		CurrentRevision: rev,
	}
}

func inputFieldErr(subcode, field string) error {
	return NewErrorDetail(ErrInput, subcode, field, nil)
}

func forbiddenErr(subcode, field string) error {
	return NewErrorDetail(ErrForbidden, subcode, field, nil)
}

func conflictStateErr(subcode, field string) error {
	return NewErrorDetail(ErrConflict, subcode, field, nil)
}

func revisionConflictErr(current int64) error {
	return NewErrorDetail(ErrConflict, "revision_conflict", "revision", &current)
}

func idempotencyConflictErr() error {
	return NewErrorDetail(ErrConflict, "idempotency_conflict", "idempotency_key", nil)
}
