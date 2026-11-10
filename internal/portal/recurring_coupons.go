package portal

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

// Binding is derived from paid invoices for this exact service. An unpaid or
// cancelled invoice never establishes a recurring discount, and another order
// owned by the same customer cannot inherit it.
func (a *App) recurringCoupon(ctx context.Context, user, root string) (Coupon, bool, error) {
	var c Coupon
	var data json.RawMessage
	e := a.rowQueries(ctx).QueryRow(ctx, `SELECT to_jsonb(c) FROM orders o JOIN coupons c ON c.id=o.coupon_id WHERE o.customer_id=$1 AND (o.id=$2 OR o.renewal_of=$2) AND o.state='paid' AND (o.snapshot->>'coupon_recurring'='true' OR c.recurrence<>'none') ORDER BY COALESCE(o.paid_at,o.created_at) DESC,o.created_at DESC,o.id DESC LIMIT 1`, user, root).Scan(&data)
	if e == pgx.ErrNoRows {
		return c, false, nil
	}
	if e != nil {
		return c, false, e
	}
	e = json.Unmarshal(data, &c)
	return c, e == nil, e
}
func (a *App) recurringActive(c Coupon) bool {
	return c.Enabled && (c.Recurrence == "forever" || c.Recurrence == "until" && c.RecurringUntil != nil && a.now().Before(*c.RecurringUntil))
}

func (a *App) applyCoupon(r *http.Request, tx pgx.Tx, b Buy, q *Quote, paid int) error {
	code := strings.ToUpper(strings.TrimSpace(b.Coupon))
	if code != "" && !couponPattern.MatchString(code) {
		return bad("优惠码格式不正确")
	}
	var c Coupon
	reused := false
	if b.RenewalOf != "" {
		bound, found, e := a.recurringCoupon(r.Context(), current(r).ID, b.RenewalOf)
		if e != nil {
			return e
		}
		if found && (code == "" || code == bound.Code) {
			if !a.recurringActive(bound) {
				if code != "" {
					return bad("此订阅的循环优惠已停用或到期，请重新核价")
				}
				q.CouponNote = "原循环优惠已停用或到期，本次按当前套餐原价计算"
				return nil
			}
			c = bound
			reused = true
			q.CouponAutomatic = code == ""
		}
	}
	if !reused {
		if code == "" {
			return nil
		}
		var data json.RawMessage
		e := tx.QueryRow(r.Context(), "SELECT to_jsonb(c) FROM coupons c WHERE code=$1 AND enabled AND starts_at<=$2 AND ends_at>$2", code, a.now()).Scan(&data)
		if e != nil {
			return bad("优惠码无效或不在领取有效时间内")
		}
		if e = json.Unmarshal(data, &c); e != nil {
			return e
		}
		if c.Recurrence != "none" && !a.recurringActive(c) {
			return bad("循环优惠已到期")
		}
	}
	if !eligibleScope(c.Plans, q.Plan.ID) || !eligibleScope(c.Prices, q.Price.ID) || !eligibleScope(c.Customers, current(r).ID) || !reused && c.NewOnly && paid > 0 || q.Base < c.Minimum {
		if reused && code == "" {
			q.CouponAutomatic = false
			q.CouponNote = "原循环优惠不适用于本次套餐、周期或金额，已按原价计算"
			return nil
		}
		return bad("优惠码不适用于该用户、套餐、周期或订单金额")
	}
	if !reused {
		var total, own int
		if e := tx.QueryRow(r.Context(), "SELECT count(*),count(*) FILTER(WHERE customer_id=$2) FROM orders WHERE coupon_id=$1 AND NOT coupon_reused AND state IN "+couponCountStates, c.ID, current(r).ID).Scan(&total, &own); e != nil {
			return e
		}
		if c.MaxUses > 0 && total >= c.MaxUses || own >= c.PerCustomer {
			return Problem{409, "优惠码领取名额已满或达到个人次数上限"}
		}
	}
	q.Discount = c.Amount
	if c.Kind == "percent" {
		q.Discount = q.Base * c.Amount / 10000
	}
	if c.Maximum > 0 {
		q.Discount = min(q.Discount, c.Maximum)
	}
	q.Discount = min(q.Discount, q.Base-1)
	q.Payable = q.Base - q.Discount
	q.CouponID = c.ID
	q.CouponCode = c.Code
	q.CouponReused = reused
	q.CouponRecurring = c.Recurrence != "none"
	q.CouponRecurrence = c.Recurrence
	q.CouponRecurringUntil = c.RecurringUntil
	if !reused && c.Ends.Before(q.Expires) {
		q.Expires = c.Ends
	}
	if c.Recurrence == "until" && c.RecurringUntil != nil && c.RecurringUntil.Before(q.Expires) {
		q.Expires = *c.RecurringUntil
	}
	if reused {
		q.CouponNote = "已沿用此订阅的循环优惠，本次续费不重复占用领取次数"
	} else if q.CouponRecurring {
		q.CouponNote = "本次付款生效后，此订阅后续续费自动应用循环优惠"
	}
	return nil
}
