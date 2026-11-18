package reviews

import (
	"context"
	"errors"
	"slices"
	"time"
)

type Presentation struct {
	SchoolID    string                `json:"school_id"`
	Revision    int64                 `json:"revision"`
	RatingTexts map[string][][]string `json:"rating_texts"`
}
type PresentationView struct {
	Effective  Presentation  `json:"effective"`
	Configured *Presentation `json:"configured"`
	Source     string        `json:"source"`
}

func withDefaultRatingTexts(v Presentation) Presentation {
	if len(v.RatingTexts) == 0 {
		v.RatingTexts = DefaultRatingTexts()
	}
	return v
}

func (s *Service) Presentation(ctx context.Context, school string) (PresentationView, error) {
	out := PresentationView{}
	if school != "*" && !validID(school) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		local, e := t.Presentation(school)
		if e == nil {
			out, e = effectivePresentation(t, local)
			return e
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		global, e := t.Presentation("*")
		if e == nil {
			out = PresentationView{withDefaultRatingTexts(global), nil, "global"}
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		out = PresentationView{Presentation{SchoolID: school, RatingTexts: DefaultRatingTexts()}, nil, "default"}
		return nil
	})
	return out, err
}
func configuredPresentation(v Presentation) PresentationView {
	source := "school"
	if v.SchoolID == "*" {
		source = "global"
	}
	return PresentationView{Effective: v, Configured: &v, Source: source}
}

// Effective values may inherit the global texts while Configured keeps the school's stored revision.
func effectivePresentation(t Transaction, v Presentation) (PresentationView, error) {
	view := configuredPresentation(v)
	if len(v.RatingTexts) == 0 && v.SchoolID != "*" {
		global, err := t.Presentation("*")
		if err != nil && !errors.Is(err, ErrNotFound) {
			return PresentationView{}, err
		}
		view.Effective.RatingTexts = global.RatingTexts
	}
	view.Effective = withDefaultRatingTexts(view.Effective)
	return view, nil
}
func (s *Service) AdminPresentation(ctx context.Context, p Principal, school string) (PresentationView, error) {
	if school != "*" && !validID(school) {
		return PresentationView{}, ErrInput
	}
	if _, err := s.authorize(ctx, p, "configure", school); err != nil {
		return PresentationView{}, err
	}
	return s.Presentation(ctx, school)
}
func validPresentation(v Presentation) bool {
	if v.SchoolID != "*" && !validID(v.SchoolID) || v.Revision < 0 {
		return false
	}
	if len(v.RatingTexts) != 0 && len(v.RatingTexts) != len(SupportedLocales) {
		return false
	}
	for locale, dimensions := range v.RatingTexts {
		if !slices.Contains(SupportedLocales[:], locale) || len(dimensions) != len(RatingDimensionKeys) {
			return false
		}
		for _, texts := range dimensions {
			if len(texts) != 6 {
				return false
			}
			for _, text := range texts {
				if text == "" || !textOK(text, 100) {
					return false
				}
			}
		}
	}
	return true
}
func (s *Service) SavePresentation(ctx context.Context, p Principal, key string, in Presentation) (PresentationView, error) {
	if !ValidIdempotencyKey(key) {
		return PresentationView{}, inputFieldErr("invalid_idempotency_key", "idempotency_key")
	}
	if !validPresentation(in) {
		return PresentationView{}, inputFieldErr("invalid_presentation", "school_id")
	}
	if _, e := s.authorize(ctx, p, "configure", in.SchoolID); e != nil {
		return PresentationView{}, e
	}
	rk := hash([]string{p.Subject, "presentation", in.SchoolID, key})
	h := hash(in)
	saved := in
	saved.Revision++
	if saved.RatingTexts == nil {
		saved.RatingTexts = map[string][][]string{}
	}

	out := configuredPresentation(saved)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock("presentation:" + in.SchoolID); e != nil {
			return e
		}
		oldReplay, e := t.Replay(rk)
		if e == nil && s.now().Before(oldReplay.Until) {
			if oldReplay.Digest != h {
				return idempotencyConflictErr()
			}
			if oldReplay.InheritedRatingTexts != nil {
				out.Effective.RatingTexts = oldReplay.InheritedRatingTexts
			}
			return nil
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		old, e := t.Presentation(in.SchoolID)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		if old.Revision != in.Revision {
			return revisionConflictErr(old.Revision)
		}
		if e = t.SavePresentation(saved); e != nil {
			return e
		}
		if e = t.Event(Event{ID: rk, Type: "reviews.presentation.v1", Subject: in.SchoolID, SchoolID: in.SchoolID, Actor: p.Subject, Revision: saved.Revision, At: s.now()}); e != nil {
			return e
		}
		out, e = effectivePresentation(t, saved)
		if e != nil {
			return e
		}
		receipt := Replay{Owner: p.Subject, Digest: h, ObjectID: in.SchoolID, Until: s.now().Add(24 * time.Hour)}
		if len(saved.RatingTexts) == 0 {
			receipt.InheritedRatingTexts = out.Effective.RatingTexts
		}
		return t.SaveReplay(rk, receipt)
	})
	if err != nil {
		return PresentationView{}, err
	}
	return out, nil
}

type AttributionPreview struct {
	Revision           int64    `json:"revision"`
	EvidenceRevision   string   `json:"evidence_revision"`
	TeacherIDs         []string `json:"teacher_ids"`
	PreviousTeacherIDs []string `json:"previous_teacher_ids"`
}

func (s *Service) attribution(ctx context.Context, p Principal, id, teacher string) (AttributionPreview, []Evidence, error) {
	out := AttributionPreview{}
	if !validID(id) || (teacher != "" && !validID(teacher)) {
		return out, nil, ErrInput
	}
	var entry Entry
	err := s.Repo.Transaction(ctx, func(t Transaction) error { var e error; entry, e = t.Entry(id); return e })
	if err != nil {
		return out, nil, err
	}
	if _, err = s.authorize(ctx, p, "moderate", entry.SchoolID); err != nil {
		return out, nil, err
	}
	if entry.ExpiresAt != nil && !s.now().Before(*entry.ExpiresAt) {
		return out, nil, ErrNotFound
	}
	if entry.Input.Kind != "course" || entry.State == "withdrawn" {
		return out, nil, ErrConflict
	}
	target, err := s.catalogTarget(ctx, "course", entry.Input.TargetID)
	if err != nil {
		return out, nil, err
	}
	if target.SchoolID != entry.SchoolID || target.Inactive {
		return out, nil, ErrForbidden
	}
	if target.TeacherRequired && teacher == "" {
		return out, nil, ErrInput
	}
	evidence, err := s.catalogEvidence(ctx, Principal{Subject: entry.Owner}, entry.Input.TargetID, entry.Input.TermID)
	if err != nil {
		return out, nil, err
	}
	if len(evidence) == 0 || len(evidence) > 1000 {
		return out, nil, ErrEnrollment
	}
	ids := []string{}
	for _, record := range evidence {
		if record.TermID != entry.Input.TermID || record.ID == "" || record.SourceID == "" || record.Revision < 1 || record.FetchedAt.IsZero() {
			return out, nil, ErrUnavailable
		}
		ids = append(ids, record.TeacherIDs...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	ids, err = s.selectedTeachers(ctx, teacher, ids)
	if err != nil {
		return out, nil, err
	}
	ids, err = s.activityTeacherIDs(ctx, entry.SchoolID, ids)
	if err != nil {
		return out, nil, err
	}
	out = AttributionPreview{entry.Revision, hash(evidence), ids, entry.TeacherIDs}
	return out, evidence, nil
}
func (s *Service) PreviewAttribution(ctx context.Context, p Principal, id, teacher string) (AttributionPreview, error) {
	v, _, err := s.attribution(ctx, p, id, teacher)
	if err != nil {
		return AttributionPreview{}, err
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(id); err != nil {
			return err
		}
		live, err := t.Entry(id)
		if err != nil {
			return err
		}
		if live.ExpiresAt != nil && !s.now().Before(*live.ExpiresAt) {
			return ErrNotFound
		}
		if live.Revision != v.Revision || live.State == "withdrawn" {
			return ErrConflict
		}
		return s.auditRead(t, p, live.SchoolID, "attribution/"+id, live.Revision)
	})
	if err != nil {
		return AttributionPreview{}, err
	}
	return v, nil
}
