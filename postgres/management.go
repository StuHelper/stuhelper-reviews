package postgres

import r "github.com/StuHelper/stuhelper-reviews"

func (t *transaction) Presentation(school string) (r.Presentation, error) {
	return read[r.Presentation](t, `SELECT data FROM reviews.presentations WHERE school_id=$1`, school)
}
func (t *transaction) SavePresentation(v r.Presentation) error {
	if err := t.save(`INSERT INTO reviews.presentation_history(school_id,revision,data) VALUES($1,$2,$3)`, v, v.SchoolID, v.Revision); err != nil {
		return err
	}
	return t.save(`INSERT INTO reviews.presentations(school_id,data) VALUES($1,$2) ON CONFLICT(school_id) DO UPDATE SET data=excluded.data`, v, v.SchoolID)
}

func (t *transaction) History(id string, before int64, limit int) ([]r.Entry, error) {
	return list[r.Entry](t, `SELECT data FROM reviews.history WHERE id=$1 AND ($2=0 OR revision<$2) ORDER BY revision DESC LIMIT $3`, id, before, limit)
}
