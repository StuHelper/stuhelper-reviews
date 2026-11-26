package reviews

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Catalog owns aliases; reviews keeps its immutable publication evidence and
// queries all equivalent IDs without updating another module's tables.
type ReferenceCatalog interface {
	EquivalentIDs(context.Context, string, string) ([]string, error)
	CanonicalCourses(context.Context, []string) (map[string]string, error)
}

func (s *Service) equivalentIDs(ctx context.Context, kind, id string) ([]string, error) {
	port, ok := s.Catalog.(ReferenceCatalog)
	if !ok {
		return []string{id}, nil
	}
	ids, err := s.catalogEquivalentIDs(ctx, port, kind, id)
	if err != nil {
		return nil, err
	}
	if len(ids) < 1 || len(ids) > 1000 {
		return nil, ErrUnavailable
	}
	for _, v := range ids {
		if !validID(v) {
			return nil, ErrUnavailable
		}
	}
	return ids, nil
}
func (s *Service) canonicalAggregates(ctx context.Context, rows []Aggregate) ([]Aggregate, error) {
	port, ok := s.Catalog.(ReferenceCatalog)
	if !ok || len(rows) == 0 {
		return rows, nil
	}
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, row.CourseID)
	}
	mapping, err := s.catalogCanonicalCourses(ctx, port, ids)
	if err != nil {
		return nil, err
	}
	grouped := map[string]Aggregate{}
	for _, row := range rows {
		id := mapping[row.CourseID]
		if !validID(id) {
			return nil, ErrUnavailable
		}
		value := grouped[id]
		value.CourseID = id
		for i := range 5 {
			value.Sums[i] += row.Sums[i]
			value.Counts[i] += row.Counts[i]
		}
		grouped[id] = value
	}
	out := []Aggregate{}
	for _, row := range grouped {
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b Aggregate) int { return strings.Compare(a.CourseID, b.CourseID) })
	return out, nil
}

func (s *Service) selectedTeachers(ctx context.Context, selected, teachers []string) ([]string, error) {
	if len(selected) == 0 {
		if len(teachers) > maxSelectedTeachers {
			return nil, ErrUnavailable
		}
		return teachers, nil
	}
	out := make([]string, 0, len(selected))
	for _, id := range selected {
		if _, found := slices.BinarySearch(teachers, id); !found {
			equivalent, err := s.equivalentIDs(ctx, "teacher", id)
			if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInput) && !errors.Is(err, ErrForbidden) {
				return nil, err
			}
			match := slices.IndexFunc(equivalent, func(candidate string) bool {
				_, found := slices.BinarySearch(teachers, candidate)
				return found
			})
			if match < 0 {
				return nil, forbiddenErr("teacher_not_candidate", "teacher_ids")
			}
			id = equivalent[match]
		}
		if slices.Contains(out, id) {
			return nil, inputFieldErr("invalid_teacher_ids", "teacher_ids")
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}
