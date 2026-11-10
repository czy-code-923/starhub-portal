package portal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCouponRemainingUsesAndPagedHistoryMatchCheckout(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := ID()
	_, e := f.a.DB.Exec(ctx, `INSERT INTO coupons(id,code,name,kind,amount,starts_at,ends_at,max_uses,per_customer) VALUES($1,'HISTORY10','History','percent',1000,now()-interval '1 hour',now()+interval '1 day',40,40)`, id)
	if e != nil {
		t.Fatal(e)
	}
	w := f.order(f.plan(), ID(), &f.customer, "HISTORY10")
	expect(t, w, 201)
	root := object(t, w)["id"].(string)
	_, e = f.a.DB.Exec(ctx, `INSERT INTO orders(id,customer_id,plan_id,price_id,snapshot,base_amount,discount_amount,payable,coupon_id,state,expires_at,request_key,request_hash,created_at) SELECT md5(o.id||g),o.customer_id,o.plan_id,o.price_id,o.snapshot,o.base_amount,o.discount_amount,o.payable,o.coupon_id,CASE g%6 WHEN 0 THEN 'cancelled' WHEN 1 THEN 'pending' WHEN 2 THEN 'paid' WHEN 3 THEN 'refund_pending' WHEN 4 THEN 'refunded' ELSE 'late_paid' END,o.expires_at,md5(o.id||g),'fixture',o.created_at-g*interval '1 second' FROM orders o CROSS JOIN generate_series(1,29)g WHERE o.id=$1`, root)
	if e != nil {
		t.Fatal(e)
	}
	path := "admin/coupons/" + id + "/usage"
	w = f.req("GET", path, nil, &f.admin, true)
	expect(t, w, 200)
	data := object(t, w)
	summary := data["summary"].(map[string]any)
	if data["total"] != float64(30) || summary["used_uses"] != float64(21) || summary["reserved_uses"] != float64(6) || summary["remaining_uses"] != float64(19) {
		t.Fatal(data)
	}
	items := data["items"].([]any)
	if len(items) != 25 {
		t.Fatal(len(items))
	}
	second := object(t, f.req("GET", path+"?offset=25", nil, &f.admin, true))["items"].([]any)
	if len(second) != 5 {
		t.Fatal(len(second))
	}
	seen := map[string]bool{}
	for _, item := range append(items, second...) {
		r := item.(map[string]any)
		if seen[r["id"].(string)] {
			t.Fatal("pagination repeated an order")
		}
		seen[r["id"].(string)] = true
	}
	for _, secret := range []string{"snapshot", "password_hash", "request_hash", "sealed_code"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("history leaked private fields", secret)
		}
	}
	w = f.req("GET", "admin/coupons", nil, &f.admin, true)
	expect(t, w, 200)
	var list []map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &list); e != nil {
		t.Fatal(e)
	}
	if list[0]["remaining_uses"] != summary["remaining_uses"] {
		t.Fatal("list and history disagree")
	}
	expect(t, f.req("POST", "orders/"+root+"/cancel", map[string]any{}, &f.customer, true), 202)
	if object(t, f.req("GET", path, nil, &f.admin, true))["summary"].(map[string]any)["remaining_uses"] != float64(20) {
		t.Fatal("cancelled reservation not released")
	}
	f.a.DB.Exec(ctx, "UPDATE coupons SET max_uses=0 WHERE id=$1", id)
	if object(t, f.req("GET", path, nil, &f.admin, true))["summary"].(map[string]any)["remaining_uses"] != nil {
		t.Fatal("unlimited coupon shown as zero")
	}
	expect(t, f.req("GET", path+"?offset=-1", nil, &f.admin, true), 400)
	expect(t, f.req("GET", "admin/coupons/"+ID()+"/usage", nil, &f.admin, true), 404)
	expect(t, f.req("GET", path, nil, &f.customer, true), 403)
	expect(t, f.req("GET", path, nil, nil, true), 401)
	scoped := f.user("coupon-reader@example.test", "admin")
	f.a.DB.Exec(ctx, "UPDATE customers SET admin_scopes=$2 WHERE id=$1", scoped.ID, raw([]string{"coupons"}))
	expect(t, f.req("GET", path, nil, &scoped, true), 200)
	expect(t, f.req("GET", "admin/invitations/"+ID()+"/usage", nil, &scoped, true), 403)
}

func TestInvitationHistoryDistinguishesReservationVerificationAndRelease(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := ID()
	_, e := f.a.DB.Exec(ctx, `INSERT INTO invitations(id,code_hash,prefix,ends_at,max_uses,claimed) VALUES($1,$2,'fixture',now()+interval '1 day',4,2)`, id, hash(id))
	if e != nil {
		t.Fatal(e)
	}
	f.a.DB.Exec(ctx, `INSERT INTO invitation_claims(customer_id,invitation_id,finalized,expires_at) VALUES($1,$3,true,now()+interval '1 day'),($2,$3,false,now()+interval '1 day')`, f.customer.ID, f.other.ID, id)
	path := "admin/invitations/" + id + "/usage"
	w := f.req("GET", path, nil, &f.admin, true)
	expect(t, w, 200)
	data := object(t, w)
	if data["total"] != float64(2) || data["summary"].(map[string]any)["verified_uses"] != float64(1) || data["summary"].(map[string]any)["remaining_uses"] != float64(2) {
		t.Fatal(data)
	}
	f.a.DB.Exec(ctx, "UPDATE invitation_claims SET released=true WHERE customer_id=$1", f.other.ID)
	f.a.DB.Exec(ctx, "UPDATE invitations SET claimed=1 WHERE id=$1", id)
	f.a.DB.Exec(ctx, "UPDATE customers SET deleted_at=now() WHERE id=$1", f.customer.ID)
	w = f.req("GET", path, nil, &f.admin, true)
	expect(t, w, 200)
	data = object(t, w)
	if data["summary"].(map[string]any)["remaining_uses"] != float64(3) || data["summary"].(map[string]any)["released_uses"] != float64(1) {
		t.Fatal(data)
	}
	if len(data["items"].([]any)) != 2 {
		t.Fatal("deleted customers must not erase usage history")
	}
	w = f.req("GET", "admin/invitations", nil, &f.admin, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"remaining_uses":3`) {
		t.Fatal(w.Body.String())
	}
}

func TestHelpDownloadLinksPreserveEditedContentAndCanLoadNewDefaults(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	legacy := map[string]any{"title": "自定义标题", "intro": "自定义介绍", "apps": []map[string]any{{"id": "one", "name": "FlClash", "format": "Mihomo", "url": "https://example.test/help", "platforms": []string{"Android"}, "steps": []string{"自定义步骤"}}}, "faqs": []any{}}
	_, e := f.a.DB.Exec(ctx, "INSERT INTO portal_content(name,document,updated_by) VALUES('client-help',$1,$2)", raw(legacy), f.admin.ID)
	if e != nil {
		t.Fatal(e)
	}
	w := f.req("GET", "client-help", nil, &f.customer, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "自定义步骤") || !strings.Contains(w.Body.String(), "FlClash/releases/latest") {
		t.Fatal(w.Body.String())
	}
	d := object(t, w)
	var doc helpDocument
	json.Unmarshal(raw(d["document"]), &doc)
	doc.Apps[0].DownloadURL = "javascript:alert(1)"
	expect(t, f.req("PUT", "admin/client-help", map[string]any{"version": 1, "document": doc}, &f.admin, true), 400)
	doc.Apps[0].DownloadURL = ""
	expect(t, f.req("PUT", "admin/client-help", map[string]any{"version": 1, "document": doc}, &f.admin, true), 200)
	w = f.req("GET", "client-help", nil, &f.customer, true)
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), "FlClash/releases/latest") {
		t.Fatal("explicitly removed download link restored")
	}
	w = f.req("GET", "client-help?defaults=1", nil, &f.admin, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "下载") || object(t, w)["version"] != float64(2) {
		t.Fatal("defaults must preserve current edit version")
	}
}
