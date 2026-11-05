package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdministratorOwnSubscriptionsAreRevokedOnDisableAndDeletion(t *testing.T) {
	for _, operation := range []string{"disable", "delete"} {
		t.Run(operation, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			sub := f.user(operation+"-buyer@example.test", "admin")
			plan := f.plan()
			w := f.order(plan, ID(), &sub, "")
			expect(t, w, 201)
			order := object(t, w)["id"].(string)
			f.a.DB.Exec(ctx, "UPDATE orders SET state='paid',paid_at=now(),fulfillment='queued' WHERE id=$1", order)
			if e := f.a.fulfill(ctx, order); e != nil {
				t.Fatal(e)
			}
			if operation == "delete" {
				expect(t, f.req("DELETE", "admin/administrators", map[string]any{"id": sub.ID, "version": 1, "confirmed": true}, &f.admin, true), 200)
			} else {
				expect(t, f.req("PUT", "admin/administrators", map[string]any{"id": sub.ID, "version": 1, "name": sub.Name, "email": sub.Email, "scopes": []string{"payments"}, "enabled": false}, &f.admin, true), 200)
			}
			calls := 0
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if !strings.Contains(r.URL.Path, "/commerce/accounts/") || b["enabled"] != false {
					t.Error("administrator proxy access survived", r.URL.String(), b)
				}
				calls++
				send(w, 200, map[string]bool{"ok": true})
			}))
			defer mock.Close()
			f.a.TestControlURL = mock.URL
			if e := f.a.WorkOnce(ctx); e != nil {
				t.Fatal(e)
			}
			if calls < 1 {
				t.Fatal("administrator subscriptions were not revoked")
			}
			var state string
			f.a.DB.QueryRow(ctx, "SELECT state FROM jobs WHERE kind='access' AND subject=$1", sub.ID).Scan(&state)
			if state != "succeeded" {
				t.Fatal("revocation failed", state)
			}
		})
	}
}
