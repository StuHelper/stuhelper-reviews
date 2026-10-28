package reviews

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"
)

type Presentation struct {
	SchoolID      string                `json:"school_id"`
	Revision      int64                 `json:"revision"`
	GuestPreview  int                   `json:"guest_preview"`
	MemberPreview int                   `json:"member_preview"`
	PreviewRatio  float64               `json:"preview_ratio"`
	RatingTexts   map[string][][]string `json:"rating_texts"`
}
type PresentationView struct {
	Effective  Presentation  `json:"effective"`
	Configured *Presentation `json:"configured"`
	Source     string        `json:"source"`
}

func (s *Service) Presentation(ctx context.Context, school string) (PresentationView, error) {
	out := PresentationView{}
	if school != "*" && !validID(school) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		local, e := t.Presentation(school)
		if e == nil {
			out = PresentationView{local, &local, "school"}
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		global, e := t.Presentation("*")
		if e == nil {
			out = PresentationView{global, nil, "global"}
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		out = PresentationView{Presentation{SchoolID: school, GuestPreview: s.Config.GuestPreview, MemberPreview: s.Config.MemberPreview, PreviewRatio: s.Config.PreviewRatio, RatingTexts: map[string][][]string{}}, nil, "default"}
		return nil
	})
	return out, err
}
func validPresentation(v Presentation) bool {
	if v.SchoolID != "*" && !validID(v.SchoolID) || v.Revision < 0 || v.GuestPreview < 0 || v.GuestPreview > 1000 || v.MemberPreview < 0 || v.MemberPreview > 1000 || math.IsNaN(v.PreviewRatio) || v.PreviewRatio <= 0 || v.PreviewRatio >= 1 {
		return false
	}
	locales := []string{"zh-CN", "en-US", "ja-JP", "ko-KR", "fr-FR", "es-ES", "ru-RU", "ar-EG"}
	if len(v.RatingTexts) != 0 && len(v.RatingTexts) != 8 {
		return false
	}
	for locale, dimensions := range v.RatingTexts {
		if !slices.Contains(locales, locale) || len(dimensions) != 5 {
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
	if !validKey(key) || !validPresentation(in) {
		return PresentationView{}, ErrInput
	}
	if _, e := s.authorize(ctx, p, "configure", in.SchoolID); e != nil {
		return PresentationView{}, e
	}
	rk := hash([]string{p.Subject, "presentation", in.SchoolID, key})
	h := hash(in)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock("presentation:" + in.SchoolID); e != nil {
			return e
		}
		oldReplay, e := t.Replay(rk)
		if e == nil && s.now().Before(oldReplay.Until) {
			if oldReplay.Digest != h {
				return ErrConflict
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
			return ErrConflict
		}
		in.Revision++
		if in.RatingTexts == nil {
			in.RatingTexts = map[string][][]string{}
		}
		if e = t.SavePresentation(in); e != nil {
			return e
		}
		if e = t.Event(Event{ID: rk, Type: "reviews.presentation.v1", Subject: in.SchoolID, SchoolID: in.SchoolID, Actor: p.Subject, Revision: in.Revision, At: s.now()}); e != nil {
			return e
		}
		return t.SaveReplay(rk, Replay{Digest: h, ObjectID: in.SchoolID, Until: s.now().Add(24 * time.Hour)})
	})
	if err != nil {
		return PresentationView{}, err
	}
	return s.Presentation(ctx, in.SchoolID)
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
	if entry.Input.Kind != "course" || entry.State == "withdrawn" {
		return out, nil, ErrConflict
	}
	evidence, err := s.Catalog.Evidence(ctx, Principal{Subject: entry.Owner}, entry.Input.TargetID, entry.Input.TermID)
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
	if teacher != "" {
		if !slices.Contains(ids, teacher) {
			return out, nil, ErrForbidden
		}
		ids = []string{teacher}
	}
	out = AttributionPreview{entry.Revision, hash(evidence), ids, entry.TeacherIDs}
	return out, evidence, nil
}
func (s *Service) PreviewAttribution(ctx context.Context, p Principal, id, teacher string) (AttributionPreview, error) {
	v, _, err := s.attribution(ctx, p, id, teacher)
	return v, err
}
