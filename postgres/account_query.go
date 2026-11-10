package postgres

import (
	"context"
	"encoding/json"
	aq "github.com/StuHelper/stuhelper-reviews/accountquery"
	"sort"
)

// ReadAccount filters authoritative module facts before the stable exclusive page.
func (r *Repository) ReadAccount(ctx context.Context, s aq.Selection) ([]aq.Record, error) {
	scopes, _ := json.Marshal(s.Scopes)
	sensitive, _ := json.Marshal(s.SensitiveScopes)
	rows, err := r.DB.QueryContext(ctx, accountQuerySQL, s.Query.Value, s.Query.After, scopes, aq.Domain, s.Query.Limit, sensitive)
	if err != nil {
		return nil, aq.ErrUnavailable
	}
	defer rows.Close()
	out := []aq.Record{}
	for rows.Next() {
		var v aq.Record
		var fields []byte
		var secret string
		if err = rows.Scan(&v.ID, &v.Scope.School, &v.Scope.Group, &v.ChangedAt, &v.Current, &fields, &secret); err != nil {
			return nil, aq.ErrUnavailable
		}
		v.Scope.Module = aq.Domain
		v.Subject = s.Query.Value
		v.Source = aq.Domain
		var values map[string]*string
		if json.Unmarshal(fields, &values) != nil {
			return nil, aq.ErrUnavailable
		}
		names := []string{}
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if values[name] != nil {
				v.Fields = append(v.Fields, aq.Field{Name: name, Category: "business", Value: *values[name]})
			}
		}
		if secret != "" {
			v.Fields = append(v.Fields, aq.Field{Name: "anonymous_author_link", Category: "identity", Value: secret})
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		return nil, aq.ErrUnavailable
	}
	return out, nil
}

const accountQuerySQL = `WITH records AS (SELECT 'reviews:'||e.id AS id,e.school_id AS school,'*'::text AS grp,
 COALESCE((e.data->>'updated_at')::timestamptz,e.created_at) AS changed,true AS current,
 jsonb_build_object('kind',e.kind,'state',e.state,'target_id',e.target_id,'title',e.data->'input'->>'title','body',e.data->'input'->>'body') AS fields,
 CASE WHEN (i.data->>'Signed'='true' AND NOT EXISTS (SELECT 1 FROM reviews.closed_owners c WHERE c.owner=e.owner)) THEN '' ELSE 'associated' END AS sensitive
 FROM reviews.entries e JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner
 WHERE e.owner=$1 AND e.kind IN ('course','mentor') AND e.state IN ('published','pending','hidden')
 AND (e.expires_at IS NULL OR e.expires_at>now())
 AND ((i.data->>'Signed'='true' AND NOT EXISTS (SELECT 1 FROM reviews.closed_owners c WHERE c.owner=e.owner)) OR EXISTS (SELECT 1 FROM jsonb_array_elements($6::jsonb) s WHERE (s->>'school'='*' OR s->>'school'=e.school_id) AND s->>'group'='*'))) SELECT id,school,grp,changed,current,fields,sensitive FROM records
 WHERE id COLLATE "C">$2 COLLATE "C"
 AND EXISTS (SELECT 1 FROM jsonb_array_elements($3::jsonb) s WHERE (s->>'school'='*' OR s->>'school'=school) AND (s->>'group'='*' OR s->>'group'=grp))
 AND $4::text='reviews' AND $1::text<>'' AND $6::jsonb IS NOT NULL
 ORDER BY id COLLATE "C" LIMIT $5`
