package postgres

import (
	"encoding/json"
	r "github.com/StuHelper/stuhelper-reviews"
	"time"
)

const replyVisible = ` FROM reviews.entries e JOIN reviews.entries root ON root.id=e.root_id
 WHERE e.root_id=ANY($1::text[]) AND e.kind='reply' AND e.state='published' AND root.state='published'
 AND (e.expires_at IS NULL OR e.expires_at>$3) AND (root.expires_at IS NULL OR root.expires_at>$3)`

func (t *transaction) ReplyDisclosureCandidates(roots []string, viewer string, now time.Time) ([]r.Entry, error) {
	entries, err := list[r.Entry](t, `SELECT DISTINCT ON(e.root_id,e.owner)
 jsonb_build_object('root_id',e.root_id,'owner',e.owner,'school_id',e.school_id,
 'teacher_reply',true,'disclosure_course',e.data->'disclosure_course','disclosure_term',e.data->'disclosure_term',
 'input',jsonb_build_object('kind','reply','disclosures',e.data->'input'->'disclosures','ratings',e.data->'input'->'ratings'))`+replyVisible+`
 AND COALESCE((e.data->>'teacher_reply')::boolean,false) AND e.owner<>$2
 AND NOT EXISTS(SELECT 1 FROM reviews.closed_owners c WHERE c.owner=e.owner)
 ORDER BY e.root_id,e.owner,e.id LIMIT 1001`, roots, viewer, now)
	if len(entries) > 1000 {
		return nil, r.ErrUnavailable
	}
	return entries, err
}

func (t *transaction) ReplyPreviews(roots []string, viewer string, now time.Time, allowed []r.DiscussionAuthor) (r.ReplyBatch, error) {
	out := r.ReplyBatch{Counts: map[string]int{}, Entries: []r.Entry{}}
	if _, err := t.tx.ExecContext(t.ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
		return out, safe(err)
	}
	raw, err := json.Marshal(allowed)
	if err != nil {
		return out, r.ErrInput
	}
	predicate := replyVisible + ` AND ($2::text IS NOT NULL)
 AND (NOT COALESCE((e.data->>'teacher_reply')::boolean,false) OR e.owner=$2
 OR EXISTS(SELECT 1 FROM reviews.closed_owners c WHERE c.owner=e.owner)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements($4::jsonb) allowed
 WHERE allowed->>'RootID'=e.root_id AND allowed->>'Owner'=e.owner))`
	rows, err := t.tx.QueryContext(t.ctx, `SELECT e.root_id,count(*)`+predicate+` GROUP BY e.root_id`, roots, viewer, now, string(raw))
	if err != nil {
		return out, safe(err)
	}
	for rows.Next() {
		var id string
		var count int
		if err = rows.Scan(&id, &count); err != nil {
			rows.Close()
			return out, safe(err)
		}
		out.Counts[id] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, safe(err)
	}
	out.Entries, err = t.projected(`WITH ranked AS (
 SELECT e.id,row_number() OVER(PARTITION BY e.root_id ORDER BY e.created_at,e.id) AS position`+predicate+`)
 SELECT e.data,i.data,
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=1),
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=-1),
 COALESCE((SELECT value FROM reviews.votes v WHERE v.id=e.id AND v.subject=$2),0)
 FROM ranked JOIN reviews.entries e USING(id) JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner
 WHERE ranked.position<=$5 ORDER BY e.root_id,e.created_at,e.id`, roots, viewer, now, string(raw), r.ReplyPreviewLimit)
	return out, err
}
