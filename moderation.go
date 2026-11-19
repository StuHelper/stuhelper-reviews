package reviews

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

var moderationReasonCodes = []string{
	"pending_review",
	"spam",
	"personal_attack",
	"privacy_leak",
	"wrong_teacher",
	"academic_integrity",
	"low_quality",
	"policy_violation",
	"other",
}

func validModerationReasonCode(code string) bool {
	return slices.Contains(moderationReasonCodes, code)
}

// Apply executes a moderation decision after authorization for this object/school.
// A decision ID and content revision fence every retry and stale delivery.
func (s *Service) Apply(ctx context.Context, p Principal, d Decision) (Result, error) {
	var out Result
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !validID(d.ID) || !validID(d.ObjectID) || d.Revision < 1 || !textOK(d.Reason, 2000) || strings.TrimSpace(d.Reason) == "" || !slices.Contains([]string{"approve_append", "reject_append", "publish", "hide", "restore", "delete", "edit", "identity", "feature", "unfeature", "reassign"}, d.Action) {
		return out, ErrInput
	}
	isAppend := d.Action == "approve_append" || d.Action == "reject_append"
	if isAppend != (d.AppendID != "") || (isAppend && !validID(d.AppendID)) {
		return out, ErrInput
	}
	if d.ReasonCode != "" && !validModerationReasonCode(d.ReasonCode) {
		return out, ErrInput
	}
	var attribution AttributionPreview
	var evidence []Evidence
	var preEntry Entry
	if err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		preEntry, e = t.Entry(d.ObjectID)
		return e
	}); err != nil {
		return out, err
	}
	if _, err := s.authorize(ctx, p, "moderate", preEntry.SchoolID); err != nil {
		return out, err
	}
	if d.Action == "reassign" {
		var err error
		attribution, evidence, err = s.attribution(ctx, p, d.ObjectID, d.TeacherID)
		if err != nil {
			return out, err
		}
		if d.EvidenceRevision != attribution.EvidenceRevision {
			return out, ErrConflict
		}
	}
	rk := hash([]string{"moderation", d.ID})
	h := hash(d)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		entry, e := t.Entry(d.ObjectID)
		if e != nil {
			return e
		}
		if entry.SchoolID != preEntry.SchoolID {
			return ErrConflict
		}
		if e = t.Lock(entry.RootID); e != nil {
			return e
		}
		entry, e = t.Entry(d.ObjectID)
		if e != nil {
			return e
		}
		if entry.SchoolID != preEntry.SchoolID {
			return ErrConflict
		}
		cached, ok, e := replay(t, rk, h, s.now())
		if e != nil {
			return e
		}
		if ok {
			out = cached
			return nil
		}
		if entry.Revision != d.Revision {
			return revisionConflictErr(entry.Revision)
		}
		if entry.ExpiresAt != nil && !s.now().Before(*entry.ExpiresAt) {
			return ErrNotFound
		}
		switch d.Action {
		case "approve_append", "reject_append":
			idx := slices.IndexFunc(entry.Appends, func(a Append) bool { return a.ID == d.AppendID })
			if idx < 0 {
				return ErrNotFound
			}
			item := &entry.Appends[idx]
			if entry.State != "published" || (!item.ReviewRequested && ((item.State == "published" && d.Action == "approve_append") || (item.State == "rejected" && d.Action == "reject_append"))) {
				return ErrConflict
			}
			item.ReviewRequested = false
			if d.Action == "approve_append" {
				item.State = "published"
			} else {
				item.State = "rejected"
			}
		case "feature", "unfeature":
			if entry.State != "published" {
				return ErrConflict
			}
			entry.Featured = d.Action == "feature"
		case "reassign":
			if entry.Input.Kind != "course" || entry.State == "withdrawn" {
				return ErrConflict
			}
			if attribution.Revision != entry.Revision {
				return ErrConflict
			}
			entry.Input.TeacherID = d.TeacherID
			entry.TeacherIDs = attribution.TeacherIDs
			entry.Evidence = evidence
			if d.ReasonCode != "" {
				entry.ModerationReasonCode = d.ReasonCode
			}

		case "publish":
			if entry.State != "pending" && !(entry.State == "published" && entry.ReviewRequested) {
				return ErrConflict
			}
			entry.State = "published"
			entry.ExpiresAt = nil
			entry.ReviewRequested = false
			entry.ModerationReasonCode = ""
			if entry.FirstPublishedAt == nil {
				now := s.now()
				entry.FirstPublishedAt = &now
			}
		case "hide":
			if entry.State != "published" {
				return ErrConflict
			}
			entry.State = "hidden"
			entry.ReviewRequested = false
			if d.ReasonCode != "" {
				entry.ModerationReasonCode = d.ReasonCode
			} else {
				entry.ModerationReasonCode = "policy_violation"
			}
			entry.ExpiresAt = nil
		case "restore":
			if entry.State != "hidden" && entry.State != "deleted" {
				return ErrConflict
			}
			entry.State = "published"
			entry.ExpiresAt = nil
			entry.ReviewRequested = false
			entry.ModerationReasonCode = ""
			if entry.FirstPublishedAt == nil {
				now := s.now()
				entry.FirstPublishedAt = &now
			}
		case "delete":
			if entry.State == "withdrawn" {
				return ErrConflict
			}
			entry.State = "deleted"
			entry.ReviewRequested = false
			if d.ReasonCode != "" {
				entry.ModerationReasonCode = d.ReasonCode
			} else {
				entry.ModerationReasonCode = "policy_violation"
			}
			entry.ExpiresAt = nil
		case "edit":
			if d.Body == nil || !textOK(*d.Body, 16000) || strings.TrimSpace(*d.Body) == "" {
				return ErrInput
			}
			if entry.State == "withdrawn" {
				return ErrConflict
			}
			entry.Input.Body = *d.Body
			entry.ReviewRequested = false
			if d.ReasonCode != "" {
				entry.ModerationReasonCode = d.ReasonCode
			}
		case "identity":
			if (d.Subject == "" && d.AuthorHandle == "") || (d.Subject != "" && d.AuthorHandle != "") {
				return ErrInput
			}
			var identity Identity
			var e error
			if d.AuthorHandle != "" {
				identity, e = t.IdentityByHandle(entry.RootID, d.AuthorHandle)
			} else {
				identity, e = t.Identity(entry.RootID, d.Subject)
			}
			if errors.Is(e, ErrNotFound) {
				return ErrNotFound
			}
			if e != nil {
				return e
			}
			identity.Signed = d.Signed
			if e = t.SaveIdentity(identity); e != nil {
				return e
			}
		}
		entry.ChangeOrigin = "moderator"
		entry.ChangeReason = d.Reason
		entry.Revision++
		entry.UpdatedAt = s.now()
		if e = t.SaveEntry(entry); e != nil {
			return e
		}
		if e = s.record(t, rk, h, "moderated", p, entry); e != nil {
			return e
		}
		out = result(entry)
		return nil
	})
	return out, err
}

func (s *Service) BatchApprove(ctx context.Context, p Principal, key string, req AdminBatchApproveRequest) (AdminBatchApproveResponse, error) {
	out := AdminBatchApproveResponse{Items: []AdminBatchItemResult{}}
	if p.Subject == "" {
		return out, ErrForbidden
	}
	if !ValidIdempotencyKey(key) || !validID(req.SchoolID) || strings.TrimSpace(req.Reason) == "" || !textOK(req.Reason, 2000) || len(req.Items) < 1 || len(req.Items) > 20 {
		return out, ErrInput
	}
	seen := make(map[string]bool, len(req.Items))
	for _, item := range req.Items {
		if !validID(item.ObjectID) || item.Revision < 1 || seen[item.ObjectID] {
			return out, ErrInput
		}
		seen[item.ObjectID] = true
	}
	_, err := s.authorize(ctx, p, "moderate", req.SchoolID)
	if err != nil {
		return out, err
	}
	rk := hash([]string{p.Subject, "batch_approve", req.SchoolID, key})
	h := hash(req)
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(rk); e != nil {
			return e
		}
		now := s.now()
		oldReplay, e := t.Replay(rk)
		if e == nil && now.Before(oldReplay.Until) {
			if oldReplay.Digest != h || oldReplay.Batch == nil {
				return ErrConflict
			}
			out = *oldReplay.Batch
			return nil
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		roots := make([]string, 0, len(req.Items))
		for _, item := range req.Items {
			entry, err := t.Entry(item.ObjectID)
			if err == nil {
				roots = append(roots, entry.RootID)
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		slices.Sort(roots)
		roots = slices.Compact(roots)
		for _, rootID := range roots {
			if err := t.Lock(rootID); err != nil {
				return err
			}
		}
		approved := make([]Entry, 0, len(req.Items))
		results := make([]AdminBatchItemResult, 0, len(req.Items))
		for _, item := range req.Items {
			entry, err := t.Entry(item.ObjectID)
			if errors.Is(err, ErrNotFound) {
				results = append(results, AdminBatchItemResult{ObjectID: item.ObjectID, Status: "not_found"})
				continue
			}
			if err != nil {
				return err
			}
			if entry.SchoolID != req.SchoolID {
				results = append(results, AdminBatchItemResult{ObjectID: item.ObjectID, Status: "forbidden"})
				continue
			}
			now := s.now()
			if entry.State == "withdrawn" || (entry.ExpiresAt != nil && !now.Before(*entry.ExpiresAt)) {
				results = append(results, AdminBatchItemResult{ObjectID: item.ObjectID, Status: "not_found"})
				continue
			}
			if entry.RootID != entry.ID {
				root, err := t.Entry(entry.RootID)
				if errors.Is(err, ErrNotFound) {
					results = append(results, AdminBatchItemResult{ObjectID: item.ObjectID, Status: "not_found"})
					continue
				}
				if err != nil {
					return err
				}
				if root.State == "withdrawn" || (root.ExpiresAt != nil && !now.Before(*root.ExpiresAt)) {
					results = append(results, AdminBatchItemResult{ObjectID: item.ObjectID, Status: "not_found"})
					continue
				}
			}
			if entry.Revision != item.Revision || (entry.State != "pending" && !(entry.State == "published" && entry.ReviewRequested)) {
				results = append(results, AdminBatchItemResult{ObjectID: item.ObjectID, Status: "conflict", State: entry.State, Revision: entry.Revision})
				continue
			}
			entry.State = "published"
			entry.ExpiresAt = nil
			entry.ReviewRequested = false
			entry.ModerationReasonCode = ""
			if entry.FirstPublishedAt == nil {
				publishedAt := now
				entry.FirstPublishedAt = &publishedAt
			}
			entry.ChangeOrigin = "moderator"
			entry.ChangeReason = req.Reason
			entry.Revision++
			entry.UpdatedAt = now
			approved = append(approved, entry)
			eventID := hash([]string{rk, entry.ID})
			if err := t.Event(Event{ID: eventID, Type: "reviews.moderated.v1", Reason: req.Reason, Subject: "entry/" + entry.ID, SchoolID: entry.SchoolID, Actor: p.Subject, Revision: entry.Revision, At: now}); err != nil {
				return err
			}
			results = append(results, AdminBatchItemResult{ObjectID: item.ObjectID, Status: "approved", State: entry.State, Revision: entry.Revision})
		}
		if len(approved) > 0 {
			writer, ok := t.(interface{ SaveEntryBatch([]Entry) error })
			if !ok {
				return ErrUnavailable
			}
			if err := writer.SaveEntryBatch(approved); err != nil {
				return err
			}
		}
		out = AdminBatchApproveResponse{Items: results}
		return t.SaveReplay(rk, Replay{Owner: p.Subject, Digest: h, Batch: &out, Until: s.now().Add(24 * time.Hour), ObjectID: req.SchoolID})
	})
	return out, err
}
