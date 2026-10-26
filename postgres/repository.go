// Package postgres owns review persistence and migrations.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	reviews "github.com/StuHelper/stuhelper-reviews"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var Schema string

type Repository struct{ DB *sql.DB }
type transaction struct {
	tx  *sql.Tx
	ctx context.Context
}

func safe(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, sql.ErrNoRows) {
		return reviews.ErrNotFound
	}
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return e
	}
	var pg *pgconn.PgError
	if errors.As(e, &pg) && (pg.Code == "23505" || pg.Code == "40001" || pg.Code == "40P01") {
		return reviews.ErrConflict
	}
	return reviews.ErrUnavailable
}
func (r *Repository) Transaction(ctx context.Context, run func(reviews.Transaction) error) error {
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return safe(e)
	}
	defer tx.Rollback()
	if e = run(&transaction{tx, ctx}); e != nil {
		return e
	}
	return safe(tx.Commit())
}
func (t *transaction) Lock(key string) error {
	_, e := t.tx.ExecContext(t.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "reviews:"+key)
	return safe(e)
}
func read[T any](t *transaction, q string, args ...any) (T, error) {
	var v T
	var b []byte
	if e := t.tx.QueryRowContext(t.ctx, q, args...).Scan(&b); e != nil {
		return v, safe(e)
	}
	if json.Unmarshal(b, &v) != nil {
		return v, reviews.ErrUnavailable
	}
	return v, nil
}
func list[T any](t *transaction, q string, args ...any) ([]T, error) {
	rows, e := t.tx.QueryContext(t.ctx, q, args...)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b []byte
		var v T
		if rows.Scan(&b) != nil || json.Unmarshal(b, &v) != nil {
			return nil, reviews.ErrUnavailable
		}
		out = append(out, v)
	}
	return out, safe(rows.Err())
}
func (t *transaction) save(q string, v any, args ...any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return reviews.ErrInput
	}
	args = append(args, string(b))
	_, e = t.tx.ExecContext(t.ctx, q, args...)
	return safe(e)
}
func (t *transaction) Entry(id string) (reviews.Entry, error) {
	return read[reviews.Entry](t, `SELECT data FROM reviews.entries WHERE id=$1`, id)
}
func (t *transaction) SaveEntry(e reviews.Entry) error {
	if err := t.save(`INSERT INTO reviews.entries(id,root_id,owner,school_id,kind,target_id,term_id,state,expires_at,first_published_at,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(id) DO UPDATE SET state=excluded.state,expires_at=excluded.expires_at,first_published_at=excluded.first_published_at,data=excluded.data`, e, e.ID, e.RootID, e.Owner, e.SchoolID, e.Input.Kind, e.Input.TargetID, e.Input.TermID, e.State, e.ExpiresAt, e.FirstPublishedAt); err != nil {
		return err
	}
	return t.save(`INSERT INTO reviews.history(id,revision,data) VALUES($1,$2,$3)`, e, e.ID, e.Revision)
}
func (t *transaction) DeleteEntry(e reviews.Entry) error {
	q := `DELETE FROM reviews.entries WHERE id=$1`
	if e.ID == e.RootID {
		q = `DELETE FROM reviews.entries WHERE root_id=$1`
	}
	_, err := t.tx.ExecContext(t.ctx, q, e.ID)
	return safe(err)
}
func (t *transaction) List(f reviews.Filter) ([]reviews.Entry, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT e.data,i.data,
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=1),
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=-1),
 COALESCE((SELECT value FROM reviews.votes v WHERE v.id=e.id AND v.subject=$8),0)
 FROM reviews.entries e JOIN reviews.entries root ON root.id=e.root_id
 JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner
 WHERE ((e.state='published' AND root.state='published') OR ($5<>'' AND e.owner=$5 AND e.state IN ('pending','hidden','deleted')))
 AND (e.expires_at IS NULL OR e.expires_at>clock_timestamp()) AND (root.expires_at IS NULL OR root.expires_at>clock_timestamp())
 AND ($1='' OR e.kind=$1) AND ($2='' OR e.target_id=$2) AND ($3='' OR e.term_id=$3)
 AND ($4='' OR e.root_id=$4) AND ($5='' OR e.owner=$5) AND e.id>$6
 ORDER BY e.id LIMIT $7`, f.Kind, f.TargetID, f.TermID, f.RootID, f.Owner, f.After, f.Limit+1, f.Viewer)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	out := []reviews.Entry{}
	for rows.Next() {
		var entry reviews.Entry
		var raw, identity []byte
		projection := &reviews.Projection{}
		if rows.Scan(&raw, &identity, &projection.Upvotes, &projection.Downvotes, &projection.MyVote) != nil || json.Unmarshal(raw, &entry) != nil || json.Unmarshal(identity, &projection.Identity) != nil {
			return nil, reviews.ErrUnavailable
		}
		entry.Projection = projection
		out = append(out, entry)
	}
	return out, safe(rows.Err())
}
func (t *transaction) Expired(now time.Time, limit int) ([]reviews.Entry, error) {
	return list[reviews.Entry](t, `SELECT data FROM reviews.entries WHERE expires_at<=$1 ORDER BY expires_at,id LIMIT $2`, now, limit)
}
func (t *transaction) Identity(root, subject string) (reviews.Identity, error) {
	return read[reviews.Identity](t, `SELECT data FROM reviews.identities WHERE root_id=$1 AND subject=$2`, root, subject)
}
func (t *transaction) SaveIdentity(v reviews.Identity) error {
	return t.save(`INSERT INTO reviews.identities(root_id,subject,data) VALUES($1,$2,$3) ON CONFLICT(root_id,subject) DO UPDATE SET data=excluded.data`, v, v.RootID, v.Subject)
}
func (t *transaction) Vote(id, subject string) (int, error) {
	var v int
	e := t.tx.QueryRowContext(t.ctx, `SELECT value FROM reviews.votes WHERE id=$1 AND subject=$2`, id, subject).Scan(&v)
	return v, safe(e)
}
func (t *transaction) SetVote(id, subject string, v int) error {
	if v == 0 {
		_, e := t.tx.ExecContext(t.ctx, `DELETE FROM reviews.votes WHERE id=$1 AND subject=$2`, id, subject)
		return safe(e)
	}
	_, e := t.tx.ExecContext(t.ctx, `INSERT INTO reviews.votes(id,subject,value) VALUES($1,$2,$3) ON CONFLICT(id,subject) DO UPDATE SET value=excluded.value`, id, subject, v)
	return safe(e)
}
func (t *transaction) VoteCounts(id string) (int, int, error) {
	var up, down int
	e := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FILTER(WHERE value=1),count(*) FILTER(WHERE value=-1) FROM reviews.votes WHERE id=$1`, id).Scan(&up, &down)
	return up, down, safe(e)
}
func (t *transaction) Voted(subject, after string, limit int) ([]reviews.Entry, error) {
	return list[reviews.Entry](t, `SELECT e.data FROM reviews.entries e JOIN reviews.votes v ON v.id=e.id JOIN reviews.entries root ON root.id=e.root_id WHERE v.subject=$1 AND e.id>$2 AND e.state='published' AND root.state='published' ORDER BY e.id LIMIT $3`, subject, after, limit)
}
func (t *transaction) Bookmark(subject, target string, set bool) error {
	q := `DELETE FROM reviews.bookmarks WHERE subject=$1 AND target_id=$2`
	if set {
		q = `INSERT INTO reviews.bookmarks(subject,target_id) VALUES($1,$2) ON CONFLICT DO NOTHING`
	}
	_, e := t.tx.ExecContext(t.ctx, q, subject, target)
	return safe(e)
}
func (t *transaction) Bookmarks(subject, after string, limit int) ([]string, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT target_id FROM reviews.bookmarks WHERE subject=$1 AND target_id>$2 ORDER BY target_id LIMIT $3`, subject, after, limit)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			return nil, reviews.ErrUnavailable
		}
		out = append(out, id)
	}
	return out, safe(rows.Err())
}
func (t *transaction) Draft(id string) (reviews.Draft, error) {
	return read[reviews.Draft](t, `SELECT data FROM reviews.drafts WHERE id=$1`, id)
}
func (t *transaction) SaveDraft(v reviews.Draft) error {
	return t.save(`INSERT INTO reviews.drafts(id,owner,data) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, v, v.ID, v.Owner)
}
func (t *transaction) DeleteDraft(id string) error {
	_, e := t.tx.ExecContext(t.ctx, `DELETE FROM reviews.drafts WHERE id=$1`, id)
	return safe(e)
}
func (t *transaction) Drafts(subject, after string, limit int) ([]reviews.Draft, error) {
	return list[reviews.Draft](t, `SELECT data FROM reviews.drafts WHERE owner=$1 AND id>$2 ORDER BY id LIMIT $3`, subject, after, limit)
}
func (t *transaction) Withdrawals(subject, after string, limit int) ([]reviews.Entry, error) {
	return list[reviews.Entry](t, `SELECT data FROM reviews.entries WHERE owner=$1 AND id>$2 AND state IN ('withdrawn','pending') AND expires_at IS NOT NULL ORDER BY id LIMIT $3`, subject, after, limit)
}
func (t *transaction) Replay(key string) (reviews.Replay, error) {
	return read[reviews.Replay](t, `SELECT data FROM reviews.replays WHERE key=$1`, key)
}
func (t *transaction) SaveReplay(key string, v reviews.Replay) error {
	return t.save(`INSERT INTO reviews.replays(key,object_id,data) VALUES($1,$2,$3) ON CONFLICT(key) DO UPDATE SET data=excluded.data`, v, key, v.ObjectID)
}
func (t *transaction) Event(v reviews.Event) error {
	if e := t.save(`INSERT INTO reviews.audit(id,data) VALUES($1,$2)`, v, v.ID); e != nil {
		return e
	}
	return t.save(`INSERT INTO reviews.outbox(id,data) VALUES($1,$2)`, map[string]any{"specversion": "1.0", "id": v.ID, "source": "reviews", "type": v.Type, "subject": v.Subject, "time": v.At, "schoolid": v.SchoolID, "datacontenttype": "application/json", "data": map[string]any{"revision": v.Revision}}, v.ID)
}
