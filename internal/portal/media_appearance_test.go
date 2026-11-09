package portal

import (
	"context"
	"strings"
	"testing"
)

func TestTicketImagesArePrivateAndCannotBeStolen(t *testing.T) {
	f := setup(t)
	data, _ := fixtureImage(t, 30)
	upload := func(u *actor, scope string) string {
		w := f.req("POST", "media", map[string]string{"scope": scope, "image_data": data}, u, true)
		expect(t, w, 201)
		return object(t, w)["id"].(string)
	}
	id := upload(&f.customer, "ticket")
	expect(t, f.req("GET", "media/"+id, nil, nil, false), 401)
	expect(t, f.req("GET", "media/"+id, nil, &f.other, true), 404)
	w := f.req("POST", "tickets", map[string]any{"subject": "Screenshot", "body": "", "images": []string{id}}, &f.customer, true)
	expect(t, w, 201)
	ticket := object(t, w)["id"].(string)
	expect(t, f.req("GET", "media/"+id, nil, &f.admin, true), 200)
	expect(t, f.req("DELETE", "media/"+id, nil, &f.customer, true), 409)
	reply := upload(&f.admin, "ticket")
	expect(t, f.req("POST", "tickets/"+ticket, map[string]any{"body": "回复", "images": []string{reply}}, &f.admin, true), 200)
	expect(t, f.req("GET", "media/"+reply, nil, &f.customer, true), 200)
	expect(t, f.req("GET", "media/"+reply, nil, &f.other, true), 404)
	expect(t, f.req("POST", "tickets", map[string]any{"subject": "steal", "body": "", "images": []string{id}}, &f.other, true), 403)
	limited := f.user("limited-images@example.test", "admin")
	expect(t, f.req("GET", "media/"+id, nil, &limited, true), 404)
	expect(t, f.req("POST", "media", map[string]string{"scope": "background", "image_data": data}, &f.customer, true), 403)
	expect(t, f.req("POST", "media", map[string]string{"scope": "ticket", "image_data": "data:image/svg+xml;base64,PHN2Zz4="}, &f.customer, true), 400)
	w = f.req("GET", "tickets/"+ticket, nil, &f.customer, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), reply) {
		t.Fatal("reply image missing")
	}
}

func TestAnnouncementImagesAndBackgroundAreExplicitPublicReferences(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	data, _ := fixtureImage(t, 60)
	upload := func(scope string) string {
		w := f.req("POST", "media", map[string]string{"scope": scope, "image_data": data}, &f.admin, true)
		expect(t, w, 201)
		return object(t, w)["id"].(string)
	}
	id := upload("announcement")
	b := map[string]any{"title": "图片公告", "body": "正文", "enabled": false, "images": []string{id}}
	expect(t, f.req("POST", "admin/announcements", b, &f.admin, true), 200)
	var announcement string
	f.a.DB.QueryRow(ctx, "SELECT id FROM announcements WHERE title=$1", "图片公告").Scan(&announcement)
	expect(t, f.req("GET", "media/"+id, nil, nil, false), 401)
	b["id"] = announcement
	b["enabled"] = true
	expect(t, f.req("PUT", "admin/announcements", b, &f.admin, true), 200)
	expect(t, f.req("GET", "media/"+id, nil, nil, false), 200)
	w := f.req("GET", "public", nil, nil, false)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), id) || strings.Contains(w.Body.String(), "base64") {
		t.Fatal("public bootstrap must contain references only")
	}
	b["enabled"] = false
	expect(t, f.req("PUT", "admin/announcements", b, &f.admin, true), 200)
	expect(t, f.req("GET", "media/"+id, nil, nil, false), 401)
	background := upload("background")
	s, _, _ := f.a.settings(ctx)
	s.Theme = "midnight"
	s.BackgroundDesktop = "/portal/v1/media/" + background
	s.BackgroundMobile = `url('https\://api.paugram.com/wallpaper/?category=anime') !important`
	s.BackgroundDim = 45
	expect(t, f.req("PUT", "admin/settings", map[string]any{"settings": s}, &f.admin, true), 200)
	expect(t, f.req("GET", "media/"+background, nil, nil, false), 200)
	s, _, _ = f.a.settings(ctx)
	if s.BackgroundMobile != "https://api.paugram.com/wallpaper/?category=anime" {
		t.Fatal(s.BackgroundMobile)
	}
	s.BackgroundMobile = "javascript:alert(1)"
	expect(t, f.req("PUT", "admin/settings", map[string]any{"settings": s}, &f.admin, true), 400)
}

func TestBackgroundAddressRejectsCSSAndUnsafeSchemes(t *testing.T) {
	for _, v := range []string{"javascript:alert(1)", "http://example.test/image.jpg", "https://user:pass@example.test/x", "url(https://example.test/x); color:red", "data:image/svg+xml,abc"} {
		if _, e := backgroundURL(v); e == nil {
			t.Fatal(v)
		}
	}
	if got, e := backgroundURL("url('https://example.test/pic?q=1') !important"); e != nil || got != "https://example.test/pic?q=1" {
		t.Fatal(got, e)
	}
}

func TestPublicCatalogSwitchDoesNotAuthorizePurchasing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.a.DB.Exec(ctx, `UPDATE settings SET public=jsonb_set(public,'{show_catalog}','false')`)
	expect(t, f.req("GET", "catalog", nil, nil, false), 401)
	f.a.DB.Exec(ctx, `UPDATE settings SET public=jsonb_set(public,'{show_catalog}','true')`)
	expect(t, f.req("GET", "catalog", nil, nil, false), 200)
	expect(t, f.req("POST", "orders", map[string]string{}, nil, false), 401)
	f.a.DB.Exec(ctx, `UPDATE settings SET public=jsonb_set(public,'{show_catalog}','false')`)
	expect(t, f.req("GET", "catalog", nil, nil, false), 401)
}
