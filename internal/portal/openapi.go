package portal

import (
	"net/http"
	"strings"
)

func (a *App) openapi(w http.ResponseWriter, r *http.Request) error {
	paths := map[string]any{}
	for _, item := range []struct{ path, method, role, summary string }{{"/auth/login", "post", "public", "Email/password login; returns session cookie and CSRF token"}, {"/auth/register", "post", "public", "Restricted invitation/allowlist registration"}, {"/auth/verify", "post", "public", "Consume a one-time email verification token"}, {"/auth/forgot", "post", "public", "Request a password reset without account enumeration"}, {"/auth/reset", "post", "public", "Reset password and revoke existing sessions"}, {"/me", "get", "customer", "Current authenticated account and stable CSRF token"}, {"/auth/logout", "post", "customer", "Revoke session"}, {"/public", "get", "public", "Public branding, registration mode and announcements"}, {"/catalog", "get", "public", "Published plan categories, recommendations and prices"}, {"/checkout/quote", "post", "customer", "Server-side price and coupon validation"}, {"/orders", "post", "customer", "Create idempotent order and reserve coupon capacity"}, {"/orders", "get", "customer", "Own orders only"}, {"/orders/{id}", "get", "customer", "Own order payment and fulfillment state"}, {"/orders/{id}/cancel", "post", "customer", "Cancel own unpaid order; channel closure is reconciled"}, {"/orders/{id}/payment", "post", "customer", "Signed Alipay page or mobile website payment"}, {"/payments/alipay/notify", "post", "signed-channel", "RSA2-verified Alipay notification"}, {"/me/service", "get", "customer", "Own authoritative control-plane quota and expiry"}, {"/me/subscription/{view}", "get", "customer", "Own share/qr/content/nodes/check; client query parameter"}, {"/me/devices", "get", "customer", "Own client observations, never other customers"}, {"/clients", "get", "customer", "Control-plane client formats"}, {"/tickets", "get", "customer", "Own support tickets"}, {"/tickets", "post", "customer", "Create support ticket"}, {"/tickets/{id}", "get", "customer", "Read own ticket; administrators may read all"}, {"/tickets/{id}", "post", "customer", "Reply to or close an authorized ticket"}} {
		key := "/portal/v1" + item.path
		entry, ok := paths[key].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[key] = entry
		}
		entry[item.method] = map[string]any{"summary": item.summary, "x-role": item.role, "responses": map[string]any{"200": map[string]string{"description": "Success"}, "400": map[string]string{"description": "Invalid typed request"}, "401": map[string]string{"description": "Authentication required"}, "403": map[string]string{"description": "Role, CSRF or ownership denied"}, "409": map[string]string{"description": "Version, coupon or order conflict"}}}
	}
	for path, methods := range map[string][]string{"settings": {"get", "put"}, "categories": {"get", "post", "put"}, "plans": {"get", "post", "put"}, "coupons": {"get", "post", "put"}, "invitations": {"get", "post", "put"}, "customers": {"get", "put"}, "orders": {"get"}, "jobs": {"get", "post"}, "announcements": {"get", "post", "put"}, "tickets": {"get"}, "audit": {"get"}, "control-groups": {"get"}, "version": {"get", "post"}} {
		entry := map[string]any{}
		for _, method := range methods {
			entry[method] = map[string]any{"summary": "Administrator " + method + ": " + path, "x-role": "admin", "responses": map[string]any{"200": map[string]string{"description": "Success; secrets masked"}, "403": map[string]string{"description": "Admin role and CSRF required"}, "409": map[string]string{"description": "Optimistic version or state conflict"}}}
		}
		paths["/portal/v1/admin/"+path] = entry
	}
	paths["/portal/v1/health"] = map[string]any{"get": map[string]any{"summary": "Database health and installed version", "x-role": "public", "responses": map[string]any{"200": map[string]string{"description": "Healthy"}}}}
	for _, v := range paths {
		entry := v.(map[string]any)
		for _, vv := range entry {
			operation := vv.(map[string]any)
			if operation["x-role"] == "public" || operation["x-role"] == "signed-channel" {
				operation["security"] = []any{}
			}
		}
	}
	for path, v := range paths {
		entry := v.(map[string]any)
		for _, vv := range entry {
			operation := vv.(map[string]any)
			parameters := []any{}
			if strings.Contains(path, "{id}") {
				parameters = append(parameters, map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]string{"type": "string"}})
			}
			if strings.Contains(path, "{view}") {
				parameters = append(parameters, map[string]any{"name": "view", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"share", "qr", "content", "nodes", "check"}}})
			}
			if len(parameters) > 0 {
				operation["parameters"] = parameters
			}
		}
	}

	send(w, 200, map[string]any{"openapi": "3.0.3", "info": map[string]string{"title": "Starhub Portal API", "version": Version}, "paths": paths, "components": map[string]any{"securitySchemes": map[string]any{"Session": map[string]string{"type": "apiKey", "in": "cookie", "name": "sp_session"}}}, "security": []map[string]any{{"Session": []string{}}}})
	return nil
}
