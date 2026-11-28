package reviews

import (
	"errors"
	"time"
)

// ErrorDetail carries a stable subcode and optional field, current revision, or retry time.
// It preserves sentinel errors.Is checks.
type ErrorDetail struct {
	Base            error
	Subcode         string
	Field           string
	CurrentRevision *int64
	RetryAt         *time.Time
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

// Only report conditions that the active rule explicitly found missing.
func eligibilityError(grant Grant, fallback string) error {
	for _, requirement := range grant.Requirements {
		if requirement.State != "missing" {
			continue
		}
		switch requirement.Key {
		case "phone_bound":
			return forbiddenErr("phone_binding_required", "")
		case "campus_eligibility":
			return forbiddenErr("campus_verification_required", "")
		case "identity_verified":
			return forbiddenErr("identity_verification_required", "")
		case "content_access":
			return forbiddenErr("content_admission_required", "")
		}
	}
	return forbiddenErr(fallback, "")
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
