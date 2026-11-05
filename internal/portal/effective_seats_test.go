package portal

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSeatReleaseOnExpiryAndDeletionNotOnOrdinaryRenewal(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	root := paidService(t, f)
	var plan string
	f.a.DB.QueryRow(ctx, "SELECT plan_id FROM service_accounts WHERE order_id=$1", root).Scan(&plan)
	f.a.DB.Exec(ctx, "UPDATE plans SET stock=1 WHERE id=$1", plan)
	renew := Buy{PlanID: plan, PriceID: "MONTH", Key: ID(), RenewalOf: root}
	expect(t, f.order(plan, ID(), &f.other, ""), 409)
	expect(t, f.req("POST", "checkout/quote", renew, &f.customer, true), 200)
	// No new seat exists after a paid renewal of the same active identity.
	created := f.req("POST", "orders", renew, &f.customer, true)
	expect(t, created, 201)
	id := object(t, created)["id"].(string)
	f.a.DB.Exec(ctx, "UPDATE orders SET state='paid',paid_at=now() WHERE id=$1", id)
	if e := f.a.fulfill(ctx, id); e != nil {
		t.Fatal(e)
	}
	var n int
	f.a.DB.QueryRow(ctx, inventoryCountSQL, plan, f.a.now()).Scan(&n)
	if n != 1 {
		t.Fatal("renewal duplicated seat", n)
	}
	expired := time.Now().Add(-time.Hour)
	f.expiry.Store(&expired)
	if e := f.a.refreshServiceStates(ctx, true); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, inventoryCountSQL, plan, f.a.now()).Scan(&n)
	if n != 0 {
		t.Fatal("expiry did not release seat", n)
	}
	other := f.order(plan, ID(), &f.other, "")
	expect(t, other, 201)
	renew.Key = ID()
	expect(t, f.req("POST", "orders", renew, &f.customer, true), 409)
	expect(t, f.req("POST", "orders/"+object(t, other)["id"].(string)+"/cancel", map[string]any{}, &f.other, true), 202)
	f.missing.Store(true)
	if e := f.a.refreshServiceStates(ctx, true); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, inventoryCountSQL, plan, f.a.now()).Scan(&n)
	if n != 0 {
		t.Fatal("deletion did not release seat", n)
	}
	expect(t, f.order(plan, ID(), &f.other, ""), 201)
}

func TestPlanPolicyChangesReachPaidServicesWithoutChangingPurchase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	root := paidService(t, f)
	var p Plan
	var plan, before []byte
	f.a.DB.QueryRow(ctx, "SELECT to_jsonb(p),o.snapshot FROM plans p JOIN orders o ON o.plan_id=p.id WHERE o.id=$1", root).Scan(&plan, &before)
	json.Unmarshal(plan, &p)
	p.Protocols = []string{"hysteria2"}
	expect(t, f.req("PUT", "admin/plans", p, &f.admin, true), 200)
	w := f.req("GET", "me/subscription/check?service="+root+"&client=mihomo", nil, &f.customer, true)
	expect(t, w, 200)
	last := f.lastPolicy.Load()
	if last == nil {
		t.Fatal("no policy sent")
	}
	protocols := (*last)["protocols"].([]any)
	if len(protocols) != 1 || protocols[0] != "hysteria2" {
		t.Fatal(*last)
	}
	var applied int
	var after []byte
	f.a.DB.QueryRow(ctx, "SELECT s.policy_version,o.snapshot FROM service_accounts s JOIN orders o ON o.id=s.order_id WHERE s.order_id=$1", root).Scan(&applied, &after)
	if applied != p.Version+1 || string(before) != string(after) {
		t.Fatal("policy not applied or financial snapshot rewritten")
	}
	calls := f.policies.Load()
	expect(t, f.req("GET", "me/subscription/check?service="+root, nil, &f.customer, true), 200)
	if f.policies.Load() != calls {
		t.Fatal("unchanged subscription refresh repeatedly wrote policy")
	}
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	if f.policies.Load() != calls {
		t.Fatal("queued policy duplicated current revision")
	}
}
