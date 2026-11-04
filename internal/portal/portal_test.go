package portal

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	t                      *testing.T
	a                      *App
	handler                http.Handler
	ready                  atomic.Bool
	grants                 atomic.Int32
	admin, customer, other actor
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration coverage")
	}
	ctx := context.Background()
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "portal_test_" + ID()
	if _, e = db.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	a, e := Open(ctx, u.String(), bytes.Repeat([]byte{37}, 32), "http://portal.test", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.DB.Close(); db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); db.Close() })
	f := &fixture{t: t, a: a, handler: a.Handler()}
	f.ready.Store(true)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xs_"+strings.Repeat("a", 64) {
			t.Error("missing restricted core key")
		}
		switch {
		case r.URL.Path == "/api/v1/commerce/validate":
			send(w, 200, map[string]any{"ready": f.ready.Load(), "reason": "fixture not ready"})
		case r.URL.Path == "/api/v1/commerce/entitlements":
			f.grants.Add(1)
			send(w, 200, map[string]string{"user_id": strings.Repeat("b", 32), "subscription_id": strings.Repeat("c", 32), "operation_id": "mock-operation"})
		case strings.HasSuffix(r.URL.Path, "/check"):
			send(w, 200, map[string]bool{"ready": true})
		default:
			send(w, 200, map[string]bool{"ok": true})
		}
	}))
	t.Cleanup(core.Close)
	a.TestControlURL = core.URL
	a.Mail = func(context.Context, string, string, string) error { return nil }
	sealed, _ := a.seal(Secrets{ControlOrigin: "https://core.test", ControlKey: "xs_" + strings.Repeat("a", 64)})
	if _, e = a.DB.Exec(ctx, "UPDATE settings SET secrets=$1", sealed); e != nil {
		t.Fatal(e)
	}
	f.admin = f.user("owner@example.test", "admin")
	f.customer = f.user("one@example.test", "customer")
	f.other = f.user("two@example.test", "customer")
	return f
}
func (f *fixture) user(email, role string) actor {
	f.t.Helper()
	u := actor{ID: ID(), Email: email, Name: email, Role: role}
	encoded, e := passwordHash("test-only-password-47")
	if e != nil {
		f.t.Fatal(e)
	}
	_, e = f.a.DB.Exec(context.Background(), "INSERT INTO customers(id,email,name,password_hash,role,status,email_verified_at) VALUES($1,$2,$3,$4,$5,'active',now())", u.ID, email, email, encoded, role)
	if e != nil {
		f.t.Fatal(e)
	}
	return u
}
func (f *fixture) req(method, path string, body any, user *actor, csrf bool) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, "http://portal.test/portal/v1/"+path, bytes.NewReader(raw(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://portal.test")
	if user != nil {
		token := hash(user.ID + "test-session")
		c := f.a.csrf(token)
		_, e := f.a.DB.Exec(context.Background(), "INSERT INTO sessions(token_hash,customer_id,csrf_hash,expires_at) VALUES($1,$2,$3,now()+interval '1 hour') ON CONFLICT DO NOTHING", hash(token), user.ID, hash(c))
		if e != nil {
			f.t.Fatal(e)
		}
		r.AddCookie(&http.Cookie{Name: "sp_session", Value: token})
		if csrf {
			r.Header.Set("X-CSRF-Token", c)
		}
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}
func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}
func object(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var o map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &o); e != nil {
		t.Fatal(e)
	}
	return o
}
func (f *fixture) plan() string {
	f.t.Helper()
	ctx := context.Background()
	category, plan := ID(), ID()
	_, e := f.a.DB.Exec(ctx, "INSERT INTO categories(id,name) VALUES($1,'测试分类')", category)
	if e != nil {
		f.t.Fatal(e)
	}
	_, e = f.a.DB.Exec(ctx, `INSERT INTO plans(id,category_id,name,enabled,starts_at,ends_at,recommend_ends_at,quota_bytes,group_ids,protocols,prices) VALUES($1,$2,'计费测试套餐',true,now()-interval '1 hour',now()+interval '1 year',now()+interval '1 year',100000000000,$3,$4,$5)`, plan, category, raw([]string{strings.Repeat("d", 32)}), raw([]string{"vless"}), raw([]Price{{ID: "MONTH", Months: 1, Amount: 1000, Enabled: true}}))
	if e != nil {
		f.t.Fatal(e)
	}
	return plan
}
func (f *fixture) order(plan, key string, u *actor, coupon string) *httptest.ResponseRecorder {
	return f.req("POST", "orders", Buy{PlanID: plan, PriceID: "MONTH", Key: key, Coupon: coupon}, u, true)
}

func TestRolesOwnershipCSRFAndOrigin(t *testing.T) {
	f := setup(t)
	expect(t, f.req("GET", "admin/settings", nil, &f.customer, true), 403)
	expect(t, f.req("GET", "admin/settings", nil, nil, true), 401)
	expect(t, f.req("POST", "tickets", map[string]string{"subject": "help", "body": "message"}, &f.customer, false), 403)
	w := f.req("POST", "tickets", map[string]string{"subject": "help", "body": "private body"}, &f.customer, true)
	expect(t, w, 201)
	id := object(t, w)["id"].(string)
	expect(t, f.req("GET", "tickets/"+id, nil, &f.other, true), 404)
	expect(t, f.req("GET", "tickets/"+id, nil, &f.admin, true), 200)
	p := f.plan()
	w = f.order(p, ID(), &f.customer, "")
	expect(t, w, 201)
	id = object(t, w)["id"].(string)
	expect(t, f.req("GET", "orders/"+id, nil, &f.other, true), 404)
	r := httptest.NewRequest("POST", "http://portal.test/portal/v1/auth/login", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.test")
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, r)
	expect(t, out, 403)
	me := object(t, f.req("GET", "me", nil, &f.customer, true))
	again := object(t, f.req("GET", "me", nil, &f.customer, true))
	if me["csrf"] != again["csrf"] {
		t.Fatal("GET /me rotated token and broke other tabs")
	}
	settings := f.req("GET", "admin/settings", nil, &f.admin, true).Body.String()
	if strings.Contains(settings, "xs_"+strings.Repeat("a", 64)) {
		t.Fatal("secret leaked in settings")
	}
}

func TestInviteRaceVerificationAndReplay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	code := ID() + ID()
	_, e := f.a.DB.Exec(ctx, "INSERT INTO invitations(id,code_hash,prefix,ends_at,max_uses) VALUES($1,$2,$3,now()+interval '1 day',1)", ID(), hash(code), code[:8])
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	status := make(chan int, 2)
	for _, email := range []string{"invite1@example.test", "invite2@example.test"} {
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			w := f.req("POST", "auth/register", map[string]string{"email": email, "name": "test", "password": "test-invite-password", "invitation": code}, nil, false)
			status <- w.Code
		}(email)
	}
	wg.Wait()
	close(status)
	success := 0
	for c := range status {
		if c == 201 {
			success++
		} else if c != 403 {
			t.Fatal(c)
		}
	}
	if success != 1 {
		t.Fatal("invitation capacity exceeded")
	}
	var payload []byte
	if e = f.a.DB.QueryRow(ctx, "SELECT payload FROM jobs WHERE kind='mail'").Scan(&payload); e != nil {
		t.Fatal(e)
	}
	var mail map[string]string
	if e = f.a.unseal(payload, &mail); e != nil {
		t.Fatal(e)
	}
	expect(t, f.req("POST", "auth/login", map[string]string{"email": mail["email"], "password": "test-invite-password"}, nil, false), 403)
	expect(t, f.req("POST", "auth/verify", map[string]string{"token": mail["token"]}, nil, false), 200)
	expect(t, f.req("POST", "auth/verify", map[string]string{"token": mail["token"]}, nil, false), 400)
	expect(t, f.req("POST", "auth/login", map[string]string{"email": mail["email"], "password": "test-invite-password"}, nil, false), 200)
	var claimed int
	f.a.DB.QueryRow(ctx, "SELECT claimed FROM invitations").Scan(&claimed)
	if claimed != 1 {
		t.Fatal(claimed)
	}
}

func TestCouponCapacityIdempotenceAndReadiness(t *testing.T) {
	f := setup(t)
	p := f.plan()
	ctx := context.Background()
	coupon := ID()
	_, e := f.a.DB.Exec(ctx, `INSERT INTO coupons(id,code,name,kind,amount,starts_at,ends_at,max_uses,per_customer) VALUES($1,'LIMIT10','优惠','percent',1000,now()-interval '1 hour',now()+interval '1 day',1,1)`, coupon)
	if e != nil {
		t.Fatal(e)
	}
	key := ID()
	w := f.order(p, key, &f.customer, "LIMIT10")
	expect(t, w, 201)
	id := object(t, w)["id"].(string)
	w = f.order(p, key, &f.customer, "LIMIT10")
	expect(t, w, 200)
	if object(t, w)["id"] != id {
		t.Fatal("duplicate request created another order")
	}
	expect(t, f.order(p, key, &f.customer, ""), 409)
	expect(t, f.order(p, ID(), &f.other, "LIMIT10"), 409)
	var amount, discount int64
	f.a.DB.QueryRow(ctx, "SELECT payable,discount_amount FROM orders WHERE id=$1", id).Scan(&amount, &discount)
	if amount != 900 || discount != 100 {
		t.Fatal(amount, discount)
	}
	expect(t, f.req("POST", "orders/"+id+"/cancel", map[string]string{}, &f.customer, true), 202)
	expect(t, f.order(p, ID(), &f.other, "LIMIT10"), 201)
	f.ready.Store(false)
	expect(t, f.order(p, ID(), &f.customer, ""), 409)
}

func TestPaymentOnceFulfillmentAndLatePayment(t *testing.T) {
	f := setup(t)
	p := f.plan()
	ctx := context.Background()
	w := f.order(p, ID(), &f.customer, "")
	expect(t, w, 201)
	id := object(t, w)["id"].(string)
	channel, payment := ID(), ID()
	sealed, _ := f.a.seal(AlipayConfig{})
	f.a.DB.Exec(ctx, "INSERT INTO payment_channels(id,config) VALUES($1,$2)", channel, sealed)
	_, e := f.a.DB.Exec(ctx, "INSERT INTO payments(id,order_id,channel_id,out_trade_no) VALUES($1,$2,$3,$4)", payment, id, channel, ID())
	if e != nil {
		t.Fatal(e)
	}
	result := paymentResult{Status: "TRADE_SUCCESS", Trade: "test-trade-123456", Amount: 1000, Paid: time.Now()}
	if e = f.a.recordPayment(ctx, payment, result, hash("one")); e != nil {
		t.Fatal(e)
	}
	if e = f.a.recordPayment(ctx, payment, result, hash("two")); e != nil {
		t.Fatal(e)
	}
	var count int
	f.a.DB.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='fulfill'").Scan(&count)
	if count != 1 {
		t.Fatal("callback duplicated fulfillment")
	}
	wrong := result
	wrong.Trade = "different-trade"
	if e = f.a.recordPayment(ctx, payment, wrong, hash("bad-trade")); e == nil {
		t.Fatal("accepted different trade number")
	}
	wrong = result
	wrong.Amount = 999
	if e = f.a.recordPayment(ctx, payment, wrong, hash("bad-amount")); e == nil {
		t.Fatal("accepted wrong total")
	}
	if e = f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	var fulfillment string
	f.a.DB.QueryRow(ctx, "SELECT fulfillment FROM orders WHERE id=$1", id).Scan(&fulfillment)
	if fulfillment != "active" || f.grants.Load() != 1 {
		t.Fatal(fulfillment, f.grants.Load())
	}
	if e = f.a.WorkOnce(ctx); e != nil {
		t.Fatal(e)
	}
	if f.grants.Load() != 1 {
		t.Fatal("completed task ran twice")
	}
	w = f.order(p, ID(), &f.other, "")
	expect(t, w, 201)
	lateID := object(t, w)["id"].(string)
	latePay := ID()
	f.a.DB.Exec(ctx, "UPDATE orders SET expires_at=now()-interval '2 hours' WHERE id=$1", lateID)
	f.a.DB.Exec(ctx, "INSERT INTO payments(id,order_id,channel_id,out_trade_no) VALUES($1,$2,$3,$4)", latePay, lateID, channel, ID())
	result.Trade = "test-late-456"
	if e = f.a.recordPayment(ctx, latePay, result, hash("late")); e != nil {
		t.Fatal(e)
	}
	var state string
	f.a.DB.QueryRow(ctx, "SELECT state FROM orders WHERE id=$1", lateID).Scan(&state)
	if state != "late_paid" {
		t.Fatal("expired payment silently granted service", state)
	}
}

func TestRSA2NotificationVerificationAndMoney(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	cfg := AlipayConfig{PublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))}
	v := url.Values{"app_id": {"app123456"}, "seller_id": {"seller123456"}, "total_amount": {"10.01"}, "out_trade_no": {"our-order"}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"RSA2"}}
	h := sha256.Sum256([]byte(canonical(v, true)))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	v.Set("sign", base64.StdEncoding.EncodeToString(sig))
	if e = verifyAlipay(cfg, v); e != nil {
		t.Fatal(e)
	}
	v.Set("total_amount", "0.01")
	if e = verifyAlipay(cfg, v); e == nil {
		t.Fatal("modified signed notification accepted")
	}
	for _, bad := range []string{"NaN", "-1", "1e2", "1.001", "1.0.0", "99999999999"} {
		if _, e = parseMoney(bad); e == nil {
			t.Fatal("invalid money accepted", bad)
		}
	}
	if n, e := parseMoney("10.01"); e != nil || n != 1001 {
		t.Fatal(n, e)
	}
}

func TestPasswordResetRevokesSessionsAndUniqueKeys(t *testing.T) {
	f := setup(t)
	before := f.req("GET", "me", nil, &f.admin, true)
	expect(t, before, 200)
	if e := f.a.ResetAdmin(context.Background(), f.admin.Email, "different-new-password-73"); e != nil {
		t.Fatal(e)
	}
	var sessions int
	f.a.DB.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE customer_id=$1", f.admin.ID).Scan(&sessions)
	if sessions != 0 {
		t.Fatal("admin recovery preserved sessions")
	}
	b, _ := f.a.seal(map[string]string{"key": "not-plaintext"})
	if bytes.Contains(b, []byte("not-plaintext")) {
		t.Fatal("secret unencrypted")
	}
	other := *f.a
	other.Key = bytes.Repeat([]byte{39}, 32)
	if e := other.unseal(b, &map[string]string{}); e == nil {
		t.Fatal("another installation decrypted secrets")
	}
}

func TestSavedDomainIsAcceptedAndWorkerIsPrivate(t *testing.T) {
	f := setup(t)
	f.a.WorkerToken = strings.Repeat("w", 64)
	_, e := f.a.DB.Exec(context.Background(), `UPDATE settings SET public=jsonb_set(public,'{public_origin}','"https://shop.example.test"')`)
	if e != nil {
		t.Fatal(e)
	}
	if !f.a.allowedOrigin(context.Background(), "https://shop.example.test") || f.a.allowedOrigin(context.Background(), "https://evil.test") {
		t.Fatal("domain origin authorization wrong")
	}
	expect(t, f.req("POST", "local/claim", map[string]string{}, &f.admin, true), 401)
	r := httptest.NewRequest("POST", "http://portal.test/portal/v1/local/heartbeat", strings.NewReader(`{"status":{"mode":"test"}}`))
	r.Header.Set("Authorization", "Bearer "+f.a.WorkerToken)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	expect(t, w, 200)
}

var _ = io.EOF
