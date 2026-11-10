package portal

import (
	"context"
	"testing"
	"time"
)

func recurringFixture(t *testing.T, f *fixture, mode string) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	id, plan := ID(), f.plan()
	var until any
	if mode == "until" {
		until = f.a.now().Add(time.Hour)
	}
	_, e := f.a.DB.Exec(ctx, `INSERT INTO coupons(id,code,name,kind,amount,starts_at,ends_at,max_uses,per_customer,new_only,recurrence,recurring_until) VALUES($1,'CYCLE10','循环测试','percent',1000,now()-interval '1 hour',now()+interval '1 day',1,1,true,$2,$3)`, id, mode, until)
	if e != nil {
		t.Fatal(e)
	}
	w := f.order(plan, ID(), &f.customer, "CYCLE10")
	expect(t, w, 201)
	root := object(t, w)["id"].(string)
	payRecurring(t, f, root)
	return id, plan, root
}
func payRecurring(t *testing.T, f *fixture, id string) {
	t.Helper()
	if _, e := f.a.DB.Exec(context.Background(), "UPDATE orders SET state='paid',paid_at=now() WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	if e := f.a.fulfill(context.Background(), id); e != nil {
		t.Fatal(e)
	}
}
func TestRecurringCouponBoundToExactPaidSubscriptionAndCountsOnce(t *testing.T) {
	f := setup(t)
	coupon, plan, root := recurringFixture(t, f, "forever")
	ctx := context.Background()
	// Claim window and total/person quota are exhausted. The bound service keeps its benefit.
	f.a.DB.Exec(ctx, "UPDATE coupons SET ends_at=now()-interval '1 second' WHERE id=$1", coupon)
	f.a.DB.Exec(ctx, `UPDATE plans SET prices='[{"id":"MONTH","months":1,"amount":2000,"enabled":true}]' WHERE id=$1`, plan)
	for i := 0; i < 2; i++ {
		body := Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}
		w := f.req("POST", "checkout/quote", body, &f.customer, true)
		expect(t, w, 200)
		q := object(t, w)
		if q["payable"] != float64(1800) || q["coupon_reused"] != true || q["coupon_automatic"] != true {
			t.Fatal(q)
		}
		expected := int64(1800)
		body.ExpectedPayable = &expected
		w = f.req("POST", "orders", body, &f.customer, true)
		expect(t, w, 201)
		renew := object(t, w)["id"].(string)
		expect(t, f.req("POST", "orders", body, &f.customer, true), 200)
		payRecurring(t, f, renew)
	}
	var claims, reuses int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FILTER(WHERE NOT coupon_reused),count(*) FILTER(WHERE coupon_reused) FROM orders WHERE coupon_id=$1", coupon).Scan(&claims, &reuses)
	if claims != 1 || reuses != 2 {
		t.Fatal(claims, reuses)
	}
	w := f.req("GET", "admin/coupons/"+coupon+"/usage", nil, &f.admin, true)
	expect(t, w, 200)
	summary := object(t, w)["summary"].(map[string]any)
	if summary["used_uses"] != float64(1) || summary["recurring_uses"] != float64(2) || summary["remaining_uses"] != float64(0) {
		t.Fatal(summary)
	}
	// Same account, same plan, a different subscription must not inherit it.
	w = f.order(plan, ID(), &f.customer, "")
	expect(t, w, 201)
	other := object(t, w)["id"].(string)
	payRecurring(t, f, other)
	w = f.req("POST", "checkout/quote", Buy{PlanID: plan, PriceID: "MONTH", RenewalOf: other}, &f.customer, true)
	expect(t, w, 200)
	if q := object(t, w); q["payable"] != float64(2000) || q["coupon_code"] != "" {
		t.Fatal(q)
	}
	expect(t, f.req("POST", "checkout/quote", Buy{PlanID: plan, PriceID: "MONTH", RenewalOf: root}, &f.other, true), 404)
	w = f.req("GET", "me/renewal?service="+root, nil, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["recurring_coupon"].(map[string]any)["active"] != true {
		t.Fatal(w.Body.String())
	}
}
func TestRecurringCouponStopsOnExpiryDisableAndScopeChangeAndProtectsQuote(t *testing.T) {
	f := setup(t)
	coupon, plan, root := recurringFixture(t, f, "until")
	ctx := context.Background()
	body := Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}
	expect(t, f.req("POST", "checkout/quote", body, &f.customer, true), 200)
	f.a.DB.Exec(ctx, "UPDATE coupons SET recurring_until=now()-interval '1 second' WHERE id=$1", coupon)
	expected := int64(900)
	body.ExpectedPayable = &expected
	expect(t, f.req("POST", "orders", body, &f.customer, true), 409)
	w := f.req("POST", "checkout/quote", body, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["payable"] != float64(1000) {
		t.Fatal(w.Body.String())
	}
	body.Coupon = "CYCLE10"
	expect(t, f.req("POST", "checkout/quote", body, &f.customer, true), 400)
	body.Coupon = ""
	f.a.DB.Exec(ctx, "UPDATE coupons SET recurring_until=now()+interval '1 day',enabled=false WHERE id=$1", coupon)
	w = f.req("POST", "checkout/quote", body, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["payable"] != float64(1000) {
		t.Fatal(w.Body.String())
	}
	f.a.DB.Exec(ctx, `UPDATE coupons SET enabled=true,plan_ids=$2 WHERE id=$1`, coupon, raw([]string{ID()}))
	w = f.req("POST", "checkout/quote", body, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["payable"] != float64(1000) || object(t, w)["coupon_note"] == "" {
		t.Fatal(w.Body.String())
	}
	body.Coupon = "CYCLE10"
	expect(t, f.req("POST", "checkout/quote", body, &f.customer, true), 400)
}
func TestOrdinaryCouponDoesNotRecurAndExplicitOverridePreservesBinding(t *testing.T) {
	f := setup(t)
	coupon, plan, root := recurringFixture(t, f, "none")
	ctx := context.Background()
	body := Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}
	w := f.req("POST", "checkout/quote", body, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["payable"] != float64(1000) {
		t.Fatal(w.Body.String())
	}
	// Converting an existing paid coupon deliberately enables recurring renewals.
	f.a.DB.Exec(ctx, "UPDATE coupons SET recurrence='forever' WHERE id=$1", coupon)
	f.a.DB.Exec(ctx, `INSERT INTO coupons(id,code,name,kind,amount,starts_at,ends_at,per_customer) VALUES($1,'OTHER20','单次','percent',2000,now()-interval '1 hour',now()+interval '1 day',5)`, ID())
	body.Coupon = "OTHER20"
	w = f.req("POST", "orders", body, &f.customer, true)
	expect(t, w, 201)
	renew := object(t, w)["id"].(string)
	payRecurring(t, f, renew)
	body.Coupon = ""
	w = f.req("POST", "checkout/quote", body, &f.customer, true)
	expect(t, w, 200)
	if q := object(t, w); q["payable"] != float64(900) || q["coupon_code"] != "CYCLE10" {
		t.Fatal(q)
	}
	// Changing the original coupon to ordinary stops its future auto benefit.
	f.a.DB.Exec(ctx, "UPDATE coupons SET recurrence='none' WHERE id=$1", coupon)
	w = f.req("POST", "checkout/quote", body, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["payable"] != float64(1000) {
		t.Fatal(w.Body.String())
	}
}
func TestUnpaidRecurringOverrideCannotBindAndAdminValidatesModes(t *testing.T) {
	f := setup(t)
	_, plan, root := recurringFixture(t, f, "forever")
	ctx := context.Background()
	second := ID()
	f.a.DB.Exec(ctx, `INSERT INTO coupons(id,code,name,kind,amount,starts_at,ends_at,per_customer,recurrence) VALUES($1,'NEW20','新循环','percent',2000,now()-interval '1 hour',now()+interval '1 day',5,'forever')`, second)
	body := Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root, Coupon: "NEW20"}
	w := f.req("POST", "orders", body, &f.customer, true)
	expect(t, w, 201)
	pending := object(t, w)["id"].(string)
	body.Coupon = ""
	w = f.req("POST", "checkout/quote", body, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["coupon_code"] != "CYCLE10" {
		t.Fatal(w.Body.String())
	}
	payRecurring(t, f, pending)
	w = f.req("POST", "checkout/quote", body, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["coupon_code"] != "NEW20" {
		t.Fatal(w.Body.String())
	}
	c := Coupon{Code: "ADMIN20", Name: "管理", Kind: "percent", Amount: 2000, Enabled: true, PerCustomer: 1, Starts: f.a.now().Add(-time.Hour), Ends: f.a.now().Add(time.Hour), Recurrence: "until"}
	expect(t, f.req("POST", "admin/coupons", c, &f.admin, true), 400)
	until := f.a.now().Add(time.Hour)
	c.RecurringUntil = &until
	expect(t, f.req("POST", "admin/coupons", c, &f.admin, true), 200)
	c.Code = "ADMIN30"
	c.Recurrence = "bogus"
	expect(t, f.req("POST", "admin/coupons", c, &f.admin, true), 400)
	c.Code = "LEGACY20"
	c.Recurrence = ""
	expect(t, f.req("POST", "admin/coupons", c, &f.admin, true), 200)
	var mode string
	var expiry *time.Time
	if e := f.a.DB.QueryRow(ctx, "SELECT recurrence,recurring_until FROM coupons WHERE code='LEGACY20'").Scan(&mode, &expiry); e != nil || mode != "none" || expiry != nil {
		t.Fatal(mode, expiry, e)
	}
}
