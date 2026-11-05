package portal

import (
	"net/http"
	"time"
)

func (a *App) planLifecycle(w http.ResponseWriter, r *http.Request) error {
	if e := requireAdminScope(r, "plans"); e != nil {
		return e
	}
	var b struct {
		ID        string `json:"id"`
		Version   int    `json:"version"`
		Confirmed bool   `json:"confirmed"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if !portalID.MatchString(b.ID) || !b.Confirmed {
		return bad("请确认要删除或恢复的套餐")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	var version int
	var deleted *time.Time
	if e = tx.QueryRow(r.Context(), "SELECT version,deleted_at FROM plans WHERE id=$1 FOR UPDATE", b.ID).Scan(&version, &deleted); e != nil {
		return e
	}
	restore := r.Method == "POST"
	if version != b.Version || (restore && deleted == nil) || (!restore && deleted != nil) {
		return Problem{409, "套餐状态已变化，请刷新后重试"}
	}
	action := "plan.delete"
	message := "套餐已删除，已有订阅、订单与付款记录保留"
	if restore {
		action = "plan.restore"
		message = "套餐已恢复为下架状态，请编辑核对后上架"
		_, e = tx.Exec(r.Context(), "UPDATE plans SET deleted_at=NULL,deleted_by=NULL,enabled=false,version=version+1 WHERE id=$1", b.ID)
	} else {
		_, e = tx.Exec(r.Context(), "UPDATE plans SET deleted_at=now(),deleted_by=$2,enabled=false,version=version+1 WHERE id=$1", b.ID, current(r).ID)
	}
	if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, current(r).ID, action, b.ID); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": message})
	return nil
}
