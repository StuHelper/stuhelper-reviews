package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	m "github.com/StuHelper/stuhelper-reviews"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"strings"
	"testing"
	"time"
)

func activityDatabase(t *testing.T) (*Repository, *Repository, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("STUHELPER_REVIEWS_TEST_DSN")
	if dsn == "" {
		t.Fatal("isolated PostgreSQL required")
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	admin := stdlib.OpenDB(*cfg)
	name := fmt.Sprintf("activity_%d", time.Now().UnixNano())
	role := name + "_runtime"
	cleaner := name + "_maintenance"
	if _, e = admin.Exec(`CREATE DATABASE ` + name); e != nil {
		t.Fatal(e)
	}
	cfg.Database = name
	db := stdlib.OpenDB(*cfg)
	if _, e = db.Exec(Schema); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(`CREATE ROLE ` + role + ` LOGIN; CREATE ROLE ` + cleaner + ` LOGIN`); e != nil {
		t.Fatal(e)
	}
	grants := `
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT USAGE ON SCHEMA reviews TO ` + role + `,` + cleaner + `;
		GRANT SELECT ON reviews.activity_buckets TO ` + cleaner + `;
		GRANT SELECT,DELETE ON reviews.activity_history TO ` + cleaner + `;
		GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA reviews TO ` + role + `;
		GRANT SELECT,INSERT,UPDATE,DELETE ON reviews.entries,reviews.votes,reviews.drafts,reviews.replays,reviews.activity_contributions TO ` + role + `;
		GRANT SELECT,INSERT,UPDATE ON reviews.identities,reviews.presentations,reviews.activity_buckets,reviews.outbox TO ` + role + `;
		GRANT SELECT,INSERT ON reviews.audit,reviews.history,reviews.presentation_history,reviews.discovery_history,reviews.activity_history,reviews.closed_owners TO ` + role + `;
		GRANT SELECT ON reviews.audit_retention,reviews.discovery_retention,reviews.activity_retention TO ` + role + `;
	`
	if _, e = db.Exec(grants); e != nil {
		t.Fatal(e)
	}
	runtimeCfg := cfg.Copy()
	runtimeCfg.User = role
	runtime := stdlib.OpenDB(*runtimeCfg)
	cleanerCfg := cfg.Copy()
	cleanerCfg.User = cleaner
	maintenance := stdlib.OpenDB(*cleanerCfg)
	repo := &Repository{DB: runtime}
	if err := repo.CheckRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		maintenance.Close()
		runtime.Close()
		db.Close()
		if _, e := admin.Exec(`DROP DATABASE ` + name + ` WITH(FORCE)`); e != nil {
			t.Error(e)
		}
		if _, e := admin.Exec(`DROP ROLE ` + role + `,` + cleaner); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	return repo, &Repository{DB: maintenance}, db
}
func TestActivitySnapshotSchemaRollbackAndBoundedCleanup(t *testing.T) {
	repo, maintenance, db := activityDatabase(t)
	ctx := context.Background()
	if e := repo.CheckRuntime(ctx); e != nil {
		t.Fatal(e)
	}
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	change := func(tx *sql.Tx, id string, delta int64) error {
		return (&transaction{tx: tx, ctx: ctx}).changeActivity("course", id, "school", day, delta, 0, nil, day)
	}
	apply := func(id string, delta int64) error {
		tx, e := repo.DB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = change(tx, id, delta); e != nil {
			return e
		}
		return tx.Commit()
	}
	listener, e := repo.DB.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	if _, e = listener.ExecContext(ctx, "LISTEN "+m.OutboxChannel); e != nil {
		t.Fatal(e)
	}
	// Business-independent repository fixture observes the public snapshot and relay ports.
	tx, e := repo.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = change(tx, "rolled-back", 1); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	wait, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	e = listener.Raw(func(raw any) error { _, e := raw.(*stdlib.Conn).Conn().WaitForNotification(wait); return e })
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("rollback notified", e)
	}
	pending, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(pending) != 0 {
		t.Fatal(pending, e)
	}
	if e = apply("a", 1); e != nil {
		t.Fatal(e)
	}
	if e = apply("b", 1); e != nil {
		t.Fatal(e)
	}
	wait, cancel = context.WithTimeout(ctx, time.Second)
	e = listener.Raw(func(raw any) error {
		n, e := raw.(*stdlib.Conn).Conn().WaitForNotification(wait)
		if e == nil && (n.Channel != m.OutboxChannel || n.Payload != "") {
			t.Error(n)
		}
		return e
	})
	cancel()
	if e != nil {
		t.Fatal(e)
	}
	late, e := repo.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer late.Rollback()
	if e = change(late, "z-late", 1); e != nil {
		t.Fatal(e)
	}
	page, cursor, e := repo.ActivitySnapshot(ctx, day, day.AddDate(0, 0, 1), "", 1)
	if e != nil || len(page) != 1 || cursor == "" {
		t.Fatal(page, cursor, e)
	}
	firstCursor := cursor
	// Runtime role cannot delete activity history; maintenance role performs cleanup.
	if _, e := repo.PruneActivityHistory(ctx, day, 1); !errors.Is(e, m.ErrUnavailable) {
		t.Fatalf("runtime identity deleted activity history: %v", e)
	}
	// Empty cleanup does not invalidate a reader when no history was removed.
	if n, e := maintenance.PruneActivityHistory(ctx, day, 1); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if _, _, e := repo.ActivitySnapshot(ctx, day, day.AddDate(0, 0, 1), firstCursor, 1); e != nil {
		t.Fatal("empty cleanup invalidated cursor", e)
	}
	if e = late.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = apply("b", -1); e != nil {
		t.Fatal(e)
	}
	seen := append([]m.Activity{}, page...)
	for cursor != "" {
		before := cursor
		page, cursor, e = repo.ActivitySnapshot(ctx, day, day.AddDate(0, 0, 1), cursor, 1)
		if e != nil || cursor == before {
			t.Fatal(page, cursor, e)
		}
		seen = append(seen, page...)
	}
	if len(seen) != 2 || !seen[1].Visible || seen[1].Revision != 1 {
		t.Fatal(seen)
	}
	fresh, _, e := repo.ActivitySnapshot(ctx, day, day.AddDate(0, 0, 1), "", 100)
	if e != nil || len(fresh) != 3 || fresh[1].Visible || fresh[1].Revision != 2 {
		t.Fatal(fresh, e)
	}
	for _, n := range fresh[1].Metrics {
		if n != 0 {
			t.Fatal("tombstone retained a count", fresh[1])
		}
	}
	if _, _, e = repo.ActivitySnapshot(ctx, day, day.AddDate(0, 0, 2), firstCursor, 1); e == nil {
		t.Fatal("window changed")
	}
	if _, _, e = repo.ActivitySnapshot(ctx, day, day.AddDate(0, 0, 1), firstCursor, 2); e == nil {
		t.Fatal("limit changed")
	}
	if _, _, e = repo.ActivitySnapshot(ctx, day.Add(time.Hour), day.AddDate(0, 0, 1), firstCursor, 1); e != nil {
		t.Fatal("same Beijing date changed the cursor window", e)
	}
	if _, _, e = repo.ActivitySnapshot(ctx, day, day.Add(time.Hour), "", 1); !errors.Is(e, m.ErrInput) {
		t.Fatal("empty date window accepted", e)
	}
	raw, e := os.ReadFile("../contracts/discovery-activity.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "b536d1161b4dbf53db99c3da9760dbe294be6e705f4e8f75541df02c6f318bb7" {
		t.Fatal("activity contract drift")
	}
	var definition any
	if e = json.Unmarshal(raw, &definition); e != nil {
		t.Fatal(e)
	}
	compiler := jsonschema.NewCompiler()
	if e = compiler.AddResource("urn:activity", definition); e != nil {
		t.Fatal(e)
	}
	schema, e := compiler.Compile("urn:activity")
	if e != nil {
		t.Fatal(e)
	}
	pending, e = repo.PendingOutbox(ctx, 100, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, event := range pending {
		raw, e := json.Marshal(event)
		if e != nil {
			t.Fatal(e)
		}
		var value any
		if e = json.Unmarshal(raw, &value); e != nil {
			t.Fatal(e)
		}
		if e = schema.Validate(value); e != nil {
			t.Fatal(e, string(raw))
		}
		var envelope struct {
			Subject string     `json:"subject"`
			Data    m.Activity `json:"data"`
		}
		if e = json.Unmarshal(raw, &envelope); e != nil {
			t.Fatal(e)
		}
		if envelope.Subject != envelope.Data.ID || envelope.Data.ID != m.ActivityID(envelope.Data.Target.Kind, envelope.Data.Target.ID, day) {
			t.Fatal(envelope)
		}
		if strings.Contains(string(raw), "owner") || strings.Contains(string(raw), "actor") {
			t.Fatal("identity leaked", string(raw))
		}
	}
	excluded, e := repo.PendingOutbox(ctx, 100, []string{m.ActivityID("course", "a", day)})
	if e != nil || len(excluded) != len(pending)-1 {
		t.Fatal(excluded, e)
	}
	for _, event := range pending {
		if e = repo.ConfirmOutbox(ctx, event.ID); e != nil {
			t.Fatal(e)
		}
		if e = repo.ConfirmOutbox(ctx, event.ID); e != nil {
			t.Fatal(e)
		}
	}
	if e = apply("a", 1); e != nil {
		t.Fatal(e)
	}
	if e = apply("a", 1); e != nil {
		t.Fatal(e)
	}
	n, e := maintenance.PruneActivityHistory(ctx, day.AddDate(0, 0, 1), 1)
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, _, e = repo.ActivitySnapshot(ctx, day, day.AddDate(0, 0, 1), firstCursor, 1); !errors.Is(e, m.ErrUnavailable) {
		t.Fatal("retention didn't invalidate", e)
	}
	current, e := repo.ReadActivity(ctx, m.ActivityID("course", "b", day))
	if e != nil || current.Visible {
		t.Fatal("latest tombstone removed", current, e)
	}
	// A failed outbox insert cannot leave a partial counter revision.
	if _, e = db.Exec(`ALTER TABLE reviews.outbox ADD CONSTRAINT fail_activity CHECK(data->>'type'<>'discovery.activity.v2') NOT VALID`); e != nil {
		t.Fatal(e)
	}
	current, e = repo.ReadActivity(ctx, m.ActivityID("course", "a", day))
	if e != nil {
		t.Fatal(e)
	}
	if e = apply("a", 1); e == nil {
		t.Fatal("outbox failure ignored")
	}
	after, e := repo.ReadActivity(ctx, current.ID)
	if e != nil || after.Revision != current.Revision {
		t.Fatal(after, e)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, _, e = repo.ActivitySnapshot(cancelled, day, day.AddDate(0, 0, 1), "", 1); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
