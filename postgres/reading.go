package postgres

import (
	reviews "github.com/StuHelper/stuhelper-reviews"
	"strconv"
	"time"
)

func (t *transaction) List(f reviews.Filter) ([]reviews.Entry, error) {
	cursor, order := "e.id>$10", "e.id"
	args := []any{f.Kind, f.TargetID, f.TermID, f.RootID, f.Owner, f.Now, f.Limit + 1, f.Viewer, f.TeacherID}
	upvotes := "(SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=1)"
	switch f.Sort {
	case "newest":
		cursor = "($10::timestamptz IS NULL OR (e.created_at,e.id)<($10,$11))"
		order = "e.created_at DESC,e.id DESC"
		args = append(args, f.CursorTime, f.CursorID)
	case "oldest":
		cursor = "($10::timestamptz IS NULL OR (e.created_at,e.id)>($10,$11))"
		order = "e.created_at,e.id"
		args = append(args, f.CursorTime, f.CursorID)
	case "upvotes":
		cursor = "($10::integer IS NULL OR (" + upvotes + ",e.created_at,e.id)<($10,$11,$12))"
		order = upvotes + " DESC,e.created_at DESC,e.id DESC"
		args = append(args, f.CursorVotes, f.CursorTime, f.CursorID)
	case "recommended":
		featured := "(CASE WHEN COALESCE((e.data->>'featured')::boolean,false) THEN 1 ELSE 0 END)"
		netVotes := "(SELECT COALESCE(sum(v.value),0) FROM reviews.votes v WHERE v.id=e.id)"
		cursor = "($10::integer IS NULL OR (" + featured + "," + netVotes + "," + upvotes + ",e.created_at,e.id)<($10,$11,$12,$13,$14))"
		order = featured + " DESC," + netVotes + " DESC," + upvotes + " DESC,e.created_at DESC,e.id DESC"
		args = append(args, f.CursorFeatured, f.CursorScore, f.CursorVotes, f.CursorTime, f.CursorID)
	default:
		args = append(args, f.After)
	}
	targetIndex := strconv.Itoa(len(args) + 1)
	args = append(args, f.EquivalentTargets)
	teacherIndex := strconv.Itoa(len(args) + 1)
	args = append(args, f.EquivalentTeachers)
	schoolIndex := strconv.Itoa(len(args) + 1)
	args = append(args, f.SchoolID)
	featuredIndex := strconv.Itoa(len(args) + 1)
	args = append(args, f.Featured)
	ratingIndex := strconv.Itoa(len(args) + 1)
	args = append(args, f.RatingGroup)
	teacherReplyClause := ""
	if f.HasTeacherReply != nil {
		teacherReplyClause = " AND e.id=e.root_id AND e.kind IN ('course','mentor')"
		if *f.HasTeacherReply {
			teacherReplyClause += ` AND EXISTS(SELECT 1 FROM reviews.entries tr WHERE tr.root_id=e.id AND tr.kind='reply' AND tr.state='published' AND (tr.expires_at IS NULL OR tr.expires_at>$6) AND COALESCE((tr.data->>'teacher_reply')::boolean,false) AND NOT EXISTS(SELECT 1 FROM reviews.closed_owners c WHERE c.owner=tr.owner))`
		}
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
 AND ($1='' OR e.kind=$1) AND ($2='' OR e.target_id=$2 OR e.target_id=ANY($` + targetIndex + `::text[])) AND ($3='' OR e.term_id=$3)
 AND ($4='' OR e.root_id=$4) AND ($5='' OR e.owner=$5)
 AND ($9='' OR (e.kind='course' AND ((e.data->'teacher_ids') ? $9 OR (e.data->'teacher_ids') ?| $` + teacherIndex + `::text[])))
 AND ($` + schoolIndex + `='' OR e.school_id=$` + schoolIndex + `)
 AND ($` + featuredIndex + `::boolean IS NULL OR COALESCE((e.data->>'featured')::boolean,false)=$` + featuredIndex + `)
 AND ($` + ratingIndex + `='' OR (e.kind='course' AND (($` + ratingIndex + `='low' AND (e.data->'input'->'ratings'->>0)::integer IN (1,2)) OR ($` + ratingIndex + `='mid' AND (e.data->'input'->'ratings'->>0)::integer=3) OR ($` + ratingIndex + `='high' AND (e.data->'input'->'ratings'->>0)::integer IN (4,5)))))` + teacherReplyClause + `
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
	return t.save(`INSERT INTO reviews.audit(id,reason,result,data) VALUES($1,$2,'success',$3) ON CONFLICT(id) DO NOTHING`, event, event.ID, event.Reason)
}
