package postgres

import (
	reviews "github.com/StuHelper/stuhelper-reviews"
	"time"
)

func (t *transaction) Aggregates(kind, target string, from, until *time.Time) ([]reviews.Aggregate, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT target_id,
 COALESCE(sum((data->'input'->'ratings'->>0)::integer),0),COALESCE(sum((data->'input'->'ratings'->>1)::integer),0),
 COALESCE(sum((data->'input'->'ratings'->>2)::integer),0),COALESCE(sum((data->'input'->'ratings'->>3)::integer),0),COALESCE(sum((data->'input'->'ratings'->>4)::integer),0),
 count(*) FILTER(WHERE (data->'input'->'ratings'->>0)::integer>0),count(*) FILTER(WHERE (data->'input'->'ratings'->>1)::integer>0),
 count(*) FILTER(WHERE (data->'input'->'ratings'->>2)::integer>0),count(*) FILTER(WHERE (data->'input'->'ratings'->>3)::integer>0),count(*) FILTER(WHERE (data->'input'->'ratings'->>4)::integer>0)
 FROM reviews.entries WHERE kind='course' AND state='published' AND (expires_at IS NULL OR expires_at>clock_timestamp())
 AND (($1='course' AND target_id=$2) OR ($1='teacher' AND data->'teacher_ids' ? $2))
 AND ($3::timestamptz IS NULL OR first_published_at >= $3) AND ($4::timestamptz IS NULL OR first_published_at < $4)
 GROUP BY target_id ORDER BY target_id`, kind, target, from, until)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []reviews.Aggregate{}
	for rows.Next() {
		var a reviews.Aggregate
		if e = rows.Scan(&a.CourseID, &a.Sums[0], &a.Sums[1], &a.Sums[2], &a.Sums[3], &a.Sums[4], &a.Counts[0], &a.Counts[1], &a.Counts[2], &a.Counts[3], &a.Counts[4]); e != nil {
			return nil, safe(e)
		}
		out = append(out, a)
	}
	return out, safe(rows.Err())
}
