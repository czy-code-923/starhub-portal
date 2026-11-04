package portal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixtureImage(t *testing.T, red uint8) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	img.Set(0, 0, color.RGBA{red, 20, 30, 255})
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()), b.Bytes()
}
func TestEmailRegistrationConfirmationAndProfile(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, e := f.a.DB.Exec(ctx, `UPDATE settings SET public=jsonb_set(public,'{registration_mode}','"email"')`)
	if e != nil {
		t.Fatal(e)
	}
	body := map[string]string{"email": "direct@example.test", "name": "Direct", "password": "direct-password-123", "password_confirm": "different-password"}
	expect(t, f.req("POST", "auth/register", body, nil, false), 400)
	body["password_confirm"] = body["password"]
	expect(t, f.req("POST", "auth/register", body, nil, false), 201)
	var payload []byte
	if e = f.a.DB.QueryRow(ctx, "SELECT payload FROM jobs WHERE kind='mail' ORDER BY created_at DESC LIMIT 1").Scan(&payload); e != nil {
		t.Fatal(e)
	}
	var mail map[string]string
	f.a.unseal(payload, &mail)
	expect(t, f.req("POST", "auth/verify", map[string]string{"token": mail["token"]}, nil, false), 200)
	expect(t, f.req("PUT", "me/profile", map[string]string{"name": "Changed", "email": "next@example.test", "current_password": "wrong"}, &f.customer, true), 403)
	w := f.req("PUT", "me/profile", map[string]string{"name": "Changed", "email": "next@example.test", "current_password": "test-only-password-47"}, &f.customer, true)
	expect(t, w, 200)
	var email string
	f.a.DB.QueryRow(ctx, "SELECT email FROM customers WHERE id=$1", f.customer.ID).Scan(&email)
	if email != f.customer.Email {
		t.Fatal("new email changed before verification")
	}
	if e = f.a.DB.QueryRow(ctx, "SELECT j.payload FROM jobs j JOIN email_tokens t ON t.customer_id=$1 AND t.purpose='change_email' WHERE j.kind='mail' ORDER BY j.created_at DESC LIMIT 1", f.customer.ID).Scan(&payload); e != nil {
		t.Fatal(e)
	}
	f.a.unseal(payload, &mail)
	expect(t, f.req("POST", "auth/verify-email", map[string]string{"token": mail["token"]}, nil, false), 200)
	expect(t, f.req("POST", "auth/verify-email", map[string]string{"token": mail["token"]}, nil, false), 400)
	var count int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE customer_id=$1", f.customer.ID).Scan(&count)
	if count != 0 {
		t.Fatal("email change did not revoke sessions")
	}
	f.a.DB.QueryRow(ctx, "SELECT email FROM customers WHERE id=$1", f.customer.ID).Scan(&email)
	if email != "next@example.test" {
		t.Fatal(email)
	}
}
func TestPasswordChangesConfirmCurrentAndRevoke(t *testing.T) {
	f := setup(t)
	body := map[string]string{"current_password": "test-only-password-47", "password": "replacement-password-123", "password_confirm": "mismatch"}
	expect(t, f.req("PUT", "me/password", body, &f.customer, true), 400)
	body["password_confirm"] = body["password"]
	body["current_password"] = "wrong"
	expect(t, f.req("PUT", "me/password", body, &f.customer, true), 403)
	body["current_password"] = "test-only-password-47"
	expect(t, f.req("PUT", "me/password", body, &f.customer, true), 200)
	var n int
	f.a.DB.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE customer_id=$1", f.customer.ID).Scan(&n)
	if n != 0 {
		t.Fatal("password change preserved sessions")
	}
	expect(t, f.req("POST", "auth/login", map[string]string{"email": f.customer.Email, "password": "test-only-password-47"}, nil, false), 401)
	expect(t, f.req("POST", "auth/login", map[string]string{"email": f.customer.Email, "password": body["password"]}, nil, false), 200)
}
func TestInviteRepeatedDisplayAndOwnerDelegation(t *testing.T) {
	f := setup(t)
	now := time.Now()
	b := map[string]any{"code": "DISPLAY-CODE-12345", "email": "", "enabled": true, "starts_at": now.Add(-time.Hour), "ends_at": now.Add(24 * time.Hour), "max_uses": 5}
	expect(t, f.req("POST", "admin/invitations", b, &f.admin, true), 200)
	for i := 0; i < 2; i++ {
		w := f.req("GET", "admin/invitations", nil, &f.admin, true)
		expect(t, w, 200)
		if !strings.Contains(w.Body.String(), "DISPLAY-CODE-12345") {
			t.Fatal("invitation not recoverable")
		}
	}
	expect(t, f.req("GET", "admin/invitations", nil, &f.customer, true), 403)
	subBody := map[string]any{"email": "helper@example.test", "name": "Support operator", "password": "subadmin-password-123", "password_confirm": "subadmin-password-123", "scopes": []string{"support"}, "enabled": true}
	w := f.req("POST", "admin/administrators", subBody, &f.admin, true)
	expect(t, w, 200)
	sub := actor{ID: object(t, w)["id"].(string), Role: "admin"}
	expect(t, f.req("GET", "admin/settings", nil, &sub, true), 403)
	expect(t, f.req("GET", "admin/orders", nil, &sub, true), 403)
	expect(t, f.req("GET", "admin/tickets", nil, &sub, true), 200)
	expect(t, f.req("POST", "admin/administrators", subBody, &sub, true), 403)
	expect(t, f.req("GET", "admin/payment-methods", nil, &sub, true), 403)
	ticket := f.req("POST", "tickets", map[string]string{"subject": "private", "body": "Support can read this"}, &f.customer, true)
	expect(t, ticket, 201)
	ticketID := object(t, ticket)["id"].(string)
	expect(t, f.req("GET", "tickets/"+ticketID, nil, &sub, true), 200)
	viewOnly := f.user("unscoped@example.test", "admin")
	expect(t, f.req("GET", "tickets/"+ticketID, nil, &viewOnly, true), 404)
	subBody["id"] = sub.ID
	subBody["version"] = 1
	subBody["enabled"] = false
	subBody["password"] = ""
	subBody["password_confirm"] = ""
	expect(t, f.req("PUT", "admin/administrators", subBody, &f.admin, true), 200)
	expect(t, f.req("GET", "admin/tickets", nil, &sub, true), 401)
	subBody["id"] = f.admin.ID
	subBody["email"] = f.admin.Email
	expect(t, f.req("PUT", "admin/administrators", subBody, &f.admin, true), 403)
}
func TestDemoPlansAreEditableIdempotentAndCannotCharge(t *testing.T) {
	f := setup(t)
	w := f.req("POST", "admin/demo-plans", map[string]string{}, &f.admin, true)
	expect(t, w, 200)
	if object(t, w)["created"] != float64(6) {
		t.Fatal(w.Body)
	}
	w = f.req("POST", "admin/demo-plans", map[string]string{}, &f.admin, true)
	expect(t, w, 200)
	if object(t, w)["created"] != float64(0) {
		t.Fatal("duplicated demo plans")
	}
	var plans []Plan
	w = f.req("GET", "admin/plans", nil, &f.admin, true)
	expect(t, w, 200)
	json.Unmarshal(w.Body.Bytes(), &plans)
	p := plans[0]
	p.Name = "Edited simulation"
	expect(t, f.req("PUT", "admin/plans", p, &f.admin, true), 200)
	expect(t, f.req("POST", "admin/demo-plans", map[string]string{}, &f.admin, true), 200)
	var name string
	f.a.DB.QueryRow(context.Background(), "SELECT name FROM plans WHERE id=$1", p.ID).Scan(&name)
	if name != p.Name {
		t.Fatal("seed overwrote owner edits")
	}
	expect(t, f.req("POST", "checkout/quote", Buy{PlanID: p.ID, PriceID: p.Prices[0].ID}, &f.customer, true), 409)
	p.Simulation = false
	p.Version = 2
	p.Groups = []string{}
	expect(t, f.req("PUT", "admin/plans", p, &f.admin, true), 400)
}
func setupManual(t *testing.T) (*fixture, string, []byte) {
	f := setup(t)
	data, raw := fixtureImage(t, 20)
	b := map[string]any{"id": "alipay_static", "version": 1, "name": "Fixture receiving code", "instructions": "Synthetic fixture, no actual receiver", "enabled": true, "image_data": data, "clear_image": false}
	expect(t, f.req("PUT", "admin/payment-methods", b, &f.admin, true), 200)
	plan := f.plan()
	return f, plan, raw
}
func TestManualReceiptReviewExpiryAndExactlyOnce(t *testing.T) {
	f, plan, qr := setupManual(t)
	ctx := context.Background()
	w := f.order(plan, ID(), &f.customer, "")
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	w = f.req("POST", "orders/"+order+"/payment", map[string]string{"method_id": "alipay_static"}, &f.customer, true)
	expect(t, w, 200)
	attempt := object(t, w)["attempt"].(map[string]any)
	id := attempt["id"].(string)
	expect(t, f.req("GET", "orders/"+order+"/payment-code", nil, &f.other, true), 404)
	img := f.req("GET", "orders/"+order+"/payment-code", nil, &f.customer, true)
	expect(t, img, 200)
	if !bytes.Equal(img.Body.Bytes(), qr) {
		t.Fatal("QR snapshot mismatch")
	}
	data, _ := fixtureImage(t, 100)
	expect(t, f.req("PUT", "admin/payment-methods", map[string]any{"id": "alipay_static", "version": 2, "name": "Changed receiver", "instructions": "Changed", "enabled": false, "image_data": data}, &f.admin, true), 200)
	img = f.req("GET", "orders/"+order+"/payment-code", nil, &f.customer, true)
	if !bytes.Equal(img.Body.Bytes(), qr) {
		t.Fatal("changing receiver changed existing order")
	}
	receipt := map[string]any{"version": 1, "reference": "user-claimed-ref", "note": "Paid fixture", "image_data": data}
	expect(t, f.req("POST", "orders/"+order+"/receipt", receipt, &f.customer, true), 200)
	var state string
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "pending" {
		t.Fatal("upload auto-paid order")
	}
	expect(t, f.req("POST", "orders/"+order+"/cancel", map[string]string{}, &f.customer, true), 409)
	expect(t, f.req("GET", "orders/"+order+"/receipt", nil, &f.other, true), 404)
	now := time.Now()
	f.a.Now = func() time.Time { return now.Add(20 * time.Minute) }
	if e := f.a.cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "pending" {
		t.Fatal("submitted proof lost review reservation")
	}
	review := map[string]any{"decision": "approve", "version": 2, "verified_reference": "VERIFIED-TRANSFER-001", "received_amount": 999, "received_at": now, "note": "Verified fixture", "confirmed": true}
	expect(t, f.req("PUT", "admin/manual-payments/"+id, review, &f.customer, true), 403)
	expect(t, f.req("PUT", "admin/manual-payments/"+id, review, &f.admin, true), 400)
	review["received_amount"] = 1000
	review["confirmed"] = false
	expect(t, f.req("PUT", "admin/manual-payments/"+id, review, &f.admin, true), 400)
	review["confirmed"] = true
	expect(t, f.req("PUT", "admin/manual-payments/"+id, review, &f.admin, true), 200)
	expect(t, f.req("PUT", "admin/manual-payments/"+id, review, &f.admin, true), 200)
	var jobs int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='fulfill'").Scan(&jobs)
	if jobs != 1 {
		t.Fatal("approval duplicated grant")
	}
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	var fulfillment string
	f.a.DB.QueryRow(ctx, "SELECT fulfillment FROM orders WHERE id=$1", order).Scan(&fulfillment)
	if fulfillment != "active" {
		t.Fatal(fulfillment)
	}
}
func TestManualReviewNeverAcceptsDuplicateTransfer(t *testing.T) {
	f, plan, _ := setupManual(t)
	ctx := context.Background()
	for i, u := range []*actor{&f.customer, &f.other} {
		w := f.order(plan, ID(), u, "")
		expect(t, w, 201)
		order := object(t, w)["id"].(string)
		w = f.req("POST", "orders/"+order+"/payment", map[string]string{"method_id": "alipay_static"}, u, true)
		expect(t, w, 200)
		id := object(t, w)["attempt"].(map[string]any)["id"].(string)
		expect(t, f.req("POST", "orders/"+order+"/receipt", map[string]any{"version": 1, "reference": "claimed-reference"}, u, true), 200)
		want := 200
		if i == 1 {
			want = 409
		}
		expect(t, f.req("PUT", "admin/manual-payments/"+id, map[string]any{"decision": "approve", "version": 2, "verified_reference": "ONE-ACTUAL-TRANSFER", "received_amount": 1000, "received_at": time.Now(), "confirmed": true}, &f.admin, true), want)
	}
	var paid int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM orders WHERE state='paid'").Scan(&paid)
	if paid != 1 {
		t.Fatal("one transfer paid multiple orders")
	}
}
func TestMediaRejectsActiveContentAndWrongTypes(t *testing.T) {
	for _, s := range []string{"https://example.test/qr.png", "data:image/svg+xml;base64,PHN2Zz4=", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not an image"))} {
		if _, _, e := parseImage(s); e == nil {
			t.Fatal("invalid image accepted")
		}
	}
	data, _ := fixtureImage(t, 1)
	if _, mime, e := parseImage(data); e != nil || mime != "image/png" {
		t.Fatal(mime, e)
	}
}

// Verify the upgrade elects exactly the pre-existing primary administrator.
func TestBootstrapHasPrimaryRole(t *testing.T) {
	f := setup(t)
	var owner bool
	f.a.DB.QueryRow(context.Background(), "SELECT is_owner FROM customers WHERE id=$1", f.admin.ID).Scan(&owner)
	if !owner {
		t.Fatal("primary administrator missing")
	}
	r := httptest.NewRequest(http.MethodPost, "http://portal.test/portal/v1/admin/administrators", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	expect(t, w, 401)
}
