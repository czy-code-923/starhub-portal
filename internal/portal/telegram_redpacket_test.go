package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const tgFixtureToken = "123456789:" + "test_only_0123456789_abcdefghijklmnop"

func configureTGTest(t *testing.T, f *fixture, principals []tgPrincipal, mode string) tgConfig {
	t.Helper()
	c := tgConfig{Enabled: true, Mode: mode, Token: tgFixtureToken, Principals: principals}
	b := map[string]any{"enabled": true, "mode": mode, "token": c.Token, "principals": principals, "version": 1}
	expect(t, f.req("PUT", "admin/telegram", b, &f.admin, true), 200)
	return c
}
func beginRedpacket(t *testing.T, f *fixture, plan string, u *actor) (string, string) {
	t.Helper()
	w := f.order(plan, ID(), u, "")
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	w = f.req("POST", "orders/"+order+"/payment", map[string]string{"method_id": "alipay_redpacket"}, u, true)
	expect(t, w, 200)
	return order, object(t, w)["attempt"].(map[string]any)["id"].(string)
}
func enableRedpacket(t *testing.T, f *fixture) {
	t.Helper()
	expect(t, f.req("PUT", "admin/payment-methods", map[string]any{"id": "alipay_redpacket", "version": 1, "name": "支付宝口令红包", "enabled": true, "instructions": "Isolated fixture"}, &f.admin, true), 200)
}
func TestRedpacketEncryptedOrderBindingAndLateClaimReview(t *testing.T) {
	f, plan, _ := setupManual(t)
	enableRedpacket(t, f)
	ctx := context.Background()
	order, id := beginRedpacket(t, f, plan, &f.customer)
	expect(t, f.req("GET", "orders/"+order+"/payment-code", nil, &f.customer, true), 404)
	body := map[string]any{"version": 1, "redpacket_password": "仅测试口令938271", "reference": "", "note": ""}
	expect(t, f.req("POST", "orders/"+order+"/receipt", body, &f.customer, true), 200)
	var sealed []byte
	var state string
	if e := f.a.DB.QueryRow(ctx, "SELECT redpacket_sealed FROM manual_payments WHERE id=$1", id).Scan(&sealed); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(sealed), body["redpacket_password"].(string)) {
		t.Fatal("plaintext persisted")
	}
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "pending" {
		t.Fatal("password auto-paid order")
	}
	for _, path := range []string{"orders/" + order + "/payment", "admin/manual-payments/" + id} {
		u := &f.admin
		if strings.HasPrefix(path, "orders/") {
			u = &f.customer
		}
		w := f.req("GET", path, nil, u, true)
		expect(t, w, 200)
		if strings.Contains(w.Body.String(), body["redpacket_password"].(string)) {
			t.Fatal("passcode leaked in list/status")
		}
	}
	expect(t, f.req("GET", "admin/manual-payments/"+id+"/redpacket", nil, &f.customer, true), 403)
	w := f.req("GET", "admin/manual-payments/"+id+"/redpacket", nil, &f.admin, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), body["redpacket_password"].(string)) {
		t.Fatal("authorized reveal missing")
	}
	other, _ := beginRedpacket(t, f, plan, &f.other)
	expect(t, f.req("POST", "orders/"+other+"/receipt", body, &f.other, true), 409)
	body["version"] = 2
	body["redpacket_password"] = ""
	body["note"] = "retain encrypted original"
	expect(t, f.req("POST", "orders/"+order+"/receipt", body, &f.customer, true), 200)
	now := time.Now().Add(20 * time.Minute)
	f.a.Now = func() time.Time { return now }
	expect(t, f.req("PUT", "admin/manual-payments/"+id, map[string]any{"version": 3, "decision": "approve", "verified_reference": "REDPACK-ACTUAL-CLAIM-001", "received_amount": 1000, "received_at": now, "confirmed": true}, &f.admin, true), 200)
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, "SELECT fulfillment FROM orders WHERE id=$1", order).Scan(&state)
	if state != "active" {
		t.Fatal(state)
	}
}
func TestTelegramSettingsOwnerOnlyAndSecretMask(t *testing.T) {
	f := setup(t)
	expect(t, f.req("GET", "admin/telegram", nil, &f.customer, true), 403)
	c := configureTGTest(t, f, []tgPrincipal{{UserID: 42, Scopes: []string{"payments"}, Notify: true}}, "notify_only")
	w := f.req("GET", "admin/telegram", nil, &f.admin, true)
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), c.Token) {
		t.Fatal("bot token leaked")
	}
	expect(t, f.req("PUT", "admin/telegram", map[string]any{"enabled": true, "mode": "polling", "principals": []tgPrincipal{{UserID: 42, Scopes: []string{"redpacket"}}}, "version": 2}, &f.admin, true), 400)
	expect(t, f.req("PUT", "admin/telegram", map[string]any{"enabled": false, "mode": "notify_only", "principals": c.Principals, "version": 2, "token": ""}, &f.admin, true), 200)
	saved, _, e := f.a.tgLoad(context.Background())
	if e != nil || saved.Token != c.Token {
		t.Fatal("blank replaced stored token", e)
	}
	expect(t, f.req("PUT", "admin/telegram", map[string]any{"enabled": true, "mode": "notify_only", "principals": c.Principals, "version": 3, "clear_token": true}, &f.admin, true), 200)
	saved, _, _ = f.a.tgLoad(context.Background())
	if saved.Enabled || saved.Token != "" {
		t.Fatal("clear token did not disable")
	}
}
func TestTelegramPrivateACLAndNoWebhookTakeover(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var messages []string
	var methods []string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		methods = append(methods, method)
		switch method {
		case "getMe":
			send(w, 200, map[string]any{"ok": true, "result": map[string]string{"username": "fixture_bot"}})
		case "getWebhookInfo":
			send(w, 200, map[string]any{"ok": true, "result": map[string]string{"url": "https://other.example.test/hook"}})
		case "sendMessage":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			messages = append(messages, b["text"].(string))
			send(w, 200, map[string]any{"ok": true, "result": map[string]any{}})
		default:
			t.Error("unexpected Telegram action", method)
			send(w, 400, map[string]any{"ok": false})
		}
	}))
	defer mock.Close()
	f.a.TestTelegramURL = mock.URL
	c := configureTGTest(t, f, []tgPrincipal{{UserID: 42, Scopes: []string{"overview"}}}, "polling")
	if _, e := f.a.tgProbe(ctx, c); e == nil {
		t.Fatal("existing webhook accepted")
	}
	for _, m := range methods {
		if m == "deleteWebhook" || m == "setWebhook" {
			t.Fatal("webhook taken over")
		}
	}
	for _, id := range []int64{1, 42} {
		chat := id
		if id == 42 {
			chat = -99
		}
		if e := f.a.tgCommand(ctx, c, id, chat, "/overview"); e != nil {
			t.Fatal(e)
		}
	}
	if len(messages) != 0 {
		t.Fatal("unapproved chat received data")
	}
	if e := f.a.tgCommand(ctx, c, 42, 42, "/overview"); e != nil {
		t.Fatal(e)
	}
	if len(messages) != 1 || !strings.Contains(messages[0], "星枢购买面板") {
		t.Fatal(messages)
	}
	f.a.DB.Exec(ctx, "UPDATE telegram_command_limits SET last_at=now()-interval '10 seconds'")
	if e := f.a.tgCommand(ctx, c, 42, 42, "/payments"); e != nil {
		t.Fatal(e)
	}
	if len(messages) != 1 {
		t.Fatal("ungranted payment command replied")
	}
}
func TestTelegramOutboxRetrySkipsAlreadyDeliveredRecipients(t *testing.T) {
	f, plan, _ := setupManual(t)
	ctx := context.Background()
	w := f.order(plan, ID(), &f.customer, "")
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	c := configureTGTest(t, f, []tgPrincipal{{UserID: 42, Scopes: []string{"payments"}, Notify: true}, {UserID: 43, Scopes: []string{"payments"}, Notify: true}}, "notify_only")
	tx, e := f.a.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = queueTelegram(ctx, tx, "test-event", order, "payment.submitted"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	counts := map[int64]int{}
	fail := true
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Error("notify-only started polling")
		}
		var b struct {
			ID   int64  `json:"chat_id"`
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		counts[b.ID]++
		if strings.Contains(b.Text, "红包口令：") {
			t.Error("passcode in ordinary notification")
		}
		if b.ID == 43 && fail {
			send(w, 403, map[string]any{"ok": false, "error_code": 403})
			return
		}
		send(w, 200, map[string]any{"ok": true, "result": map[string]any{}})
	}))
	defer mock.Close()
	f.a.TestTelegramURL = mock.URL
	if e = f.a.tgTick(ctx); e != nil {
		t.Fatal(e)
	}
	fail = false
	f.a.DB.Exec(ctx, "UPDATE telegram_events SET available_at=now()")
	if e = f.a.tgTick(ctx); e != nil {
		t.Fatal(e)
	}
	if counts[42] != 1 || counts[43] != 2 {
		t.Fatal("recipient delivery state was lost", counts)
	}
	var state string
	f.a.DB.QueryRow(ctx, "SELECT state FROM telegram_events WHERE id='test-event'").Scan(&state)
	if state != "sent" {
		t.Fatal(state)
	}
	if c.Mode != "notify_only" {
		t.Fatal("fixture")
	}
}

func TestPausedTelegramPreservesQueuedEvent(t *testing.T) {
	f, plan, _ := setupManual(t)
	ctx := context.Background()
	w := f.order(plan, ID(), &f.customer, "")
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	c := configureTGTest(t, f, []tgPrincipal{{UserID: 42, Scopes: []string{"payments"}, Notify: true}}, "notify_only")
	if _, e := f.a.DB.Exec(ctx, "INSERT INTO telegram_events(id,order_id,kind) VALUES('paused-event',$1,'payment.submitted')", order); e != nil {
		t.Fatal(e)
	}
	paused := c
	paused.Enabled = false
	sealed, e := f.a.seal(paused)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.DB.Exec(ctx, "UPDATE telegram_settings SET enabled=false,sealed=$1 WHERE id=true", sealed); e != nil {
		t.Fatal(e)
	}
	if e := f.a.tgDeliver(ctx, c); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := f.a.DB.QueryRow(ctx, "SELECT state FROM telegram_events WHERE id='paused-event'").Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state != "queued" {
		t.Fatal("paused notification was discarded", state)
	}
}

func TestGatewayRetryAndExplicitIPOrigin(t *testing.T) {
	f := setup(t)
	f.a.IPOrigin = "http://192.0.2.10:18088"
	for origin, code := range map[string]int{"http://192.0.2.10:18088": 401, "http://untrusted.test": 403} {
		r := httptest.NewRequest("POST", "http://portal.test/portal/v1/auth/login", strings.NewReader(`{"email":"invalid@example.test","password":"invalid-password"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		expect(t, w, code)
	}
	expect(t, f.req("POST", "admin/gateway/retry", map[string]any{}, &f.customer, true), 403)
	expect(t, f.req("POST", "admin/gateway/retry", map[string]any{}, &f.admin, true), 200)
	expect(t, f.req("POST", "admin/gateway/retry", map[string]any{}, &f.admin, true), 409)
}

func TestTelegramRedpacketApprovalRequiresFreshAdminAndOneTimeConfirmation(t *testing.T) {
	f, plan, _ := setupManual(t)
	enableRedpacket(t, f)
	ctx := context.Background()
	order, id := beginRedpacket(t, f, plan, &f.customer)
	expect(t, f.req("POST", "orders/"+order+"/receipt", map[string]any{"version": 1, "redpacket_password": "fixture-only-passcode-7261"}, &f.customer, true), 200)
	p := tgPrincipal{UserID: 42, Scopes: []string{"payments", "redpacket", "approve"}, Administrator: f.admin.ID}
	configureTGTest(t, f, []tgPrincipal{p}, "polling")
	replies := []string{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if text, ok := b["text"].(string); ok {
			replies = append(replies, text)
		}
		send(w, 200, map[string]any{"ok": true, "result": map[string]any{}})
	}))
	defer mock.Close()
	f.a.TestTelegramURL = mock.URL
	if e := f.a.tgCallback(ctx, 42, 42, "review-1", "review:"+order); e != nil {
		t.Fatal(e)
	}
	var nonce, state string
	f.a.DB.QueryRow(ctx, "SELECT id FROM telegram_confirmations WHERE payment_id=$1", id).Scan(&nonce)
	if nonce == "" {
		t.Fatal("no confirmation nonce")
	}
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "pending" {
		t.Fatal("review immediately paid order")
	}
	if e := f.a.tgCallback(ctx, 43, 43, "other-user", "confirm:"+nonce); e != nil {
		t.Fatal(e)
	}
	f.a.DB.Exec(ctx, "UPDATE customers SET status='disabled' WHERE id=$1", f.admin.ID)
	if e := f.a.tgCallback(ctx, 42, 42, "revoked", "confirm:"+nonce); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "pending" {
		t.Fatal("revoked admin approved")
	}
	f.a.DB.Exec(ctx, "UPDATE customers SET status='active' WHERE id=$1", f.admin.ID)
	if e := f.a.tgCallback(ctx, 42, 42, "confirm-1", "confirm:"+nonce); e != nil {
		t.Fatal(e)
	}
	if e := f.a.tgCallback(ctx, 42, 42, "replay", "confirm:"+nonce); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "paid" {
		t.Fatal("authorized confirm not paid", replies)
	}
	var jobs int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='fulfill' AND subject=$1", order).Scan(&jobs)
	if jobs != 1 {
		t.Fatal("replay queued duplicate", jobs)
	}
	if !strings.Contains(strings.Join(replies, "\n"), "已使用或已过期") {
		t.Fatal("nonce replay not rejected")
	}
}
