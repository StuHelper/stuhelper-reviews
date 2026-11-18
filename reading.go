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
	Scope    string    `json:"scope"`
	Time     time.Time `json:"time"`
	ID       string    `json:"id"`
	Votes    *int      `json:"votes,omitempty"`
	Featured *int      `json:"featured,omitempty"`
	Score    *int      `json:"score,omitempty"`
}

func boolFilterKey(v *bool) string {
	if v == nil {
		return ""
	}
	if *v {
		return "true"
	}
	return "false"
}

func cursorScope(f Filter) string {
	return hash([]string{f.Kind, f.TargetID, f.TermID, f.RootID, f.Owner, f.TeacherID, f.Sort, f.SchoolID, boolFilterKey(f.Featured), boolFilterKey(f.HasTeacherReply), f.RatingGroup})
}
func decodeCursor(f *Filter) error {
	f.CursorTime = nil
	f.CursorID = ""
	f.CursorVotes = nil
	f.CursorFeatured = nil
	f.CursorScore = nil
	if f.Sort == "" {
		f.Sort = "id"
	}
	if !slices.Contains([]string{"id", "newest", "oldest", "upvotes", "recommended"}, f.Sort) || len(f.After) > 512 {
		return ErrInput
	}
	if f.RatingGroup != "" && !slices.Contains([]string{"low", "mid", "high"}, f.RatingGroup) {
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
	if f.Sort == "upvotes" {
		if cursor.Votes == nil || *cursor.Votes < 0 {
			return ErrInput
		}
		f.CursorVotes = cursor.Votes
	}
	if f.Sort == "recommended" {
		if cursor.Featured == nil || (*cursor.Featured != 0 && *cursor.Featured != 1) || cursor.Score == nil || cursor.Votes == nil || *cursor.Votes < 0 {
			return ErrInput
		}
		f.CursorFeatured = cursor.Featured
		f.CursorScore = cursor.Score
		f.CursorVotes = cursor.Votes
	}
	return nil
}
func encodeCursor(f Filter, entry Entry) string {
	if f.Sort == "id" {
		return entry.ID
	}
	cursor := pageCursor{Scope: cursorScope(f), Time: entry.CreatedAt.Round(time.Microsecond), ID: entry.ID}
	if f.Sort == "upvotes" && entry.Projection != nil {
		cursor.Votes = &entry.Projection.Upvotes
	}
	if f.Sort == "recommended" && entry.Projection != nil {
		feat := 0
		if entry.Featured {
			feat = 1
		}
		score := entry.Projection.Upvotes - entry.Projection.Downvotes
		votes := entry.Projection.Upvotes
		cursor.Featured = &feat
		cursor.Score = &score
		cursor.Votes = &votes
	}
	b, _ := json.Marshal(cursor)
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
func (s *Service) render(ctx context.Context, p Principal, entries []Entry, own bool, school string, options ViewOptions) ([]View, error) {
	out := make([]View, 0, len(entries))
	profiles := map[string]Profile{}
	owners := make([]string, 0, len(entries))
	parentIDs := make([]string, 0)
	for _, entry := range entries {
		owners = append(owners, entry.Owner)
		if options.IncludeParentSummary && entry.Input.Kind == "reply" && entry.ParentID != "" {
			parentIDs = append(parentIDs, entry.ParentID)
		}
	}
	slices.Sort(parentIDs)
	parentIDs = slices.Compact(parentIDs)
	var closed map[string]bool
	var parents map[string]ParentSummary
	if e := s.Repo.Transaction(ctx, func(t Transaction) error {
		var err error
		closed, err = t.ClosedOwners(owners)
		if err != nil {
			return err
		}
		if len(parentIDs) > 0 {
			if port, ok := t.(PersonalContextTransaction); ok {
				parents, err = port.ParentSummaries(parentIDs)
				return err
			}
		}
		return nil
	}); e != nil {
		return nil, e
	}
	for _, entry := range entries {
		if own {
			if p.Subject == "" || entry.Owner != p.Subject {
				return nil, ErrForbidden
			}
		} else if !validID(school) || entry.SchoolID != school {
			return nil, ErrForbidden
		}
		var labels []DisclosureOption
		public := true
		if !closed[entry.Owner] {
			labels, public = s.disclosed(ctx, entry)
		}
		if !public && p.Subject != entry.Owner {
			continue
		}
		if entry.Projection == nil {
			return nil, ErrUnavailable
		}
		v := View{Featured: entry.Featured, State: entry.State, ID: entry.ID, RootID: entry.RootID, ParentID: entry.ParentID, Kind: entry.Input.Kind, SchoolID: entry.SchoolID, TargetID: entry.Input.TargetID, TermID: entry.Input.TermID, TeacherID: entry.Input.TeacherID, Title: entry.Input.Title, Body: entry.Input.Body, Ratings: entry.Input.Ratings, Revision: entry.Revision, PublishedAt: entry.FirstPublishedAt, Mine: p.Subject != "" && p.Subject == entry.Owner}
		if own && entry.ModerationReasonCode != "" {
			v.ModerationReasonCode = entry.ModerationReasonCode
		}
		if options.IncludeTeacherIDs && entry.Input.Kind == "course" && len(entry.TeacherIDs) > 0 {
			v.TeacherIDs = slices.Clone(entry.TeacherIDs)
		}
		if options.IncludeParentSummary && entry.Input.Kind == "reply" && entry.ParentID != "" {
			parent, found := parents[entry.ParentID]
			if !found {
				parentVisible := false
				v.ParentVisible = &parentVisible
				if entry.ParentAuthorHandle != "" {
					v.ParentAuthorHandle = entry.ParentAuthorHandle
				}
			} else {
				parentVisible := visible(parent.Entry, s.now())
				if parentVisible && !parent.Closed && parent.Entry.TeacherReply {
					_, parentVisible = s.disclosed(ctx, parent.Entry)
				}
				v.ParentVisible = &parentVisible
				if parent.Entry.State != "withdrawn" {
					handle := parent.Handle
					if handle == "" {
						handle = entry.ParentAuthorHandle
					}
					v.ParentAuthorHandle = handle
				}
			}
		}
		v.CanEdit = v.Mine && entry.State == "published" && entry.FirstPublishedAt != nil && s.now().Before(entry.FirstPublishedAt.Add(s.Config.EditWindow))
		identity := entry.Projection.Identity
		if closed[entry.Owner] {
			identity.Signed = false
		}
		v.Author = Author{Anonymous: !identity.Signed && !slices.ContainsFunc(labels, func(option DisclosureOption) bool { return option.Key == "real_name" }), Handle: identity.Handle, Disclosures: labels}
		if identity.Signed && !slices.ContainsFunc(labels, func(option DisclosureOption) bool { return option.Key == "real_name" }) {
			profile, ok := profiles[entry.Owner]
			if !ok && s.Profiles != nil {
				var err error
				profile, err = s.lookupProfile(ctx, entry.Owner)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if err != nil && !own {
					return nil, err
				}
				if err == nil {
					profiles[entry.Owner] = profile
					ok = true
				}
			}
			if ok {
				v.Author.Profile = &profile
			} else if !own {
				return nil, ErrUnavailable
			}
		}
		v.Upvotes = entry.Projection.Upvotes
		v.Downvotes = entry.Projection.Downvotes
		v.MyVote = entry.Projection.MyVote
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) Get(ctx context.Context, p Principal, id string) (View, error) {
	return s.GetWithOptions(ctx, p, id, ViewOptions{})
}
func (s *Service) GetOwn(ctx context.Context, p Principal, id string) (View, error) {
	return s.GetWithOptions(ctx, p, id, ViewOptions{Own: true})
}

// GetWithOptions reads a public or owner-scoped entry with the requested response fields.
func (s *Service) GetWithOptions(ctx context.Context, p Principal, id string, options ViewOptions) (View, error) {
	own := options.Own
	if own && p.Subject == "" {
		return View{}, ErrForbidden
	}
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
	views, err := s.render(ctx, p, []Entry{entry}, own, school, options)
	if err != nil {
		return View{}, err
	}
	if len(views) == 0 {
		return View{}, ErrNotFound
	}
	return views[0], nil
}
func (s *Service) List(ctx context.Context, p Principal, f Filter) (Page, error) {
	s = s.withReadCache()
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
	// Personal filters refer only to this owner's locally stored IDs.
	f.EquivalentTargets, f.EquivalentTeachers = nil, nil
	if f.Owner == "" && f.TargetID != "" {
		kind := "course"
		if f.Kind == "mentor" {
			kind = "teacher"
		}
		object, err := s.catalogTarget(ctx, kind, f.TargetID)
		if errors.Is(err, ErrNotFound) && (f.Kind == "reply" || f.Kind == "") {
			kind = "teacher"
			object, err = s.catalogTarget(ctx, kind, f.TargetID)
		}
		if err != nil {
			return out, err
		}
		if object.SchoolID != f.SchoolID {
			return out, ErrForbidden
		}
		if object.Inactive {
			return out, ErrNotFound
		}
		f.EquivalentTargets, err = s.equivalentIDs(ctx, kind, f.TargetID)
		if err != nil {
			return out, err
		}
	}
	if f.Owner == "" && f.TeacherID != "" {
		object, err := s.catalogTarget(ctx, "teacher", f.TeacherID)
		if err != nil {
			return out, err
		}
		if object.SchoolID != f.SchoolID {
			return out, ErrForbidden
		}
		if object.Inactive {
			return out, ErrNotFound
		}
		f.EquivalentTeachers, err = s.equivalentIDs(ctx, "teacher", f.TeacherID)
		if err != nil {
			return out, err
		}
	}
	f.Viewer = p.Subject
	f.Now = s.now()
	const maxEmptyPageRefills = 3
	stepFilter := f
	for attempt := 0; attempt <= maxEmptyPageRefills; attempt++ {
		if attempt > 0 {
			stepFilter = f
			stepFilter.After = out.Next
			if err := decodeCursor(&stepFilter); err != nil {
				return Page{Items: []View{}}, err
			}
		}
		var entries []Entry
		err := s.Repo.Transaction(ctx, func(t Transaction) error {
			if stepFilter.RootID != "" && stepFilter.Owner == "" {
				root, err := s.accessible(t, stepFilter.RootID)
				if err != nil {
					return err
				}
				if root.SchoolID != stepFilter.SchoolID {
					return ErrForbidden
				}
			}
			var err error
			entries, err = t.List(stepFilter)
			return err
		})
		if err != nil {
			return Page{Items: []View{}}, err
		}
		nextCursor := ""
		if len(entries) > f.Limit {
			nextCursor = encodeCursor(f, entries[f.Limit-1])
			entries = entries[:f.Limit]
		}
		if f.HasTeacherReply != nil && len(entries) > 0 {
			entries, err = s.filterByTeacherReply(ctx, entries, *f.HasTeacherReply, stepFilter.Now)
			if err != nil {
				return Page{Items: []View{}}, err
			}
		}
		items, err := s.render(ctx, p, entries, f.Owner != "", f.SchoolID, ViewOptions{IncludeTeacherIDs: f.IncludeTeacherIDs, IncludeParentSummary: f.IncludeParentSummary})
		if err != nil {
			return Page{Items: []View{}}, err
		}
		out.Items = items
		out.Next = nextCursor
		if len(out.Items) > 0 || out.Next == "" {
			break
		}
	}
	if f.IncludeReplyPreview {
		if err := s.addReplyPreviews(ctx, p, &out, f.SchoolID, f.Owner != "", f.IncludeParentSummary); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Service) filterByTeacherReply(ctx context.Context, entries []Entry, want bool, now time.Time) ([]Entry, error) {
	roots := make([]string, 0, len(entries))
	for _, entry := range entries {
		roots = append(roots, entry.ID)
	}
	var candidates []Entry
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(ReplyPreviewTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		candidates, err = port.ReplyDisclosureCandidates(roots, "", now)
		return err
	})
	if err != nil {
		return nil, err
	}
	disclosedRoots := map[string]bool{}
	for _, cand := range candidates {
		if disclosedRoots[cand.RootID] {
			continue
		}
		if _, public := s.disclosed(ctx, cand); public {
			disclosedRoots[cand.RootID] = true
		}
	}
	filtered := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if disclosedRoots[entry.ID] == want {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}
