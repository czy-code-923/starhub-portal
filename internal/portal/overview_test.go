package portal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOverviewCountsCurrentCustomersAndDistinctValidBuyers(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	first, second := paidService(t, f), paidService(t, f)
	f.a.nextHealthSync = f.a.now().Add(time.Hour)
	if _, e := f.a.DB.Exec(ctx, `UPDATE service_accounts SET remote_state='active',remote_summary=$1,remote_checked_at=now()`, raw(map[string]any{"expires_at": f.a.now().Add(time.Hour)})); e != nil {
		t.Fatal(e)
	}
	check := func(users, buyers, historical, unknown float64) {
		t.Helper()
		w := f.req("GET", "admin/overview", nil, &f.admin, true)
		expect(t, w, 200)
		o := object(t, w)
		for key, want := range map[string]float64{"current_users": users, "current_buying_users": buyers, "historical_buying_users": historical, "unchecked_buying_users": unknown} {
			if o[key] != want {
				t.Fatalf("%s=%v want %v", key, o[key], want)
			}
		}
	}
	check(2, 1, 1, 0) // Two paid orders for one customer count as one buyer.
	f.a.DB.Exec(ctx, "UPDATE service_accounts SET remote_state='exhausted' WHERE order_id=$1", first)
	f.a.DB.Exec(ctx, "UPDATE service_accounts SET remote_state='deleted' WHERE order_id=$1", second)
	check(2, 1, 1, 0) // Monthly quota exhaustion does not mean the subscription expired.
	f.a.DB.Exec(ctx, "UPDATE service_accounts SET remote_summary=$1 WHERE order_id=$2", raw(map[string]any{"expires_at": f.a.now().Add(-time.Second)}), first)
	check(2, 0, 1, 0) // Expiry is checked even if the cached state is stale.
	f.a.DB.Exec(ctx, "UPDATE service_accounts SET remote_state='unknown' WHERE order_id=$1", first)
	check(2, 0, 1, 1)
	f.a.DB.Exec(ctx, "UPDATE service_accounts SET remote_state='active',remote_summary=$1 WHERE order_id=$2", raw(map[string]any{"expires_at": f.a.now().Add(time.Hour)}), first)
	f.a.DB.Exec(ctx, "UPDATE customers SET status='disabled' WHERE id=$1", f.customer.ID)
	check(2, 0, 1, 0)
	f.a.DB.Exec(ctx, "UPDATE customers SET deleted_at=now() WHERE id=$1", f.customer.ID)
	check(1, 0, 0, 0)
	// Counts are not capped by the 500-record customer management list.
	if _, e := f.a.DB.Exec(ctx, `INSERT INTO customers(id,email,name,password_hash,role,status) SELECT 'bulk-'||g,'bulk-'||g||'@example.test','fixture','not-a-login-hash','customer','unverified' FROM generate_series(1,501) g`); e != nil {
		t.Fatal(e)
	}
	check(502, 0, 0, 0)
	expect(t, f.req("GET", "admin/overview", nil, &f.customer, true), 401)
	expect(t, f.req("GET", "admin/overview", nil, &f.other, true), 403)
	expect(t, f.req("GET", "admin/overview", nil, nil, true), 401)
	assistant := f.user("assistant@example.test", "admin")
	expect(t, f.req("GET", "admin/overview", nil, &assistant, true), 403)
}

func TestCatalogGuideSettingsDefaultsPersistenceAndValidation(t *testing.T) {
	f := setup(t)
	get := func() Settings {
		t.Helper()
		s, _, e := f.a.settings(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	initial := object(t, f.req("GET", "admin/settings", nil, &f.admin, true))["settings"].(map[string]any)
	if initial["catalog_guide_title"] != "购买与续费说明" || initial["catalog_guide_body"] == "" {
		t.Fatal(initial)
	}
	s := get()
	s.ShowCatalog = true
	s.CatalogGuideTitle = "购买须知"
	s.CatalogGuideBody = "第一行\n第二行 <b>纯文字</b>"
	w := f.req("PUT", "admin/settings", map[string]any{"settings": s}, &f.admin, true)
	expect(t, w, 200)
	for _, path := range []string{"public", "catalog"} {
		w = f.req("GET", path, nil, nil, false)
		expect(t, w, 200)
		o := object(t, w)
		if o["catalog_guide_title"] != s.CatalogGuideTitle || o["catalog_guide_body"] != s.CatalogGuideBody {
			t.Fatal(path, o)
		}
	}
	s = get()
	s.CatalogGuideTitle = strings.Repeat("字", 81)
	expect(t, f.req("PUT", "admin/settings", map[string]any{"settings": s}, &f.admin, true), 400)
	s.CatalogGuideTitle = "说明"
	s.CatalogGuideBody = strings.Repeat("字", 6001)
	expect(t, f.req("PUT", "admin/settings", map[string]any{"settings": s}, &f.admin, true), 400)
	s.CatalogGuideTitle, s.CatalogGuideBody = "", ""
	expect(t, f.req("PUT", "admin/settings", map[string]any{"settings": s}, &f.other, true), 403)
	expect(t, f.req("PUT", "admin/settings", map[string]any{"settings": s}, &f.admin, true), 200)
	if object(t, f.req("GET", "catalog", nil, nil, false))["catalog_guide_title"] != "购买与续费说明" {
		t.Fatal("blank values must restore defaults")
	}
}
