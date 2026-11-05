package portal

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPlanSelectionArchiveAndRestore(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.plan()
	var p Plan
	var b []byte
	if e := f.a.DB.QueryRow(ctx, "SELECT to_jsonb(p) FROM plans p WHERE id=$1", id).Scan(&b); e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(b, &p)
	p.Protocols = []string{"vless", "snell"}
	expect(t, f.req("PUT", "admin/plans", p, &f.admin, true), 409)
	p.Protocols = []string{"vless"}
	expect(t, f.req("PUT", "admin/plans", p, &f.admin, true), 200)
	p.Version++
	// A stale protocol in stored legacy data is never advertised as available.
	f.a.DB.Exec(ctx, "UPDATE plans SET protocols='[\"vless\",\"snell\"]' WHERE id=$1", id)
	c := f.req("GET", "catalog", nil, nil, false)
	expect(t, c, 200)
	got := object(t, c)["plans"].([]any)[0].(map[string]any)["protocols"].([]any)
	if len(got) != 1 || got[0] != "vless" {
		t.Fatal(c.Body.String())
	}
	body := map[string]any{"id": id, "version": p.Version, "confirmed": true}
	expect(t, f.req("DELETE", "admin/plans", body, &f.customer, true), 403)
	expect(t, f.req("DELETE", "admin/plans", body, &f.admin, false), 403)
	expect(t, f.req("DELETE", "admin/plans", map[string]any{"id": id, "version": p.Version}, &f.admin, true), 400)
	expect(t, f.req("DELETE", "admin/plans", body, &f.admin, true), 200)
	expect(t, f.req("DELETE", "admin/plans", body, &f.admin, true), 409)
	c = f.req("GET", "catalog", nil, nil, false)
	expect(t, c, 200)
	if len(object(t, c)["plans"].([]any)) != 0 {
		t.Fatal(c.Body.String())
	}
	expect(t, f.order(id, ID(), &f.customer, ""), 400)
	c = f.req("GET", "admin/plans?deleted=1", nil, &f.admin, true)
	expect(t, c, 200)
	if !strings.Contains(c.Body.String(), id) {
		t.Fatal(c.Body.String())
	}
	body["version"] = p.Version + 1
	expect(t, f.req("POST", "admin/plans/restore", body, &f.admin, true), 200)
	var enabled bool
	f.a.DB.QueryRow(ctx, "SELECT enabled FROM plans WHERE id=$1", id).Scan(&enabled)
	if enabled {
		t.Fatal("restore must not reopen sales")
	}
}

func TestInactiveServiceVisibilityPreservesOwnershipAndLedger(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	root := paidService(t, f)
	path := "me/services/" + root
	body := map[string]bool{"confirmed": true}
	expect(t, f.req("DELETE", path, body, &f.other, true), 404)
	expect(t, f.req("DELETE", path, body, &f.customer, false), 403)
	expect(t, f.req("DELETE", path, body, &f.customer, true), 409)
	f.missing.Store(true)
	expect(t, f.req("DELETE", path, body, &f.customer, true), 200)
	// Refresh may update health, but never unhides records.
	c := f.req("GET", "me/services", nil, &f.customer, true)
	expect(t, c, 200)
	if object(t, c)["total"] != float64(0) {
		t.Fatal(c.Body.String())
	}
	c = f.req("GET", "me/services?hidden=1", nil, &f.customer, true)
	expect(t, c, 200)
	if object(t, c)["total"] != float64(1) {
		t.Fatal(c.Body.String())
	}
	expect(t, f.req("GET", "me/subscription/share?service="+root, nil, &f.customer, true), 404)
	expect(t, f.req("POST", path+"/restore", body, &f.customer, true), 200)
	var n int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM orders WHERE id=$1 AND state='paid'", root).Scan(&n)
	if n != 1 {
		t.Fatal("visibility changed payment ledger")
	}
	c = f.req("GET", "me/services", nil, &f.customer, true)
	expect(t, c, 200)
	if object(t, c)["total"] != float64(1) {
		t.Fatal(c.Body.String())
	}
}

func TestRenewalGraceBoundaryAndPaymentDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		age time.Duration
		ok  bool
	}{{-time.Hour, true}, {72*time.Hour - time.Nanosecond, true}, {72 * time.Hour, false}, {73 * time.Hour, false}} {
		_, e := renewalWindow(map[string]any{"state": "expired", "expires_at": now.Add(-tc.age).Format(time.RFC3339Nano)}, now)
		if (e == nil) != tc.ok {
			t.Fatalf("age %v: %v", tc.age, e)
		}
	}
	f := setup(t)
	ctx := context.Background()
	root := paidService(t, f)
	enableRedpacket(t, f)
	var plan string
	f.a.DB.QueryRow(ctx, "SELECT plan_id FROM service_accounts WHERE order_id=$1", root).Scan(&plan)
	expiry := now.Add(-72*time.Hour - time.Second)
	f.expiry.Store(&expiry)
	buy := Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}
	opts := f.req("GET", "me/renewal?service="+root, nil, &f.customer, true)
	expect(t, opts, 200)
	if object(t, opts)["available"] != false {
		t.Fatal(opts.Body.String())
	}
	expect(t, f.req("POST", "checkout/quote", buy, &f.customer, true), 409)
	expect(t, f.req("POST", "orders", buy, &f.customer, true), 409)
	expiry = now.Add(-71 * time.Hour)
	f.expiry.Store(&expiry)
	created := f.req("POST", "orders", buy, &f.customer, true)
	expect(t, created, 201)
	order := object(t, created)["id"].(string)
	f.a.DB.Exec(ctx, "UPDATE orders SET renewal_deadline_at=now()-interval '1 second' WHERE id=$1", order)
	expect(t, f.req("POST", "orders/"+order+"/payment", map[string]any{"method_id": "alipay_redpacket"}, &f.customer, true), 409)
	f.a.DB.Exec(ctx, "UPDATE orders SET state='paid',paid_at=now() WHERE id=$1", order)
	if e := f.a.fulfill(ctx, order); e == nil {
		t.Fatal("late paid renewal fulfilled")
	}
	if f.renewals.Load() != 0 {
		t.Fatal("late payment reached core")
	}
}

func TestRenewalReservesSharedStockUnderConcurrency(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	root := paidService(t, f)
	var plan string
	f.a.DB.QueryRow(ctx, "SELECT plan_id FROM service_accounts WHERE order_id=$1", root).Scan(&plan)
	f.a.DB.Exec(ctx, "UPDATE plans SET stock=2 WHERE id=$1", plan)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		codes <- f.req("POST", "orders", Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}, &f.customer, true).Code
	}()
	go func() { defer wg.Done(); codes <- f.order(plan, ID(), &f.other, "").Code }()
	wg.Wait()
	close(codes)
	created, denied := 0, 0
	for c := range codes {
		if c == 201 {
			created++
		} else if c == 409 {
			denied++
		} else {
			t.Fatalf("unexpected %d", c)
		}
	}
	if created != 1 || denied != 1 {
		t.Fatal("sales oversold", created, denied)
	}
	var id, owner string
	f.a.DB.QueryRow(ctx, "SELECT id,customer_id FROM orders WHERE plan_id=$1 AND state='pending'", plan).Scan(&id, &owner)
	customer := &f.customer
	if owner == f.other.ID {
		customer = &f.other
	}
	expect(t, f.req("POST", "orders/"+id+"/cancel", map[string]any{}, customer, true), 202)
	expect(t, f.req("POST", "orders", Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}, &f.customer, true), 201)
}

func TestRenewalReviewUsesActualPaymentTimeAndPendingBlocksHide(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	root := paidService(t, f)
	enableRedpacket(t, f)
	var plan string
	f.a.DB.QueryRow(ctx, "SELECT plan_id FROM service_accounts WHERE order_id=$1", root).Scan(&plan)
	created := f.req("POST", "orders", Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}, &f.customer, true)
	expect(t, created, 201)
	id := object(t, created)["id"].(string)
	expiry := time.Now().Add(-time.Hour)
	f.expiry.Store(&expiry)
	expect(t, f.req("DELETE", "me/services/"+root, map[string]bool{"confirmed": true}, &f.customer, true), 409)
	p := f.req("POST", "orders/"+id+"/payment", map[string]any{"method_id": "alipay_redpacket"}, &f.customer, true)
	expect(t, p, 200)
	payment := object(t, p)["attempt"].(map[string]any)["id"].(string)
	expect(t, f.req("POST", "orders/"+id+"/receipt", map[string]any{"version": 1, "redpacket_password": "TEST-RENEWAL-ONLY"}, &f.customer, true), 200)
	deadline := time.Now().UTC().Add(-time.Second)
	f.a.DB.Exec(ctx, "UPDATE orders SET renewal_deadline_at=$2 WHERE id=$1", id, deadline)
	review := map[string]any{"version": 2, "decision": "approve", "confirmed": true, "received_amount": 1000, "received_at": deadline.Add(time.Second)}
	expect(t, f.req("PUT", "admin/manual-payments/"+payment, review, &f.admin, true), 409)
	review["received_at"] = deadline.Add(-time.Second)
	expect(t, f.req("PUT", "admin/manual-payments/"+payment, review, &f.admin, true), 200)
	// A queued order remains fulfillable after the deadline if paid in time.
	if e := f.a.fulfill(ctx, id); e != nil {
		t.Fatal(e)
	}
	if f.renewals.Load() != 1 {
		t.Fatal("on-time payment did not extend")
	}
}
