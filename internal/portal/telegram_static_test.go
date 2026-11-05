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

func TestTelegramPollToStaticReceiptReviewAndProvision(t *testing.T) {
	f, plan, _ := setupManual(t)
	ctx := context.Background()
	w := f.order(plan, ID(), &f.customer, "")
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	w = f.req("POST", "orders/"+order+"/payment", map[string]string{"method_id": "alipay_static"}, &f.customer, true)
	expect(t, w, 200)
	payment := object(t, w)["attempt"].(map[string]any)["id"].(string)
	expect(t, f.req("POST", "orders/"+order+"/receipt", map[string]any{"version": 1, "reference": "USER-CLAIMED-ONLY"}, &f.customer, true), 200)
	configureTGTest(t, f, []tgPrincipal{{UserID: 42, Administrator: f.admin.ID, Scopes: []string{"payments", "approve"}}}, "polling")
	step := 0
	var nonce string
	receipt := "REAL-TRANSFER-00018 10.00 " + time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04")
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var result any = map[string]any{}
		switch method {
		case "getMe":
			result = map[string]string{"username": "isolated_fixture_bot"}
		case "getWebhookInfo":
			result = map[string]string{"url": ""}
		case "sendMessage":
			result = map[string]int{"message_id": 401}
		case "answerCallbackQuery": // Expired callback ACK must not block the polling cursor.
			send(w, 400, map[string]any{"ok": false, "error_code": 400})
			return
		case "getUpdates":
			step++
			updates := []any{}
			if step == 1 {
				updates = append(updates, map[string]any{"update_id": 1, "callback_query": map[string]any{"id": "review", "data": "review:" + order, "from": map[string]int{"id": 42}, "message": map[string]any{"chat": map[string]any{"id": 42, "type": "private"}}}})
			}
			if step == 2 {
				updates = append(updates, map[string]any{"update_id": 2, "message": map[string]any{"from": map[string]int{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "reply_to_message": map[string]int{"message_id": 401}, "text": receipt}})
			}
			if step == 3 {
				f.a.DB.QueryRow(ctx, "SELECT id FROM telegram_confirmations WHERE payment_id=$1 AND used_at IS NULL", payment).Scan(&nonce)
				updates = append(updates, map[string]any{"update_id": 3, "callback_query": map[string]any{"id": "confirm", "data": "confirm:" + nonce, "from": map[string]int{"id": 42}, "message": map[string]any{"chat": map[string]any{"id": 42, "type": "private"}}}})
			}
			result = updates
		default:
			t.Error("unexpected bot method", method)
		}
		send(w, 200, map[string]any{"ok": true, "result": result})
	}))
	defer mock.Close()
	f.a.TestTelegramURL = mock.URL
	for range 2 {
		if e := f.a.tgTick(ctx); e != nil {
			t.Fatal(e)
		}
	}
	var state, reference string
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "pending" {
		t.Fatal("receipt input skipped final confirmation")
	}
	if handled, e := f.a.tgPaymentReply(ctx, 42, 42, 401, strings.Replace(receipt, "REAL-TRANSFER-00018", "CHANGED-REFERENCE", 1)); e != nil || !handled {
		t.Fatal(handled, e)
	}
	f.a.DB.QueryRow(ctx, "SELECT verified_reference FROM telegram_confirmations WHERE payment_id=$1", payment).Scan(&reference)
	if reference != "REAL-TRANSFER-00018" {
		t.Fatal("confirmed receipt data was overwritten", reference)
	}
	if e := f.a.tgTick(ctx); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "paid" {
		t.Fatal("polling callback failed to confirm", state)
	}
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	f.a.DB.QueryRow(ctx, "SELECT fulfillment FROM orders WHERE id=$1", order).Scan(&state)
	if state != "active" || f.grants.Load() != 1 {
		t.Fatal("approved payment did not provision exactly once", state, f.grants.Load())
	}
	if e := f.a.tgCallback(ctx, 42, 42, "replay", "confirm:"+nonce); e != nil {
		t.Fatal(e)
	}
	if e := f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	if f.grants.Load() != 1 {
		t.Fatal("callback replay created another subscription")
	}
	var cursor int
	f.a.DB.QueryRow(ctx, "SELECT cursor FROM telegram_settings WHERE id=true").Scan(&cursor)
	if cursor != 4 {
		t.Fatal("expired callback ACK blocked polling", cursor)
	}
}

func TestTelegramStaticReviewRejectsMissingDetailsAndWrongPrincipal(t *testing.T) {
	f, plan, _ := setupManual(t)
	ctx := context.Background()
	w := f.order(plan, ID(), &f.customer, "")
	expect(t, w, 201)
	order := object(t, w)["id"].(string)
	w = f.req("POST", "orders/"+order+"/payment", map[string]string{"method_id": "alipay_static"}, &f.customer, true)
	expect(t, w, 200)
	payment := object(t, w)["attempt"].(map[string]any)["id"].(string)
	expect(t, f.req("POST", "orders/"+order+"/receipt", map[string]any{"version": 1, "reference": "CUSTOMER-REFERENCE"}, &f.customer, true), 200)
	configureTGTest(t, f, []tgPrincipal{{UserID: 42, Administrator: f.admin.ID, Scopes: []string{"payments", "approve"}}}, "polling")
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		send(w, 200, map[string]any{"ok": true, "result": map[string]int{"message_id": 402}})
	}))
	defer mock.Close()
	f.a.TestTelegramURL = mock.URL
	if e := f.a.tgCallback(ctx, 42, 42, "start", "review:"+order); e != nil {
		t.Fatal(e)
	}
	var nonce string
	f.a.DB.QueryRow(ctx, "SELECT id FROM telegram_confirmations WHERE payment_id=$1", payment).Scan(&nonce)
	if e := f.a.tgCallback(ctx, 42, 42, "early", "confirm:"+nonce); e != nil {
		t.Fatal(e)
	}
	if handled, e := f.a.tgPaymentReply(ctx, 99, 99, 402, "NOT-REAL 10.00 2026-11-05 12:34"); handled || e != nil {
		t.Fatal("ungranted principal accepted")
	}
	if handled, e := f.a.tgPaymentReply(ctx, 42, 42, 402, "INVALID 0.01 "+time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04")); !handled || e != nil {
		t.Fatal(handled, e)
	}
	var state string
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", order).Scan(&state)
	if state != "pending" {
		t.Fatal("missing or mismatched payment details were approved")
	}
	for _, value := range []string{"REF000 1e3 2026-11-05 12:00", "REF000 10.001 2026-11-05 12:00", "REF000 -1 2026-11-05 12:00"} {
		if _, _, _, e := parseTGReceipt(value); e == nil {
			t.Fatal("invalid receipt format accepted", value)
		}
	}
}
