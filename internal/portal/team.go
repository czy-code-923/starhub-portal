package portal

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

var adminScopeNames = map[string]string{"plans": "套餐与分类", "customers": "用户与审核", "orders": "订单查询", "payments": "付款凭证审核", "coupons": "优惠码", "invitations": "注册邀请", "announcements": "公告", "support": "工单", "jobs": "业务任务重试", "audit": "操作记录"}

func (u actor) can(scope string) bool {
	return u.Role == "admin" && (u.Owner || includes(u.Scopes, scope))
}
func requireOwner(r *http.Request) error {
	if !current(r).Owner {
		return Problem{403, "仅主管理员可以执行此操作"}
	}
	return nil
}
func requireAdminScope(r *http.Request, scope string) error {
	if !current(r).can(scope) {
		return Problem{403, "没有此管理功能的权限"}
	}
	return nil
}
func adminResourceScope(resource string) string {
	switch resource {
	case "categories", "control-groups", "demo-plans":
		return "plans"
	case "tickets":
		return "support"
	default:
		return resource
	}
}

func (a *App) team(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	ctx := r.Context()
	if r.Method == "GET" {
		rows, e := jsonRows(ctx, a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,email,name,is_owner,admin_scopes,admin_version,status,created_at FROM customers WHERE role='admin' ORDER BY is_owner DESC,created_at)x`)
		if e != nil {
			return e
		}
		send(w, 200, map[string]any{"items": rows, "scopes": adminScopeNames})
		return nil
	}
	if r.Method != "POST" && r.Method != "PUT" {
		return Problem{405, "method not allowed"}
	}
	var b struct {
		ID       string   `json:"id"`
		Version  int      `json:"version"`
		Email    string   `json:"email"`
		Name     string   `json:"name"`
		Password string   `json:"password"`
		Confirm  string   `json:"password_confirm"`
		Scopes   []string `json:"scopes"`
		Enabled  bool     `json:"enabled"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if strings.TrimSpace(b.Name) == "" || !cleanText(b.Name, 80) || len(b.Scopes) == 0 || len(b.Scopes) > len(adminScopeNames) {
		return bad("请填写管理员名称及功能权限")
	}
	seen := map[string]bool{}
	for _, s := range b.Scopes {
		if _, ok := adminScopeNames[s]; !ok || seen[s] {
			return bad("无效或重复的管理员权限")
		}
		seen[s] = true
	}
	email, e := emailAddress(b.Email)
	if e != nil {
		return e
	}
	encoded := ""
	if b.ID == "" || b.Password != "" {
		if b.Password != b.Confirm {
			return bad("两次输入的密码不一致")
		}
		encoded, e = passwordHash(b.Password)
		if e != nil {
			return e
		}
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	status := "disabled"
	if b.Enabled {
		status = "active"
	}
	id := b.ID
	if id == "" {
		var count int
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM customers WHERE role='admin'").Scan(&count); e != nil {
			return e
		}
		if count >= 20 {
			return bad("最多 20 位管理员，请停用或复用已有账户")
		}
		id = ID()
		_, e = tx.Exec(ctx, `INSERT INTO customers(id,email,name,password_hash,role,status,email_verified_at,admin_scopes) VALUES($1,$2,$3,$4,'admin',$5,now(),$6)`, id, email, b.Name, encoded, status, raw(b.Scopes))
	} else {
		var owner bool
		var oldEmail string
		var version int
		if e = tx.QueryRow(ctx, "SELECT is_owner,email,admin_version FROM customers WHERE id=$1 AND role='admin' FOR UPDATE", id).Scan(&owner, &oldEmail, &version); e != nil {
			return e
		}
		if owner {
			return Problem{403, "主管理员资料请在个人账户中修改，不能通过副管理员管理降权"}
		}
		if b.Version != version {
			return Problem{409, "管理员已变化，请刷新"}
		}
		if oldEmail != email {
			return bad("副管理员邮箱变更需由本人验证新邮箱")
		}
		_, e = tx.Exec(ctx, `UPDATE customers SET name=$2,status=$3,admin_scopes=$4,password_hash=CASE WHEN $5='' THEN password_hash ELSE $5 END,admin_version=admin_version+1 WHERE id=$1`, id, b.Name, status, raw(b.Scopes), encoded)
		if e == nil {
			_, e = tx.Exec(ctx, "DELETE FROM sessions WHERE customer_id=$1", id)
		}
	}
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, current(r).ID, "administrator.save", id); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]any{"id": id, "saved": true})
	return nil
}

// Kept in one helper so nickname/email updates can re-read an actor safely.
func actorRow(row pgx.Row) (actor, error) {
	var u actor
	e := row.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Owner, &u.Scopes)
	return u, e
}
