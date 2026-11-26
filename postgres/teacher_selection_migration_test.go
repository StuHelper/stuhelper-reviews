package postgres

import (
	"context"
	"slices"
	"testing"

	r "github.com/StuHelper/stuhelper-reviews"
)

func TestTeacherSelectionMigrationPreservesAttributionAndCounts(t *testing.T) {
	repo, _, db := activityDatabase(t)
	run := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO reviews.entries(id,root_id,owner,school_id,kind,target_id,term_id,state,created_at,data)
 VALUES('legacy','legacy','owner','school','course','course','term','withdrawn',now(),
 '{"teacher_ids":["canonical"],"input":{"kind":"course","teacher_id":"old-alias","ratings":[4,4,4,4,4]}}'),
 ('current','current','owner','school','course','course','term','pending',now(),
 '{"teacher_ids":["a","b"],"input":{"kind":"course","teacher_ids":["a","b"],"ratings":[4,4,4,4,4],"grade":{"scale":"percent","value":0}}}');
 INSERT INTO reviews.history(id,revision,data) SELECT id,1,data FROM reviews.entries;
 INSERT INTO reviews.drafts(id,owner,data) VALUES
 ('selected','owner','{"input":{"kind":"course","teacher_id":"old-alias","ratings":[4,4,4,4,4]}}'),
 ('clear','owner','{"input":{"kind":"course","teacher_id":"","ratings":[0,0,0,0,0]}}');`)
	run(Schema)
	run(Schema)
	var counts [3]int
	if err := db.QueryRow(`SELECT migrated_entries,migrated_history,migrated_drafts
 FROM reviews.teacher_selection_migration`).Scan(&counts[0], &counts[1], &counts[2]); err != nil || counts != [3]int{1, 1, 2} {
		t.Fatal(counts, err)
	}
	var clean bool
	if err := db.QueryRow(`SELECT NOT EXISTS(
  SELECT 1 FROM reviews.entries WHERE data->'input' ? 'teacher_id'
  UNION ALL SELECT 1 FROM reviews.history WHERE data->'input' ? 'teacher_id'
  UNION ALL SELECT 1 FROM reviews.drafts WHERE data->'input' ? 'teacher_id')
 AND (SELECT data#>'{input,teacher_ids}'='[]' FROM reviews.drafts WHERE id='clear')
 AND (SELECT data#>'{input,teacher_ids}'='["old-alias"]' FROM reviews.history WHERE id='legacy')
 AND (SELECT data#>'{input,grade}'='{"scale":"percent","value":0}' FROM reviews.entries WHERE id='current')`).Scan(&clean); err != nil || !clean {
		t.Fatal(clean, err)
	}
	if err := repo.Transaction(context.Background(), func(tx r.Transaction) error {
		entry, err := tx.Entry("legacy")
		if err != nil {
			return err
		}
		if !slices.Equal(entry.Input.TeacherIDs, []string{"old-alias"}) || !slices.Equal(entry.TeacherIDs, []string{"canonical"}) {
			t.Fatal("migration changed the effective attribution", entry.Input.TeacherIDs, entry.TeacherIDs)
		}
		draft, err := tx.Draft("selected")
		if err == nil && !slices.Equal(draft.Input.TeacherIDs, []string{"old-alias"}) {
			t.Fatal("migration lost the draft selection", draft.Input.TeacherIDs)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
