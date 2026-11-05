package portal

import (
	"net/http"
	"time"
)

// Archive accounts instead of cascading through orders and reconciliation ledgers.
// Restoring an account never automatically restores its access permissions.
func (a *App) deleteAccount(w http.ResponseWriter, r *http.Request) error {
	kind := r.PathValue("kind")
	if kind != "administrators" && kind != "customers" {
		return bad("无效账户类型")
	}
	admin := kind == "administrators"
	if admin {
		if e := requireOwner(r); e != nil {
			return e
		}
	} else {
		if e := requireAdminScope(r, "delete_customers"); e != nil {
			return e
		}
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
		return bad("请确认待删除的账户")
	}
	if b.ID == current(r).ID {
		return Problem{403, "不能删除自己的账户"}
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	var role string
	var owner, deleted, verified bool
	var email string
	var version int
	e = tx.QueryRow(ctx, "SELECT role,is_owner,deleted_at IS NOT NULL,admin_version,email_verified_at IS NOT NULL,email FROM customers WHERE id=$1 FOR UPDATE", b.ID).Scan(&role, &owner, &deleted, &version, &verified, &email)
	if e != nil {
		return e
	}
	if owner || admin != (role == "admin") {
		return Problem{403, "不能通过此入口删除该账户"}
	}
	restore := r.Method == "POST"
	if restore && !deleted || !restore && deleted {
		return Problem{409, "账户状态已变化，请刷新"}
	}
	if version != b.Version {
		return Problem{409, "账户资料已变化，请刷新后核对"}
	}
	action := "account.delete"
	if restore {
		action = "account.restore"
		_, e = tx.Exec(ctx, "UPDATE customers SET deleted_at=NULL,deleted_by=NULL,status=CASE WHEN email_verified_at IS NULL THEN 'unverified' ELSE 'disabled' END,admin_version=admin_version+1 WHERE id=$1", b.ID)
	} else {
		_, e = tx.Exec(ctx, "UPDATE customers SET deleted_at=now(),deleted_by=$2,status='disabled',admin_version=admin_version+1 WHERE id=$1", b.ID, current(r).ID)
	}
	if e != nil {
		return e
	}
	for _, q := range []string{"DELETE FROM sessions WHERE customer_id=$1", "UPDATE email_tokens SET used_at=now() WHERE customer_id=$1 AND used_at IS NULL", "UPDATE telegram_confirmations SET used_at=now() WHERE administrator_id=$1 AND used_at IS NULL"} {
		if _, e = tx.Exec(ctx, q, b.ID); e != nil {
			return e
		}
	}
	if restore && !verified {
		if e = a.queueToken(ctx, tx, b.ID, email, "verify", 24*time.Hour); e != nil {
			return e
		}
	}
	// Release an unverified registration's invitation reservation exactly once.
	if !restore {
		if _, e = tx.Exec(ctx, `WITH released AS(UPDATE invitation_claims SET released=true WHERE customer_id=$1 AND NOT finalized AND NOT released RETURNING invitation_id) UPDATE invitations i SET claimed=GREATEST(0,claimed-1) FROM released r WHERE i.id=r.invitation_id`, b.ID); e != nil {
			return e
		}
	}
	needsAccess := !admin
	if admin {
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM service_accounts WHERE customer_id=$1) OR EXISTS(SELECT 1 FROM orders WHERE customer_id=$1 AND state='paid')", b.ID).Scan(&needsAccess); e != nil {
			return e
		}
	}
	if needsAccess {
		if _, e = tx.Exec(ctx, `INSERT INTO jobs(id,kind,subject) VALUES($1,'access',$2) ON CONFLICT(kind,subject) DO UPDATE SET state='queued',attempts=0,available_at=now(),detail='',updated_at=now()`, ID(), b.ID); e != nil {
			return e
		}
	}
	if e = audit(ctx, tx, current(r).ID, action, b.ID); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	message := "账户已删除并注销登录；代理撤权已排队，订单核账记录保留，可在已删除列表恢复"
	if admin {
		message = "副管理员已删除并注销登录，相关机器人核款权限立即失效；如有订阅，撤权任务已排队"
	}
	if restore {
		message = "账户已恢复为停用状态，请核对权限后手动启用"
		if !verified {
			message = "账户已恢复为未验证状态，新的验证邮件已排队；请先验证邮箱再审核启用"
		}
	}
	send(w, 200, map[string]any{"message": message, "deleted": !restore})
	return nil
}
