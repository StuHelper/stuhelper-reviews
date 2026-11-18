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
	if _, e = migration.Exec(`REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT CONNECT ON DATABASE ` + name + ` TO ` + role + `; GRANT USAGE ON SCHEMA reviews TO ` + role + `; GRANT USAGE ON ALL SEQUENCES IN SCHEMA reviews TO ` + role + `; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA reviews TO ` + role + `; REVOKE UPDATE,DELETE ON reviews.audit,reviews.discovery_history,reviews.activity_history FROM ` + role + `; CREATE SCHEMA unrelated; CREATE TABLE unrelated.private_data(id integer); REVOKE ALL ON SCHEMA unrelated FROM PUBLIC;`); e != nil {
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
	if _, e = db.Exec(`SELECT * FROM unrelated.private_data`); e == nil {
		t.Fatal("runtime identity escaped its schema")
	}
	if _, e = db.Exec(`CREATE TABLE reviews.forbidden_ddl(id integer)`); e == nil {
		t.Fatal("runtime identity can execute migrations")
	}
	if _, e = db.Exec(`DELETE FROM reviews.audit`); e == nil {
		t.Fatal("runtime identity can delete audit")
	}
	return repo, migration
}
