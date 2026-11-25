package postgres

import (
	"encoding/json"
	"fmt"
	"strings"

	r "github.com/StuHelper/stuhelper-reviews"
)

func (t *transaction) SummaryAggregates(q r.SummaryQuery) ([]r.SummaryAggregate, error) {
	courseAliases, err := json.Marshal(q.CourseAliases)
	if err != nil {
		return nil, r.ErrInput
	}
	targetAliases, err := json.Marshal(q.TargetAliases)
	if err != nil {
		return nil, r.ErrInput
	}
	termAliases, err := json.Marshal(q.TermAliases)
	if err != nil {
		return nil, r.ErrInput
	}
	// Reference maps came from catalog's public port. This SQL never reads a
	// catalog table. DISTINCT removes alias/canonical duplicates per attribution.
	target := `COALESCE($5::jsonb->>e.target_id,e.target_id)`
	join := ""
	if q.Kind == "teacher" {
		target = "teachers.id"
		join = ` CROSS JOIN LATERAL (SELECT DISTINCT COALESCE($6::jsonb->>value,value) AS id FROM jsonb_array_elements_text(e.data->'teacher_ids')) teachers`
	}
	term := `''::text`
	if q.Charts {
		term = `COALESCE($7::jsonb->>e.term_id,e.term_id)`
	}
	columns := []string{"target", "course", "term", "count(*)"}
	for dimension := range 5 {
		v := fmt.Sprintf("(ratings->>%d)::integer", dimension)
		columns = append(columns, "COALESCE(sum("+v+"),0)", "count(*) FILTER(WHERE "+v+">0)")
		for level := 1; level <= 5; level++ {
			if q.Charts {
				columns = append(columns, fmt.Sprintf("count(*) FILTER(WHERE %s=%d)", v, level))
			}
		}
	}
	// Bind all maps even when a branch does not need them, keeping typed
	// parameters valid with both pgx's simple and extended protocols.
	args := []any{q.SchoolID, q.Targets, q.Charts, q.Now, string(courseAliases), string(targetAliases), string(termAliases)}
	filter := ` AND ($2::text[] IS NULL OR ` + target + `=ANY($2))`
	if len(q.Targets) > 0 {
		expanded := q.ExpandedTargets
		if len(expanded) == 0 {
			expanded = q.Targets
		}
		args = append(args, expanded)
		if q.Kind == "teacher" {
			filter = ` AND e.data->'teacher_ids' ?| $8::text[] AND teachers.id=ANY($2::text[])`
		} else {
			filter = ` AND e.target_id=ANY($8::text[]) AND ` + target + `=ANY($2::text[])`
		}
	}
	query := `WITH reference_maps AS (SELECT $5::jsonb AS course,$6::jsonb AS teacher,$7::jsonb AS term), source AS (
 SELECT ` + target + ` AS target,COALESCE($5::jsonb->>e.target_id,e.target_id) AS course,` + term + ` AS term,
 e.data->'input'->'ratings' AS ratings
 FROM reviews.entries e` + join + `
 WHERE e.school_id=$1 AND e.kind='course' AND e.id=e.root_id AND e.state='published'
 AND (e.expires_at IS NULL OR e.expires_at>$4)` + filter + ` AND $3::boolean IN (true,false)
 ) SELECT ` + strings.Join(columns, ",") + ` FROM source GROUP BY target,course,term ORDER BY target,course,term LIMIT 100001`
	rows, err := t.tx.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	out := []r.SummaryAggregate{}
	var value r.SummaryAggregate
	fields := make([]any, 0, 40)
	fields = append(fields, &value.TargetID, &value.CourseID, &value.TermID, &value.ReviewCount)
	for dimension := range 5 {
		fields = append(fields, &value.Sums[dimension], &value.Counts[dimension])
		if q.Charts {
			for level := range 5 {
				fields = append(fields, &value.Histogram[dimension][level])
			}
		}
	}
	for rows.Next() {
		if err = rows.Scan(fields...); err != nil {
			return nil, safe(err)
		}
		out = append(out, value)
		if len(out) > 100000 {
			return nil, r.ErrUnavailable
		}
	}
	return out, safe(rows.Err())
}
