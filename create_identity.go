package reviews

import (
	"context"
	"errors"
	"time"
)

type creationReplay struct{ key, digest string }

func (s *Service) previousCreationKeys(ctx context.Context, p Principal, key string, in Input, canonical string) ([]creationReplay, error) {
	if in.Kind != "course" {
		return nil, nil
	}
	ids, err := s.equivalentIDs(ctx, "course", canonical)
	if err != nil {
		return nil, err
	}
	out := []creationReplay{}
	for _, id := range ids {
		if id == canonical {
			continue
		}
		original := in
		original.TargetID = id
		out = append(out, creationReplay{hash([]string{p.Subject, "create", id, key}), hash(original)})
	}
	return out, nil
}

// Existing receipts were keyed by the submitted ID. Check them before creating
// a canonical receipt, including after the course mapping changed. An expired
// receipt with a retained entry remains a conflict rather than a second write.
func replayPreviousCreation(t Transaction, keys []creationReplay, now time.Time) (Result, bool, error) {
	for _, key := range keys {
		result, ok, err := replay(t, key.key, key.digest, now)
		if err != nil || ok {
			return result, ok, err
		}
		if _, err := t.Entry(key.key); err == nil {
			return Result{}, false, ErrConflict
		} else if !errors.Is(err, ErrNotFound) {
			return Result{}, false, err
		}
	}
	return Result{}, false, nil
}
