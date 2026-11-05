package portal

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

const renewalGrace = 72 * time.Hour
const inventoryCountSQL = "SELECT count(*) FROM orders WHERE plan_id=$1 AND state IN ('pending','paid','refund_pending')"

func renewalWindow(u map[string]any, now time.Time) (time.Time, error) {
	state, _ := u["state"].(string)
	if state != "active" && state != "expired" && state != "exhausted" {
		return time.Time{}, Problem{409, "订阅已删除或停用，不能续费"}
	}
	value, _ := u["expires_at"].(string)
	expiry, e := time.Parse(time.RFC3339Nano, value)
	if e != nil {
		return time.Time{}, Problem{503, "订阅到期时间暂未同步，请稍后重试"}
	}
	deadline := expiry.Add(renewalGrace)
	if !now.Before(deadline) {
		return deadline, Problem{409, "订阅已过期超过 3 天，不能续费"}
	}
	return deadline, nil
}

// Orders reserve a sales slot at creation. Rechecking a reserved invoice must
// not count its own slot as unavailable; only new order creation checks stock.
func checkRenewalInvoice(ctx context.Context, tx pgx.Tx, order string, at time.Time) error {
	var renewal string
	var deadline *time.Time
	if e := tx.QueryRow(ctx, "SELECT COALESCE(renewal_of,''),renewal_deadline_at FROM orders WHERE id=$1", order).Scan(&renewal, &deadline); e != nil {
		return e
	}
	if renewal != "" && (deadline == nil || !at.Before(*deadline)) {
		return Problem{http.StatusConflict, "续费付款已超过到期后 3 天的截止时间，请勿继续付款"}
	}
	return nil
}
