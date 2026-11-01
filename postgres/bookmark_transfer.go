package postgres

import r "github.com/StuHelper/stuhelper-reviews"

func (t *transaction) bookmarkWritable(owner string) error {
	if e := t.Lock("bookmark-owner:" + owner); e != nil {
		return e
	}
	var sealed bool
	e := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM reviews.bookmark_transfers WHERE subject=$1)`, owner).Scan(&sealed)
	if e != nil {
		return safe(e)
	}
	if sealed {
		return r.ErrBookmarksMoved
	}
	return nil
}
func (t *transaction) BookmarkTransfer(owner, after string, limit int) ([]string, error) {
	if e := t.Lock("bookmark-owner:" + owner); e != nil {
		return nil, e
	}
	if _, e := t.tx.ExecContext(t.ctx, `INSERT INTO reviews.bookmark_transfers(subject) VALUES($1) ON CONFLICT DO NOTHING`, owner); e != nil {
		return nil, safe(e)
	}
	return t.bookmarkPage(owner, after, limit)
}
