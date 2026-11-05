package portal

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
)

type serviceSelection struct {
	Order, User, Subscription, Unit string
	Legacy                          bool
}

func (a *App) selectedService(r *http.Request) (serviceSelection, error) {
	var s serviceSelection
	id := r.URL.Query().Get("service")
	if id != "" && !portalID.MatchString(id) {
		return s, bad("订阅服务编号无效")
	}
	e := a.DB.QueryRow(r.Context(), `SELECT s.order_id,s.control_user_id,s.subscription_id,COALESCE(o.snapshot->'plan'->>'quota_unit','GB'),s.legacy_combined FROM service_accounts s JOIN orders o ON o.id=s.order_id WHERE s.customer_id=$1 AND s.hidden_at IS NULL AND o.state='paid' AND ($2='' OR s.order_id=$2) ORDER BY s.updated_at DESC,s.order_id DESC LIMIT 1`, current(r).ID, id).Scan(&s.Order, &s.User, &s.Subscription, &s.Unit, &s.Legacy)
	if e == pgx.ErrNoRows && id != "" {
		return s, Problem{404, "未找到属于当前用户的订阅服务"}
	}
	return s, e
}
func (a *App) services(w http.ResponseWriter, r *http.Request) error {
	_ = a.refreshServiceStates(r.Context())
	hidden := r.URL.Query().Get("hidden") == "1"
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		var err error
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 10000000 {
			return bad("分页位置无效")
		}
	}
	rows, e := jsonRows(r.Context(), a.DB, `SELECT jsonb_build_object('items',(SELECT COALESCE(jsonb_agg(x ORDER BY x.created_at DESC,x.id DESC),'[]') FROM(SELECT s.order_id id,s.order_id,s.legacy_combined,s.remote_state service_state,s.remote_checked_at,s.hidden_at,s.remote_summary->'expires_at' expires_at,o.snapshot->'plan'->>'name' plan_name,COALESCE(o.snapshot->'plan'->>'quota_unit','GB') quota_unit,o.snapshot->'plan'->'quota_bytes' quota_bytes,o.paid_at,o.created_at FROM service_accounts s JOIN orders o ON o.id=s.order_id WHERE s.customer_id=$1 AND (s.hidden_at IS NOT NULL)=$3 AND o.state='paid' ORDER BY o.created_at DESC,o.id DESC LIMIT 25 OFFSET $2)x),'total',(SELECT count(*) FROM service_accounts s JOIN orders o ON o.id=s.order_id WHERE s.customer_id=$1 AND (s.hidden_at IS NOT NULL)=$3 AND o.state='paid'),'offset',$2::int,'limit',25)`, current(r).ID, offset, hidden)
	if e != nil {
		return e
	}
	send(w, 200, rows)
	return nil
}
