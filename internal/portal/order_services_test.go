package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOrdersHaveSeparateSubscriptionsAndDownloadsEnforceOwnership(t *testing.T) {
	f := setup(t)
	plan := f.plan()
	ctx := context.Background()
	ids := []string{}
	for range 2 {
		w := f.order(plan, ID(), &f.customer, "")
		expect(t, w, 201)
		id := object(t, w)["id"].(string)
		ids = append(ids, id)
		if _, e := f.a.DB.Exec(ctx, "UPDATE orders SET state='paid',fulfillment='queued',paid_at=now() WHERE id=$1", id); e != nil {
			t.Fatal(e)
		}
		if e := f.a.fulfill(ctx, id); e != nil {
			t.Fatal(e)
		}
	}
	w := f.req("GET", "me/services", nil, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["total"] != float64(2) {
		t.Fatal(w.Body.String())
	}
	var distinct int
	f.a.DB.QueryRow(ctx, "SELECT count(DISTINCT control_user_id) FROM service_accounts WHERE customer_id=$1", f.customer.ID).Scan(&distinct)
	if distinct != 2 {
		t.Fatal("orders shared proxy identity")
	}
	f.a.DB.QueryRow(ctx, "SELECT count(DISTINCT subscription_id) FROM service_accounts WHERE customer_id=$1", f.customer.ID).Scan(&distinct)
	if distinct != 2 {
		t.Fatal("orders shared subscription")
	}
	before := f.grants.Load()
	if e := f.a.fulfill(ctx, ids[0]); e != nil {
		t.Fatal(e)
	}
	if f.grants.Load() != before {
		t.Fatal("active order granted twice")
	}
	for _, view := range []string{"service", "subscription/share", "subscription/content", "subscription/qr", "devices"} {
		expect(t, f.req("GET", "me/"+view+"?service="+ids[0], nil, &f.other, true), 404)
	}
	var sub string
	f.a.DB.QueryRow(ctx, "SELECT subscription_id FROM service_accounts WHERE order_id=$1", ids[0]).Scan(&sub)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/subscriptions/"+sub+"/content" || r.URL.Query().Get("client") != "mihomo" {
			t.Error("wrong order/client", r.URL.String())
		}
		w.Header().Set("Content-Type", "text/yaml")
		w.Write([]byte("proxies: []\n"))
	}))
	defer mock.Close()
	f.a.TestControlURL = mock.URL
	w = f.req("GET", "me/subscription/content?service="+ids[0]+"&client=auto", nil, &f.customer, true)
	expect(t, w, 200)
	if !strings.Contains(w.Header().Get("Content-Disposition"), ids[0]+".yaml") || w.Body.String() != "proxies: []\n" {
		t.Fatal("bad download", w.Header(), w.Body.String())
	}
}

func TestLegacyMigrationFreezesRequestAndMapsEveryOrder(t *testing.T) {
	f := setup(t)
	plan := f.plan()
	ctx := context.Background()
	ids := []string{}
	for range 2 {
		w := f.order(plan, ID(), &f.customer, "")
		expect(t, w, 201)
		id := object(t, w)["id"].(string)
		ids = append(ids, id)
		f.a.DB.Exec(ctx, "UPDATE orders SET state='paid',fulfillment='active',paid_at=now(),per_order=false WHERE id=$1", id)
	}
	oldUser, oldSub := ID(), ID()
	_, e := f.a.DB.Exec(ctx, `INSERT INTO service_accounts(customer_id,order_id,last_order_id,control_user_id,subscription_id,plan_id,external_id,legacy_combined) VALUES($1,$2,$2,$3,$4,$5,$6,true)`, f.customer.ID, ids[1], oldUser, oldSub, plan, "portal:"+f.customer.ID)
	if e != nil {
		t.Fatal(e)
	}
	var first []byte
	calls := 0
	expiry := time.Now().Add(45 * 24 * time.Hour)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "proxy-users/") {
			send(w, 200, map[string]any{"expires_at": expiry})
			return
		}
		if !strings.HasSuffix(r.URL.Path, "commerce/migrate-orders") {
			t.Error(r.URL.Path)
			send(w, 500, nil)
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		payload := raw(b)
		calls++
		if first == nil {
			first = payload
			send(w, 503, map[string]string{"error": "fixture transient"})
			return
		}
		if !bytes.Equal(first, payload) {
			t.Error("retry changed immutable migration payload")
		}
		orders := []map[string]any{}
		for _, v := range b["orders"].([]any) {
			o := v.(map[string]any)
			id := o["order_id"].(string)
			orders = append(orders, map[string]any{"order_id": id, "external_id": "portal:" + id, "user_id": hash("u/" + id)[:32], "subscription_id": hash("s/" + id)[:32], "operation_id": hash(id)[:32], "quota_bytes": o["quota_bytes"], "used_bytes": 1, "remaining_bytes": o["quota_bytes"].(float64) - 1})
		}
		send(w, 200, map[string]any{"orders": orders, "credentials_revoked": true, "legacy_user_id": oldUser, "expires_at": expiry, "remaining_bytes": 10})
	}))
	defer mock.Close()
	f.a.TestControlURL = mock.URL
	if e = f.a.migrateCustomer(ctx, f.customer.ID); e == nil {
		t.Fatal("transient failure accepted")
	}
	f.a.DB.Exec(ctx, "UPDATE customers SET name='renamed after remote request' WHERE id=$1", f.customer.ID)
	if e = f.a.migrateCustomer(ctx, f.customer.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.a.migrateCustomer(ctx, f.customer.ID); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("migration replay called remote twice", calls)
	}
	var count int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM service_accounts WHERE customer_id=$1 AND NOT legacy_combined", f.customer.ID).Scan(&count)
	if count != 2 {
		t.Fatal(count)
	}
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM orders WHERE customer_id=$1 AND per_order AND snapshot->'plan'->>'quota_unit'='GiB'", f.customer.ID).Scan(&count)
	if count != 2 {
		t.Fatal("snapshots not migrated", count)
	}
}

func TestPlanFeaturesAndMultiplePinnedAnnouncements(t *testing.T) {
	f := setup(t)
	plan := f.plan()
	ctx := context.Background()
	var p Plan
	var data []byte
	f.a.DB.QueryRow(ctx, "SELECT to_jsonb(p) FROM plans p WHERE id=$1", plan).Scan(&data)
	json.Unmarshal(data, &p)
	p.DisplayFeatures = []PlanFeature{{"速率", "500 Mbps"}, {"适用场景", "日常浏览 / 多端使用"}}
	p.Multiplier = 2000
	expect(t, f.req("PUT", "admin/plans", p, &f.admin, true), 200)
	w := f.req("GET", "catalog", nil, &f.customer, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "500 Mbps") || strings.Contains(w.Body.String(), "multiplier_milli") || strings.Contains(w.Body.String(), "billing_mode") {
		t.Fatal("catalog feature or billing projection", w.Body.String())
	}
	p.DisplayFeatures = make([]PlanFeature, 13)
	expect(t, f.req("PUT", "admin/plans", p, &f.admin, true), 400)
	for _, b := range []map[string]any{{"title": "PIN-2", "body": "second", "enabled": true, "pinned": true, "pin_order": 2}, {"title": "PIN-1", "body": "first", "enabled": true, "pinned": true, "pin_order": 1}, {"title": "NEWEST-UNPINNED", "body": "normal", "enabled": true}} {
		expect(t, f.req("POST", "admin/announcements", b, &f.admin, true), 200)
	}
	w = f.req("GET", "public", nil, nil, false)
	expect(t, w, 200)
	o := object(t, w)
	items := o["announcements"].([]any)
	if items[0].(map[string]any)["title"] != "PIN-1" || items[1].(map[string]any)["title"] != "PIN-2" {
		t.Fatal("multiple pins lost ordering", w.Body.String())
	}
}

func TestRedpacketApprovalNeedsNoReferenceButStaticQRDoes(t *testing.T) {
	f, plan, _ := setupManual(t)
	enableRedpacket(t, f)
	order, id := beginRedpacket(t, f, plan, &f.customer)
	expect(t, f.req("POST", "orders/"+order+"/receipt", map[string]any{"version": 1, "redpacket_password": "fixture-only-redpacket-3948"}, &f.customer, true), 200)
	b := map[string]any{"version": 2, "decision": "approve", "received_amount": 1000, "received_at": time.Now(), "confirmed": true}
	expect(t, f.req("PUT", "admin/manual-payments/"+id, b, &f.admin, true), 200)
	var ref string
	f.a.DB.QueryRow(context.Background(), "SELECT verified_reference FROM manual_payments WHERE id=$1", id).Scan(&ref)
	if !strings.HasPrefix(ref, "REDPACK-") {
		t.Fatal("missing internal reconciliation identifier")
	}
	w := f.order(plan, ID(), &f.other, "")
	expect(t, w, 201)
	staticOrder := object(t, w)["id"].(string)
	w = f.req("POST", "orders/"+staticOrder+"/payment", map[string]string{"method_id": "alipay_static"}, &f.other, true)
	expect(t, w, 200)
	staticID := object(t, w)["attempt"].(map[string]any)["id"].(string)
	expect(t, f.req("POST", "orders/"+staticOrder+"/receipt", map[string]any{"version": 1, "reference": "REAL-QR-FIXTURE-1122"}, &f.other, true), 200)
	expect(t, f.req("PUT", "admin/manual-payments/"+staticID, b, &f.admin, true), 400)
}
