package portal

import "net/http"

func (a *App) overview(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	syncErr := a.refreshServiceStates(r.Context())
	var users, buyers, historical, unknown, pending int
	var amount int64
	var checked any
	e := a.DB.QueryRow(r.Context(), `SELECT
 (SELECT count(*) FROM customers WHERE role='customer' AND deleted_at IS NULL),
 (SELECT count(*) FROM customers c WHERE c.role='customer' AND c.deleted_at IS NULL AND c.status='active' AND EXISTS(SELECT 1 FROM service_accounts s JOIN orders o ON o.id=s.order_id WHERE s.customer_id=c.id AND o.state='paid' AND s.remote_state IN ('active','exhausted') AND (s.remote_summary->>'expires_at')::timestamptz>$1)),
 (SELECT count(*) FROM customers c WHERE c.role='customer' AND c.deleted_at IS NULL AND EXISTS(SELECT 1 FROM orders o WHERE o.customer_id=c.id AND o.state='paid')),
 (SELECT count(DISTINCT s.customer_id) FROM service_accounts s JOIN orders o ON o.id=s.order_id JOIN customers c ON c.id=s.customer_id WHERE c.role='customer' AND c.deleted_at IS NULL AND c.status='active' AND o.state='paid' AND s.remote_state='unknown'),
 (SELECT count(*) FROM orders WHERE state='pending'),
 (SELECT COALESCE(sum(payable) FILTER(WHERE state='paid'),0) FROM(SELECT state,payable FROM orders ORDER BY created_at DESC,id DESC LIMIT 300)x),
 (SELECT min(s.remote_checked_at) FROM service_accounts s JOIN customers c ON c.id=s.customer_id WHERE c.role='customer' AND c.deleted_at IS NULL)`, a.now()).Scan(&users, &buyers, &historical, &unknown, &pending, &amount, &checked)
	if e != nil {
		return e
	}
	result := map[string]any{"current_users": users, "current_buying_users": buyers, "historical_buying_users": historical, "unchecked_buying_users": unknown, "pending_orders": pending, "recent_paid_amount": amount, "services_checked_at": checked}
	if syncErr != nil {
		result["sync_warning"] = "星枢状态暂未同步，购买人数使用最近成功核验的服务状态"
	}
	send(w, 200, result)
	return nil
}
