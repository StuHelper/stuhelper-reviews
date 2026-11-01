package reviews

import "context"

// AnonymizeOwner is a trusted module port, not an HTTP endpoint. The permanent
// marker also covers in-flight publications and later administrative changes.
func (s *Service) AnonymizeOwner(ctx context.Context, owner, id string) error {
	if owner == "" || len(owner) > 2048 || len(id) != 36 {
		return ErrInput
	}
	return s.Repo.Transaction(ctx, func(t Transaction) error { return t.CloseOwner(owner, id) })
}
