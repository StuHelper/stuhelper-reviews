package postgres

import (
	"encoding/json"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
)

func (t *transaction) OwnReviewOverview(owner string, now time.Time) (r.OwnOverview, error) {
	return read[r.OwnOverview](t, `SELECT jsonb_build_object(
 'review_count',(SELECT count(*) FROM reviews.entries WHERE owner=$1 AND id=root_id AND kind IN ('course','mentor') AND state='published' AND (expires_at IS NULL OR expires_at>$2)),
 'draft_count',(SELECT count(*) FROM reviews.drafts WHERE owner=$1)+(SELECT count(*) FROM reviews.entries WHERE owner=$1 AND state='withdrawn' AND expires_at>$2))`, owner, now)
}

func (t *transaction) PendingReviewCourses(owner string, candidates []r.PendingCourse, courses, terms map[string]string, now time.Time) (r.PendingCourses, error) {
	input, err := json.Marshal(candidates)
	if err != nil {
		return r.PendingCourses{}, r.ErrInput
	}
	courseAliases, err := json.Marshal(courses)
	if err != nil {
		return r.PendingCourses{}, r.ErrInput
	}
	termAliases, err := json.Marshal(terms)
	if err != nil {
		return r.PendingCourses{}, r.ErrInput
	}
	return read[r.PendingCourses](t, `WITH normalized AS (
 SELECT COALESCE($3::jsonb->>course_id,course_id) AS course_id,course_name,COALESCE($4::jsonb->>term_id,term_id) AS term_id,term_name,
 ($3::jsonb->>course_id IS NULL AND $4::jsonb->>term_id IS NULL) AS canonical_name
 FROM jsonb_to_recordset($2::jsonb) AS v(course_id text,course_name text,term_id text,term_name text)
 ), candidates AS (
 SELECT DISTINCT ON (course_id,term_id) course_id,course_name,term_id,term_name FROM normalized
 ORDER BY course_id,term_id,canonical_name DESC,course_name,term_name
 ), pending AS MATERIALIZED (
 SELECT c.* FROM candidates c WHERE NOT EXISTS(SELECT 1 FROM reviews.entries e WHERE e.owner=$1 AND e.kind='course' AND e.id=e.root_id
 AND e.state<>'deleted' AND (e.expires_at IS NULL OR e.expires_at>$5)
 AND COALESCE($3::jsonb->>e.target_id,e.target_id)=c.course_id AND COALESCE($4::jsonb->>e.term_id,e.term_id)=c.term_id)
 ), page_items AS (
 SELECT v.course_id,v.course_name,v.term_id,v.term_name,
 (SELECT d.id FROM reviews.drafts d WHERE d.owner=$1 AND d.data->'input'->>'kind'='course'
  AND COALESCE($3::jsonb->>(d.data->'input'->>'target_id'),d.data->'input'->>'target_id')=v.course_id
  AND (COALESCE(d.data->'input'->>'term_id','')='' OR COALESCE($4::jsonb->>(d.data->'input'->>'term_id'),d.data->'input'->>'term_id')=v.term_id)
  ORDER BY (COALESCE($4::jsonb->>(d.data->'input'->>'term_id'),d.data->'input'->>'term_id')=v.term_id) DESC,(d.data->>'updated_at')::timestamptz DESC,d.id ASC LIMIT 1) AS draft_id
 FROM (SELECT * FROM pending ORDER BY term_id DESC,course_id LIMIT 20) v
 ) SELECT jsonb_build_object('state','available','reason','','count',(SELECT count(*) FROM pending),
 'items',COALESCE((SELECT jsonb_agg(jsonb_strip_nulls(to_jsonb(p)) ORDER BY p.term_id DESC,p.course_id) FROM page_items p),'[]'::jsonb))`, owner, string(input), string(courseAliases), string(termAliases), now)
}
