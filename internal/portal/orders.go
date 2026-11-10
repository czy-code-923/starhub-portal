package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Buy struct {
	RenewalOf string `json:"renewal_of,omitempty"`
	PlanID    string `json:"plan_id"`
	PriceID   string `json:"price_id"`
	Coupon    string `json:"coupon"`
	Key       string `json:"request_key"`
}
type Quote struct {
	Plan            Plan       `json:"-"`
	Price           Price      `json:"-"`
	CouponID        string     `json:"-"`
	Base            int64      `json:"base_amount"`
	Discount        int64      `json:"discount_amount"`
	Payable         int64      `json:"payable"`
	Expires         time.Time  `json:"expires_at"`
	CouponCode      string     `json:"coupon_code"`
	PlanName        string     `json:"plan_name"`
	Months          int        `json:"months"`
	Quota           int64      `json:"quota_bytes"`
	RenewalDeadline *time.Time `json:"renewal_deadline,omitempty"`
}

func (a *App) catalog(w http.ResponseWriter, r *http.Request) error {
	s, secret, e := a.settings(r.Context())
	if e != nil {
		return e
	}
	if !s.ShowCatalog {
		cookie, e := r.Cookie("sp_session")
		if e != nil {
			return Problem{401, "请登录后查看套餐"}
		}
		var valid bool
		if e = a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions s JOIN customers c ON c.id=s.customer_id WHERE s.token_hash=$1 AND s.expires_at>$2 AND c.status='active' AND c.deleted_at IS NULL)`, hash(cookie.Value), a.now()).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return Problem{401, "请登录后查看套餐"}
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), controlSecretKey{}, secret))
	healthErr := a.refreshServiceStates(r.Context())
	groupMetadata, groupErr := a.commercialGroups(r.Context(), false)
	cats, e := jsonRows(r.Context(), a.DB, "SELECT COALESCE(jsonb_agg(c ORDER BY c.sort,c.name),'[]') FROM categories c WHERE enabled")
	if e != nil {
		return e
	}
	rows, e := a.DB.Query(r.Context(), `SELECT to_jsonb(p),(SELECT count(*) FROM portal_effective_seats(p.id,$1)) FROM plans p JOIN categories c ON c.id=p.category_id WHERE p.deleted_at IS NULL AND p.enabled AND c.enabled AND p.starts_at<=$1 AND p.ends_at>$1 ORDER BY p.sort,p.name LIMIT 100`, a.now())
	if e != nil {
		return e
	}
	defer rows.Close()
	plans := []any{}
	for rows.Next() {
		var raw json.RawMessage
		var stockUsed int
		var p Plan
		if e = rows.Scan(&raw, &stockUsed); e != nil {
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
		var remaining any
		if p.Stock > 0 {
			remaining = max(0, p.Stock-stockUsed)
		}
		plans = append(plans, map[string]any{"effective_members": stockUsed, "stock_limit": p.Stock, "remaining_stock": remaining, "sold_out": p.Stock > 0 && stockUsed >= p.Stock, "id": p.ID, "category_id": p.CategoryID, "name": p.Name, "description": p.Description, "recommended": p.Recommended && !a.now().Before(p.RecommendStarts) && a.now().Before(p.RecommendEnds), "quota_bytes": p.Quota, "quota_unit": p.QuotaUnit, "display_features": p.DisplayFeatures, "prices": prices, "protocols": selectedProtocols(groupMetadata, p.Groups, p.Protocols), "protocols_verified": groupErr == nil, "max_devices": p.MaxDevices, "allow_new": p.AllowNew, "allow_renew": p.AllowRenew, "simulation": p.Simulation})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	guideTitle, guideBody := s.catalogGuide()
	send(w, 200, map[string]any{"categories": cats, "plans": plans, "catalog_guide_title": guideTitle, "catalog_guide_body": guideBody, "stock_checked_at": a.now(), "stock_synced": healthErr == nil})
	return nil
}
func (a *App) calculate(r *http.Request, tx pgx.Tx, b Buy) (Quote, error) {
	r = r.WithContext(context.WithValue(r.Context(), transactionKey{}, tx))
	q := Quote{}
	var data json.RawMessage
	e := tx.QueryRow(r.Context(), `SELECT to_jsonb(p) FROM plans p JOIN categories c ON c.id=p.category_id WHERE p.id=$1 AND p.deleted_at IS NULL AND p.enabled AND c.enabled AND p.starts_at<=$2 AND p.ends_at>$2`, b.PlanID, a.now()).Scan(&data)
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
	if e = tx.QueryRow(r.Context(), "SELECT count(*) FROM orders WHERE customer_id=$1 AND state IN ('paid','refund_pending','refunded')", current(r).ID).Scan(&paid); e != nil {
		return q, e
	}
	if b.RenewalOf != "" {
		target, e := a.renewalTarget(r.Context(), current(r).ID, b.RenewalOf)
		if e != nil {
			return q, e
		}
		if target.Plan != p.ID {
			return q, bad("续费必须使用原订阅套餐")
		}
		if !p.AllowRenew {
			return q, bad("套餐未开放续费")
		}
		u, e := a.liveService(r.Context(), target.Selection)
		if e != nil {
			return q, e
		}
		deadline, err := renewalWindow(u, a.now())
		if err != nil {
			return q, err
		}
		q.RenewalDeadline = &deadline
	} else {
		if !p.AllowNew {
			return q, bad("套餐暂不接受新订阅，请从已有订阅续费")
		}

		if e = a.checkGroups(r.Context(), p.Groups, p.Protocols); e != nil {
			return q, e
		}
	}
	needsSeat := true
	if b.RenewalOf != "" {
		var held bool
		if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM portal_effective_seats($1,$2) WHERE service_id=$3)", p.ID, a.now(), b.RenewalOf).Scan(&held); e != nil {
			return q, e
		}
		needsSeat = !held
	}
	if p.Stock > 0 && needsSeat {
		var used int
		if e = tx.QueryRow(r.Context(), inventoryCountSQL, p.ID, a.now()).Scan(&used); e != nil {
			return q, e
		}
		if used >= p.Stock {
			return q, Problem{409, "套餐有效人数已达上限，新购或过期后重新开通需要剩余名额"}
		}
	}
	q.Base = q.Price.Amount
	q.Payable = q.Base
	q.Expires = a.now().Add(15 * time.Minute)
	if q.RenewalDeadline != nil && q.RenewalDeadline.Before(q.Expires) {
		q.Expires = *q.RenewalDeadline
	}
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
		if e = tx.QueryRow(r.Context(), "SELECT count(*),count(*) FILTER(WHERE customer_id=$2) FROM orders WHERE coupon_id=$1 AND state IN "+couponCountStates, c.ID, current(r).ID).Scan(&total, &own); e != nil {
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
	var preload error
	r, preload = a.withControlSecrets(r)
	if preload != nil {
		return preload
	}
	if e := a.limit(r, "quote", 60); e != nil {
		return e
	}
	var b Buy
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if e := a.refreshServiceStates(r.Context(), true); e != nil {
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
	var preload error
	r, preload = a.withControlSecrets(r)
	if preload != nil {
		return preload
	}
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
	if e := a.refreshServiceStates(r.Context(), true); e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	requestFields := []string{b.PlanID, b.PriceID, strings.ToUpper(strings.TrimSpace(b.Coupon))}
	if b.RenewalOf != "" {
		if !portalID.MatchString(b.RenewalOf) {
			return bad("续费服务编号无效")
		}
		requestFields = append(requestFields, b.RenewalOf)
	}
	requestHash := hash(string(raw(requestFields)))
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
	if b.RenewalOf == "" {
		capacity, err := a.capacity(r.Context())
		if err != nil {
			return err
		}
		remaining, ok := capacity["remaining_identities"].(float64)
		if !ok {
			return Problem{503, "控制面容量信息无效，请先升级控制面"}
		}
		var reserved int
		if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM orders WHERE renewal_of IS NULL AND (state='pending' OR (state='paid' AND fulfillment<>'active' AND per_order))").Scan(&reserved); err != nil {
			return err
		}
		if int(remaining) <= reserved {
			return Problem{409, "可用独立订阅名额已被已付或待付订单预占，请稍后再试"}
		}
	} else {
		var pendingRenewal bool
		if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM orders WHERE renewal_of=$1 AND (state='pending' OR (state='paid' AND fulfillment<>'active')))", b.RenewalOf).Scan(&pendingRenewal); e != nil {
			return e
		}
		if pendingRenewal {
			return Problem{409, "此订阅已有待付款或待生效续费，请先处理原订单"}
		}
	}
	id := ID()
	snapshot := map[string]any{"plan": q.Plan, "price": q.Price, "coupon_code": q.CouponCode, "discount_amount": q.Discount}
	_, e = tx.Exec(r.Context(), `INSERT INTO orders(id,customer_id,plan_id,price_id,snapshot,base_amount,discount_amount,payable,coupon_id,expires_at,request_key,request_hash,renewal_of,renewal_deadline_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11,$12,NULLIF($13,''),$14)`, id, current(r).ID, b.PlanID, b.PriceID, raw(snapshot), q.Base, q.Discount, q.Payable, q.CouponID, q.Expires, b.Key, requestHash, b.RenewalOf, q.RenewalDeadline)
	if e != nil {
		return e
	}
	if e = queueTelegram(r.Context(), tx, "order:"+id, id, "order.created"); e != nil {
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
	_ = a.refreshServiceStates(r.Context())
	data, e := jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,plan_id,price_id,renewal_of,COALESCE(renewal_of,id) service_id,COALESCE((SELECT s.hidden_at IS NOT NULL FROM service_accounts s WHERE s.order_id=COALESCE(o.renewal_of,o.id)),false) subscription_hidden,(SELECT s.remote_state FROM service_accounts s WHERE s.order_id=COALESCE(o.renewal_of,o.id)) service_state,(SELECT s.remote_checked_at FROM service_accounts s WHERE s.order_id=COALESCE(o.renewal_of,o.id)) service_checked_at,EXISTS(SELECT 1 FROM service_accounts s WHERE s.order_id=COALESCE(o.renewal_of,o.id)) has_subscription,(SELECT state FROM manual_payments m WHERE m.order_id=o.id) manual_state,snapshot->'plan'->>'name' plan_name,base_amount,discount_amount,payable,state,fulfillment,expires_at,paid_at,created_at FROM orders o WHERE customer_id=$1 ORDER BY created_at DESC LIMIT 100)x`, current(r).ID)
	if e != nil {
		return e
	}
	send(w, 200, data)
	return nil
}
func (a *App) order(w http.ResponseWriter, r *http.Request) error {
	data, e := jsonRows(r.Context(), a.DB, `SELECT jsonb_build_object('id',id,'renewal_of',renewal_of,'service_id',COALESCE(renewal_of,id),'plan_name',snapshot->'plan'->>'name','base_amount',base_amount,'discount_amount',discount_amount,'payable',payable,'coupon_code',snapshot->>'coupon_code','state',state,'fulfillment',fulfillment,'expires_at',expires_at,'paid_at',paid_at) FROM orders WHERE id=$1 AND customer_id=$2`, r.PathValue("id"), current(r).ID)
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
