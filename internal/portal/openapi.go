package portal

import (
	"net/http"
	"strings"
)

func (a *App) openapi(w http.ResponseWriter, r *http.Request) error {
	paths := map[string]any{}
	for _, item := range []struct{ path, method, role, summary string }{{"/auth/login", "post", "public", "Email/password login; returns session cookie and CSRF token"}, {"/auth/register", "post", "public", "Registration with confirmed password: invitation, allowlist or direct verified email"}, {"/auth/verify-email", "post", "public", "Verify changed email and revoke sessions"}, {"/me/profile", "get", "customer", "Own editable profile"}, {"/me/profile", "put", "customer", "Update own profile; changed email requires current password and verification"}, {"/me/password", "put", "customer", "Change confirmed password using current password; revoke all sessions"}, {"/payment-methods", "get", "customer", "Enabled payment methods"}, {"/orders/{id}/payment", "get", "customer", "Own manual payment status"}, {"/orders/{id}/payment-code", "get", "customer-or-payments", "Snapshot QR image with ownership checks"}, {"/orders/{id}/receipt", "get", "customer-or-payments", "Private payment proof image"}, {"/orders/{id}/receipt", "post", "customer", "Submit proof for review; never marks paid automatically"}, {"/auth/verify", "post", "public", "Consume a one-time email verification token"}, {"/auth/forgot", "post", "public", "Request a password reset without account enumeration"}, {"/auth/reset", "post", "public", "Reset password and revoke existing sessions"}, {"/me", "get", "customer", "Current authenticated account and stable CSRF token"}, {"/auth/logout", "post", "customer", "Revoke session"}, {"/public", "get", "public", "Public branding, registration mode and announcements"}, {"/catalog", "get", "public", "Published plans with live remaining_stock, stock_limit and stock_checked_at"}, {"/checkout/quote", "post", "customer", "Server-side price and coupon validation"}, {"/orders", "post", "customer", "Create idempotent new order or renewal_of an existing service; stock applies only to new services"}, {"/orders", "get", "customer", "Own orders only"}, {"/orders/{id}", "get", "customer", "Own order payment and fulfillment state"}, {"/orders/{id}/cancel", "post", "customer", "Cancel own unpaid order; channel closure is reconciled"}, {"/orders/{id}/payment", "post", "customer", "Choose official Alipay or configured personal Alipay/WeChat static QR; static QR requires manual verification"}, {"/payments/alipay/notify", "post", "signed-channel", "RSA2-verified Alipay notification"}, {"/me/renewal", "get", "customer", "Own existing subscription renewal options and pending renewal orders; service=original order ID"}, {"/me/services", "get", "customer", "Own per-order services; 25-row offset pagination"}, {"/me/service", "get", "customer", "Own authoritative control-plane quota and expiry"}, {"/me/subscription/{view}", "get", "customer", "Own independent order share/qr/nodes/check; service=order_id, client=format; configuration downloads disabled (410)"}, {"/me/devices", "get", "customer", "Own client observations, never other customers"}, {"/client-help", "get", "customer", "Platform client tutorials maintained by administrators"}, {"/clients", "get", "customer", "Control-plane client formats"}, {"/tickets", "get", "customer", "Own support tickets"}, {"/tickets", "post", "customer", "Create support ticket"}, {"/tickets/{id}", "get", "customer", "Read own ticket; administrators may read all"}, {"/tickets/{id}", "post", "customer", "Reply to or close an authorized ticket"}} {
		key := "/portal/v1" + item.path
		entry, ok := paths[key].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[key] = entry
		}
		entry[item.method] = map[string]any{"summary": item.summary, "x-role": item.role, "responses": map[string]any{"200": map[string]string{"description": "Success"}, "400": map[string]string{"description": "Invalid typed request"}, "401": map[string]string{"description": "Authentication required"}, "403": map[string]string{"description": "Role, CSRF or ownership denied"}, "409": map[string]string{"description": "Version, coupon or order conflict"}}}
	}
	for path, methods := range map[string][]string{"settings": {"get", "put"}, "categories": {"get", "post", "put"}, "plans": {"get", "post", "put"}, "coupons": {"get", "post", "put"}, "invitations": {"get", "post", "put"}, "customers": {"get", "put", "delete"}, "customers/restore": {"post"}, "client-help": {"put"}, "orders": {"get"}, "jobs": {"get", "post"}, "announcements": {"get", "post", "put"}, "tickets": {"get"}, "audit": {"get"}, "control-groups": {"get"}, "capacity": {"get"}, "service-migrations": {"get"}, "record-retention": {"get", "post"}, "version": {"get", "post"}, "payment-methods": {"get", "put"}, "payment-methods/{id}/image": {"get"}, "administrators": {"get", "post", "put", "delete"}, "administrators/restore": {"post"}, "demo-plans": {"post"}, "manual-payments": {"get"}, "manual-payments/{id}": {"get", "put"}, "manual-payments/{id}/redpacket": {"get"}, "telegram": {"get", "put"}, "telegram/check": {"post"}, "telegram/commands": {"post"}, "telegram/retry": {"post"}, "gateway/retry": {"post"}} {
		entry := map[string]any{}
		for _, method := range methods {
			role := adminDocRole(path)
			if path == "customers" && method == "delete" {
				role = "admin:customers+delete_customers"
			}
			entry[method] = map[string]any{"summary": "Administrator " + method + ": " + path, "x-role": role, "responses": map[string]any{"200": map[string]string{"description": "Success; sensitive secrets masked; authorized invitation/coupon codes remain viewable"}, "403": map[string]string{"description": "Admin role and CSRF required"}, "409": map[string]string{"description": "Optimistic version or state conflict"}}}
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
			if strings.HasPrefix(path, "/portal/v1/me/") && (strings.Contains(path, "/service") || strings.HasSuffix(path, "/devices") || strings.HasSuffix(path, "/renewal")) && !strings.HasSuffix(path, "/services") {
				parameters = append(parameters, map[string]any{"name": "service", "in": "query", "schema": map[string]string{"type": "string"}, "description": "Purchased order ID owned by the current customer; omitted means latest service"})
			}
			if strings.Contains(path, "/subscription/") {
				parameters = append(parameters, map[string]any{"name": "client", "in": "query", "schema": map[string]string{"type": "string"}})
			}
			if strings.Contains(path, "{id}") {
				parameters = append(parameters, map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]string{"type": "string"}})
			}
			if strings.Contains(path, "{view}") {
				parameters = append(parameters, map[string]any{"name": "view", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"share", "qr", "nodes", "check"}}})
			}
			if len(parameters) > 0 {
				operation["parameters"] = parameters
			}
		}
	}

	send(w, 200, map[string]any{"openapi": "3.0.3", "info": map[string]string{"title": "Starhub Portal API", "version": Version}, "paths": paths, "components": map[string]any{"securitySchemes": map[string]any{"Session": map[string]string{"type": "apiKey", "in": "cookie", "name": "sp_session"}}}, "security": []map[string]any{{"Session": []string{}}}})
	return nil
}

func adminDocRole(path string) string {
	switch path {
	case "telegram", "administrators/restore", "telegram/check", "telegram/commands", "telegram/retry", "gateway/retry", "settings", "version", "capacity", "service-migrations", "payment-methods", "payment-methods/{id}/image", "administrators", "demo-plans":
		return "owner"
	case "client-help":
		return "admin:content"
	case "customers/restore":
		return "admin:customers+delete_customers"
	case "manual-payments", "manual-payments/{id}", "manual-payments/{id}/redpacket":
		return "admin:payments"
	default:
		return "admin:" + adminResourceScope(path)
	}
}
