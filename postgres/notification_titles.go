package postgres

import (
	r "github.com/StuHelper/stuhelper-reviews"
	"time"
)

func (t *transaction) NotificationCandidates(ids []string, school string, now time.Time) ([]r.NotificationCandidate, error) {
	return list[r.NotificationCandidate](t, `SELECT jsonb_build_object('Entry',e.data-'evidence'-'projection', 'Root',root.data-'evidence'-'projection')
 FROM reviews.entries e JOIN reviews.entries root ON root.id=e.root_id
 WHERE e.id=ANY($1::text[]) AND e.school_id=$2 AND root.school_id=$2 AND e.state='published' AND root.state='published'
 AND (e.expires_at IS NULL OR e.expires_at>$3) AND (root.expires_at IS NULL OR root.expires_at>$3)
 ORDER BY e.id`, ids, school, now)
}
