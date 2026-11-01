package reviews

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

type pageCursor struct {
	Scope string    `json:"scope"`
	Time  time.Time `json:"time"`
	ID    string    `json:"id"`
}

func cursorScope(f Filter) string {
	return hash([]string{f.Kind, f.TargetID, f.TermID, f.RootID, f.Owner, f.TeacherID, f.Sort, f.SchoolID})
}
func decodeCursor(f *Filter) error {
	if f.Sort == "" {
		f.Sort = "id"
	}
	if !slices.Contains([]string{"id", "newest", "oldest"}, f.Sort) || len(f.After) > 512 {
		return ErrInput
	}
	if f.After == "" {
		return nil
	}
	if f.Sort == "id" {
		if !validID(f.After) {
			return ErrInput
		}
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(f.After)
	if err != nil {
		return ErrInput
	}
	var cursor pageCursor
	if json.Unmarshal(b, &cursor) != nil || cursor.Time.IsZero() || !validID(cursor.ID) || cursor.Scope != cursorScope(*f) {
		return ErrInput
	}
	f.CursorTime = &cursor.Time
	f.CursorID = cursor.ID
	return nil
}
func encodeCursor(f Filter, entry Entry) string {
	if f.Sort == "id" {
		return entry.ID
	}
	b, _ := json.Marshal(pageCursor{cursorScope(f), entry.CreatedAt.Round(time.Microsecond), entry.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

// Identity/vote snapshots come from this module's repository. Qualification and
// profile lookups happen after its transaction closes, once per school/author.
func project(t Transaction, p Principal, entry Entry) (Entry, error) {
	if entry.Projection != nil {
		return entry, nil
	}
	identity, err := t.Identity(entry.RootID, entry.Owner)
	if err != nil {
		return entry, err
	}
	v := &Projection{Identity: identity}
	v.Upvotes, v.Downvotes, err = t.VoteCounts(entry.ID)
	if err != nil {
		return entry, err
	}
	if p.Subject != "" {
		v.MyVote, err = t.Vote(entry.ID, p.Subject)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return entry, err
		}
	}
	entry.Projection = v
	return entry, nil
}
func (s *Service) render(ctx context.Context, p Principal, entries []Entry, own bool, school string) ([]View, error) {
	out := make([]View, 0, len(entries))
	profiles := map[string]Profile{}
	for _, entry := range entries {
		if own {
			if p.Subject == "" || entry.Owner != p.Subject {
				return nil, ErrForbidden
			}
		} else if !validID(school) || entry.SchoolID != school {
			return nil, ErrForbidden
		}
		labels, public := s.disclosed(ctx, entry)
		if !public && p.Subject != entry.Owner {
			continue
		}
		if entry.Projection == nil {
			return nil, ErrUnavailable
		}
		v := View{Featured: entry.Featured, State: entry.State, ID: entry.ID, RootID: entry.RootID, ParentID: entry.ParentID, Kind: entry.Input.Kind, SchoolID: entry.SchoolID, TargetID: entry.Input.TargetID, TermID: entry.Input.TermID, TeacherID: entry.Input.TeacherID, Title: entry.Input.Title, Body: entry.Input.Body, Ratings: entry.Input.Ratings, Revision: entry.Revision, PublishedAt: entry.FirstPublishedAt, Mine: p.Subject != "" && p.Subject == entry.Owner}
		v.CanEdit = v.Mine && entry.State == "published" && entry.FirstPublishedAt != nil && s.now().Before(entry.FirstPublishedAt.Add(s.Config.EditWindow))
		identity := entry.Projection.Identity
		v.Author = Author{Anonymous: !identity.Signed && !slices.ContainsFunc(labels, func(option DisclosureOption) bool { return option.Key == "real_name" }), Handle: identity.Handle, Disclosures: labels}
		if identity.Signed && !slices.ContainsFunc(labels, func(option DisclosureOption) bool { return option.Key == "real_name" }) {
			if s.Profiles == nil {
				return nil, ErrUnavailable
			}
			profile, ok := profiles[entry.Owner]
			if !ok {
				var err error
				profile, err = s.Profiles.Lookup(ctx, entry.Owner)
				if err != nil {
					return nil, err
				}
				profiles[entry.Owner] = profile
			}
			v.Author.Profile = &profile
		}
		v.Upvotes = entry.Projection.Upvotes
		v.Downvotes = entry.Projection.Downvotes
		v.MyVote = entry.Projection.MyVote
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) Get(ctx context.Context, p Principal, id string) (View, error) {
	return s.get(ctx, p, id, false)
}
func (s *Service) GetOwn(ctx context.Context, p Principal, id string) (View, error) {
	if p.Subject == "" {
		return View{}, ErrForbidden
	}
	return s.get(ctx, p, id, true)
}
func (s *Service) get(ctx context.Context, p Principal, id string, own bool) (View, error) {
	school := ""
	if !own {
		var e error
		school, e = s.contentScope(ctx, p, "")
		if e != nil {
			return View{}, e
		}
	}
	if !validID(id) {
		return View{}, ErrInput
	}
	var entry Entry
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var err error
		if own {
			entry, err = t.Entry(id)
			if err != nil {
				return err
			}
			if entry.Owner != p.Subject || entry.State == "withdrawn" || (entry.ExpiresAt != nil && !s.now().Before(*entry.ExpiresAt)) {
				return ErrNotFound
			}
			if entry.RootID != entry.ID {
				if _, err = s.accessible(t, entry.RootID); err != nil {
					return err
				}
			}
		} else {
			entry, err = s.accessible(t, id)
			if err != nil {
				return err
			}
		}
		entry, err = project(t, p, entry)
		return err
	})
	if err != nil {
		return View{}, err
	}
	views, err := s.render(ctx, p, []Entry{entry}, own, school)
	if err != nil {
		return View{}, err
	}
	if len(views) == 0 {
		return View{}, ErrNotFound
	}
	return views[0], nil
}
func (s *Service) List(ctx context.Context, p Principal, f Filter) (Page, error) {
	out := Page{Items: []View{}}
	if f.Limit < 1 || f.Limit > 100 || f.Owner != "" && f.Owner != p.Subject || (f.Kind != "" && !slices.Contains([]string{"course", "mentor", "reply"}, f.Kind)) {
		return out, ErrInput
	}
	for _, id := range []string{f.TargetID, f.TermID, f.RootID, f.TeacherID} {
		if id != "" && !validID(id) {
			return out, ErrInput
		}
	}
	if f.Owner == "" {
		scope, err := s.contentScope(ctx, p, f.SchoolID)
		if err != nil {
			return out, err
		}
		f.SchoolID = scope
	}
	if err := decodeCursor(&f); err != nil {
		return out, err
	}
	if f.TargetID != "" {
		kind := "course"
		if f.Kind == "mentor" {
			kind = "teacher"
		}
		if f.Owner == "" {
			object, err := s.Catalog.Target(ctx, kind, f.TargetID)
			if err != nil {
				return out, err
			}
			if object.SchoolID != f.SchoolID {
				return out, ErrForbidden
			}
		}
		var err error
		f.EquivalentTargets, err = s.equivalentIDs(ctx, kind, f.TargetID)
		if err != nil {
			return out, err
		}
	}
	if f.TeacherID != "" {
		if f.Owner == "" {
			object, err := s.Catalog.Target(ctx, "teacher", f.TeacherID)
			if err != nil {
				return out, err
			}
			if object.SchoolID != f.SchoolID {
				return out, ErrForbidden
			}
		}
		var err error
		f.EquivalentTeachers, err = s.equivalentIDs(ctx, "teacher", f.TeacherID)
		if err != nil {
			return out, err
		}
	}
	f.Viewer = p.Subject
	f.Now = s.now()
	var entries []Entry
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if f.RootID != "" {
			root, err := s.accessible(t, f.RootID)
			if err != nil {
				return err
			}
			if f.Owner == "" && root.SchoolID != f.SchoolID {
				return ErrForbidden
			}
		}
		var err error
		entries, err = t.List(f)
		return err
	})
	if err != nil {
		return out, err
	}
	if len(entries) > f.Limit {
		out.Next = encodeCursor(f, entries[f.Limit-1])
		entries = entries[:f.Limit]
	}
	out.Items, err = s.render(ctx, p, entries, f.Owner != "", f.SchoolID)
	return out, err
}
