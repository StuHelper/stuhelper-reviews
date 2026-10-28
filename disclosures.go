package reviews

import (
	"context"
	"slices"
	"strings"
	"time"
)

// These facts are private input from verification/identity. A value may be
// disclosed only when both its source validity and field authorization permit it.
type DisclosureFact struct {
	Value, Source, Revision string
	ValidUntil              time.Time
	MayDisclose             bool
}
type DisclosureFacts struct {
	StudentVerified, TeacherVerified bool
	TeacherIDs                       []string
	ValidUntil                       time.Time
	Attributes                       map[string]DisclosureFact
}
type DisclosureProvider interface {
	Facts(context.Context, Principal, string, string, string) (DisclosureFacts, error)
}
type TeachingCatalog interface {
	HasTaught(context.Context, []string, string) (bool, error)
}
type DisclosureOption struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Required bool   `json:"required"`
}

var disclosureKeys = []string{"gender", "department", "enrollment", "real_name", "teacher", "organization"}

func validDisclosureKeys(keys []string) bool {
	if len(keys) > len(disclosureKeys) {
		return false
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if !slices.Contains(disclosureKeys, key) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

type disclosureContext struct {
	options            []DisclosureOption
	teacher, ownCourse bool
}

func (s *Service) disclosureContext(ctx context.Context, p Principal, school, course, term string, reply bool) (disclosureContext, error) {
	out := disclosureContext{options: []DisclosureOption{}}
	if p.Subject == "" {
		return out, nil
	}
	if s.Disclosures == nil {
		return out, ErrUnavailable
	}
	facts, err := s.Disclosures.Facts(ctx, p, school, course, term)
	if err != nil {
		return out, ErrUnavailable
	}
	if !s.now().Before(facts.ValidUntil) {
		return out, ErrUnavailable
	}
	if !facts.StudentVerified && !facts.TeacherVerified {
		return out, nil
	}
	out.teacher = facts.TeacherVerified
	if facts.TeacherVerified {
		if len(facts.TeacherIDs) < 1 || len(facts.TeacherIDs) > 100 {
			return out, ErrUnavailable
		}
		for _, id := range facts.TeacherIDs {
			if !validID(id) {
				return out, ErrUnavailable
			}
		}
		if reply && course != "" {
			catalog, ok := s.Catalog.(TeachingCatalog)
			if !ok {
				return out, ErrUnavailable
			}
			out.ownCourse, err = catalog.HasTaught(ctx, facts.TeacherIDs, course)
			if err != nil {
				return out, ErrUnavailable
			}
		}
	}
	for _, key := range disclosureKeys {
		if (key == "teacher" || key == "organization") && !facts.TeacherVerified {
			continue
		}
		if key == "department" && !facts.StudentVerified {
			continue
		}
		if key == "enrollment" && (course == "" || !facts.StudentVerified) {
			continue
		}
		fact, ok := facts.Attributes[key]
		if !ok || !fact.MayDisclose || !s.now().Before(fact.ValidUntil) || strings.TrimSpace(fact.Source) == "" || strings.TrimSpace(fact.Revision) == "" || !textOK(fact.Value, 256) || strings.TrimSpace(fact.Value) == "" {
			continue
		}
		if key == "teacher" && fact.Value != "verified" {
			continue
		}
		if key == "enrollment" && !slices.Contains([]string{"enrolled", "not_enrolled"}, fact.Value) {
			continue
		}
		out.options = append(out.options, DisclosureOption{key, fact.Value, out.ownCourse && (key == "real_name" || key == "teacher")})
	}
	return out, nil
}
func (c disclosureContext) permits(keys []string) bool {
	for _, key := range keys {
		if !slices.ContainsFunc(c.options, func(o DisclosureOption) bool { return o.Key == key }) {
			return false
		}
	}
	if c.ownCourse {
		for _, key := range []string{"real_name", "teacher"} {
			if !slices.Contains(keys, key) {
				return false
			}
		}
	}
	return true
}
func (c disclosureContext) canDiscloseRequired() bool {
	return !c.ownCourse || (slices.ContainsFunc(c.options, func(o DisclosureOption) bool { return o.Key == "real_name" }) && slices.ContainsFunc(c.options, func(o DisclosureOption) bool { return o.Key == "teacher" }))
}
func (s *Service) validateDisclosures(ctx context.Context, p Principal, school, course, term string, in Input) (disclosureContext, error) {
	if len(in.Disclosures) == 0 && in.Kind != "reply" {
		return disclosureContext{}, nil
	}
	c, err := s.disclosureContext(ctx, p, school, course, term, in.Kind == "reply")
	if err != nil {
		return c, err
	}
	if !c.permits(in.Disclosures) {
		return c, ErrForbidden
	}
	return c, nil
}
func (s *Service) replyGrant(ctx context.Context, p Principal, root Entry, c disclosureContext) (Grant, error) {
	if c.teacher {
		return s.authorize(ctx, p, "reply_teacher", root.SchoolID)
	}
	return s.authorize(ctx, p, "reply", root.SchoolID)
}

// A dependency failure never reuses cached private values. Required teacher
// identification must remain available for a teacher reply to be publicly read.
func (s *Service) disclosed(ctx context.Context, e Entry) ([]DisclosureOption, bool) {
	if len(e.Input.Disclosures) == 0 && !e.TeacherReply {
		return nil, true
	}
	course, term := e.DisclosureCourse, e.DisclosureTerm
	if e.Input.Kind == "course" {
		course = e.Input.TargetID
		term = e.Input.TermID
	}
	c, err := s.disclosureContext(ctx, Principal{Subject: e.Owner}, e.SchoolID, course, term, false)
	if err != nil {
		return nil, !e.TeacherReply
	}
	out := []DisclosureOption{}
	for _, o := range c.options {
		if slices.Contains(e.Input.Disclosures, o.Key) {
			o.Required = e.TeacherReply && (o.Key == "real_name" || o.Key == "teacher")
			out = append(out, o)
		}
	}
	if e.TeacherReply {
		for _, key := range []string{"real_name", "teacher"} {
			if !slices.ContainsFunc(out, func(o DisclosureOption) bool { return o.Key == key }) {
				return nil, false
			}
		}
	}
	return out, true
}

func (s *Service) requirePublicDisclosure(ctx context.Context, id string) error {
	var entry Entry
	err := s.Repo.Transaction(ctx, func(t Transaction) error { var e error; entry, e = t.Entry(id); return e })
	if err != nil {
		return err
	}
	if entry.TeacherReply {
		_, public := s.disclosed(ctx, entry)
		if !public {
			return ErrNotFound
		}
	}
	return nil
}
