package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHelpPermissionsValidationAndConcurrentEdits(t *testing.T) {
	f := setup(t)
	w := f.req("GET", "client-help", nil, &f.customer, true)
	expect(t, w, 200)
	d := object(t, w)
	var doc helpDocument
	if e := json.Unmarshal(raw(d["document"]), &doc); e != nil {
		t.Fatal(e)
	}
	if len(doc.Apps) != 6 {
		t.Fatal("default clients missing")
	}
	body := map[string]any{"version": 0, "document": doc}
	expect(t, f.req("PUT", "admin/client-help", body, &f.customer, true), 403)
	doc.Title = "管理员维护的导入教程"
	doc.Apps[0].Steps = []string{"第一步：复制属于此订单的订阅链接"}
	body["document"] = doc
	expect(t, f.req("PUT", "admin/client-help", body, &f.admin, true), 200)
	expect(t, f.req("PUT", "admin/client-help", body, &f.admin, true), 409)
	w = f.req("GET", "client-help", nil, &f.customer, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), doc.Title) {
		t.Fatal("edited help not visible")
	}
	doc.Apps[0].URL = "javascript:alert(1)"
	body["document"] = doc
	body["version"] = 1
	expect(t, f.req("PUT", "admin/client-help", body, &f.admin, true), 400)
	doc.Apps[0].URL = "https://example.test/help"
	body["document"] = doc
	expect(t, f.req("PUT", "admin/client-help", body, &f.admin, true), 200)
}

func TestDeleteCustomerRevokesEveryServiceAndPreservesReconciliation(t *testing.T) {
	f := setup(t)
	plan := f.plan()
	ctx := context.Background()
	orders := []string{}
	for range 2 {
		w := f.order(plan, ID(), &f.customer, "")
		expect(t, w, 201)
		id := object(t, w)["id"].(string)
		orders = append(orders, id)
		f.a.DB.Exec(ctx, "UPDATE orders SET state='paid',paid_at=now(),fulfillment='queued' WHERE id=$1", id)
		if e := f.a.fulfill(ctx, id); e != nil {
			t.Fatal(e)
		}
	}
	token := ID() + ID()
	f.a.DB.Exec(ctx, "INSERT INTO email_tokens(id,customer_id,purpose,token_hash,expires_at) VALUES($1,$2,'reset',$3,now()+interval '1 hour')", ID(), f.customer.ID, hash(token))
	body := map[string]any{"id": f.customer.ID, "version": 1, "confirmed": true}
	expect(t, f.req("DELETE", "admin/customers", body, &f.other, true), 403)
	scoped := f.user("limited@example.test", "admin")
	f.a.DB.Exec(ctx, "UPDATE customers SET admin_scopes=$2 WHERE id=$1", scoped.ID, raw([]string{"customers"}))
	expect(t, f.req("DELETE", "admin/customers", body, &scoped, true), 403)
	body["version"] = 9
	expect(t, f.req("DELETE", "admin/customers", body, &f.admin, true), 409)
	body["version"] = 1
	expect(t, f.req("DELETE", "admin/customers", body, &f.admin, true), 200)
	expect(t, f.req("GET", "me", nil, &f.customer, true), 401)
	expect(t, f.req("POST", "auth/reset", map[string]string{"token": token, "password": "replacement-password", "password_confirm": "replacement-password"}, nil, false), 400)
	w := f.req("GET", "admin/customers", nil, &f.admin, true)
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), f.customer.ID) {
		t.Fatal("deleted account still in active list")
	}
	w = f.req("GET", "admin/customers?deleted=1", nil, &f.admin, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), f.customer.ID) {
		t.Fatal("archive not queryable")
	}
	count := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if r.Method != "PUT" || !strings.Contains(r.URL.Path, "commerce/accounts/") || b["enabled"] != false {
			t.Error("deleted service re-enabled", r.URL.String(), b)
		}
		count++
		send(w, 200, map[string]bool{"ok": true})
	}))
	defer mock.Close()
	f.a.TestControlURL = mock.URL
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	if count < 2 {
		t.Fatal("not all order identities revoked", count)
	}
	var n int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM orders WHERE customer_id=$1 AND state='paid'", f.customer.ID).Scan(&n)
	if n != 2 {
		t.Fatal("financial history lost", n)
	}
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM entitlement_ledger WHERE customer_id=$1", f.customer.ID).Scan(&n)
	if n != 2 {
		t.Fatal("entitlement journal lost")
	}
	expect(t, f.req("PUT", "admin/customers", map[string]string{"id": f.customer.ID, "status": "active"}, &f.admin, true), 400)
	body["version"] = 2
	expect(t, f.req("POST", "admin/customers/restore", body, &f.admin, true), 200)
	var status string
	f.a.DB.QueryRow(ctx, "SELECT status FROM customers WHERE id=$1", f.customer.ID).Scan(&status)
	if status != "disabled" {
		t.Fatal("restore auto-enabled access")
	}
}

func TestDeleteSubAdministratorProtectsOwnerAndRevokesAccess(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sub := f.user("subadmin@example.test", "admin")
	f.a.DB.Exec(ctx, "UPDATE customers SET admin_scopes=$2 WHERE id=$1", sub.ID, raw([]string{"payments"}))
	body := map[string]any{"id": f.admin.ID, "version": 1, "confirmed": true}
	expect(t, f.req("DELETE", "admin/administrators", body, &f.admin, true), 403)
	body["id"] = sub.ID
	expect(t, f.req("DELETE", "admin/administrators", body, &sub, true), 403)
	expect(t, f.req("DELETE", "admin/administrators", body, &f.admin, true), 200)
	expect(t, f.req("GET", "me", nil, &sub, true), 401)
	if _, e := f.a.tgReviewer(ctx, tgPrincipal{Administrator: sub.ID}); e == nil {
		t.Fatal("deleted admin can review Telegram payments")
	}
	expect(t, f.req("GET", "admin/administrators?deleted=1", nil, &f.admin, true), 200)
	body["version"] = 2
	expect(t, f.req("POST", "admin/administrators/restore", body, &f.admin, true), 200)
	if _, e := f.a.tgReviewer(ctx, tgPrincipal{Administrator: sub.ID}); e == nil {
		t.Fatal("restored admin auto-activated")
	}
}

func TestAccessWorkerCannotOverwriteNewerLease(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	job := ID()
	newLease := ID()
	f.a.DB.Exec(ctx, "INSERT INTO jobs(id,kind,subject) VALUES($1,'access',$2)", job, f.customer.ID)
	changed := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !changed {
			changed = true
			if _, e := f.a.DB.Exec(ctx, "UPDATE jobs SET state='working',lease_generation=$2,lease_until=now()+interval '1 hour' WHERE id=$1", job, newLease); e != nil {
				t.Error(e)
			}
		}
		send(w, 200, map[string]bool{"ok": true})
	}))
	defer mock.Close()
	f.a.TestControlURL = mock.URL
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	var state, lease string
	f.a.DB.QueryRow(ctx, "SELECT state,lease_generation FROM jobs WHERE id=$1", job).Scan(&state, &lease)
	if state != "working" || lease != newLease {
		t.Fatal("stale worker clobbered a newer lease", state, lease)
	}
}

func TestExpiredFulfillmentLeaseCannotMarkNewlyActiveOrderFailed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	plan := f.plan()
	w := f.order(plan, ID(), &f.customer, "")
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	job, lease := ID(), ID()
	f.a.DB.Exec(ctx, "UPDATE orders SET state='paid',fulfillment='queued',paid_at=now() WHERE id=$1", order)
	f.a.DB.Exec(ctx, "INSERT INTO jobs(id,kind,subject,attempts) VALUES($1,'fulfill',$2,9)", job, order)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/validate") {
			send(w, 200, map[string]bool{"ready": true})
			return
		}
		f.a.DB.Exec(ctx, "UPDATE jobs SET state='working',lease_generation=$2,lease_until=now()+interval '1 hour' WHERE id=$1", job, lease)
		f.a.DB.Exec(ctx, "UPDATE orders SET fulfillment='active' WHERE id=$1", order)
		send(w, 503, map[string]string{"error": "isolated transient"})
	}))
	defer mock.Close()
	f.a.TestControlURL = mock.URL
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	var state string
	f.a.DB.QueryRow(ctx, "SELECT fulfillment FROM orders WHERE id=$1", order).Scan(&state)
	if state != "active" {
		t.Fatal("stale failure overwrote a newer successful fulfillment", state)
	}
}
