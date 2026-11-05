package portal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func paidService(t *testing.T, f *fixture) string {
	t.Helper()
	w := f.order(f.plan(), ID(), &f.customer, "")
	expect(t, w, 201)
	id := object(t, w)["id"].(string)
	if _, e := f.a.DB.Exec(context.Background(), "UPDATE orders SET state='paid',paid_at=now() WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	if e := f.a.fulfill(context.Background(), id); e != nil {
		t.Fatal(e)
	}
	return id
}
func TestDeletedCoreIdentityInvalidatesPortalWithoutRegrant(t *testing.T) {
	f := setup(t)
	id := paidService(t, f)
	ctx := context.Background()
	var user string
	f.a.DB.QueryRow(ctx, "SELECT control_user_id FROM service_accounts WHERE order_id=$1", id).Scan(&user)
	var missing, unavailable atomic.Bool
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			send(w, 503, map[string]string{"error": "temporary"})
			return
		}
		u := map[string]any{"id": user, "enabled": true, "active": true, "expires_at": time.Now().Add(time.Hour), "quota_bytes": 1000, "used_bytes": 20}
		switch {
		case r.URL.Path == "/api/v1/commerce/capacity":
			send(w, 200, map[string]int{"identities": 7, "identity_limit": 500})
		case r.URL.Path == "/api/v1/proxy-users/page":
			items := []any{}
			if !missing.Load() {
				items = append(items, u)
			}
			send(w, 200, map[string]any{"items": items, "next_cursor": ""})
		case r.URL.Path == "/api/v1/proxy-users/"+user:
			if missing.Load() {
				send(w, 404, map[string]string{"error": "not found"})
			} else {
				send(w, 200, u)
			}
		case strings.HasSuffix(r.URL.Path, "/check"):
			send(w, 200, map[string]any{"ready": true})
		case strings.HasSuffix(r.URL.Path, "/share"):
			send(w, 200, map[string]string{"url": "https://example.test/" + r.URL.Query().Get("client")})
		default:
			send(w, 200, []any{})
		}
	}))
	defer core.Close()
	f.a.TestControlURL = core.URL
	expect(t, f.req("GET", "me/service?service="+id, nil, &f.customer, true), 200)
	for _, client := range []string{"mihomo", "sing-box", "v2rayng"} {
		w := f.req("GET", "me/subscription/share?service="+id+"&client="+client, nil, &f.customer, true)
		expect(t, w, 200)
		if object(t, w)["url"] != "https://example.test/"+client {
			t.Fatal(w.Body.String())
		}
	}
	unavailable.Store(true)
	expect(t, f.req("GET", "me/service?service="+id, nil, &f.customer, true), 503)
	if e := f.a.refreshServiceStates(ctx, true); e == nil {
		t.Fatal("unavailable snapshot accepted")
	}
	var state string
	f.a.DB.QueryRow(ctx, "SELECT remote_state FROM service_accounts WHERE order_id=$1", id).Scan(&state)
	if state != "active" {
		t.Fatal("temporary outage invalidated service", state)
	}
	unavailable.Store(false)
	missing.Store(true)
	w := f.req("GET", "me/service?service="+id, nil, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["service"].(map[string]any)["state"] != "deleted" {
		t.Fatal(w.Body.String())
	}
	expect(t, f.req("GET", "me/subscription/share?service="+id, nil, &f.customer, true), 410)
	expect(t, f.req("GET", "me/subscription/qr?service="+id, nil, &f.customer, true), 410)
	w = f.req("GET", "me/subscription/check?service="+id, nil, &f.customer, true)
	expect(t, w, 200)
	if object(t, w)["ready"] != false {
		t.Fatal(w.Body.String())
	}
	expect(t, f.req("GET", "me/devices?service="+id, nil, &f.customer, true), 200)
	f.a.nextHealthSync = time.Time{}
	w = f.req("GET", "admin/capacity", nil, &f.admin, true)
	expect(t, w, 200)
	c := object(t, w)
	if c["portal_services"] != float64(0) || c["invalid_services"] != float64(1) || c["paid_orders"] != float64(1) || c["identities"] != float64(7) {
		t.Fatal(w.Body.String())
	}
	w = f.req("GET", "orders", nil, &f.customer, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"service_state":"deleted"`) {
		t.Fatal(w.Body.String())
	}
	before := f.grants.Load()
	if e := f.a.fulfill(ctx, id); e != nil {
		t.Fatal(e)
	}
	if f.grants.Load() != before {
		t.Fatal("deleted service was recreated")
	}
}
func TestServiceStateExpiryQuotaAndMalformedSnapshots(t *testing.T) {
	f := setup(t)
	id := paidService(t, f)
	ctx := context.Background()
	var user string
	f.a.DB.QueryRow(ctx, "SELECT control_user_id FROM service_accounts WHERE order_id=$1", id).Scan(&user)
	for _, tc := range []struct {
		enabled     bool
		expiry      time.Time
		quota, used int
		want        string
	}{{false, time.Now().Add(time.Hour), 100, 0, "disabled"}, {true, time.Now().Add(-time.Hour), 100, 0, "expired"}, {true, time.Now().Add(time.Hour), 100, 100, "exhausted"}, {true, time.Now().Add(time.Hour), 0, 900, "active"}} {
		state, e := f.a.identityState(map[string]any{"enabled": tc.enabled, "expires_at": tc.expiry.Format(time.RFC3339Nano), "quota_bytes": float64(tc.quota), "used_bytes": float64(tc.used)})
		if e != nil || state != tc.want {
			t.Fatal(state, e, tc.want)
		}
	}
	if e := f.a.saveServiceHealth(ctx, user, "active", nil, f.a.now()); e != nil {
		t.Fatal(e)
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, map[string]any{"items": []any{}, "next_cursor": "broken"})
	}))
	defer core.Close()
	f.a.TestControlURL = core.URL
	if e := f.a.refreshServiceStates(ctx, true); e == nil {
		t.Fatal("partial snapshot accepted")
	}
	var state string
	f.a.DB.QueryRow(ctx, "SELECT remote_state FROM service_accounts WHERE order_id=$1", id).Scan(&state)
	if state != "active" {
		t.Fatal("partial snapshot invalidated service")
	}
}
