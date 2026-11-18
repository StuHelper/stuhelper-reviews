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
	EditWindow, DraftLifetime, ExternalTimeout time.Duration
	MinSamples, MaxDrafts                      int
	Recommendations                            RecommendationPolicy
}

func DefaultConfig() Config {
	return Config{EditWindow: 5 * time.Minute, DraftLifetime: 24 * time.Hour, ExternalTimeout: defaultExternalTimeout, MaxDrafts: DefaultMaxDrafts, MinSamples: 1, Recommendations: DefaultRecommendationPolicy()}
}

type Service struct {
	OnDiagnostic DiagnosticHook
	Favorites    FavoriteReferences
	Disclosures  DisclosureProvider
	Repo         Repository
	Catalog      Catalog
	Access       Access
	Moderation   Moderation
	Profiles     Profiles
	Config       Config
	Clock        func() time.Time
}

func New(repo Repository, catalog Catalog, access Access, moderation Moderation, profiles Profiles, c Config) (*Service, error) {
	if c.Recommendations == (RecommendationPolicy{}) {
		c.Recommendations = DefaultRecommendationPolicy()
	}
	if !c.Recommendations.valid() {
		return nil, ErrInput
	}
	if repo == nil || catalog == nil || access == nil || moderation == nil || c.EditWindow <= 0 || c.DraftLifetime <= 0 || c.MinSamples < 1 {
		return nil, ErrInput
	}
	if c.MaxDrafts <= 0 {
		c.MaxDrafts = DefaultMaxDrafts
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

// ValidIdempotencyKey reports whether a write key can be stored as a review receipt.
func ValidIdempotencyKey(v string) bool {
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
	if !validDisclosureKeys(in.Disclosures) {
		return inputFieldErr("invalid_disclosures", "disclosures")
	}
	if !slices.Contains([]string{"course", "mentor", "reply"}, in.Kind) {
		return inputFieldErr("invalid_kind", "kind")
	}
	if !validID(in.TargetID) {
		return inputFieldErr("invalid_target_id", "target_id")
	}
	if !textOK(in.Title, 200) {
		return inputFieldErr("invalid_title", "title")
	}
	if !textOK(in.Body, 16000) {
		return inputFieldErr("invalid_body", "body")
	}
	if !draft {
		if in.Kind != "reply" && strings.TrimSpace(in.Title) == "" {
			return inputFieldErr("title_required", "title")
		}
		if strings.TrimSpace(in.Body) == "" {
			return inputFieldErr("body_required", "body")
		}
	}
	if in.ParentID != "" && (in.Kind != "reply" || !validID(in.ParentID)) {
		return inputFieldErr("invalid_parent_id", "parent_id")
	}
	if in.Kind == "course" {
		if (!draft || in.TermID != "") && !validID(in.TermID) {
			return inputFieldErr("invalid_term_id", "term_id")
		}
		if in.TeacherID != "" && !validID(in.TeacherID) {
			return inputFieldErr("invalid_teacher_id", "teacher_id")
		}
		for _, v := range in.Ratings {
			if v < 0 || v > 5 || !draft && v == 0 {
				return inputFieldErr("invalid_ratings", "ratings")
			}
		}
	} else {
		if in.TermID != "" {
			return inputFieldErr("unexpected_term_id", "term_id")
		}
		if in.TeacherID != "" {
			return inputFieldErr("unexpected_teacher_id", "teacher_id")
		}
		if in.Ratings != [5]int{} {
			return inputFieldErr("unexpected_ratings", "ratings")
		}
	}
	return nil
}
func result(e Entry) Result { return Result{e.ID, e.State, e.Revision, e.ExpiresAt} }
func (s *Service) authorize(ctx context.Context, p Principal, action, school string) (Grant, error) {
	if p.Subject == "" && action != "read_full" {
		return Grant{}, ErrForbidden
	}
	if s.Access == nil {
		s.emitDiagnostic("authorize_"+action, "access", time.Now(), ErrUnavailable)
		return Grant{}, ErrUnavailable
	}
	start := time.Now()
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	g, err := s.Access.Authorize(callCtx, p, action, school)
	s.emitDiagnostic("authorize_"+action, "access", start, err)
	return g, wrapExternalErr(ctx, err)
}
func (s *Service) prepare(ctx context.Context, p Principal, in Input) (Target, []Evidence, []string, Grant, error) {
	kind := "course"
	action := "publish_course"
	if in.Kind == "mentor" {
		kind = "teacher"
		action = "publish_mentor"
	}
	target, e := s.catalogTargetFor(ctx, "prepare_target", kind, in.TargetID)
	if e != nil {
		return target, nil, nil, Grant{}, e
	}
	if target.Inactive {
		return target, nil, nil, Grant{}, forbiddenErr("target_inactive", "target_id")
	}
	g, e := s.authorize(ctx, p, action, target.SchoolID)
	if e != nil {
		if errors.Is(e, ErrForbidden) {
			return target, nil, nil, g, forbiddenErr("eligibility_missing", "")
		}
		return target, nil, nil, g, e
	}
	if in.Kind == "mentor" {
		return target, nil, nil, g, nil
	}
	term, e := s.catalogTargetFor(ctx, "prepare_term", "term", in.TermID)
	if e != nil {
		return target, nil, nil, g, e
	}
	if term.SchoolID != target.SchoolID {
		return target, nil, nil, g, inputFieldErr("term_school_mismatch", "term_id")
	}
	evidence, e := s.catalogEvidenceFor(ctx, "prepare_evidence", p, in.TargetID, in.TermID)
	if e != nil {
		return target, nil, nil, g, e
	}
	if len(evidence) > 1000 {
		return target, nil, nil, g, ErrUnavailable
	}
	if (g.EnrollmentRequired || target.TeacherRequired) && len(evidence) == 0 {
		g.Requirements = append(g.Requirements, Requirement{"course_enrollment", "missing"})
		return target, nil, nil, g, NewErrorDetail(ErrEnrollment, "enrollment_missing", "term_id", nil)
	}
	if g.EnrollmentRequired || target.TeacherRequired {
		g.Requirements = append(g.Requirements, Requirement{"course_enrollment", "satisfied"})
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
	ids, e = s.selectedTeachers(ctx, in.TeacherID, ids)
	if e != nil {
		return target, nil, nil, g, e
	}
	ids, e = s.activityTeacherIDs(ctx, target.SchoolID, ids)
	if e != nil {
		return target, nil, nil, g, e
	}
	return target, evidence, ids, g, nil
}
func (s *Service) check(ctx context.Context, e Entry) (string, error) {
	start := time.Now()
	if s.Moderation == nil {
		return "", ErrUnavailable
	}
	callCtx, cancel := s.externalCtx(ctx)
	defer cancel()
	out, err := s.Moderation.Check(callCtx, Candidate{e.ID, e.Input.Kind, e.SchoolID, e.Owner, e.Input.Title, e.Input.Body, e.Revision})
	if err != nil {
		s.emitDiagnostic("moderation_check", "moderation", start, err)
		return "", wrapExternalErr(ctx, err)
	}
	if !slices.Contains([]string{"published", "pending", "blocked", "review"}, out) {
		s.emitDiagnostic("moderation_check", "moderation", start, ErrUnavailable)
		return "", ErrUnavailable
	}
	if out == "blocked" {
		return "", forbiddenErr("moderation_blocked", "body")
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
		return Result{}, false, idempotencyConflictErr()
	}
	return r.Result, true, nil
}
func (s *Service) record(t Transaction, key, digest, action string, p Principal, e Entry) error {
	reason := ""
	actor := "anonymous"
	if action == "moderated" {
		reason = e.ChangeReason
		actor = p.Subject
	}
	if err := t.Event(Event{ID: key, Type: "reviews." + action + ".v1", Reason: reason, Subject: "entry/" + e.ID, SchoolID: e.SchoolID, Actor: actor, Revision: e.Revision, At: s.now()}); err != nil {
		return err
	}
	return t.SaveReplay(key, Replay{Owner: p.Subject, Digest: digest, Result: result(e), Until: s.now().Add(24 * time.Hour), ObjectID: e.ID})
}
func (s *Service) Create(ctx context.Context, p Principal, key string, in Input) (Result, error) {
	return s.create(ctx, p, key, in, "", 0)
}
func (s *Service) PublishDraft(ctx context.Context, p Principal, id, key string, in PublishDraft) (Result, error) {
	if !validID(id) {
		return Result{}, inputFieldErr("invalid_id", "id")
	}
	if in.Revision < 1 {
		return Result{}, inputFieldErr("invalid_revision", "revision")
	}
	if in.Input.Kind == "reply" {
		return s.publishReply(ctx, p, in.Input.ParentID, key, in.Input, id, in.Revision)
	}
	return s.create(ctx, p, key, in.Input, id, in.Revision)
}
func (s *Service) create(ctx context.Context, p Principal, key string, in Input, draftID string, draftRevision int64) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !ValidIdempotencyKey(key) {
		return out, inputFieldErr("invalid_idempotency_key", "idempotency_key")
	}
	if in.Kind == "reply" {
		return out, inputFieldErr("invalid_kind", "kind")
	}
	if err := validate(in, false); err != nil {
		return out, err
	}
	target, evidence, ids, g, err := s.prepare(ctx, p, in)
	if err != nil {
		return out, err
	}
	if target.TeacherRequired && in.TeacherID == "" {
		return out, inputFieldErr("teacher_required", "teacher_id")
	}
	if _, err = s.validateDisclosures(ctx, p, target.SchoolID, func() string {
		if in.Kind == "course" {
			return in.TargetID
		}
		return ""
	}(), in.TermID, in); err != nil {
		return out, err
	}
	// An old link and its canonical course are the same submission target.
	// Keep Input as the original publication evidence while normalizing only
	// the idempotency identity and digest.
	canonicalInput := in
	canonicalInput.TargetID = target.ID
	rk := hash([]string{p.Subject, "create", target.ID, key})
	h := hash(canonicalInput)
	if draftID != "" {
		rk = hash([]string{p.Subject, "publish_draft", draftID, key})
		h = hash(PublishDraft{Revision: draftRevision, Input: canonicalInput})
	}
	var previous []creationReplay
	if draftID == "" {
		previous, err = s.previousCreationKeys(ctx, p, key, in, target.ID)
		if err != nil {
			return out, err
		}
	}
	now := s.now()
	entry := Entry{ActivityTargetID: target.ID, ChangeOrigin: "author", ID: rk, RootID: rk, Owner: p.Subject, SchoolID: target.SchoolID, Input: in, TeacherIDs: ids, Evidence: evidence, Revision: 1, CreatedAt: now, UpdatedAt: now, RuleVersion: g.RuleVersion}
	state, err := s.check(ctx, entry)
	if err != nil {
		return out, err
	}
	applyModerationOutcome(&entry, state, now)
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		if draftID != "" {
			if e := t.Lock("draft:" + draftID); e != nil {
				return e
			}
		}
		if e := t.Lock(rk); e != nil {
			return e
		}
		if e := t.Lock("closure-owner:" + p.Subject); e != nil {
			return e
		}
		closed, e := t.ClosedOwners([]string{p.Subject})
		if e != nil {
			return e
		}
		if closed[p.Subject] {
			return forbiddenErr("account_closed", "")
		}
		cached, ok, e := replay(t, rk, h, now)
		if e != nil {
			return e
		}
		if ok {
			out = cached
			return nil
		}
		if legacy, found, err := replayPreviousCreation(t, previous, now); err != nil {
			return err
		} else if found {
			out = legacy
			return nil
		}
		if draftID != "" {
			draft, e := t.Draft(draftID)
			if e != nil {
				return e
			}
			if draft.Owner != p.Subject {
				return ErrNotFound
			}
			if draft.Revision != draftRevision {
				return revisionConflictErr(draft.Revision)
			}
			if draft.Input.Kind != in.Kind {
				return inputFieldErr("attribution_immutable", "kind")
			}
			if draft.Input.TargetID != in.TargetID {
				return inputFieldErr("attribution_immutable", "target_id")
			}
			if e = t.DeleteDraft(draftID); e != nil {
				return e
			}
		}
		if _, e = t.Entry(rk); e == nil {
			return ErrConflict
		} else if !errors.Is(e, ErrNotFound) {
			return e
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
	return s.publishReply(ctx, p, parent, key, in, "", 0)
}

func (s *Service) publishReply(ctx context.Context, p Principal, parent, key string, in Input, draftID string, draftRevision int64) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if draftID == "" && (!validID(parent) || in.ParentID != "" && in.ParentID != parent) {
		return out, inputFieldErr("invalid_parent_id", "id")
	}
	if !ValidIdempotencyKey(key) {
		return out, inputFieldErr("invalid_idempotency_key", "idempotency_key")
	}
	if in.Kind != "reply" {
		return out, inputFieldErr("invalid_kind", "kind")
	}
	if err := validate(in, false); err != nil {
		return out, err
	}
	rk := hash([]string{p.Subject, "reply", parent, key})
	h := hash(in)
	if draftID != "" {
		rk = hash([]string{p.Subject, "publish_draft", draftID, key})
		h = hash(PublishDraft{Revision: draftRevision, Input: in})
	}
	var base, root Entry
	replayed := false
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if draftID != "" {
			if e := t.Lock("draft:" + draftID); e != nil {
				return e
			}
		}
		cached, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = cached
			replayed = true
			return nil
		}
		if draftID != "" {
			draft, e := replyDraftForPublish(t, p, draftID, draftRevision, in)
			if e != nil {
				return e
			}
			parent = draft.Input.ParentID
			in.ParentID = parent
		}
		base, e = s.accessible(t, parent)
		if e != nil {
			return e
		}
		root, e = s.accessible(t, base.RootID)
		if e != nil {
			return e
		}
		return nil
	})
	if err != nil || replayed {
		return out, err
	}
	if !s.sameTarget(ctx, root.Input.Kind, base.Input.TargetID, in.TargetID) {
		return out, inputFieldErr("target_mismatch", "target_id")
	}
	parentRevision := base.Revision
	if err := s.requirePublicDisclosure(ctx, parent); err != nil {
		return out, err
	}
	course, term := "", ""
	if root.Input.Kind == "course" {
		course = root.Input.TargetID
		term = root.Input.TermID
	}
	disclosure, err := s.validateDisclosures(ctx, p, root.SchoolID, course, term, in)
	if err != nil {
		return out, err
	}
	g, err := s.replyGrant(ctx, p, root, disclosure)
	if err != nil {
		return out, err
	}
	candidate := Entry{ChangeOrigin: "author", TeacherReply: disclosure.ownCourse, DisclosureCourse: course, DisclosureTerm: term, ID: rk, RootID: base.RootID, ParentID: parent, Owner: p.Subject, SchoolID: base.SchoolID, Input: in, Revision: 1, RuleVersion: g.RuleVersion}
	state, err := s.check(ctx, candidate)
	if err != nil {
		return out, err
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		if draftID != "" {
			if e := t.Lock("draft:" + draftID); e != nil {
				return e
			}
		}
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
		if draftID != "" {
			if _, e := replyDraftForPublish(t, p, draftID, draftRevision, in); e != nil {
				return e
			}
		}
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
		current, e := s.accessible(t, root.ID)
		if e != nil {
			return e
		}
		if current.Revision != root.Revision {
			return revisionConflictErr(current.Revision)
		}
		if base.SchoolID != root.SchoolID {
			return ErrConflict
		}
		if e = t.Lock("closure-owner:" + p.Subject); e != nil {
			return e
		}
		closed, e := t.ClosedOwners([]string{p.Subject})
		if e != nil {
			return e
		}
		if closed[p.Subject] {
			return forbiddenErr("account_closed", "")
		}
		if base.Revision != parentRevision {
			return revisionConflictErr(base.Revision)
		}
		parentIdentity, e := t.Identity(base.RootID, base.Owner)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
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
		entry := Entry{ParentAuthorHandle: parentIdentity.Handle, ChangeOrigin: "author", TeacherReply: disclosure.ownCourse, DisclosureCourse: course, DisclosureTerm: term, ID: rk, RootID: base.RootID, ParentID: parent, Owner: p.Subject, SchoolID: base.SchoolID, Input: in, Revision: 1, CreatedAt: now, UpdatedAt: now, RuleVersion: g.RuleVersion}
		applyModerationOutcome(&entry, state, now)
		if e = t.SaveEntry(entry); e != nil {
			return e
		}
		if draftID != "" {
			if e = t.DeleteDraft(draftID); e != nil {
				return e
			}
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
	if !validID(id) {
		return out, inputFieldErr("invalid_id", "id")
	}
	if !ValidIdempotencyKey(key) {
		return out, inputFieldErr("invalid_idempotency_key", "idempotency_key")
	}
	if !slices.Contains([]string{"withdraw", "delete", "republish", "save_withdrawn", "vote"}, in.Action) {
		return out, inputFieldErr("invalid_action", "action")
	}
	if in.Action == "republish" {
		if in.Input == nil {
			return out, inputFieldErr("input_required", "input")
		}
		if err := validate(*in.Input, false); err != nil {
			return out, err
		}
	}
	if in.Action == "save_withdrawn" {
		if in.Input == nil {
			return out, inputFieldErr("input_required", "input")
		}
		if err := validate(*in.Input, true); err != nil {
			return out, err
		}
	}
	if in.Action == "vote" && (in.Vote < -1 || in.Vote > 1) {
		return out, inputFieldErr("invalid_vote", "vote")
	}
	rk := hash([]string{p.Subject, in.Action, id, key})
	h := hash(in)
	var preEntry Entry
	var preIdentity Identity
	replayed := false
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		cached, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = cached
			replayed = true
			return nil
		}
		preEntry, e = t.Entry(id)
		if e != nil {
			return e
		}
		if in.Action == "vote" {
			return nil
		}
		if preEntry.Owner != p.Subject {
			return ErrNotFound
		}
		if preEntry.ExpiresAt != nil && !s.now().Before(*preEntry.ExpiresAt) {
			return ErrNotFound
		}
		if in.Revision != preEntry.Revision {
			return revisionConflictErr(preEntry.Revision)
		}
		if in.Action == "save_withdrawn" || in.Action == "republish" {
			if preEntry.State != "withdrawn" {
				return conflictStateErr("invalid_state", "state")
			}
			next := *in.Input
			if field := attributionMismatchField(preEntry.Input, next); field != "" {
				return inputFieldErr("attribution_immutable", field)
			}
			if in.Action == "republish" {
				if preEntry.RootID != preEntry.ID {
					if _, e = s.accessible(t, preEntry.RootID); e != nil {
						return e
					}
				}
				preIdentity, e = t.Identity(preEntry.RootID, p.Subject)
				if e != nil {
					return e
				}
			}
		}
		return nil
	})
	if err != nil || replayed {
		return out, err
	}
	if in.Action == "vote" && in.Vote != 0 {
		if err := s.requirePublicDisclosure(ctx, id); err != nil {
			return out, err
		}
		if _, err := s.authorize(ctx, p, "vote", preEntry.SchoolID); err != nil {
			return out, err
		}
	}
	var repubGrant Grant
	var repubState string
	var repubTeacherReply bool
	if in.Action == "save_withdrawn" || in.Action == "republish" {
		course, term := preEntry.DisclosureCourse, preEntry.DisclosureTerm
		if preEntry.Input.Kind == "course" {
			course = preEntry.Input.TargetID
			term = preEntry.Input.TermID
		}
		disclosure, err := s.validateDisclosures(ctx, p, preEntry.SchoolID, course, term, *in.Input)
		if err != nil {
			return out, err
		}
		if in.Action == "republish" {
			if preEntry.Input.Kind == "reply" {
				repubGrant, err = s.replyGrant(ctx, p, preEntry, disclosure)
				if preEntry.TeacherReply && !disclosure.ownCourse {
					return out, forbiddenErr("teacher_disclosure_required", "disclosures")
				}
				if err != nil {
					return out, err
				}
				repubTeacherReply = disclosure.ownCourse
			} else {
				target, _, _, grant, err := s.prepare(ctx, p, *in.Input)
				if err == nil && target.TeacherRequired && in.Input.TeacherID == "" {
					return out, inputFieldErr("teacher_required", "teacher_id")
				}
				if err != nil {
					return out, err
				}
				repubGrant = grant
			}
			cand := preEntry
			nextIn := *in.Input
			nextIn.Signed = preIdentity.Signed
			cand.Input = nextIn
			cand.Revision = preEntry.Revision + 1
			repubState, err = s.check(ctx, cand)
			if err != nil {
				return out, err
			}
		}
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		// Serialize replay, including a retry after permanent deletion.
		if e := t.Lock(rk); e != nil {
			return e
		}
		if e := t.Lock("closure-owner:" + p.Subject); e != nil {
			return e
		}
		closed, e := t.ClosedOwners([]string{p.Subject})
		if e != nil {
			return e
		}
		if closed[p.Subject] {
			return forbiddenErr("account_closed", "")
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
			// Removal only affects this caller's row and must survive qualification
			// expiry, hidden content and unavailable external facts.
			if in.Vote != 0 {
				if entry.SchoolID != preEntry.SchoolID {
					return ErrConflict
				}
				if _, e = s.accessible(t, id); e != nil {
					return e
				}
			}
			if e = t.SetVote(id, p.Subject, in.Vote, s.now()); e != nil {
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
			return revisionConflictErr(entry.Revision)
		}
		if in.Action == "save_withdrawn" {
			if entry.State != "withdrawn" {
				return conflictStateErr("invalid_state", "state")
			}
			next := *in.Input
			if field := attributionMismatchField(entry.Input, next); field != "" {
				return inputFieldErr("attribution_immutable", field)
			}
			next.Signed = entry.Input.Signed
			entry.ChangeOrigin = "author"
			entry.ChangeReason = ""
			entry.Input = next
			entry.Revision++
			entry.UpdatedAt = s.now()
			if e = t.SaveEntry(entry); e != nil {
				return e
			}
			if e = s.record(t, rk, h, "draft_saved", p, entry); e != nil {
				return e
			}
			out = result(entry)
			return nil
		}
		if in.Action == "republish" {
			if entry.State != "withdrawn" {
				return conflictStateErr("invalid_state", "state")
			}
			return s.commitRepublish(t, p, entry, rk, h, *in.Input, repubGrant, repubState, repubTeacherReply, &out)
		}
		if _, e = s.accessible(t, id); e != nil {
			return e
		}
		if entry.FirstPublishedAt == nil || !s.now().Before(entry.FirstPublishedAt.Add(s.Config.EditWindow)) {
			return NewErrorDetail(ErrWindow, "edit_window_expired", "action", nil)
		}
		entry.ChangeOrigin = "author"
		entry.ChangeReason = ""
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
func attributionMismatchField(old, next Input) string {
	switch {
	case next.Kind != old.Kind:
		return "kind"
	case next.TargetID != old.TargetID:
		return "target_id"
	case next.TermID != old.TermID:
		return "term_id"
	case next.TeacherID != old.TeacherID:
		return "teacher_id"
	default:
		return ""
	}
}

func (s *Service) commitRepublish(t Transaction, p Principal, e Entry, key, digest string, in Input, g Grant, state string, teacherReply bool, out *Result) error {
	// Editing text cannot change attribution, the term, or the discussion identity.
	if field := attributionMismatchField(e.Input, in); field != "" {
		return inputFieldErr("attribution_immutable", field)
	}
	if e.RootID != e.ID {
		if _, err := s.accessible(t, e.RootID); err != nil {
			return err
		}
	}
	if e.Input.Kind == "reply" {
		e.TeacherReply = teacherReply
	}
	identity, err := t.Identity(e.RootID, p.Subject)
	if err != nil {
		return err
	}
	in.Signed = identity.Signed
	e.ChangeOrigin = "author"
	e.ChangeReason = ""
	e.Input = in
	e.Revision++
	e.UpdatedAt = s.now()
	e.RuleVersion = g.RuleVersion
	applyModerationOutcome(&e, state, s.now())
	if err = t.SaveEntry(e); err != nil {
		return err
	}
	if err = s.record(t, key, digest, "republished", p, e); err != nil {
		return err
	}
	*out = result(e)
	return nil
}
func (s *Service) statsPage(ctx context.Context, kind, target string, from, until *time.Time, after string, limit int, school string) (Statistics, error) {
	out := Statistics{From: from, Until: until, Courses: []CourseStatistic{}}
	if limit < 1 || limit > 100 {
		return out, ErrInput
	}
	if !slices.Contains([]string{"course", "teacher"}, kind) || !validID(target) || from != nil && until != nil && !from.Before(*until) {
		return out, ErrInput
	}
	ids, err := s.equivalentIDs(ctx, kind, target)
	if err != nil {
		return out, err
	}
	var rows []Aggregate
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		rows, e = t.Aggregates(kind, ids, school, from, until, s.now())
		return e
	})
	if err != nil {
		return out, err
	}
	rows, err = s.canonicalAggregates(ctx, rows)
	if err != nil {
		return out, err
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
	return out, nil
}

// applyModerationOutcome folds pre/post-publication moderation outcomes onto an entry.
// Pending moderation preserves any existing withdrawal ExpiresAt so editing cannot extend it.
func applyModerationOutcome(e *Entry, state string, now time.Time) {
	e.ReviewRequested = state == "review" || state == "pending"
	e.ModerationReasonCode = ""
	if state == "pending" {
		e.ModerationReasonCode = "pending_review"
	}
	if state == "review" {
		state = "published"
	}
	e.State = state
	if state == "published" {
		e.ExpiresAt = nil
		if e.FirstPublishedAt == nil {
			publishedAt := now
			e.FirstPublishedAt = &publishedAt
		}
	}
}
