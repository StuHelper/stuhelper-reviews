package postgres

import (
	"encoding/json"
	"fmt"
	m "github.com/StuHelper/stuhelper-reviews"
	"slices"
	"time"
)

func (t *transaction) writeDiscovery(d m.DiscoveryDocument) error {
	if d.Revision < 1 || d.At.IsZero() {
		return m.ErrInput
	}
	if !d.Visible {
		d.Title = ""
		d.CourseID = ""
		d.TeacherIDs = nil
	}
	if e := t.save(`INSERT INTO reviews.discovery_history(id,revision,data) VALUES($1,$2,$3)`, d, d.Target.ID, d.Revision); e != nil {
		return e
	}
	data, e := json.Marshal(d)
	if e != nil {
		return m.ErrInput
	}
	return t.saveEnvelope(m.Envelope{SpecVersion: "1.0", ID: fmt.Sprintf("discovery:%s:%d", d.Target.ID, d.Revision), Source: "reviews", Type: "reviews.discovery.v1", Subject: d.Target.ID, Time: d.At, Data: data})
}

// Search indexes course-review roots. Mentor comments and discussion replies
// are not course-review documents. Teacher IDs describe attribution; enrollment evidence and authors are excluded.
func (t *transaction) saveDiscovery(e m.Entry) error {
	if e.Input.Kind != "course" || e.RootID != e.ID {
		return nil
	}
	d := m.DiscoveryDocument{Target: m.DiscoveryTarget{Source: "reviews", ID: e.ID, Kind: "review"}, Revision: e.Revision, Title: e.Input.Title, SchoolID: e.SchoolID, Visible: e.State == "published", CourseID: e.Input.TargetID, At: e.UpdatedAt}
	d.TeacherIDs = append([]string{}, e.TeacherIDs...)
	slices.Sort(d.TeacherIDs)
	d.TeacherIDs = slices.Compact(d.TeacherIDs)
	if len(d.TeacherIDs) > 100 {
		return m.ErrInput
	}
	for _, id := range d.TeacherIDs {
		if id == "" || len(id) > 200 {
			return m.ErrInput
		}
	}
	return t.writeDiscovery(d)
}
func (t *transaction) DiscoveryCurrent(school string, ids []string, now time.Time) ([]m.DiscoveryDocument, error) {
	return list[m.DiscoveryDocument](t, `SELECT h.data FROM reviews.entries e JOIN reviews.discovery_history h ON h.id=e.id AND h.revision=(e.data->>'revision')::bigint WHERE e.id=ANY($1::text[]) AND e.school_id=$2 AND e.kind='course' AND e.id=e.root_id AND e.state='published' AND (e.expires_at IS NULL OR e.expires_at>$3) AND (h.data->>'visible')::boolean ORDER BY e.id COLLATE "C"`, ids, school, now)
}
