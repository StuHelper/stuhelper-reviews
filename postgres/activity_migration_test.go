package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	m "github.com/StuHelper/stuhelper-reviews"
)

func TestActivityCalendarMigrationAndRollbackPreserveNewWrites(t *testing.T) {
	repo, _, db := activityDatabase(t)
	ctx := context.Background()
	midnight := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	target := strings.Repeat("课", 30) + "/course"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	entry := func(id, root, kind, target, state string, at time.Time, teacherReply bool) {
		t.Helper()
		exec(`INSERT INTO reviews.entries(id,root_id,owner,school_id,kind,target_id,term_id,state,created_at,first_published_at,data)
 VALUES($1,$2,'owner','school',$3,$4,'term',$5,$6,$6,
 jsonb_build_object('id',$1::text,'root_id',$2::text,'school_id','school','owner','owner',
  'input',jsonb_build_object('kind',$3::text,'target_id',$4::text),'state',$5::text,'activity_target_id',$4::text,
  'teacher_ids',jsonb_build_array('teacher-a','teacher-a'),'teacher_reply',$7::boolean))`,
			id, root, kind, target, state, at, teacherReply)
	}
	entry("early", "early", "course", target, "published", midnight.Add(-time.Second), false)
	entry("late", "late", "course", target, "published", midnight, false)
	entry("reply", "early", "reply", target, "published", midnight, true)
	entry("hidden", "early", "reply", target, "hidden", midnight, false)
	entry("moved", "moved", "course", "other", "published", midnight, false)
	exec(`INSERT INTO reviews.votes(id,subject,value,created_at) VALUES
 ('early','old-voter',1,$1),('hidden','hidden-voter',1,$1)`, midnight.Add(-time.Second))
	exec(`INSERT INTO reviews.outbox(id,data) VALUES('content-event','{"type":"discovery.document.v1","subject":"early"}')`)

	assertBucket := func(kind, target string, at time.Time, reviews, interactions int64) m.Activity {
		t.Helper()
		id := m.ActivityID(kind, target, at)
		bucket, err := repo.ReadActivity(ctx, id)
		if err != nil || bucket.ID != id || bucket.Metrics["reviews"] != reviews ||
			bucket.Metrics["interactions"] != interactions || bucket.Visible != (reviews+interactions > 0) {
			t.Fatalf("read %s: %+v, %v", id, bucket, err)
		}
		return bucket
	}
	assertRejectsVersion := func(version string) {
		t.Helper()
		_, err := repo.DB.ExecContext(ctx, `INSERT INTO reviews.outbox(id,data) VALUES('wrong-calendar',jsonb_build_object('type',$1::text))`, version)
		if err == nil {
			t.Fatalf("database accepted an event from the wrong calendar: %s", version)
		}
	}
	assertPending := func(version string) {
		t.Helper()
		var expected, opposite, content int
		err := db.QueryRow(`SELECT
 count(*) FILTER(WHERE data->>'type'=$1),
 count(*) FILTER(WHERE data->>'type'<>$1 AND data->>'type' IN ('discovery.activity.v1','discovery.activity.v2')),
 count(*) FILTER(WHERE id='content-event') FROM reviews.outbox WHERE sent_at IS NULL`, version).Scan(&expected, &opposite, &content)
		if err != nil || expected == 0 || opposite != 0 || content != 1 {
			t.Fatalf("pending %s expected=%d opposite=%d content=%d: %v", version, expected, opposite, content, err)
		}
	}
	if err := RollbackActivityCalendar(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckRuntime(ctx); !errors.Is(err, m.ErrUnavailable) {
		t.Fatalf("Shanghai runtime accepted UTC calendar: %v", err)
	}
	if err := RollbackActivityCalendar(ctx, repo.DB); !errors.Is(err, m.ErrUnavailable) {
		t.Fatalf("runtime identity could change the database calendar: %v", err)
	}
	utc := assertBucket("course", target, midnight.Add(-time.Second), 2, 2)
	assertPending("discovery.activity.v1")
	assertRejectsVersion(m.ActivityEventType)
	from, until := midnight.AddDate(0, 0, -1), midnight
	_, cursor, err := repo.ActivitySnapshot(ctx, from, until, "", 1)
	if err != nil || cursor == "" {
		t.Fatalf("snapshot cursor: %q %v", cursor, err)
	}

	// Calendar metadata, updated facts, and queue changes must roll back together.
	exec(`ALTER TABLE reviews.outbox ADD CONSTRAINT fail_calendar CHECK(data->>'type'<>'discovery.activity.v2') NOT VALID`)
	if _, err = db.ExecContext(ctx, Schema); err == nil {
		t.Fatal("calendar switch ignored the failed outbox insert")
	}
	exec(`ALTER TABLE reviews.outbox DROP CONSTRAINT fail_calendar`)
	if got := assertBucket("course", target, from, 2, 2); got.Revision != utc.Revision {
		t.Fatal("failed migration committed a revision", got)
	}
	assertPending("discovery.activity.v1")

	exec(Schema)
	if err = repo.CheckRuntime(ctx); err != nil {
		t.Fatalf("runtime not ready after migration: %v", err)
	}
	if _, _, err = repo.ActivitySnapshot(ctx, from, until, cursor, 1); !errors.Is(err, m.ErrUnavailable) {
		t.Fatalf("calendar switch accepted a prior snapshot cursor: %v", err)
	}
	early := assertBucket("course", target, from, 1, 1)
	late := assertBucket("course", target, midnight, 1, 1)
	assertBucket("course", "other", from, 0, 0)
	assertBucket("course", "other", midnight, 1, 0)
	assertBucket("teacher", "teacher-a", midnight, 2, 1)
	if early.Day != "2026-10-31" || late.Day != "2026-11-01" || early.Revision <= utc.Revision {
		t.Fatal(early, late)
	}
	assertPending(m.ActivityEventType)
	assertRejectsVersion("discovery.activity.v1")
	for at, want := range map[time.Time]int{from: 0, midnight: 1} {
		ids, err := repo.ActivityDisclosureEntries(ctx, m.ActivityID("course", target, at), 100)
		if err != nil || len(ids) != want || (want == 1 && ids[0] != "reply") {
			t.Fatalf("disclosure candidates for %s: %v %v", at, ids, err)
		}
	}
	exec(Schema)
	if got := assertBucket("course", target, midnight, 1, 1); got.Revision != late.Revision {
		t.Fatal("retry changed the revision", got)
	}

	// A write after the switch is regrouped into its original UTC day on rollback.
	if err = repo.Transaction(ctx, func(tx m.Transaction) error {
		return tx.SetVote("late", "new-voter", -1, midnight.Add(time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	assertBucket("course", target, midnight, 1, 2)
	if err = RollbackActivityCalendar(ctx, db); err != nil {
		t.Fatal(err)
	}
	restored := assertBucket("course", target, from, 2, 3)
	assertBucket("course", target, midnight, 0, 0)
	assertPending("discovery.activity.v1")
	if restored.Revision <= early.Revision {
		t.Fatal("rollback reused a revision", restored)
	}
	if err = RollbackActivityCalendar(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := assertBucket("course", target, from, 2, 3); got.Revision != restored.Revision {
		t.Fatal("repeated rollback changed the revision", got)
	}
	exec(Schema)
	assertBucket("course", target, midnight, 1, 2)
	assertPending(m.ActivityEventType)
}
