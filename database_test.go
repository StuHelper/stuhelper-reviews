package reviews_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"github.com/StuHelper/stuhelper-reviews/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
)

func database(t *testing.T) (*postgres.Repository, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("STUHELPER_REVIEWS_TEST_DSN")
	if dsn == "" {
		t.Fatal("isolated PostgreSQL test DSN is required")
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal("invalid test DSN")
	}
	admin := stdlib.OpenDB(*cfg)
	b := make([]byte, 6)
	if _, e = rand.Read(b); e != nil {
		t.Fatal(e)
	}
	name := "academic_reviews_" + hex.EncodeToString(b)
	role := name + "_runtime"
	if _, e = admin.ExecContext(context.Background(), `CREATE DATABASE `+name); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(`CREATE ROLE ` + role + ` LOGIN`); e != nil {
		t.Fatal(e)
	}
	dbcfg := cfg.Copy()
	dbcfg.Database = name
	migration := stdlib.OpenDB(*dbcfg)
	t.Cleanup(func() {
		migration.Close()
		if _, e := admin.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`); e != nil {
			t.Error(e)
		}
		if _, e := admin.Exec(`DROP ROLE ` + role); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	if _, e = migration.Exec(postgres.Schema); e != nil {
		t.Fatal(e)
	}
	grants := `
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT CONNECT ON DATABASE ` + name + ` TO ` + role + `;
		GRANT USAGE ON SCHEMA reviews TO ` + role + `;
		GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA reviews TO ` + role + `;
		GRANT SELECT,INSERT,UPDATE,DELETE ON reviews.entries,reviews.votes,reviews.drafts,reviews.replays,reviews.activity_contributions TO ` + role + `;
		GRANT SELECT,INSERT,UPDATE ON reviews.identities,reviews.presentations,reviews.activity_buckets,reviews.outbox TO ` + role + `;
		GRANT SELECT,INSERT ON reviews.audit,reviews.history,reviews.presentation_history,reviews.discovery_history,reviews.activity_history,reviews.closed_owners TO ` + role + `;
		GRANT SELECT ON reviews.audit_retention,reviews.discovery_retention,reviews.activity_retention TO ` + role + `;
		CREATE SCHEMA unrelated;
		CREATE TABLE unrelated.private_data(id integer);
		REVOKE ALL ON SCHEMA unrelated FROM PUBLIC;
	`
	if _, e = migration.Exec(grants); e != nil {
		t.Fatal(e)
	}
	if err := (&postgres.Repository{DB: migration}).CheckRuntime(context.Background()); err == nil {
		t.Fatal("CheckRuntime accepted migration identity")
	}
	runtime := dbcfg.Copy()
	runtime.User = role
	runtime.Password = ""
	db := stdlib.OpenDB(*runtime)
	db.SetMaxOpenConns(12)
	t.Cleanup(func() { db.Close() })
	repo := &postgres.Repository{DB: db}
	if err := repo.CheckRuntime(context.Background()); err != nil {
		t.Fatalf("CheckRuntime rejected runtime role: %v", err)
	}
	for _, forbiddenSQL := range []string{
		`SELECT * FROM unrelated.private_data`,
		`CREATE TABLE reviews.forbidden_ddl(id integer)`,
		`DELETE FROM reviews.audit`,
		`DELETE FROM reviews.history`,
		`UPDATE reviews.history SET revision=0`,
		`DELETE FROM reviews.discovery_history`,
		`UPDATE reviews.activity_history SET revision=0`,
		`DELETE FROM reviews.outbox`,
		`DELETE FROM reviews.closed_owners`,
		`UPDATE reviews.audit_retention SET revision=0`,
	} {
		if _, e = db.Exec(forbiddenSQL); e == nil {
			t.Fatalf("runtime identity executed forbidden SQL: %s", forbiddenSQL)
		}
	}
	return repo, migration
}

// Confirmed outbox events are removed by a dedicated maintenance role.
func outboxPruner(t *testing.T, migration *sql.DB) *postgres.Repository {
	t.Helper()
	cfg, err := pgx.ParseConfig(os.Getenv("STUHELPER_REVIEWS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	if err = migration.QueryRow("SELECT current_database()").Scan(&cfg.Database); err != nil {
		t.Fatal(err)
	}
	cfg.User = cfg.Database + "_outbox_pruner"
	cfg.Password = ""
	role := pgx.Identifier{cfg.User}.Sanitize()
	if _, err = migration.Exec("CREATE ROLE " + role + " LOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := migration.Exec("DROP OWNED BY " + role + "; DROP ROLE " + role); err != nil {
			t.Error(err)
		}
	})
	if _, err = migration.Exec("GRANT USAGE ON SCHEMA reviews TO " + role + "; GRANT SELECT,DELETE ON reviews.outbox TO " + role); err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	return &postgres.Repository{DB: db}
}
