package portal

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Buy struct {
	PlanID  string `json:"plan_id"`
	PriceID string `json:"price_id"`
	Coupon  string `json:"coupon"`
	Key     string `json:"request_key"`
}
type Quote struct {
	Plan       Plan      `json:"-"`
	Price      Price     `json:"-"`
	CouponID   string    `json:"-"`
	Base       int64     `json:"base_amount"`
	Discount   int64     `json:"discount_amount"`
	Payable    int64     `json:"payable"`
	Expires    time.Time `json:"expires_at"`
	CouponCode string    `json:"coupon_code"`
	PlanName   string    `json:"plan_name"`
	Months     int       `json:"months"`
	Quota      int64     `json:"quota_bytes"`
}

func (a *App) catalog(w http.ResponseWriter, r *http.Request) error {
	s, _, e := a.settings(r.Context())
	if e != nil {
		return e
	}
	if !s.ShowCatalog {
		cookie, e := r.Cookie("sp_session")
		if e != nil {
			return Problem{401, "请登录后查看套餐"}
		}
		var valid bool
		if e = a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions s JOIN customers c ON c.id=s.customer_id WHERE s.token_hash=$1 AND s.expires_at>$2 AND c.status='active')`, hash(cookie.Value), a.now()).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return Problem{401, "请登录后查看套餐"}
		}
	}
	cats, e := jsonRows(r.Context(), a.DB, "SELECT COALESCE(jsonb_agg(c ORDER BY c.sort,c.name),'[]') FROM categories c WHERE enabled")
	if e != nil {
		return e
	}
	rows, e := a.DB.Query(r.Context(), `SELECT to_jsonb(p) FROM plans p JOIN categories c ON c.id=p.category_id WHERE p.enabled AND c.enabled AND p.starts_at<=$1 AND p.ends_at>$1 ORDER BY p.sort,p.name LIMIT 100`, a.now())
	if e != nil {
		return e
	}
	defer rows.Close()
	plans := []any{}
	for rows.Next() {
		var raw json.RawMessage
		var p Plan
		if e = rows.Scan(&raw); e != nil {
			return e
		}
		if e = json.Unmarshal(raw, &p); e != nil {
			return e
		}
		prices := []Price{}
		for _, pr := range p.Prices {
			if pr.Enabled {
				prices = append(prices, pr)
			}
		}
		plans = append(plans, map[string]any{"id": p.ID, "category_id": p.CategoryID, "name": p.Name, "description": p.Description, "recommended": p.Recommended && !a.now().Before(p.RecommendStarts) && a.now().Before(p.RecommendEnds), "quota_bytes": p.Quota, "prices": prices, "protocols": p.Protocols, "max_devices": p.MaxDevices, "billing_mode": p.Billing, "multiplier_milli": p.Multiplier, "allow_new": p.AllowNew, "allow_renew": p.AllowRenew, "simulation": p.Simulation})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	send(w, 200, map[string]any{"categories": cats, "plans": plans})
	return nil
}
func (a *App) calculate(r *http.Request, tx pgx.Tx, b Buy) (Quote, error) {
	q := Quote{}
	var data json.RawMessage
	e := tx.QueryRow(r.Context(), `SELECT to_jsonb(p) FROM plans p JOIN categories c ON c.id=p.category_id WHERE p.id=$1 AND p.enabled AND c.enabled AND p.starts_at<=$2 AND p.ends_at>$2`, b.PlanID, a.now()).Scan(&data)
	if e != nil {
		return q, bad("套餐未上架或不在销售期")
	}
	if e = json.Unmarshal(data, &q.Plan); e != nil {
		return q, e
	}
	p := q.Plan
	if p.Simulation {
		return q, Problem{409, "模拟套餐仅供展示，需管理员绑定可计费用户组并转换为正式套餐后购买"}
	}
	found := false
	for _, price := range p.Prices {
		if price.ID == b.PriceID && price.Enabled {
			q.Price = price
			found = true
		}
	}
	if !found {
		return q, bad("价格选项不可用")
	}
	var paid int
	var lastPlan string
	if e = tx.QueryRow(r.Context(), "SELECT count(*) FROM orders WHERE customer_id=$1 AND state IN ('paid','refund_pending','refunded')", current(r).ID).Scan(&paid); e != nil {
		return q, e
	}
	if paid == 0 && !p.AllowNew || paid > 0 && !p.AllowRenew {
		return q, bad("套餐不允许本次购买类型")
	}
	e = tx.QueryRow(r.Context(), "SELECT plan_id FROM service_accounts WHERE customer_id=$1", current(r).ID).Scan(&lastPlan)
	if e != nil && e != pgx.ErrNoRows {
		return q, e
	}
	if e == nil && lastPlan != p.ID {
		return q, bad("已有其他套餐，请联系管理员安排套餐切换")
	}
	if p.Stock > 0 {
		var used int
		if e = tx.QueryRow(r.Context(), "SELECT count(*) FROM orders WHERE plan_id=$1 AND state IN ('pending','paid','refund_pending')", p.ID).Scan(&used); e != nil {
			return q, e
		}
		if used >= p.Stock {
			return q, Problem{409, "套餐销售名额已满"}
		}
	}
	if e = a.checkGroups(r.Context(), p.Groups, p.Protocols); e != nil {
		return q, e
	}
	q.Base = q.Price.Amount
	q.Payable = q.Base
	q.Expires = a.now().Add(15 * time.Minute)
	if p.Ends.Before(q.Expires) {
		q.Expires = p.Ends
	}
	q.PlanName = p.Name
	q.Months = q.Price.Months
	q.Quota = p.Quota
	code := strings.ToUpper(strings.TrimSpace(b.Coupon))
	if code != "" {
		if !couponPattern.MatchString(code) {
			return q, bad("优惠码格式不正确")
		}
		var c Coupon
		e = tx.QueryRow(r.Context(), "SELECT to_jsonb(c) FROM coupons c WHERE code=$1 AND enabled AND starts_at<=$2 AND ends_at>$2", code, a.now()).Scan(&data)
		if e != nil {
			return q, bad("优惠码无效或不在有效时间内")
		}
		if e = json.Unmarshal(data, &c); e != nil {
			return q, e
		}
		if !eligibleScope(c.Plans, p.ID) || !eligibleScope(c.Prices, q.Price.ID) || !eligibleScope(c.Customers, current(r).ID) || c.NewOnly && paid > 0 || q.Base < c.Minimum {
			return q, bad("优惠码不适用于该用户、套餐、周期或订单金额")
		}
		var total, own int
		if e = tx.QueryRow(r.Context(), "SELECT count(*),count(*) FILTER(WHERE customer_id=$2) FROM orders WHERE coupon_id=$1 AND state IN ('pending','paid','refund_pending','refunded')", c.ID, current(r).ID).Scan(&total, &own); e != nil {
			return q, e
		}
		if c.MaxUses > 0 && total >= c.MaxUses || own >= c.PerCustomer {
			return q, Problem{409, "优惠码使用名额已满或达到个人次数上限"}
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
		if c.Ends.Before(q.Expires) {
			q.Expires = c.Ends
		}
	}
	if q.Expires.Sub(a.now()) < time.Minute {
		return q, bad("活动即将结束，已不足一个有效支付窗口")
	}
	return q, nil
}
func (a *App) quote(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "quote", 60); e != nil {
		return e
	}
	var b Buy
	if e := decode(w, r, &b); e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	q, e := a.calculate(r, tx, b)
	if e != nil {
		return e
	}
	send(w, 200, q)
	return nil
}
func (a *App) createOrder(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "order", 20); e != nil {
		return e
	}
	var b Buy
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if len(b.Key) < 16 || len(b.Key) > 100 {
		return bad("缺少有效订单幂等键")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	requestHash := hash(string(raw([]string{b.PlanID, b.PriceID, strings.ToUpper(strings.TrimSpace(b.Coupon))})))
	var oldID, oldHash string
	e = tx.QueryRow(r.Context(), "SELECT id,request_hash FROM orders WHERE customer_id=$1 AND request_key=$2", current(r).ID, b.Key).Scan(&oldID, &oldHash)
	if e == nil {
		if oldHash != requestHash {
			return Problem{409, "订单请求键已用于其他内容"}
		}
		send(w, 200, map[string]string{"id": oldID})
		return nil
	}
	if e != pgx.ErrNoRows {
		return e
	}
	var pending int
	if e = tx.QueryRow(r.Context(), "SELECT count(*) FROM orders WHERE customer_id=$1 AND state='pending'", current(r).ID).Scan(&pending); e != nil {
		return e
	}
	if pending >= 2 {
		return Problem{409, "请先支付或取消待支付订单"}
	}
	q, e := a.calculate(r, tx, b)
	if e != nil {
		return e
	}
	id := ID()
	snapshot := map[string]any{"plan": q.Plan, "price": q.Price, "coupon_code": q.CouponCode, "discount_amount": q.Discount}
	_, e = tx.Exec(r.Context(), `INSERT INTO orders(id,customer_id,plan_id,price_id,snapshot,base_amount,discount_amount,payable,coupon_id,expires_at,request_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11,$12)`, id, current(r).ID, b.PlanID, b.PriceID, raw(snapshot), q.Base, q.Discount, q.Payable, q.CouponID, q.Expires, b.Key, requestHash)
	if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, current(r).ID, "order.create", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 201, map[string]string{"id": id})
	return nil
}
func (a *App) orders(w http.ResponseWriter, r *http.Request) error {
	data, e := jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,plan_id,price_id,(SELECT state FROM manual_payments m WHERE m.order_id=o.id) manual_state,snapshot->'plan'->>'name' plan_name,base_amount,discount_amount,payable,state,fulfillment,expires_at,paid_at,created_at FROM orders o WHERE customer_id=$1 ORDER BY created_at DESC LIMIT 100)x`, current(r).ID)
	if e != nil {
		return e
	}
	send(w, 200, data)
	return nil
}
func (a *App) order(w http.ResponseWriter, r *http.Request) error {
	data, e := jsonRows(r.Context(), a.DB, `SELECT jsonb_build_object('id',id,'plan_name',snapshot->'plan'->>'name','base_amount',base_amount,'discount_amount',discount_amount,'payable',payable,'coupon_code',snapshot->>'coupon_code','state',state,'fulfillment',fulfillment,'expires_at',expires_at,'paid_at',paid_at) FROM orders WHERE id=$1 AND customer_id=$2`, r.PathValue("id"), current(r).ID)
	if e != nil {
		return e
	}
	send(w, 200, data)
	return nil
}
func (a *App) cancelOrder(w http.ResponseWriter, r *http.Request) error {
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	var state string
	var payment bool
	if e = tx.QueryRow(r.Context(), "SELECT state,EXISTS(SELECT 1 FROM payments p WHERE p.order_id=o.id) FROM orders o WHERE id=$1 AND customer_id=$2 FOR UPDATE", r.PathValue("id"), current(r).ID).Scan(&state, &payment); e != nil {
		return e
	}
	if state != "pending" {
		return bad("只有待支付订单可以取消")
	}
	var manualState string
	e = tx.QueryRow(r.Context(), "SELECT state FROM manual_payments WHERE order_id=$1", r.PathValue("id")).Scan(&manualState)
	if e != nil && e != pgx.ErrNoRows {
		return e
	}
	if manualState == "submitted" {
		return Problem{409, "已提交付款凭证，等待核款期间不能自行取消；如有疑问请创建工单"}
	}
	if manualState == "awaiting" || manualState == "rejected" {
		if _, e = tx.Exec(r.Context(), "UPDATE manual_payments SET state='cancelled',version=version+1 WHERE order_id=$1", r.PathValue("id")); e != nil {
			return e
		}
	}
	if payment {
		_, e = tx.Exec(r.Context(), "UPDATE orders SET expires_at=LEAST(expires_at,$2) WHERE id=$1", r.PathValue("id"), a.now())
	} else {
		_, e = tx.Exec(r.Context(), "UPDATE orders SET state='cancelled' WHERE id=$1", r.PathValue("id"))
	}
	if e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 202, map[string]string{"message": "取消已提交；已发起支付的订单将在确认渠道状态后释放优惠名额"})
	return nil
}
