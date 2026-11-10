package postgres

import (
	"context"
	m "github.com/StuHelper/stuhelper-reviews"
	"time"
)

func (r *Repository) ActivityDisclosureEntries(ctx context.Context, id string, limit int) ([]string, error) {
	target, day, e := m.ParseActivityID(id)
	if e != nil {
		return nil, e
	}
	if limit < 1 || limit > 100 {
		return nil, m.ErrInput
	}
	rows, e := r.DB.QueryContext(ctx, `SELECT e.id FROM reviews.entries e JOIN reviews.entries root ON root.id=e.root_id
 WHERE e.state='published' AND root.state='published' AND COALESCE((e.data->>'teacher_reply')::boolean,false)
 AND ((root.kind='course' AND (($1='course' AND root.data->>'activity_target_id'=$2) OR ($1='teacher' AND root.data->'teacher_ids' ? $2))) OR (root.kind='mentor' AND $1='teacher' AND root.data->>'activity_target_id'=$2))
 AND ((e.first_published_at>=$3 AND e.first_published_at<$4) OR EXISTS(SELECT 1 FROM reviews.votes v WHERE v.id=e.id AND v.created_at>=$3 AND v.created_at<$4))
 ORDER BY e.id LIMIT $5`, target.Kind, target.ID, day, day.Add(24*time.Hour), limit+1)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, safe(e)
		}
		out = append(out, id)
	}
	if e = rows.Err(); e != nil {
		return nil, safe(e)
	}
	if len(out) > limit {
		return nil, m.ErrUnavailable
	}
	return out, nil
}
