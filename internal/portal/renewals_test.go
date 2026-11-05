package portal

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestStockAndRenewalKeepOneServiceAndPreserveOrderIdempotency(t *testing.T) {
	f := setup(t)
	root := paidService(t, f)
	ctx := context.Background()
	var plan, user, sub string
	if e := f.a.DB.QueryRow(ctx, "SELECT plan_id,control_user_id,subscription_id FROM service_accounts WHERE order_id=$1", root).Scan(&plan, &user, &sub); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.DB.Exec(ctx, "UPDATE plans SET stock=1,allow_new=true,allow_renew=true WHERE id=$1", plan); e != nil {
		t.Fatal(e)
	}
	catalog := f.req("GET", "catalog", nil, nil, false)
	expect(t, catalog, 200)
	plans := object(t, catalog)["plans"].([]any)
	p := plans[0].(map[string]any)
	if p["remaining_stock"] != float64(0) || p["sold_out"] != true {
		t.Fatal(catalog.Body.String())
	}
	expect(t, f.order(plan, ID(), &f.other, ""), 409)
	expect(t, f.req("GET", "me/renewal?service="+root, nil, &f.other, true), 404)
	expect(t, f.req("GET", "me/renewal?service="+root, nil, &f.customer, true), 200)
	expect(t, f.req("POST", "checkout/quote", Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}, &f.customer, true), 200)
	checks := f.checks.Load()
	f.ready.Store(false)
	body := map[string]any{"plan_id": plan, "price_id": "MONTH", "request_key": ID(), "renewal_of": root}
	expect(t, f.req("POST", "checkout/quote", body, &f.customer, true), 200)
	w := f.req("POST", "orders", body, &f.customer, true)
	expect(t, w, 201)
	renew := object(t, w)["id"].(string)
	expect(t, f.req("POST", "orders", body, &f.customer, true), 200)
	different := map[string]any{"plan_id": plan, "price_id": "MONTH", "request_key": ID(), "renewal_of": root}
	expect(t, f.req("POST", "orders", different, &f.customer, true), 409)
	// Unpaid renewal cannot extend an existing subscription.
	before := f.renewals.Load()
	if e := f.a.fulfill(ctx, renew); e != nil {
		t.Fatal(e)
	}
	if f.renewals.Load() != before {
		t.Fatal("unpaid renewal was granted")
	}
	if _, e := f.a.DB.Exec(ctx, "UPDATE orders SET state='paid',paid_at=now() WHERE id=$1", renew); e != nil {
		t.Fatal(e)
	}
	if e := f.a.fulfill(ctx, renew); e != nil {
		t.Fatal(e)
	}
	if e := f.a.fulfill(ctx, renew); e != nil {
		t.Fatal(e)
	}
	if f.renewals.Load() != before+1 || f.grants.Load() != 1 || f.checks.Load() != checks {
		t.Fatal("renewal invoked a new grant or repeated operation", f.renewals.Load(), f.grants.Load())
	}
	var count int
	var finalUser, finalSub, last string
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM service_accounts WHERE customer_id=$1", f.customer.ID).Scan(&count)
	f.a.DB.QueryRow(ctx, "SELECT control_user_id,subscription_id,last_order_id FROM service_accounts WHERE order_id=$1", root).Scan(&finalUser, &finalSub, &last)
	if count != 1 || finalUser != user || finalSub != sub || last != renew {
		t.Fatal("renewal changed mapping", count, finalUser, finalSub, last)
	}
	w = f.req("GET", "orders", nil, &f.customer, true)
	expect(t, w, 200)
	var rows []map[string]any
	json.Unmarshal(w.Body.Bytes(), &rows)
	for _, r := range rows {
		if r["id"] == renew && (r["service_id"] != root || r["has_subscription"] != true) {
			t.Fatal(w.Body.String())
		}
	}
	catalog = f.req("GET", "catalog", nil, nil, false)
	expect(t, catalog, 200)
	p = object(t, catalog)["plans"].([]any)[0].(map[string]any)
	if p["remaining_stock"] != float64(0) {
		t.Fatal("renewal incorrectly changed effective seat count", p)
	}
	if _, e := f.a.DB.Exec(ctx, "UPDATE plans SET stock=3,allow_new=false WHERE id=$1", plan); e != nil {
		t.Fatal(e)
	}
	expect(t, f.req("POST", "orders", different, &f.customer, true), 201)
	expect(t, f.order(plan, ID(), &f.customer, ""), 400)
}

func TestPendingOrderReservesStockAndCancellationReturnsIt(t *testing.T) {
	f := setup(t)
	plan := f.plan()
	ctx := context.Background()
	f.a.DB.Exec(ctx, "UPDATE plans SET stock=1 WHERE id=$1", plan)
	w := f.order(plan, ID(), &f.customer, "")
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	expect(t, f.order(plan, ID(), &f.other, ""), 409)
	w = f.req("GET", "catalog", nil, nil, false)
	expect(t, w, 200)
	if object(t, w)["plans"].([]any)[0].(map[string]any)["remaining_stock"] != float64(0) {
		t.Fatal(w.Body.String())
	}
	expect(t, f.req("POST", "orders/"+order+"/cancel", map[string]any{}, &f.customer, true), 202)
	w = f.req("GET", "catalog", nil, nil, false)
	expect(t, w, 200)
	if object(t, w)["plans"].([]any)[0].(map[string]any)["remaining_stock"] != float64(1) {
		t.Fatal(w.Body.String())
	}
	expect(t, f.order(plan, ID(), &f.other, ""), 201)
}

func TestRenewalManualReviewQueuesExtensionOfOriginalService(t *testing.T) {
	f := setup(t)
	root := paidService(t, f)
	ctx := context.Background()
	enableRedpacket(t, f)
	var plan string
	f.a.DB.QueryRow(ctx, "SELECT plan_id FROM service_accounts WHERE order_id=$1", root).Scan(&plan)
	w := f.req("POST", "orders", Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}, &f.customer, true)
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	w = f.req("POST", "orders/"+order+"/payment", map[string]any{"method_id": "alipay_redpacket"}, &f.customer, true)
	expect(t, w, 200)
	payment := object(t, w)["attempt"].(map[string]any)["id"].(string)
	expect(t, f.req("POST", "orders/"+order+"/receipt", map[string]any{"version": 1, "redpacket_password": "仅测试续费口令8273"}, &f.customer, true), 200)
	if f.renewals.Load() != 0 {
		t.Fatal("submission extended before approval")
	}
	review := map[string]any{"decision": "approve", "version": 2, "received_amount": 1000, "received_at": time.Now().UTC(), "confirmed": true}
	expect(t, f.req("PUT", "admin/manual-payments/"+payment, review, &f.admin, true), 200)
	var state string
	f.a.DB.QueryRow(ctx, "SELECT fulfillment FROM orders WHERE id=$1", order).Scan(&state)
	if state != "queued" {
		t.Fatal("approved renewal not queued", state)
	}
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, "SELECT fulfillment FROM orders WHERE id=$1", order).Scan(&state)
	if state != "active" || f.renewals.Load() != 1 || f.grants.Load() != 1 {
		t.Fatal("approval failed to extend original", state, f.renewals.Load(), f.grants.Load())
	}
}
