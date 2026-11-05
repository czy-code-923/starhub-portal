package portal

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

// Customer deletion only removes an inactive service from their list. It never
// deletes the identity, orders or accounting ledger, and is recoverable.
func (a *App) serviceVisibility(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !portalID.MatchString(id) {
		return bad("订阅编号无效")
	}
	var b struct {
		Confirmed bool `json:"confirmed"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if !b.Confirmed {
		return bad("请确认移除或恢复此订阅记录")
	}
	var s serviceSelection
	var hidden *time.Time
	if e := a.DB.QueryRow(r.Context(), "SELECT order_id,control_user_id,subscription_id,hidden_at FROM service_accounts WHERE order_id=$1 AND customer_id=$2", id, current(r).ID).Scan(&s.Order, &s.User, &s.Subscription, &hidden); e != nil {
		return e
	}
	restore := r.Method == "POST"
	if !restore && hidden == nil {
		u, e := a.liveService(r.Context(), s)
		if e != nil {
			return e
		}
		state, _ := u["state"].(string)
		if !inactiveService(state) {
			return Problem{409, "有效订阅不能移除，请仅移除已失效或已删除的记录"}
		}
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM service_accounts WHERE order_id=$1 AND customer_id=$2)", id, current(r).ID).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return pgx.ErrNoRows
	}
	action := "service.hide"
	message := "订阅记录已移除，可在已移除记录中恢复；订单和付款记录保留"
	if restore {
		action = "service.restore"
		message = "订阅记录已恢复显示"
		_, e = tx.Exec(r.Context(), "UPDATE service_accounts SET hidden_at=NULL WHERE order_id=$1 AND customer_id=$2", id, current(r).ID)
	} else {
		var pending bool
		if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM orders WHERE renewal_of=$1 AND (state='pending' OR (state='paid' AND fulfillment<>'active')))", id).Scan(&pending); e != nil {
			return e
		}
		if pending {
			return Problem{409, "此订阅还有待处理续费，请先处理续费订单"}
		}
		tag, err := tx.Exec(r.Context(), "UPDATE service_accounts SET hidden_at=COALESCE(hidden_at,now()) WHERE order_id=$1 AND customer_id=$2 AND remote_state IN ('expired','deleted','disabled','invalid','exhausted')", id, current(r).ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return Problem{409, "订阅状态已变化，请刷新后核对"}
		}
	}
	if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, current(r).ID, action, id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": message})
	return nil
}
