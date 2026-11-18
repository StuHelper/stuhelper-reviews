package postgres

import (
	reviews "github.com/StuHelper/stuhelper-reviews"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (t *transaction) List(f reviews.Filter) ([]reviews.Entry, error) {
	args := make([]any, 0, 12)
	addArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	viewerIdx := addArg(f.Viewer)
	nowIdx := addArg(f.Now)
	limitIdx := addArg(f.Limit + 1)

	where := make([]string, 0, 10)
	if f.Owner == "" {
		where = append(where, "e.state='published'")
	} else {
		ownerIdx := addArg(f.Owner)
		where = append(where, "e.owner="+ownerIdx+" AND e.state IN ('published','pending','hidden','deleted')")
	}
	where = append(where, "(e.expires_at IS NULL OR e.expires_at>"+nowIdx+")")
	where = append(where, "(e.id=e.root_id OR EXISTS(SELECT 1 FROM reviews.entries root WHERE root.id=e.root_id AND root.state='published' AND (root.expires_at IS NULL OR root.expires_at>"+nowIdx+")))")
	if f.Kind != "" {
		where = append(where, "e.kind="+addArg(f.Kind))
	}
	if f.TargetID != "" {
		targets := append([]string{f.TargetID}, f.EquivalentTargets...)
		slices.Sort(targets)
		targets = slices.Compact(targets)
		if len(targets) == 1 {
			where = append(where, "e.target_id="+addArg(targets[0]))
		} else {
			where = append(where, "e.target_id=ANY("+addArg(targets)+"::text[])")
		}
	}
	if f.TermID != "" {
		where = append(where, "e.term_id="+addArg(f.TermID))
	}
	if f.RootID != "" {
		where = append(where, "e.root_id="+addArg(f.RootID))
	}
	if f.TeacherID != "" {
		teachers := append([]string{f.TeacherID}, f.EquivalentTeachers...)
		slices.Sort(teachers)
		teachers = slices.Compact(teachers)
		where = append(where, "e.kind='course' AND (e.data->'teacher_ids') ?| "+addArg(teachers)+"::text[]")
	}
	if f.SchoolID != "" {
		where = append(where, "e.school_id="+addArg(f.SchoolID))
	}

	if f.Featured != nil {
		where = append(where, "COALESCE((e.data->>'featured')::boolean,false)="+addArg(*f.Featured))
	}
	if f.RatingGroup != "" {
		rating := "(e.data->'input'->'ratings'->>0)::int"
		switch f.RatingGroup {
		case "low":
			where = append(where, "e.kind='course' AND "+rating+" IN (1,2)")
		case "mid":
			where = append(where, "e.kind='course' AND "+rating+"=3")
		case "high":
			where = append(where, "e.kind='course' AND "+rating+" IN (4,5)")
		default:
			return nil, reviews.ErrInput
		}
	}
	if f.HasTeacherReply != nil {
		where = append(where, "e.id=e.root_id AND e.kind IN ('course','mentor')")
		if *f.HasTeacherReply {
			where = append(where, `EXISTS(SELECT 1 FROM reviews.entries tr WHERE tr.root_id=e.id AND tr.kind='reply' AND tr.state='published' AND (tr.expires_at IS NULL OR tr.expires_at>`+nowIdx+`) AND COALESCE((tr.data->>'teacher_reply')::boolean,false) AND NOT EXISTS(SELECT 1 FROM reviews.closed_owners c WHERE c.owner=tr.owner))`)
		}
	}
	sortColumns := ""
	order := "e.id"
	switch f.Sort {
	case "newest":
		order = "e.created_at DESC,e.id DESC"
		if f.CursorTime != nil {
			where = append(where, "(e.created_at,e.id)<("+addArg(*f.CursorTime)+","+addArg(f.CursorID)+")")
		}
	case "oldest":
		order = "e.created_at,e.id"
		if f.CursorTime != nil {
			where = append(where, "(e.created_at,e.id)>("+addArg(*f.CursorTime)+","+addArg(f.CursorID)+")")
		}
	case "upvotes":
		order = "e.upvotes DESC,e.created_at DESC,e.id DESC"
		if f.CursorVotes != nil && f.CursorTime != nil {
			where = append(where, "(e.upvotes,e.created_at,e.id)<("+addArg(*f.CursorVotes)+","+addArg(*f.CursorTime)+","+addArg(f.CursorID)+")")
		}
	case "recommended":
		const featured = "(CASE WHEN COALESCE((e.data->>'featured')::boolean,false) THEN 1 ELSE 0 END)"
		const score = "(e.upvotes-(SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=-1))"
		sortColumns = ", " + featured + " AS featured_rank, " + score + " AS net_score"
		order = "featured_rank DESC,net_score DESC,e.upvotes DESC,e.created_at DESC,e.id DESC"
		if f.CursorFeatured != nil && f.CursorScore != nil && f.CursorVotes != nil && f.CursorTime != nil {
			where = append(where, "("+featured+","+score+",e.upvotes,e.created_at,e.id)<("+addArg(*f.CursorFeatured)+","+addArg(*f.CursorScore)+","+addArg(*f.CursorVotes)+","+addArg(*f.CursorTime)+","+addArg(f.CursorID)+")")
		}
	default:
		if f.After != "" {
			where = append(where, "e.id>"+addArg(f.After))
		}
	}

	q := `WITH page AS MATERIALIZED (
 SELECT e.id,e.root_id,e.owner,e.data,e.upvotes,e.created_at` + sortColumns + `
 FROM reviews.entries e
 WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ` + order + ` LIMIT ` + limitIdx + `
) SELECT e.data,i.data,
 e.upvotes,
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=-1),
 COALESCE((SELECT value FROM reviews.votes v WHERE v.id=e.id AND v.subject=` + viewerIdx + `),0)
 FROM page e
 JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner
 ORDER BY ` + order
	return t.projected(q, args...)
}
func (t *transaction) AdminEntries(school, kind, state, after string, limit int, now time.Time) ([]reviews.Entry, error) {
	return t.AdminEntriesFiltered(reviews.AdminFilter{
		SchoolID: school,
		Kind:     kind,
		State:    state,
		Sort:     "id",
		After:    after,
		Limit:    limit,
		Now:      now,
	})
}
func (t *transaction) AdminEntriesFiltered(f reviews.AdminFilter) ([]reviews.Entry, error) {
	cursor, order := "e.id>$6", "e.id"
	args := []any{f.SchoolID, f.Kind, f.State, f.Limit, f.Now}
	switch f.Sort {
	case "newest":
		cursor = "($6::timestamptz IS NULL OR (e.created_at,e.id)<($6,$7))"
		order = "e.created_at DESC,e.id DESC"
		args = append(args, f.CursorTime, f.CursorID)
	case "oldest":
		cursor = "($6::timestamptz IS NULL OR (e.created_at,e.id)>($6,$7))"
		order = "e.created_at,e.id"
		args = append(args, f.CursorTime, f.CursorID)
	default:
		args = append(args, f.After)
	}
	targetIdx := strconv.Itoa(len(args) + 1)
	args = append(args, f.TargetID)
	equivIdx := strconv.Itoa(len(args) + 1)
	args = append(args, f.EquivalentTargets)
	rootIdx := strconv.Itoa(len(args) + 1)
	args = append(args, f.RootID)
	revReqIdx := strconv.Itoa(len(args) + 1)
	args = append(args, f.ReviewRequested)
	featIdx := strconv.Itoa(len(args) + 1)
	args = append(args, f.Featured)
	revReqExpr := "(COALESCE((e.data->>'review_requested')::boolean,false) AND e.state IN ('pending','published'))"
	return t.projected(`SELECT e.data,i.data,0,0,0 FROM reviews.entries e
 JOIN reviews.entries root ON root.id=e.root_id
 JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner
 WHERE e.school_id=$1 AND ($2='' OR e.kind=$2) AND ($3='' OR e.state=$3)
 AND ($`+targetIdx+`='' OR e.target_id=$`+targetIdx+` OR e.target_id=ANY($`+equivIdx+`::text[]))
 AND ($`+rootIdx+`='' OR e.root_id=$`+rootIdx+`)
 AND ($`+revReqIdx+`::boolean IS NULL OR `+revReqExpr+`=$`+revReqIdx+`)
 AND ($`+featIdx+`::boolean IS NULL OR COALESCE((e.data->>'featured')::boolean,false)=$`+featIdx+`)
 AND e.state<>'withdrawn' AND root.state<>'withdrawn'
 AND (e.expires_at IS NULL OR e.expires_at>$5) AND (root.expires_at IS NULL OR root.expires_at>$5)
 AND `+cursor+` ORDER BY `+order+` LIMIT $4`, args...)
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
	return t.save(`INSERT INTO reviews.audit(id,reason,result,data) VALUES($1,$2,'success',$3)`, event, event.ID, event.Reason)
}
