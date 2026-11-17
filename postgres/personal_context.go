package postgres

import (
	"database/sql"
	"encoding/json"
	"time"

	r "github.com/StuHelper/stuhelper-reviews"
)

var _ r.PersonalContextTransaction = (*transaction)(nil)

func (t *transaction) DraftCount(owner string) (int, error) {
	var count int
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM reviews.drafts WHERE owner=$1`, owner).Scan(&count)
	return count, safe(err)
}

func (t *transaction) ParentSummaries(ids []string) (map[string]r.ParentSummary, error) {
	out := map[string]r.ParentSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT p.data,COALESCE(i.data->>'Handle',''),c.owner IS NOT NULL
 FROM reviews.entries p
 LEFT JOIN reviews.identities i ON i.root_id=p.root_id AND i.subject=p.owner
 LEFT JOIN reviews.closed_owners c ON c.owner=p.owner
 WHERE p.id=ANY($1::text[])`, ids)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var handle string
		var closed bool
		var entry r.Entry
		if rows.Scan(&raw, &handle, &closed) != nil || json.Unmarshal(raw, &entry) != nil {
			return nil, r.ErrUnavailable
		}
		out[entry.ID] = r.ParentSummary{Entry: entry, Handle: handle, Closed: closed}
	}
	return out, safe(rows.Err())
}

func (t *transaction) OwnComposerContext(owner, kind, parent string, targets, terms []string, now time.Time) (string, []string, error) {
	var draftID sql.NullString
	var err error
	if kind == "reply" {
		err = t.tx.QueryRowContext(t.ctx, `SELECT id FROM (
  SELECT d.id,(d.data->>'updated_at')::timestamptz AS updated_at,1 AS priority
  FROM reviews.drafts d WHERE d.owner=$1 AND d.data->'input'->>'kind'='reply' AND d.data->'input'->>'parent_id'=$2
  UNION ALL
  SELECT e.id,(e.data->>'updated_at')::timestamptz AS updated_at,2 AS priority
  FROM reviews.entries e WHERE e.owner=$1 AND e.kind='reply' AND e.state='withdrawn' AND e.expires_at>$3 AND e.data->>'parent_id'=$2
 ) candidates ORDER BY priority DESC,updated_at DESC,id ASC LIMIT 1`, owner, parent, now).Scan(&draftID)
	} else {
		err = t.tx.QueryRowContext(t.ctx, `SELECT id FROM (
  SELECT d.id,(d.data->>'updated_at')::timestamptz AS updated_at,
   CASE WHEN cardinality($4::text[])>0 AND (d.data->'input'->>'term_id')=ANY($4::text[]) THEN 2 ELSE 1 END AS term_match
  FROM reviews.drafts d WHERE d.owner=$1 AND d.data->'input'->>'kind'=$2 AND (d.data->'input'->>'target_id')=ANY($3::text[])
   AND (cardinality($4::text[])=0 OR COALESCE(d.data->'input'->>'term_id','')='' OR (d.data->'input'->>'term_id')=ANY($4::text[]))
  UNION ALL
  SELECT e.id,(e.data->>'updated_at')::timestamptz AS updated_at,3 AS term_match
  FROM reviews.entries e WHERE e.owner=$1 AND e.kind=$2 AND e.state='withdrawn' AND e.expires_at>$5 AND e.target_id=ANY($3::text[])
   AND (cardinality($4::text[])=0 OR e.term_id=ANY($4::text[]))
 ) candidates ORDER BY term_match DESC,updated_at DESC,id ASC LIMIT 1`, owner, kind, targets, terms, now).Scan(&draftID)
	}
	if err != nil && err != sql.ErrNoRows {
		return "", nil, safe(err)
	}

	var rows *sql.Rows
	if kind == "reply" {
		rows, err = t.tx.QueryContext(t.ctx, `SELECT e.id FROM reviews.entries e
 WHERE e.owner=$1 AND e.kind='reply' AND e.data->>'parent_id'=$2 AND e.state IN ('published','pending','hidden')
 AND (e.expires_at IS NULL OR e.expires_at>$3) ORDER BY e.created_at DESC,e.id DESC LIMIT 20`, owner, parent, now)
	} else {
		rows, err = t.tx.QueryContext(t.ctx, `SELECT e.id FROM reviews.entries e
 WHERE e.owner=$1 AND e.kind=$2 AND e.id=e.root_id AND e.target_id=ANY($3::text[])
 AND (cardinality($4::text[])=0 OR e.term_id=ANY($4::text[]))
 AND e.state IN ('published','pending','hidden') AND (e.expires_at IS NULL OR e.expires_at>$5)
 ORDER BY e.created_at DESC,e.id DESC LIMIT 20`, owner, kind, targets, terms, now)
	}
	if err != nil {
		return "", nil, safe(err)
	}
	defer rows.Close()
	entryIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", nil, safe(err)
		}
		entryIDs = append(entryIDs, id)
	}
	if err := rows.Err(); err != nil {
		return "", nil, safe(err)
	}
	return draftID.String, entryIDs, nil
}
