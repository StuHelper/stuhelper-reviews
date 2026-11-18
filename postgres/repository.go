// Package postgres owns review persistence and migrations.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	reviews "github.com/StuHelper/stuhelper-reviews"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	DB           *sql.DB
	OnDiagnostic reviews.DiagnosticHook
}
type transaction struct {
	tx   *sql.Tx
	ctx  context.Context
	repo *Repository
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

func (r *Repository) safeOp(op string, start time.Time, raw error) error {
	mapped := safe(raw)
	if raw != nil && r != nil && r.OnDiagnostic != nil && !errors.Is(raw, sql.ErrNoRows) {
		var pg *pgconn.PgError
		sqlState := ""
		if errors.As(raw, &pg) {
			sqlState = reviews.SanitizeSQLState(pg.Code)
		}
		r.OnDiagnostic(reviews.DiagnosticEvent{
			Operation:  op,
			Dependency: "postgres",
			Category:   reviews.ClassifyDiagnosticCategory(mapped),
			SQLState:   sqlState,
			Duration:   time.Since(start),
		})
	}
	return mapped
}

func (r *Repository) emitCorruptJSON(op string, start time.Time) error {
	if r != nil && r.OnDiagnostic != nil {
		r.OnDiagnostic(reviews.DiagnosticEvent{
			Operation:  op,
			Dependency: "postgres",
			Category:   "json_corrupt",
			Duration:   time.Since(start),
		})
	}
	return reviews.ErrUnavailable
}

func (r *Repository) Transaction(ctx context.Context, run func(reviews.Transaction) error) error {
	start := time.Now()
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return r.safeOp("begin_tx", start, e)
	}
	defer tx.Rollback()
	if e = run(&transaction{tx, ctx, r}); e != nil {
		return e
	}
	commitStart := time.Now()
	return r.safeOp("commit_tx", commitStart, tx.Commit())
}
func (t *transaction) Lock(key string) error {
	start := time.Now()
	_, e := t.tx.ExecContext(t.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "reviews:"+key)
	return t.repo.safeOp("lock", start, e)
}
func read[T any](t *transaction, q string, args ...any) (T, error) {
	var v T
	var b []byte
	start := time.Now()
	if e := t.tx.QueryRowContext(t.ctx, q, args...).Scan(&b); e != nil {
		return v, t.repo.safeOp("read", start, e)
	}
	if json.Unmarshal(b, &v) != nil {
		return v, t.repo.emitCorruptJSON("read_decode", start)
	}
	return v, nil
}
func list[T any](t *transaction, q string, args ...any) ([]T, error) {
	start := time.Now()
	rows, e := t.tx.QueryContext(t.ctx, q, args...)
	if e != nil {
		return nil, t.repo.safeOp("list", start, e)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b []byte
		var v T
		if err := rows.Scan(&b); err != nil {
			return nil, t.repo.safeOp("list_scan", start, err)
		}
		if json.Unmarshal(b, &v) != nil {
			return nil, t.repo.emitCorruptJSON("list_decode", start)
		}
		out = append(out, v)
	}
	return out, t.repo.safeOp("list_rows", start, rows.Err())
}
func (t *transaction) save(q string, v any, args ...any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return reviews.ErrInput
	}
	args = append(args, string(b))
	start := time.Now()
	_, e = t.tx.ExecContext(t.ctx, q, args...)
	return t.repo.safeOp("save", start, e)
}
func (t *transaction) Entry(id string) (reviews.Entry, error) {
	return read[reviews.Entry](t, `SELECT data FROM reviews.entries WHERE id=$1`, id)
}
func (t *transaction) SaveEntry(e reviews.Entry) error {
	if err := t.saveEntryRows(e); err != nil {
		return err
	}
	return t.refreshActivity(e.RootID, e.UpdatedAt)
}

// Batch callers hold the discussion locks. Take all owner locks before touching
// entries, then apply activity changes in bucket order across all discussions.
func (t *transaction) SaveEntryBatch(entries []reviews.Entry) error {
	owners := make(map[string]bool)
	roots := make(map[string]time.Time)
	for _, e := range entries {
		owners[e.Owner] = true
		if e.UpdatedAt.After(roots[e.RootID]) {
			roots[e.RootID] = e.UpdatedAt
		}
	}
	for _, owner := range slices.Sorted(maps.Keys(owners)) {
		if err := t.Lock("closure-owner:" + owner); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if err := t.saveEntryRows(e); err != nil {
			return err
		}
	}
	var changes []activityChange
	for _, root := range slices.Sorted(maps.Keys(roots)) {
		batch, err := t.rootActivityChanges(root, roots[root])
		if err != nil {
			return err
		}
		changes = append(changes, batch...)
	}
	return t.applyActivityChanges(changes)
}
func (t *transaction) saveEntryRows(e reviews.Entry) error {
	if err := t.closedEntry(&e); err != nil {
		return err
	}
	if err := t.save(`INSERT INTO reviews.entries(id,root_id,owner,school_id,kind,target_id,term_id,state,expires_at,first_published_at,created_at,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(id) DO UPDATE SET state=excluded.state,expires_at=excluded.expires_at,first_published_at=excluded.first_published_at,data=excluded.data`, e, e.ID, e.RootID, e.Owner, e.SchoolID, e.Input.Kind, e.Input.TargetID, e.Input.TermID, e.State, e.ExpiresAt, e.FirstPublishedAt, e.CreatedAt.Round(time.Microsecond)); err != nil {
		return err
	}
	if err := t.save(`INSERT INTO reviews.history(id,revision,data) VALUES($1,$2,$3)`, e, e.ID, e.Revision); err != nil {
		return err
	}
	return t.saveDiscovery(e)
}
func (t *transaction) DeleteEntry(e reviews.Entry) error {
	current, err := t.Entry(e.ID)
	if err != nil {
		return err
	}
	e.Revision = max(e.Revision, current.Revision+1)
	if e.State != "deleted" && e.ExpiresAt != nil {
		e.UpdatedAt = *e.ExpiresAt
	}
	e.State = "deleted"
	if err = t.saveDiscovery(e); err != nil {
		return err
	}
	q := `DELETE FROM reviews.entries WHERE id=$1`
	if e.ID == e.RootID {
		q = `DELETE FROM reviews.entries WHERE root_id=$1`
	}
	_, err = t.tx.ExecContext(t.ctx, q, e.ID)
	if err != nil {
		return safe(err)
	}
	return t.refreshActivity(e.RootID, e.UpdatedAt)
}
func (t *transaction) projected(q string, args ...any) ([]reviews.Entry, error) {
	rows, err := t.tx.QueryContext(t.ctx, q, args...)
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
	return list[reviews.Entry](t, `SELECT data FROM reviews.entries WHERE state IN ('withdrawn','pending') AND expires_at<=$1 ORDER BY expires_at,id LIMIT $2`, now, limit)
}
func (t *transaction) Identity(root, subject string) (reviews.Identity, error) {
	return read[reviews.Identity](t, `SELECT data FROM reviews.identities WHERE root_id=$1 AND subject=$2`, root, subject)
}
func (t *transaction) SaveIdentity(v reviews.Identity) error {
	if e := t.Lock("closure-owner:" + v.Subject); e != nil {
		return e
	}
	closed, e := t.ClosedOwners([]string{v.Subject})
	if e != nil {
		return e
	}
	if closed[v.Subject] {
		var exists bool
		if e = t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM reviews.identities WHERE root_id=$1 AND subject=$2)`, v.RootID, v.Subject).Scan(&exists); e != nil {
			return safe(e)
		}
		if !exists || v.Signed {
			return reviews.ErrForbidden
		}
	}
	return t.save(`INSERT INTO reviews.identities(root_id,subject,data) VALUES($1,$2,$3) ON CONFLICT(root_id,subject) DO UPDATE SET data=excluded.data`, v, v.RootID, v.Subject)
}
func (t *transaction) Vote(id, subject string) (int, error) {
	var v int
	e := t.tx.QueryRowContext(t.ctx, `SELECT value FROM reviews.votes WHERE id=$1 AND subject=$2`, id, subject).Scan(&v)
	return v, safe(e)
}
func (t *transaction) SetVote(id, subject string, v int, at time.Time) error {
	if e := t.Lock("closure-owner:" + subject); e != nil {
		return e
	}
	closed, e := t.ClosedOwners([]string{subject})
	if e != nil {
		return e
	}
	if closed[subject] {
		return reviews.NewErrorDetail(reviews.ErrForbidden, "account_closed", "", nil)
	}
	if v == 0 {
		_, e := t.tx.ExecContext(t.ctx, `DELETE FROM reviews.votes WHERE id=$1 AND subject=$2`, id, subject)
		if e != nil {
			return safe(e)
		}
		entry, e := t.Entry(id)
		if e != nil {
			return e
		}
		return t.refreshActivity(entry.RootID, at)
	}
	_, e = t.tx.ExecContext(t.ctx, `INSERT INTO reviews.votes(id,subject,value,created_at) VALUES($1,$2,$3,$4) ON CONFLICT(id,subject) DO UPDATE SET value=excluded.value`, id, subject, v, at)
	if e != nil {
		return safe(e)
	}
	entry, e := t.Entry(id)
	if e != nil {
		return e
	}
	return t.refreshActivity(entry.RootID, at)
}
func (t *transaction) VoteCounts(id string) (int, int, error) {
	var up, down int
	e := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FILTER(WHERE value=1),count(*) FILTER(WHERE value=-1) FROM reviews.votes WHERE id=$1`, id).Scan(&up, &down)
	return up, down, safe(e)
}
func (t *transaction) Voted(subject, after string, limit int, now time.Time) ([]reviews.Entry, error) {
	return t.projected(`SELECT e.data,i.data,
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=1),
 (SELECT count(*) FROM reviews.votes v WHERE v.id=e.id AND v.value=-1),mine.value
 FROM reviews.entries e JOIN reviews.votes mine ON mine.id=e.id AND mine.subject=$1
 JOIN reviews.entries root ON root.id=e.root_id
 JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner
 WHERE e.id>$2 AND e.state='published' AND root.state='published'
 AND (e.expires_at IS NULL OR e.expires_at>$4) AND (root.expires_at IS NULL OR root.expires_at>$4)
 ORDER BY e.id LIMIT $3`, subject, after, limit, now)
}
func (t *transaction) Draft(id string) (reviews.Draft, error) {
	return read[reviews.Draft](t, `SELECT data FROM reviews.drafts WHERE id=$1`, id)
}
func (t *transaction) SaveDraft(v reviews.Draft) error {
	if e := t.Lock("closure-owner:" + v.Owner); e != nil {
		return e
	}
	closed, e := t.ClosedOwners([]string{v.Owner})
	if e != nil {
		return e
	}
	if closed[v.Owner] {
		return reviews.ErrForbidden
	}
	return t.save(`INSERT INTO reviews.drafts(id,owner,data) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, v, v.ID, v.Owner)
}
func (t *transaction) DeleteDraft(id string) error {
	_, e := t.tx.ExecContext(t.ctx, `DELETE FROM reviews.drafts WHERE id=$1`, id)
	return safe(e)
}
func (t *transaction) Drafts(subject, after string, limit int) ([]reviews.Draft, error) {
	return list[reviews.Draft](t, `SELECT data FROM reviews.drafts WHERE owner=$1 AND id>$2 ORDER BY id LIMIT $3`, subject, after, limit)
}
func (t *transaction) Withdrawals(subject, after string, limit int, now time.Time) ([]reviews.Entry, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT e.data,i.data FROM reviews.entries e JOIN reviews.identities i ON i.root_id=e.root_id AND i.subject=e.owner WHERE e.owner=$1 AND e.id>$2 AND e.state='withdrawn' AND e.expires_at>$4 ORDER BY e.id LIMIT $3`, subject, after, limit, now)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []reviews.Entry{}
	for rows.Next() {
		var raw, rawIdentity []byte
		var entry reviews.Entry
		var identity reviews.Identity
		if rows.Scan(&raw, &rawIdentity) != nil || json.Unmarshal(raw, &entry) != nil || json.Unmarshal(rawIdentity, &identity) != nil {
			return nil, reviews.ErrUnavailable
		}
		entry.Input.Signed = identity.Signed
		out = append(out, entry)
	}
	return out, safe(rows.Err())
}
func (t *transaction) Replay(key string) (reviews.Replay, error) {
	return read[reviews.Replay](t, `SELECT data FROM reviews.replays WHERE key=$1`, key)
}
func (t *transaction) SaveReplay(key string, v reviews.Replay) error {
	var expires any
	if !v.Until.IsZero() {
		expires = v.Until
	}
	return t.save(`INSERT INTO reviews.replays(key,object_id,owner,expires_at,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT(key) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at,data=excluded.data`, v, key, v.ObjectID, v.Owner, expires)
}
func (r *Repository) PruneReplays(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || limit < 1 || limit > 1000 {
		return 0, reviews.ErrInput
	}
	res, err := r.DB.ExecContext(ctx, `DELETE FROM reviews.replays WHERE key IN (SELECT key FROM reviews.replays WHERE expires_at IS NOT NULL AND expires_at<=$1 ORDER BY expires_at,key LIMIT $2 FOR UPDATE SKIP LOCKED)`, before, limit)
	if err != nil {
		return 0, safe(err)
	}
	n, err := res.RowsAffected()
	return n, safe(err)
}
func (t *transaction) Event(v reviews.Event) error {
	if e := t.save(`INSERT INTO reviews.audit(id,reason,result,data) VALUES($1,$2,'success',$3)`, v, v.ID, v.Reason); e != nil {
		return e
	}
	data, e := json.Marshal(map[string]any{"revision": v.Revision})
	if e != nil {
		return reviews.ErrInput
	}
	return t.saveEnvelope(reviews.Envelope{SpecVersion: "1.0", ID: v.ID, Source: "reviews", Type: v.Type, Subject: v.Subject, Time: v.At, Data: data})
}
