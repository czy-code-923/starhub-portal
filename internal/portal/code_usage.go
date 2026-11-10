package portal

import (
	"net/http"
	"strconv"
)

// Keep this predicate aligned with calculate: pending orders reserve a use;
// refunded orders do not restore promotional eligibility.
const couponCountStates = "('pending','paid','refund_pending','refunded')"
const couponCountsSQL = `SELECT count(*) FILTER(WHERE state IN ` + couponCountStates + `) used_uses,count(*) FILTER(WHERE state='pending') reserved_uses,count(*) FILTER(WHERE state='paid') paid_uses,count(*) FILTER(WHERE state IN ('cancelled','late_paid')) released_uses FROM orders WHERE coupon_id=c.id`
const couponListSQL = `SELECT COALESCE(jsonb_agg(x ORDER BY x.code),'[]') FROM(SELECT c.*,u.*,CASE WHEN c.max_uses=0 THEN NULL ELSE GREATEST(0,c.max_uses-u.used_uses) END remaining_uses FROM coupons c CROSS JOIN LATERAL(` + couponCountsSQL + `)u)x`

func (a *App) codeUsage(w http.ResponseWriter, r *http.Request, kind string) error {
	if e := requireAdminScope(r, kind); e != nil {
		return e
	}
	id := r.PathValue("id")
	if !portalID.MatchString(id) {
		return bad("代码 ID 无效")
	}
	offset := 0
	if value := r.URL.Query().Get("offset"); value != "" {
		var e error
		offset, e = strconv.Atoi(value)
		if e != nil || offset < 0 || offset > 1000000 {
			return bad("分页位置无效")
		}
	}
	var query string
	if kind == "coupons" {
		query = `SELECT jsonb_build_object('total',(SELECT count(*) FROM orders WHERE coupon_id=c.id),'summary',jsonb_build_object('used_uses',u.used_uses,'reserved_uses',u.reserved_uses,'paid_uses',u.paid_uses,'released_uses',u.released_uses,'max_uses',c.max_uses,'remaining_uses',CASE WHEN c.max_uses=0 THEN NULL ELSE GREATEST(0,c.max_uses-u.used_uses) END),'items',(SELECT COALESCE(jsonb_agg(x ORDER BY x.created_at DESC,x.id DESC),'[]') FROM(SELECT o.id,o.customer_id,o.created_at,o.paid_at,o.state,o.payable,o.discount_amount,cust.email,cust.name,cust.deleted_at customer_deleted_at,(o.state IN ` + couponCountStates + `) counts_toward_limit FROM orders o JOIN customers cust ON cust.id=o.customer_id WHERE o.coupon_id=c.id ORDER BY o.created_at DESC,o.id DESC LIMIT 25 OFFSET $2)x)) FROM coupons c CROSS JOIN LATERAL(` + couponCountsSQL + `)u WHERE c.id=$1`
	} else {
		query = `SELECT jsonb_build_object('total',(SELECT count(*) FROM invitation_claims WHERE invitation_id=i.id),'summary',jsonb_build_object('used_uses',i.claimed,'max_uses',i.max_uses,'remaining_uses',GREATEST(0,i.max_uses-i.claimed),'verified_uses',(SELECT count(*) FROM invitation_claims WHERE invitation_id=i.id AND finalized AND NOT released),'released_uses',(SELECT count(*) FROM invitation_claims WHERE invitation_id=i.id AND released)),'items',(SELECT COALESCE(jsonb_agg(x ORDER BY x.created_at DESC,x.id DESC),'[]') FROM(SELECT c.id,c.email,c.name,c.status,c.deleted_at customer_deleted_at,c.created_at,c.email_verified_at,ic.finalized,ic.released,ic.expires_at FROM invitation_claims ic JOIN customers c ON c.id=ic.customer_id WHERE ic.invitation_id=i.id ORDER BY c.created_at DESC,c.id DESC LIMIT 25 OFFSET $2)x)) FROM invitations i WHERE i.id=$1`
	}
	var data map[string]any
	if e := a.DB.QueryRow(r.Context(), query, id, offset).Scan(&data); e != nil {
		return e
	}
	data["offset"], data["limit"] = offset, 25
	send(w, 200, data)
	return nil
}
