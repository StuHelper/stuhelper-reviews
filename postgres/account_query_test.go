package postgres

import (
	"context"
	"database/sql"
	"fmt"
	aq "github.com/StuHelper/stuhelper-reviews/accountquery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
	"time"
)

func TestAccountQueryScopePaginationAndRuntime(t *testing.T) {
	dsn := os.Getenv("STUHELPER_REVIEWS_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	admin, e := sql.Open("pgx", cfg.ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	name := fmt.Sprintf("iq_%d", time.Now().UnixNano())
	if _, e = admin.Exec(`CREATE DATABASE ` + name); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`)
	cfg.Database = name
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	if _, e = db.Exec(Schema); e != nil {
		t.Fatal(e)
	}
	exec := func(s string, args ...any) {
		t.Helper()
		if _, e := db.Exec(s, args...); e != nil {
			t.Fatal(e)
		}
	}
	target := aq.Principal{Issuer: "issuer", Subject: "target"}
	for _, v := range []struct {
		id, school string
		signed     bool
		state      string
	}{{"a", "other", true, "published"}, {"b", "school", false, "published"}, {"c", "school", true, "hidden"}, {"d", "school", true, "withdrawn"}} {
		exec(`INSERT INTO reviews.entries(id,root_id,owner,school_id,kind,target_id,term_id,state,created_at,data) VALUES($1,$1,$2,$3,'course','course','term',$4,now(),jsonb_build_object('updated_at',now(),'input',jsonb_build_object('title','title','body','body')))`, v.id, target.Key(), v.school, v.state)
		exec(`INSERT INTO reviews.identities(root_id,subject,data) VALUES($1,$2,jsonb_build_object('Signed',$3::boolean))`, v.id, target.Key(), v.signed)
	}
	// Repeat the migration against existing data, including the timestamp column.
	if _, e = db.Exec(Schema); e != nil {
		t.Fatal(e)
	}
	role := name + "_reader"
	if _, e = admin.Exec(`CREATE ROLE ` + role + ` LOGIN`); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(`DROP ROLE ` + role)
	defer admin.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`)
	exec(`GRANT USAGE ON SCHEMA reviews TO ` + role)
	exec(`GRANT SELECT ON ALL TABLES IN SCHEMA reviews TO ` + role)
	cfg.User = role
	runtimeDB := stdlib.OpenDB(*cfg)
	defer runtimeDB.Close()
	if _, e = runtimeDB.Exec(`DELETE FROM reviews.entries`); e == nil {
		t.Fatal("reader modified source")
	}
	repo := &Repository{DB: runtimeDB}
	scope := aq.Scope{School: "school", Group: "*", Module: aq.Domain}

	s := aq.Selection{Query: aq.Query{Kind: "account", Value: target.Key(), Limit: 1}, Scopes: []aq.Scope{scope}, SensitiveScopes: []aq.Scope{}}
	ids := map[string]bool{}
	for {
		page, e := repo.ReadAccount(ctx, s)
		if e != nil {
			t.Fatal(e)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 1 || ids[page[0].ID] || !scope.Contains(page[0].Scope) {
			t.Fatal("scope or page", page)
		}
		ids[page[0].ID] = true
		s.Query.After = page[0].ID
	}
	if len(ids) != 1 {
		t.Fatal("unexpected filtered records", ids)
	}
	s.Query.After = ""
	s.Scopes = []aq.Scope{{School: "school", Group: "ungranted", Module: aq.Domain}}
	page, e := repo.ReadAccount(ctx, s)
	if e != nil || len(page) != 0 {
		t.Fatal("group scope bypass", page, e)
	}
	s.Scopes = []aq.Scope{scope}
	s.SensitiveScopes = []aq.Scope{scope}
	s.Query.After = ""
	s.Query.Limit = 100
	page, e = repo.ReadAccount(ctx, s)
	if e != nil || len(page) != 2 || page[0].ID != "reviews:b" || len(page[0].Fields) == 0 {
		t.Fatal("anonymous author grant", page, e)
	}
	s.SensitiveScopes = []aq.Scope{{School: "other", Group: "*", Module: aq.Domain}}
	page, e = repo.ReadAccount(ctx, s)
	if e != nil || len(page) != 1 {
		t.Fatal("anonymous grant escaped scope", page, e)
	}
	exec(`INSERT INTO reviews.closed_owners(owner,closure_id) VALUES($1,'closure')`, target.Key())
	s.SensitiveScopes = []aq.Scope{}
	page, e = repo.ReadAccount(ctx, s)
	if e != nil || len(page) != 0 {
		t.Fatal("closed author's signed identity leaked", page, e)
	}

}
