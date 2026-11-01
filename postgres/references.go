package postgres

import "time"

func (t *transaction) OwnedReferences(subject string, ids []string, now time.Time) ([]string, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT requested.id FROM unnest($2::text[]) requested(id)
 WHERE EXISTS(SELECT 1 FROM reviews.entries e WHERE e.owner=$1 AND (e.expires_at IS NULL OR e.expires_at>$3)
 AND (requested.id IN(e.target_id,e.term_id,e.school_id) OR COALESCE(e.data->'teacher_ids','[]'::jsonb) ? requested.id))
 OR EXISTS(SELECT 1 FROM reviews.entries e JOIN reviews.votes v ON v.id=e.id JOIN reviews.entries root ON root.id=e.root_id
 WHERE v.subject=$1 AND e.state='published' AND root.state='published' AND (e.expires_at IS NULL OR e.expires_at>$3) AND (root.expires_at IS NULL OR root.expires_at>$3)
 AND requested.id IN(e.target_id,e.term_id,e.school_id))
 OR EXISTS(SELECT 1 FROM reviews.bookmarks b WHERE b.subject=$1 AND b.target_id=requested.id AND NOT EXISTS(SELECT 1 FROM reviews.bookmark_transfers transfer WHERE transfer.subject=b.subject))
 OR EXISTS(SELECT 1 FROM reviews.drafts d WHERE d.owner=$1 AND COALESCE(d.data->'trusted_reference_ids','[]'::jsonb) ? requested.id)
 ORDER BY requested.id`, subject, ids, now)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, safe(err)
		}
		out = append(out, id)
	}
	return out, safe(rows.Err())
}
