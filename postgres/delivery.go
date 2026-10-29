package postgres

import (
	"encoding/json"
	r "github.com/StuHelper/stuhelper-reviews"
	"time"
)

func (t *transaction) Events(now time.Time, limit int) ([]r.DeliveryEvent, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT data,attempts,first_failure FROM reviews.outbox WHERE NOT delivered AND NOT blocked AND next_attempt<=$1 ORDER BY id LIMIT $2`, now, limit)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []r.DeliveryEvent{}
	for rows.Next() {
		var v r.DeliveryEvent
		var b []byte
		if rows.Scan(&b, &v.Attempts, &v.FirstFailure) != nil || json.Unmarshal(b, &v) != nil {
			return nil, r.ErrUnavailable
		}
		out = append(out, v)
	}
	return out, safe(rows.Err())
}
func (t *transaction) CompleteEvent(id string) error {
	_, e := t.tx.ExecContext(t.ctx, `UPDATE reviews.outbox SET delivered=true WHERE id=$1`, id)
	return safe(e)
}
func (t *transaction) FailEvent(id string, attempts int, first, next time.Time, blocked bool) error {
	_, e := t.tx.ExecContext(t.ctx, `UPDATE reviews.outbox SET attempts=$2,first_failure=$3,next_attempt=$4,blocked=$5 WHERE id=$1 AND NOT delivered AND attempts<$2`, id, attempts, first, next, blocked)
	return safe(e)
}
