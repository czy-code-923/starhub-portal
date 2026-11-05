package portal

import (
	"context"
	"testing"
	"time"
)

func TestHistoryRetentionProtectsFinancialAndRetryRecords(t *testing.T) {
	f := setup(t)
	order := paidService(t, f)
	ctx := context.Background()
	if _, e := f.a.DB.Exec(ctx, `INSERT INTO audit(actor,action,subject,created_at) SELECT 'fixture','recent',n::text,now() FROM generate_series(1,10005) n; INSERT INTO audit(actor,action,subject,created_at) VALUES('fixture','old','old',now()-interval '31 days'); INSERT INTO jobs(id,kind,subject,state,updated_at) SELECT md5('done'||n),'mail','done'||n,'succeeded',now() FROM generate_series(1,2005) n; INSERT INTO jobs(id,kind,subject,state,updated_at) VALUES(md5('old'),'gateway','old','rolled_back',now()-interval '31 days')`); e != nil {
		t.Fatal(e)
	}
	for _, state := range []string{"queued", "working", "failed"} {
		if _, e := f.a.DB.Exec(ctx, "INSERT INTO jobs(id,kind,subject,state,updated_at) VALUES($1,'access',$2,$2,now()-interval '31 days')", ID(), state); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.a.pruneHistory(ctx, false, ""); e != nil {
		t.Fatal(e)
	}
	var audits, completed, protected int
	f.a.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit),(SELECT count(*) FROM jobs WHERE state IN ('succeeded','rolled_back')),(SELECT count(*) FROM jobs WHERE state IN ('queued','working','failed'))`).Scan(&audits, &completed, &protected)
	if audits != 10000 || completed != 2000 || protected != 3 {
		t.Fatal(audits, completed, protected)
	}
	expect(t, f.req("POST", "admin/record-retention?kind=jobs", map[string]any{"confirmed": true}, &f.customer, true), 403)
	sub := f.user("logreader@example.test", "admin")
	sub.Scopes = []string{"audit", "jobs"}
	f.a.DB.Exec(ctx, "UPDATE customers SET admin_scopes='[\"audit\",\"jobs\"]'::jsonb WHERE id=$1", sub.ID)
	expect(t, f.req("GET", "admin/record-retention?kind=audit", nil, &sub, true), 200)
	expect(t, f.req("POST", "admin/record-retention?kind=audit", map[string]any{"confirmed": true}, &sub, true), 403)
	expect(t, f.req("POST", "admin/record-retention?kind=jobs", map[string]any{"confirmed": false}, &f.admin, true), 400)
	expect(t, f.req("POST", "admin/record-retention?kind=jobs", map[string]any{"confirmed": true}, &f.admin, true), 200)
	expect(t, f.req("POST", "admin/record-retention?kind=audit", map[string]any{"confirmed": true}, &f.admin, true), 200)
	var marker, ledger, orders int
	f.a.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit),(SELECT count(*) FROM entitlement_ledger WHERE order_id=$1),(SELECT count(*) FROM orders WHERE id=$1)`, order).Scan(&marker, &ledger, &orders)
	if marker != 1 || ledger != 1 || orders != 1 {
		t.Fatal("lost durable journal", marker, ledger, orders)
	}
	before := f.grants.Load()
	if e := f.a.fulfill(ctx, order); e != nil {
		t.Fatal(e)
	}
	if f.grants.Load() != before {
		t.Fatal("history cleanup granted twice")
	}
	f.a.nextRetention = time.Time{}
	if e := f.a.pruneHistory(ctx, false, ""); e != nil {
		t.Fatal(e)
	}
}
