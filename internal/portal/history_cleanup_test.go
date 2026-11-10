package portal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHistoryCleanupProtectsPaymentsAndEntitlements(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	root := paidService(t, f)
	body := historyRequest{Kind: "orders", Action: "purge", ID: root, Days: 0, Confirmed: true}
	expect(t, f.req("POST", "admin/history-cleanup", body, &f.customer, true), 403)
	expect(t, f.req("POST", "admin/history-cleanup", body, &f.admin, false), 403)
	expect(t, f.req("POST", "admin/history-cleanup", body, &f.admin, true), 409)
	body.Action = "archive"
	expect(t, f.req("POST", "admin/history-cleanup", body, &f.admin, true), 409)
	f.a.DB.Exec(ctx, "UPDATE service_accounts SET remote_state='expired',remote_summary=$1 WHERE order_id=$2", raw(map[string]any{"expires_at": f.a.now().Add(-10 * 24 * time.Hour)}), root)
	f.a.nextHealthSync = f.a.now().Add(time.Hour)
	body.Action = "archive"
	expect(t, f.req("POST", "admin/history-cleanup", body, &f.admin, true), 200)
	var keep bool
	f.a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM entitlement_ledger WHERE order_id=$1) AND EXISTS(SELECT 1 FROM service_accounts WHERE order_id=$1)", root).Scan(&keep)
	if !keep {
		t.Fatal("archive removed entitlement journals")
	}
	before := f.grants.Load()
	if e := f.a.fulfill(ctx, root); e != nil {
		t.Fatal(e)
	}
	if f.grants.Load() != before {
		t.Fatal("archive allowed a duplicate grant")
	}
	w := f.req("GET", "admin/orders", nil, &f.admin, true)
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), root) {
		t.Fatal("archived order in active list")
	}
	w = f.req("GET", "admin/orders?archived=1", nil, &f.admin, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), root) {
		t.Fatal("archived order unavailable")
	}
	body.Action = "restore"
	expect(t, f.req("POST", "admin/history-cleanup", body, &f.admin, true), 200)
	body.Action = "purge"
	expect(t, f.req("POST", "admin/history-cleanup", body, &f.admin, true), 409)
}

func TestCleanupUsesPreviewIDsAndRechecksChangedState(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	create := func() string {
		id := ID()
		_, e := f.a.DB.Exec(ctx, "INSERT INTO tickets(id,customer_id,subject,state,created_at,closed_at) VALUES($1,$2,'old','closed',now()-interval '100 days',now()-interval '99 days')", id, f.customer.ID)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	first, second := create(), create()
	w := f.req("GET", "admin/history-cleanup?kind=tickets&action=purge&days=90", nil, &f.admin, true)
	expect(t, w, 200)
	var ids []string
	for _, v := range object(t, w)["ids"].([]any) {
		ids = append(ids, v.(string))
	}
	third := create()
	f.a.DB.Exec(ctx, "UPDATE tickets SET state='open',closed_at=NULL WHERE id=$1", second)
	b := historyRequest{Kind: "tickets", Action: "purge", Days: 90, Confirmed: true, IDs: ids}
	w = f.req("POST", "admin/history-cleanup", b, &f.admin, true)
	expect(t, w, 200)
	if object(t, w)["changed"] != float64(1) {
		t.Fatal(w.Body.String())
	}
	var firstExists, secondExists, thirdExists bool
	f.a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tickets WHERE id=$1),EXISTS(SELECT 1 FROM tickets WHERE id=$2),EXISTS(SELECT 1 FROM tickets WHERE id=$3)", first, second, third).Scan(&firstExists, &secondExists, &thirdExists)
	if firstExists || !secondExists || !thirdExists {
		t.Fatal("cleanup scope changed after preview", firstExists, secondExists, thirdExists)
	}
}

func TestCleanupDeletesOnlyUnpaidUnlinkedCancelledOrders(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	w := f.order(f.plan(), ID(), &f.customer, "")
	expect(t, w, 201)
	id := object(t, w)["id"].(string)
	f.a.DB.Exec(ctx, "UPDATE orders SET state='cancelled',expires_at=now()-interval '1 day' WHERE id=$1", id)
	b := historyRequest{Kind: "orders", Action: "purge", ID: id, Days: 0, Confirmed: false}
	expect(t, f.req("POST", "admin/history-cleanup", b, &f.admin, true), 400)
	b.Confirmed = true
	expect(t, f.req("POST", "admin/history-cleanup", b, &f.admin, true), 200)
	var exists bool
	f.a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1)", id).Scan(&exists)
	if exists {
		t.Fatal("cancelled unlinked order not removed")
	}
	expect(t, f.req("GET", "admin/history-cleanup?kind=orders&action=purge&days=0", nil, &f.admin, true), 400)
}

func TestClosedTicketCleanupPreservesOpenTicketsAndSharedImages(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	image := ID()
	_, e := f.a.DB.Exec(ctx, "INSERT INTO media_assets(id,owner_id,scope,mime,content) VALUES($1,$2,'ticket','image/png',$3)", image, f.customer.ID, []byte{1, 2, 3})
	if e != nil {
		t.Fatal(e)
	}
	makeTicket := func() string {
		w := f.req("POST", "tickets", map[string]any{"subject": "fixture", "body": "body", "images": []string{image}}, &f.customer, true)
		expect(t, w, 201)
		return object(t, w)["id"].(string)
	}
	first, second := makeTicket(), makeTicket()
	b := historyRequest{Kind: "tickets", Action: "purge", ID: first, Days: 0, Confirmed: true}
	expect(t, f.req("POST", "admin/history-cleanup", b, &f.admin, true), 409)
	expect(t, f.req("POST", "tickets/"+first, map[string]any{"close": true}, &f.customer, true), 200)
	expect(t, f.req("POST", "admin/history-cleanup", b, &f.admin, true), 200)
	var exists bool
	f.a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM media_assets WHERE id=$1)", image).Scan(&exists)
	if !exists {
		t.Fatal("shared image was removed")
	}
	expect(t, f.req("POST", "tickets/"+second, map[string]any{"close": true}, &f.customer, true), 200)
	b.ID = second
	expect(t, f.req("POST", "admin/history-cleanup", b, &f.admin, true), 200)
	f.a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM media_assets WHERE id=$1)", image).Scan(&exists)
	if exists {
		t.Fatal("unused image retained after explicit cleanup")
	}
	expect(t, f.req("GET", "tickets/"+first, nil, &f.customer, true), 404)
}

func TestManualAttachmentCleanupKeepsReplayProtection(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	root := paidService(t, f)
	id := ID()
	_, e := f.a.DB.Exec(ctx, `INSERT INTO manual_payments(id,order_id,method_id,provider,method_name,instructions,qr_image,qr_mime,state,verified_reference,proof_image,redpacket_sealed,review_deadline) VALUES($1,$2,'alipay_static','alipay','fixture','',decode('010203','hex'),'image/png','approved','unique-fixture-reference',decode('040506','hex'),decode('070809','hex'),now()-interval '1 day')`, id, root)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.a.DB.Exec(ctx, "INSERT INTO redpacket_claims(code_hash,order_id) VALUES($1,$2)", hash("fixture-packet"), root)
	if e != nil {
		t.Fatal(e)
	}
	b := historyRequest{Kind: "manual-payments", Action: "attachments", ID: id, Days: 0, Confirmed: true}
	expect(t, f.req("POST", "admin/history-cleanup", b, &f.admin, true), 409)
	b.Action = "archive"
	expect(t, f.req("POST", "admin/history-cleanup", b, &f.admin, true), 200)
	b.Action = "attachments"
	w := f.req("POST", "admin/history-cleanup", b, &f.admin, true)
	expect(t, w, 200)
	if object(t, w)["attachment_bytes_removed"] != float64(9) {
		t.Fatal(w.Body.String())
	}
	var reference string
	var cleared bool
	e = f.a.DB.QueryRow(ctx, "SELECT verified_reference,qr_image IS NULL AND proof_image IS NULL AND redpacket_sealed IS NULL FROM manual_payments WHERE id=$1", id).Scan(&reference, &cleared)
	if e != nil || reference != "unique-fixture-reference" || !cleared {
		t.Fatal(reference, cleared, e)
	}
	var keep bool
	f.a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM redpacket_claims WHERE order_id=$1)", root).Scan(&keep)
	if !keep {
		t.Fatal("redpacket replay guard deleted")
	}
	w = f.req("GET", "admin/manual-payments?archived=1", nil, &f.admin, true)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), id) {
		t.Fatal(w.Body.String())
	}
}
