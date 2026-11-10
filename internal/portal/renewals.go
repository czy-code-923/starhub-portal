package portal

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type renewalTarget struct {
	Selection      serviceSelection
	Plan, External string
}

func (a *App) renewalTarget(ctx context.Context, user, order string) (renewalTarget, error) {
	var t renewalTarget
	if !portalID.MatchString(order) {
		return t, bad("续费服务编号无效")
	}
	e := a.rowQueries(ctx).QueryRow(ctx, `SELECT s.order_id,s.control_user_id,s.subscription_id,s.plan_id,s.external_id FROM service_accounts s JOIN orders o ON o.id=s.order_id WHERE s.order_id=$1 AND s.customer_id=$2 AND o.state='paid' AND o.renewal_of IS NULL AND s.hidden_at IS NULL AND NOT s.legacy_combined`, order, user).Scan(&t.Selection.Order, &t.Selection.User, &t.Selection.Subscription, &t.Plan, &t.External)
	if e == pgx.ErrNoRows {
		return t, Problem{404, "未找到可续费的独立订阅；旧合并权益须先完成迁移"}
	}
	return t, e
}
func (a *App) renewalOptions(w http.ResponseWriter, r *http.Request) error {
	t, e := a.renewalTarget(r.Context(), current(r).ID, r.URL.Query().Get("service"))
	if e != nil {
		return e
	}
	u, e := a.liveService(r.Context(), t.Selection)
	if e != nil {
		return e
	}
	var data []byte
	var p Plan
	var categoryEnabled, planDeleted bool
	if e = a.DB.QueryRow(r.Context(), "SELECT to_jsonb(p),c.enabled,p.deleted_at IS NOT NULL FROM plans p JOIN categories c ON c.id=p.category_id WHERE p.id=$1", t.Plan).Scan(&data, &categoryEnabled, &planDeleted); e != nil {
		return e
	}
	if e = json.Unmarshal(data, &p); e != nil {
		return e
	}
	prices := []Price{}
	for _, price := range p.Prices {
		if price.Enabled {
			prices = append(prices, price)
		}
	}
	deadline, windowErr := renewalWindow(u, a.now())
	var used int
	if e = a.DB.QueryRow(r.Context(), inventoryCountSQL, p.ID, a.now()).Scan(&used); e != nil {
		return e
	}
	remaining := max(0, p.Stock-used)
	var stock any
	if p.Stock > 0 {
		stock = remaining
	}
	var held bool
	if e = a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM portal_effective_seats($1,$2) WHERE service_id=$3)", p.ID, a.now(), t.Selection.Order).Scan(&held); e != nil {
		return e
	}
	reason := ""
	if windowErr != nil {
		reason = windowErr.Error()
	} else if p.Stock > 0 && remaining == 0 && !held {
		reason = "套餐销售名额已满，暂不能续费"
	}
	state, _ := u["state"].(string)
	available := !planDeleted && windowErr == nil && (p.Stock == 0 || remaining > 0 || held) && categoryEnabled && !p.Simulation && p.Enabled && p.AllowRenew && len(prices) > 0 && (state == "active" || state == "expired" || state == "exhausted") && !a.now().Before(p.Starts) && a.now().Before(p.Ends)
	pending, e := jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,state,fulfillment,payable,expires_at,(SELECT state FROM manual_payments m WHERE m.order_id=o.id) manual_state FROM orders o WHERE renewal_of=$1 AND customer_id=$2 AND (state='pending' OR (state='paid' AND fulfillment<>'active')) ORDER BY created_at DESC LIMIT 10)x`, t.Selection.Order, current(r).ID)
	if e != nil {
		return e
	}
	var deadlineValue *time.Time
	if !deadline.IsZero() {
		deadlineValue = &deadline
	}
	var recurring any
	if c, found, err := a.recurringCoupon(r.Context(), current(r).ID, t.Selection.Order); err != nil {
		return err
	} else if found {
		recurring = map[string]any{"code": c.Code, "name": c.Name, "recurrence": c.Recurrence, "recurring_until": c.RecurringUntil, "active": a.recurringActive(c)}
	}
	send(w, 200, map[string]any{"recurring_coupon": recurring, "service_id": t.Selection.Order, "service": u, "plan": map[string]any{"id": p.ID, "name": p.Name, "prices": prices}, "available": available, "reason": reason, "renewal_deadline": deadlineValue, "stock_limit": p.Stock, "remaining_stock": stock, "requires_seat": !held, "pending_orders": pending, "note": "续费沿用原身份和订阅链接，有效期内续费沿用原名额；过期后重新开通需剩余名额；须在到期后 72 小时内下单并付款。只延长期限，不清零本期用量，月度配额按原重置日恢复。"})
	return nil
}
