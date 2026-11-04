package portal

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpgradePreservesExistingOwnerAndCredentials(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	root, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	schema := "upgrade_test_" + ID()
	if _, e = root.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	old, e := pgxpool.New(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	data, e := migrations.ReadFile("migrations/001_initial.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = old.Exec(ctx, string(data)); e != nil {
		t.Fatal(e)
	}
	if _, e = old.Exec(ctx, "CREATE TABLE schema_migrations(version text PRIMARY KEY); INSERT INTO schema_migrations VALUES('001_initial.sql')"); e != nil {
		t.Fatal(e)
	}
	id := ID()
	encoded, e := passwordHash("existing-owner-password")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = old.Exec(ctx, `INSERT INTO customers(id,email,name,password_hash,role,status,email_verified_at) VALUES($1,'existing@example.test','Existing owner',$2,'admin','active',now())`, id, encoded); e != nil {
		t.Fatal(e)
	}
	a, e := Open(ctx, u.String(), bytes.Repeat([]byte{37}, 32), "http://portal.test", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.DB.Close()
	var owner bool
	var got string
	if e = a.DB.QueryRow(ctx, "SELECT is_owner,password_hash FROM customers WHERE id=$1", id).Scan(&owner, &got); e != nil || !owner || got != encoded {
		t.Fatalf("upgrade changed existing identity: %v %v", owner, e)
	}
	if e = a.Bootstrap(ctx, "replacement@example.test", "should-not-create-owner"); e == nil {
		t.Fatal("upgrade allowed replacing primary owner")
	}
	if e = a.migrate(ctx); e != nil {
		t.Fatal("migration not idempotent", e)
	}
}
