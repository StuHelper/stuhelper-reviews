package postgres

import (
	"context"
	"database/sql"
	"time"

	m "github.com/StuHelper/stuhelper-reviews"
)

var (
	_ m.OutboxReader    = (*Repository)(nil)
	_ m.OutboxInspector = (*Repository)(nil)
)

const pendingOutboxSQL = `SELECT data FROM reviews.outbox WHERE sent_at IS NULL AND NOT ((data->>'subject')=ANY(COALESCE($2::text[],ARRAY[]::text[]))) ORDER BY (data->>'source') COLLATE "C",(data->>'subject') COLLATE "C",seq LIMIT $1`

func (r *Repository) PendingOutbox(ctx context.Context, limit int, excludeSubjects []string) ([]m.Envelope, error) {
	if limit < 1 || limit > 100 || len(excludeSubjects) > 1000 {
		return nil, m.ErrInput
	}
	var out []m.Envelope
	err := r.Transaction(ctx, func(tx m.Transaction) error {
		var e error
		out, e = list[m.Envelope](tx.(*transaction), pendingOutboxSQL, limit, excludeSubjects)
		return e
	})
	return out, err
}
func (r *Repository) ConfirmOutbox(ctx context.Context, id string) error {
	if id == "" || len(id) > 200 {
		return m.ErrInput
	}
	result, e := r.DB.ExecContext(ctx, `UPDATE reviews.outbox SET sent_at=COALESCE(sent_at,now()) WHERE id=$1`, id)
	if e != nil {
		return safe(e)
	}
	n, e := result.RowsAffected()
	if e != nil {
		return safe(e)
	}
	if n == 0 {
		return m.ErrNotFound
	}
	return nil
}
func (r *Repository) OutboxMetrics(ctx context.Context) (m.OutboxStats, error) {
	start := time.Now()
	var count int64
	var oldest sql.NullTime
	err := r.DB.QueryRowContext(ctx, `SELECT count(*),min((data->>'time')::timestamptz) FROM reviews.outbox WHERE sent_at IS NULL`).Scan(&count, &oldest)
	if err != nil {
		return m.OutboxStats{}, r.safeOp("outbox_metrics", start, err)
	}
	out := m.OutboxStats{PendingCount: count}
	if count > 0 && oldest.Valid {
		ts := oldest.Time.UTC()
		out.OldestPendingAt = &ts
	}
	return out, nil
}
func (t *transaction) saveEnvelope(v m.Envelope) error {
	if v.SpecVersion != "1.0" || v.Source != "reviews" || v.ID == "" || len(v.ID) > 200 || v.Subject == "" || len(v.Subject) > 512 || v.Type == "" || v.Time.IsZero() {
		return m.ErrInput
	}
	if e := t.save(`INSERT INTO reviews.outbox(id,data) VALUES($1,$2)`, v, v.ID); e != nil {
		return e
	}
	_, e := t.tx.ExecContext(t.ctx, `SELECT pg_notify($1,'')`, m.OutboxChannel)
	return safe(e)
}
