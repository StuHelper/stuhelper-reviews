package postgres

import "testing"

func TestEnrollmentMigrationCountsRowsOnceAndCleansRetiredDisclosure(t *testing.T) {
	_, _, db := activityDatabase(t)
	run := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO reviews.entries(id,root_id,owner,school_id,kind,target_id,term_id,state,created_at,data)
 VALUES('legacy','legacy','owner','school','course','course','term','pending',now(),'{"input":{"kind":"course","disclosures":["department","enrollment"]}}'),
 ('current','current','owner','school','course','course','term','pending',now(),'{"enrollment_status":"enrolled","input":{"kind":"course"}}');
 INSERT INTO reviews.history(id,revision,data) SELECT id,1,data FROM reviews.entries;
 INSERT INTO reviews.drafts(id,owner,data) VALUES ('draft','owner','{"input":{"disclosures":["enrollment","gender"]}}')`)
	run(Schema)
	run(Schema)
	var ok bool
	err := db.QueryRow(`SELECT
 (SELECT migrated_course_reviews=1 FROM reviews.school_data_migration) AND
 (SELECT data->>'enrollment_status'='unknown' AND data#>'{input,disclosures}'='["department"]' FROM reviews.entries WHERE id='legacy') AND
 (SELECT data->>'enrollment_status'='enrolled' FROM reviews.entries WHERE id='current') AND
 (SELECT data->>'enrollment_status'='unknown' AND data#>'{input,disclosures}'='["department"]' FROM reviews.history WHERE id='legacy') AND
 (SELECT data#>'{input,disclosures}'='["gender"]' FROM reviews.drafts WHERE id='draft')`).Scan(&ok)
	if err != nil || !ok {
		t.Fatal("migration count, snapshot or disclosure mismatch", err)
	}
}
