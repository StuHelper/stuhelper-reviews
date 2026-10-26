package reviews

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	GuestPreview, MemberPreview int
	PreviewRatio                float64
	EditWindow, DraftLifetime   time.Duration
	MinSamples                  int
}

func DefaultConfig() Config {
	return Config{GuestPreview: 80, MemberPreview: 160, PreviewRatio: 0.3, EditWindow: 5 * time.Minute, DraftLifetime: 24 * time.Hour, MinSamples: 1}
}

type Service struct {
	Repo       Repository
	Catalog    Catalog
	Access     Access
	Moderation Moderation
	Profiles   Profiles
	Config     Config
	Clock      func() time.Time
}

func New(repo Repository, catalog Catalog, access Access, moderation Moderation, profiles Profiles, c Config) (*Service, error) {
	if repo == nil || catalog == nil || access == nil || moderation == nil || c.GuestPreview < 0 || c.GuestPreview > 1000 || c.MemberPreview < 0 || c.MemberPreview > 1000 || c.PreviewRatio <= 0 || c.PreviewRatio >= 1 || c.EditWindow <= 0 || c.DraftLifetime <= 0 || c.MinSamples < 1 {
		return nil, ErrInput
	}
	return &Service{Repo: repo, Catalog: catalog, Access: access, Moderation: moderation, Profiles: profiles, Config: c}, nil
}
func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validKey(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}
func validID(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)) {
			return false
		}
	}
	return true
}
func textOK(v string, n int) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) <= n && !strings.ContainsRune(v, 0)
}
func validate(in Input, draft bool) error {
	if !slices.Contains([]string{"course", "mentor", "reply"}, in.Kind) || !validID(in.TargetID) || !textOK(in.Title, 200) || !textOK(in.Body, 16000) {
		return ErrInput
	}
	if !draft && (strings.TrimSpace(in.Body) == "" || (in.Kind != "reply" && strings.TrimSpace(in.Title) == "")) {
		return ErrInput
	}
	if in.Kind == "course" {
		if (!draft || in.TermID != "") && !validID(in.TermID) || in.TeacherID != "" && !validID(in.TeacherID) {
			return ErrInput
		}
		for _, v := range in.Ratings {
			if v < 0 || v > 5 || !draft && v == 0 {
				return ErrInput
			}
		}
	} else if in.TermID != "" || in.TeacherID != "" || in.Ratings != [5]int{} {
		return ErrInput
	}
	return nil
}
func result(e Entry) Result { return Result{e.ID, e.State, e.Revision, e.ExpiresAt} }
func (s *Service) authorize(ctx context.Context, p Principal, action, school string) (Grant, error) {
	if p.Subject == "" && action != "read_full" {
		return Grant{}, ErrForbidden
	}
	if s.Access == nil {
		return Grant{}, ErrUnavailable
	}
	return s.Access.Authorize(ctx, p, action, school)
}
func (s *Service) prepare(ctx context.Context, p Principal, in Input) (Target, []Evidence, []string, Grant, error) {
	kind := "course"
	action := "publish_course"
	if in.Kind == "mentor" {
		kind = "teacher"
		action = "publish_mentor"
	}
	target, e := s.Catalog.Target(ctx, kind, in.TargetID)
	if e != nil {
		return target, nil, nil, Grant{}, e
	}
	g, e := s.authorize(ctx, p, action, target.SchoolID)
	if e != nil {
		return target, nil, nil, g, e
	}
	if in.Kind == "mentor" {
		return target, nil, nil, g, nil
	}
	term, e := s.Catalog.Target(ctx, "term", in.TermID)
	if e != nil {
		return target, nil, nil, g, e
	}
	if term.SchoolID != target.SchoolID {
		return target, nil, nil, g, ErrInput
	}
	evidence, e := s.Catalog.Evidence(ctx, p, in.TargetID, in.TermID)
	if e != nil {
		return target, nil, nil, g, e
	}
	if len(evidence) > 1000 {
		return target, nil, nil, g, ErrUnavailable
	}
	if g.EnrollmentRequired && len(evidence) == 0 {
		return target, nil, nil, g, ErrEnrollment
	}
	ids := []string{}
	for _, v := range evidence {
		if v.TermID != in.TermID || v.ID == "" || v.SourceID == "" || v.Revision < 1 || v.FetchedAt.IsZero() {
			return target, nil, nil, g, ErrUnavailable
		}
		ids = append(ids, v.TeacherIDs...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if in.TeacherID != "" {
		if !slices.Contains(ids, in.TeacherID) {
			return target, nil, nil, g, ErrForbidden
		}
		ids = []string{in.TeacherID}
	}
	return target, evidence, ids, g, nil
}
func (s *Service) check(ctx context.Context, e Entry) (string, error) {
	out, err := s.Moderation.Check(ctx, Candidate{e.ID, e.Input.Kind, e.SchoolID, e.Owner, e.Input.Title, e.Input.Body, e.Revision})
	if err != nil {
		return "", err
	}
	if !slices.Contains([]string{"published", "pending", "blocked", "review"}, out) {
		return "", ErrUnavailable
	}
	if out == "blocked" {
		return "", ErrForbidden
	}
	return out, nil
}
func replay(t Transaction, key, digest string, now time.Time) (Result, bool, error) {
	r, e := t.Replay(key)
	if errors.Is(e, ErrNotFound) {
		return Result{}, false, nil
	}
	if e != nil {
		return Result{}, false, e
	}
	if !now.Before(r.Until) {
		return Result{}, false, nil
	}
	if r.Digest != digest {
		return Result{}, false, ErrConflict
	}
	return r.Result, true, nil
}
func (s *Service) record(t Transaction, key, digest, action string, p Principal, e Entry) error {
	if err := t.Event(Event{ID: key, Type: "reviews." + action + ".v1", Subject: "entry/" + e.ID, SchoolID: e.SchoolID, Actor: p.Subject, Revision: e.Revision, At: s.now()}); err != nil {
		return err
	}
	return t.SaveReplay(key, Replay{Digest: digest, Result: result(e), Until: s.now().Add(24 * time.Hour), ObjectID: e.ID})
}
func (s *Service) Create(ctx context.Context, p Principal, key string, in Input) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validKey(key) || in.Kind == "reply" || validate(in, false) != nil {
		return out, ErrInput
	}
	target, evidence, ids, g, err := s.prepare(ctx, p, in)
	if err != nil {
		return out, err
	}
	rk := hash([]string{p.Subject, "create", in.TargetID, key})
	h := hash(in)
	now := s.now()
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(rk); e != nil {
			return e
		}
		cached, ok, e := replay(t, rk, h, now)
		if e != nil {
			return e
		}
		if ok {
			out = cached
			return nil
		}
		if _, e = t.Entry(rk); e == nil {
			return ErrConflict
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		entry := Entry{ID: rk, RootID: rk, Owner: p.Subject, SchoolID: target.SchoolID, Input: in, TeacherIDs: ids, Evidence: evidence, Revision: 1, CreatedAt: now, UpdatedAt: now, RuleVersion: g.RuleVersion}
		state, e := s.check(ctx, entry)
		if e != nil {
			return e
		}
		entry.ReviewRequested = state == "review" || state == "pending"
		if state == "review" {
			state = "published"
		}
		entry.State = state
		if state == "published" {
			entry.FirstPublishedAt = &now
		}
		if e = t.SaveEntry(entry); e != nil {
			return e
		}
		if e = t.SaveIdentity(Identity{RootID: entry.ID, Subject: p.Subject, Handle: "author", Signed: in.Signed}); e != nil {
			return e
		}
		if e = s.record(t, rk, h, "created", p, entry); e != nil {
			return e
		}
		out = result(entry)
		return nil
	})
	return out, err
}
func visible(e Entry, now time.Time) bool {
	return e.State == "published" && (e.ExpiresAt == nil || now.Before(*e.ExpiresAt))
}
func (s *Service) accessible(t Transaction, id string) (Entry, error) {
	e, err := t.Entry(id)
	if err != nil {
		return e, err
	}
	if !visible(e, s.now()) {
		return e, ErrNotFound
	}
	if e.RootID != e.ID {
		root, err := t.Entry(e.RootID)
		if err != nil {
			return e, err
		}
		if !visible(root, s.now()) {
			return e, ErrNotFound
		}
	}
	return e, nil
}
func (s *Service) Reply(ctx context.Context, p Principal, parent, key string, in Input) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(parent) || !validKey(key) || in.Kind != "reply" || validate(in, false) != nil {
		return out, ErrInput
	}
	rk := hash([]string{p.Subject, "reply", parent, key})
	h := hash(in)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		base, e := s.accessible(t, parent)
		if e != nil {
			return e
		}
		if e = t.Lock(base.RootID); e != nil {
			return e
		}
		base, e = s.accessible(t, parent)
		if e != nil {
			return e
		}
		g, e := s.authorize(ctx, p, "reply", base.SchoolID)
		if e != nil {
			return e
		}
		cached, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = cached
			return nil
		}
		if in.TargetID != base.Input.TargetID {
			return ErrInput
		}
		identity, e := t.Identity(base.RootID, p.Subject)
		if errors.Is(e, ErrNotFound) {
			identity = Identity{RootID: base.RootID, Subject: p.Subject, Handle: hash([]string{base.RootID, p.Subject})[:16], Signed: in.Signed}
			if e = t.SaveIdentity(identity); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		in.Signed = identity.Signed
		now := s.now()
		entry := Entry{ID: rk, RootID: base.RootID, ParentID: parent, Owner: p.Subject, SchoolID: base.SchoolID, Input: in, Revision: 1, CreatedAt: now, UpdatedAt: now, RuleVersion: g.RuleVersion}
		state, e := s.check(ctx, entry)
		if e != nil {
			return e
		}
		entry.ReviewRequested = state == "review" || state == "pending"
		if state == "review" {
			state = "published"
		}
		entry.State = state
		if state == "published" {
			entry.FirstPublishedAt = &now
		}
		if e = t.SaveEntry(entry); e != nil {
			return e
		}
		if e = s.record(t, rk, h, "replied", p, entry); e != nil {
			return e
		}
		out = result(entry)
		return nil
	})
	return out, err
}
func (s *Service) Action(ctx context.Context, p Principal, id, key string, in Action) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(id) || !validKey(key) || !slices.Contains([]string{"withdraw", "delete", "republish", "vote"}, in.Action) {
		return out, ErrInput
	}
	if in.Action == "republish" && (in.Input == nil || validate(*in.Input, false) != nil) {
		return out, ErrInput
	}
	if in.Action == "vote" && (in.Vote < -1 || in.Vote > 1) {
		return out, ErrInput
	}
	rk := hash([]string{p.Subject, in.Action, id, key})
	h := hash(in)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		// Serialize replay, including a retry after permanent deletion.
		if e := t.Lock(rk); e != nil {
			return e
		}
		cached, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = cached
			return nil
		}
		entry, e := t.Entry(id)
		if e != nil {
			return e
		}
		if e = t.Lock(entry.RootID); e != nil {
			return e
		}
		entry, e = t.Entry(id)
		if e != nil {
			return e
		}
		if in.Action == "vote" {
			if _, e = s.authorize(ctx, p, "vote", entry.SchoolID); e != nil {
				return e
			}
			if _, e = s.accessible(t, id); e != nil {
				return e
			}
			if e = t.SetVote(id, p.Subject, in.Vote); e != nil {
				return e
			}
			if e = s.record(t, rk, h, "voted", p, entry); e != nil {
				return e
			}
			out = result(entry)
			return nil
		}
		if entry.Owner != p.Subject {
			return ErrNotFound
		}
		if entry.ExpiresAt != nil && !s.now().Before(*entry.ExpiresAt) {
			return ErrNotFound
		}
		if in.Revision != entry.Revision {
			return ErrConflict
		}
		if in.Action == "republish" {
			if entry.State != "withdrawn" {
				return ErrConflict
			}
			return s.republish(ctx, t, p, entry, rk, h, *in.Input, &out)
		}
		if _, e = s.accessible(t, id); e != nil {
			return e
		}
		if entry.FirstPublishedAt == nil || !s.now().Before(entry.FirstPublishedAt.Add(s.Config.EditWindow)) {
			return ErrWindow
		}
		entry.Revision++
		entry.UpdatedAt = s.now()
		if in.Action == "delete" {
			entry.State = "deleted"
			if e = t.DeleteEntry(entry); e != nil {
				return e
			}
		} else {
			entry.State = "withdrawn"
			until := s.now().Add(s.Config.DraftLifetime)
			entry.ExpiresAt = &until
			if e = t.SaveEntry(entry); e != nil {
				return e
			}
		}
		if e = s.record(t, rk, h, in.Action, p, entry); e != nil {
			return e
		}
		out = result(entry)
		return nil
	})
	return out, err
}
func (s *Service) republish(ctx context.Context, t Transaction, p Principal, e Entry, key, digest string, in Input, out *Result) error {
	// Editing text cannot change attribution, the term, or the discussion identity.
	if in.Kind != e.Input.Kind || in.TargetID != e.Input.TargetID || in.TermID != e.Input.TermID || in.TeacherID != e.Input.TeacherID {
		return ErrInput
	}
	if e.RootID != e.ID {
		if _, err := s.accessible(t, e.RootID); err != nil {
			return err
		}
	}
	var g Grant
	if e.Input.Kind == "reply" {
		var err error
		g, err = s.authorize(ctx, p, "reply", e.SchoolID)
		if err != nil {
			return err
		}
	} else {
		_, _, _, grant, err := s.prepare(ctx, p, in)
		if err != nil {
			return err
		}
		g = grant
	}
	identity, err := t.Identity(e.RootID, p.Subject)
	if err != nil {
		return err
	}
	in.Signed = identity.Signed
	e.Input = in
	e.Revision++
	e.UpdatedAt = s.now()
	e.RuleVersion = g.RuleVersion
	state, err := s.check(ctx, e)
	if err != nil {
		return err
	}
	e.ReviewRequested = state == "review" || state == "pending"
	if state == "review" {
		state = "published"
	}
	e.State = state
	// Pending moderation keeps the withdrawal expiry; editing cannot extend it.
	if state == "published" {
		e.ExpiresAt = nil
		if e.FirstPublishedAt == nil {
			now := s.now()
			e.FirstPublishedAt = &now
		}
	}
	if err = t.SaveEntry(e); err != nil {
		return err
	}
	if err = s.record(t, key, digest, "republished", p, e); err != nil {
		return err
	}
	*out = result(e)
	return nil
}
func (s *Service) view(ctx context.Context, t Transaction, p Principal, e Entry) (View, error) {
	v := View{State: e.State, ID: e.ID, RootID: e.RootID, ParentID: e.ParentID, Kind: e.Input.Kind, SchoolID: e.SchoolID, TargetID: e.Input.TargetID, TermID: e.Input.TermID, TeacherID: e.Input.TeacherID, Title: e.Input.Title, Body: e.Input.Body, Ratings: e.Input.Ratings, Revision: e.Revision, PublishedAt: e.FirstPublishedAt, Mine: p.Subject != "" && p.Subject == e.Owner}
	v.CanEdit = v.Mine && e.State == "published" && e.FirstPublishedAt != nil && s.now().Before(e.FirstPublishedAt.Add(s.Config.EditWindow))
	_, err := s.authorize(ctx, p, "read_full", e.SchoolID)
	if err != nil && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrEnrollment) {
		return View{}, err
	}
	if err != nil {
		v.Preview = true
		limit := s.Config.MemberPreview
		if p.Subject == "" {
			limit = s.Config.GuestPreview
			v.Title = ""
		}
		v.Body = preview(e.Input.Body, limit, s.Config.PreviewRatio)
	}
	var identity Identity
	if e.Projection != nil {
		identity = e.Projection.Identity
	} else {
		identity, err = t.Identity(e.RootID, e.Owner)
		if err != nil {
			return View{}, err
		}
	}
	v.Author = Author{Anonymous: !identity.Signed, Handle: identity.Handle}
	if identity.Signed {
		if s.Profiles == nil {
			return View{}, ErrUnavailable
		}
		profile, err := s.Profiles.Lookup(ctx, e.Owner)
		if err != nil {
			return View{}, err
		}
		v.Author.Profile = &profile
	}
	if e.Projection != nil {
		v.Upvotes = e.Projection.Upvotes
		v.Downvotes = e.Projection.Downvotes
		v.MyVote = e.Projection.MyVote
	} else {
		v.Upvotes, v.Downvotes, err = t.VoteCounts(e.ID)
		if err != nil {
			return View{}, err
		}
		if p.Subject != "" {
			v.MyVote, err = t.Vote(e.ID, p.Subject)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return View{}, err
			}
		}
	}
	return v, nil
}
func preview(body string, max int, ratio float64) string {
	line := ""
	for _, v := range strings.Split(body, "\n") {
		if strings.TrimSpace(v) != "" {
			line = strings.TrimSpace(v)
			break
		}
	}
	n := int(math.Floor(float64(utf8.RuneCountInString(body)) * ratio))
	if n > max {
		n = max
	}
	r := []rune(line)
	if n < len(r) {
		r = r[:n]
	}
	return string(r)
}
func (s *Service) Get(ctx context.Context, p Principal, id string) (View, error) {
	var out View
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		e, err := s.accessible(t, id)
		if err != nil {
			return err
		}
		out, err = s.view(ctx, t, p, e)
		return err
	})
	return out, err
}
func (s *Service) List(ctx context.Context, p Principal, f Filter) (Page, error) {
	out := Page{Items: []View{}}
	if f.Limit < 1 || f.Limit > 100 || f.Owner != "" && f.Owner != p.Subject {
		return out, ErrInput
	}
	f.Viewer = p.Subject
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if f.RootID != "" {
			if _, e := s.accessible(t, f.RootID); e != nil {
				return e
			}
		}
		entries, e := t.List(f)
		if e != nil {
			return e
		}
		if len(entries) > f.Limit {
			out.Next = entries[f.Limit-1].ID
			entries = entries[:f.Limit]
		}
		for _, entry := range entries {
			v, e := s.view(ctx, t, p, entry)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		return nil
	})
	return out, err
}
func (s *Service) Stats(ctx context.Context, kind, target string, from, until *time.Time) (Statistics, error) {
	return s.StatsPage(ctx, kind, target, from, until, "", 20)
}
func (s *Service) StatsPage(ctx context.Context, kind, target string, from, until *time.Time, after string, limit int) (Statistics, error) {
	out := Statistics{From: from, Until: until, Courses: []CourseStatistic{}}
	if limit < 1 || limit > 100 {
		return out, ErrInput
	}
	if !slices.Contains([]string{"course", "teacher"}, kind) || !validID(target) || from != nil && until != nil && !from.Before(*until) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		rows, e := t.Aggregates(kind, target, from, until)
		if e != nil {
			return e
		}
		var sums [5]float64
		var samples, courses [5]int
		for _, row := range rows {
			if row.CourseID > after && len(out.Courses) <= limit {
				item := CourseStatistic{CourseID: row.CourseID}
				for i := range 5 {
					item.Dimensions[i].Samples = row.Counts[i]
					if row.Counts[i] > 0 {
						item.Dimensions[i].Courses = 1
					}
					if row.Counts[i] >= s.Config.MinSamples && row.Counts[i] > 0 {
						item.Dimensions[i].Level = int(math.Floor(row.Sums[i]/float64(row.Counts[i]) + 0.5))
					}
				}
				out.Courses = append(out.Courses, item)
			}
			for i := range 5 {
				if row.Counts[i] > 0 {
					sums[i] += row.Sums[i] / float64(row.Counts[i])
					samples[i] += row.Counts[i]
					courses[i]++
				}
			}
		}
		if len(out.Courses) > limit {
			out.Next = out.Courses[limit-1].CourseID
			out.Courses = out.Courses[:limit]
		}
		for i := range 5 {
			out.Dimensions[i].Samples = samples[i]
			out.Dimensions[i].Courses = courses[i]
			if samples[i] >= s.Config.MinSamples && courses[i] > 0 {
				out.Dimensions[i].Level = int(math.Floor(sums[i]/float64(courses[i]) + 0.5))
			}
		}
		return nil
	})
	return out, err
}
