package postgres

import r "github.com/StuHelper/stuhelper-reviews"

func (t *transaction) ClosedOwners(owners []string) (map[string]bool, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT owner FROM reviews.closed_owners WHERE owner=ANY($1)`, owners)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var owner string
		if e = rows.Scan(&owner); e != nil {
			return nil, safe(e)
		}
		out[owner] = true
	}
	return out, safe(rows.Err())
}
func (t *transaction) CloseOwner(owner, id string) error {
	if e := t.Lock("closure-owner:" + owner); e != nil {
		return e
	}
	_, e := t.tx.ExecContext(t.ctx, `INSERT INTO reviews.closed_owners(owner,closure_id) VALUES($1,$2) ON CONFLICT(owner) DO NOTHING`, owner, id)
	if e != nil {
		return safe(e)
	}
	var saved string
	if e = t.tx.QueryRowContext(t.ctx, `SELECT closure_id FROM reviews.closed_owners WHERE owner=$1`, owner).Scan(&saved); e != nil {
		return safe(e)
	}
	if saved != id {
		return r.ErrConflict
	}
	// Each retained vote gets an independent random identity, with no account mapping.
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE reviews.votes SET subject='anonymous:'||gen_random_uuid()::text WHERE subject=$1`, owner); e != nil {
		return safe(e)
	}
	if _, e = t.tx.ExecContext(t.ctx, `DELETE FROM reviews.replays WHERE owner=$1`, owner); e != nil {
		return safe(e)
	}
	if _, e = t.tx.ExecContext(t.ctx, `DELETE FROM reviews.drafts WHERE owner=$1`, owner); e != nil {
		return safe(e)
	}
	withdrawn, e := list[r.Entry](t, `SELECT data FROM reviews.entries WHERE owner=$1 AND state='withdrawn' ORDER BY id`, owner)
	if e != nil {
		return e
	}
	for _, entry := range withdrawn {
		if e = t.DeleteEntry(entry); e != nil {
			return e
		}
	}
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE reviews.identities SET data=jsonb_set(data,'{Signed}','false') WHERE subject=$1 AND COALESCE((data->>'Signed')::boolean,false)`, owner); e != nil {
		return safe(e)
	}
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE reviews.entries SET data=(jsonb_set(data,'{input,signed}','false') #- '{input,disclosures}') WHERE owner=$1`, owner); e != nil {
		return safe(e)
	}

	return nil
}
func (t *transaction) closedEntry(e *r.Entry) error {
	if err := t.Lock("closure-owner:" + e.Owner); err != nil {
		return err
	}
	closed, err := t.ClosedOwners([]string{e.Owner})
	if err != nil {
		return err
	}
	if !closed[e.Owner] {
		return nil
	}
	var exists bool
	if err = t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM reviews.entries WHERE id=$1)`, e.ID).Scan(&exists); err != nil {
		return safe(err)
	}
	if !exists || e.ChangeOrigin == "author" {
		return r.ErrForbidden
	}
	e.Input.Signed = false
	e.Input.Disclosures = nil
	return nil
}
