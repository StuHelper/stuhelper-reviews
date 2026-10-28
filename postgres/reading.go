package postgres

import (
	reviews "github.com/StuHelper/stuhelper-reviews"
	"time"
)

func (t *transaction) List(f reviews.Filter) ([]reviews.Entry, error) {
	cursor, order := "e.id>$10", "e.id"
	args := []any{f.Kind, f.TargetID, f.TermID, f.RootID, f.Owner, f.Now, f.Limit + 1, f.Viewer, f.TeacherID}
	if f.Sort == "newest" {
		cursor = "($10::timestamptz IS NULL OR (e.created_at,e.id)<($10,$11))"
		order = "e.created_at DESC,e.id DESC"
	}
	if f.Sort == "oldest" {
		cursor = "($10::timestamptz IS NULL OR (e.created_at,e.id)>($10,$11))"
		order = "e.created_at,e.id"
	}
	if f.Sort == "newest" || f.Sort == "oldest" {
		args = append(args, f.CursorTime, f.CursorID)
	} else {
		args = append(args, f.After)
	}
	q := `SELECT e.data,i.data,
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=1),
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=-1),
 COALESCE((SELECT value FROM reviews.votes v WHERE v.id=e.id AND v.subject=$8),0)
 FROM reviews.entries e JOIN reviews.entries root ON root.id=e.root_id
 JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner
 WHERE ((e.state='published' AND root.state='published') OR ($5<>'' AND e.owner=$5 AND e.state IN ('pending','hidden','deleted')))
 AND (e.id=e.root_id OR root.state='published')
 AND (e.expires_at IS NULL OR e.expires_at>$6) AND (root.expires_at IS NULL OR root.expires_at>$6)
 AND ($1='' OR e.kind=$1) AND ($2='' OR e.target_id=$2) AND ($3='' OR e.term_id=$3)
 AND ($4='' OR e.root_id=$4) AND ($5='' OR e.owner=$5)
 AND ($9='' OR (e.kind='course' AND (e.data->'teacher_ids') ? $9))
 AND ` + cursor + ` ORDER BY ` + order + ` LIMIT $7`
	return t.projected(q, args...)
}
func (t *transaction) AdminEntries(school, kind, state, after string, limit int, now time.Time) ([]reviews.Entry, error) {
	return t.projected(`SELECT e.data,i.data,0,0,0 FROM reviews.entries e
 JOIN reviews.entries root ON root.id=e.root_id
 JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner
 WHERE e.school_id=$1 AND ($2='' OR e.kind=$2) AND ($3='' OR e.state=$3) AND e.id>$4
 AND e.state<>'withdrawn' AND root.state<>'withdrawn'
 AND (e.expires_at IS NULL OR e.expires_at>$6) AND (root.expires_at IS NULL OR root.expires_at>$6)
 ORDER BY e.id LIMIT $5`, school, kind, state, after, limit, now)
}
func (t *transaction) IdentityByHandle(root, handle string) (reviews.Identity, error) {
	rows, err := list[reviews.Identity](t, `SELECT data FROM reviews.identities WHERE root_id=$1 AND data->>'Handle'=$2 LIMIT 2`, root, handle)
	if err != nil {
		return reviews.Identity{}, err
	}
	if len(rows) == 0 {
		return reviews.Identity{}, reviews.ErrNotFound
	}
	if len(rows) != 1 {
		return reviews.Identity{}, reviews.ErrConflict
	}
	return rows[0], nil
}
func (t *transaction) AuditRead(event reviews.Event) error {
	return t.save(`INSERT INTO reviews.audit(id,data) VALUES($1,$2) ON CONFLICT(id) DO NOTHING`, event, event.ID)
}
