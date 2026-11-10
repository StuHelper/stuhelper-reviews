package postgres

import (
	"errors"
	m "github.com/StuHelper/stuhelper-reviews"
	"sort"
	"time"
)

type rootActivity struct {
	ID, Kind, Target, School string
	Day                      time.Time
	Reviews, Interactions    int64
}

// refreshActivity derives effective contributions solely from this discussion's
// rows. First publication and first cast dates survive edits and vote flips.
// Root visibility gates all child replies and votes. Changed buckets are applied
// as deltas, so other discussions sharing a course/teacher are never overwritten.
func (t *transaction) refreshActivity(rootID string, at time.Time) error {
	if e := t.Lock("activity-root:" + rootID); e != nil {
		return e
	}
	old := map[string]rootActivity{}
	rows, e := t.tx.QueryContext(t.ctx, `SELECT id,kind,target_id,school_id,day,reviews,interactions FROM reviews.activity_contributions WHERE root_id=$1 ORDER BY id LIMIT 10001`, rootID)
	if e != nil {
		return safe(e)
	}
	for rows.Next() {
		var v rootActivity
		if e = rows.Scan(&v.ID, &v.Kind, &v.Target, &v.School, &v.Day, &v.Reviews, &v.Interactions); e != nil {
			rows.Close()
			return safe(e)
		}
		old[v.ID] = v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return safe(e)
	}
	if len(old) > 10000 {
		return m.ErrUnavailable
	}
	next := map[string]rootActivity{}
	root, e := t.Entry(rootID)
	if e != nil && !errors.Is(e, m.ErrNotFound) {
		return e
	}
	if e == nil && root.State == "published" && (root.ExpiresAt == nil || at.Before(*root.ExpiresAt)) {
		targets := map[string]string{}
		if root.Input.Kind == "course" {
			targets["course:"+root.ActivityTargetID] = root.ActivityTargetID
			for _, id := range root.TeacherIDs {
				targets["teacher:"+id] = id
			}
		}
		if root.Input.Kind == "mentor" {
			targets["teacher:"+root.ActivityTargetID] = root.ActivityTargetID
		}
		rows, e = t.tx.QueryContext(t.ctx, `SELECT activity_day,sum(reviews),sum(interactions) FROM (
   SELECT (first_published_at AT TIME ZONE 'UTC')::date AS activity_day,CASE WHEN id=root_id AND kind='course' THEN 1 ELSE 0 END reviews,CASE WHEN kind='reply' THEN 1 ELSE 0 END interactions
   FROM reviews.entries WHERE root_id=$1 AND state='published' AND first_published_at IS NOT NULL AND (expires_at IS NULL OR expires_at>$2)
   UNION ALL
   SELECT (v.created_at AT TIME ZONE 'UTC')::date,0,1 FROM reviews.votes v JOIN reviews.entries e ON e.id=v.id
   WHERE e.root_id=$1 AND e.state='published' AND (e.expires_at IS NULL OR e.expires_at>$2)
  ) facts GROUP BY activity_day ORDER BY activity_day LIMIT 10001`, rootID, at)
		if e != nil {
			return safe(e)
		}
		for rows.Next() {
			var day time.Time
			var reviews, interactions int64
			if e = rows.Scan(&day, &reviews, &interactions); e != nil {
				rows.Close()
				return safe(e)
			}
			if reviews == 0 && interactions == 0 {
				continue
			}
			for key, target := range targets {
				kind := "course"
				if key[:7] == "teacher" {
					kind = "teacher"
				}
				id := m.ActivityID(kind, target, day)
				next[id] = rootActivity{id, kind, target, root.SchoolID, day, reviews, interactions}
			}
			if len(next) > 10000 {
				rows.Close()
				return m.ErrUnavailable
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return safe(e)
		}
	}
	keys := map[string]bool{}
	for key := range old {
		keys[key] = true
	}
	for key := range next {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		before, after := old[id], next[id]
		dr, di := after.Reviews-before.Reviews, after.Interactions-before.Interactions
		if dr == 0 && di == 0 {
			continue
		}
		bucket := after
		if bucket.ID == "" {
			bucket = before
		}
		if e = t.changeActivity(bucket.Kind, bucket.Target, bucket.School, bucket.Day, dr, di, nil, at); e != nil {
			return e
		}
		if after.ID == "" {
			_, e = t.tx.ExecContext(t.ctx, `DELETE FROM reviews.activity_contributions WHERE root_id=$1 AND id=$2`, rootID, id)
		} else {
			_, e = t.tx.ExecContext(t.ctx, `INSERT INTO reviews.activity_contributions(root_id,id,kind,target_id,school_id,day,reviews,interactions) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(root_id,id) DO UPDATE SET reviews=EXCLUDED.reviews,interactions=EXCLUDED.interactions`, rootID, id, after.Kind, after.Target, after.School, after.Day, after.Reviews, after.Interactions)
		}
		if e != nil {
			return safe(e)
		}
	}
	return nil
}
