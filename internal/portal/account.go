package portal

import (
	"net/http"
	"strings"
	"time"
)

func (a *App) profile(w http.ResponseWriter, r *http.Request) error {
	if r.Method == "GET" {
		send(w, 200, current(r))
		return nil
	}
	if e := a.limit(r, "profile", 12); e != nil {
		return e
	}
	var b struct {
		Name            string `json:"name"`
		Email           string `json:"email"`
		CurrentPassword string `json:"current_password"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if strings.TrimSpace(b.Name) == "" || !cleanText(b.Name, 80) {
		return bad("请填写有效昵称")
	}
	email, e := emailAddress(b.Email)
	if e != nil {
		return e
	}
	ctx := r.Context()
	u := current(r)
	_, secret, e := a.settings(ctx)
	if e != nil {
		return e
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	var oldEmail, encoded string
	if e = tx.QueryRow(ctx, "SELECT email,password_hash FROM customers WHERE id=$1 AND status='active' FOR UPDATE", u.ID).Scan(&oldEmail, &encoded); e != nil {
		return e
	}
	message := "资料已保存"
	if email != oldEmail {
		if !passwordOK(encoded, b.CurrentPassword) {
			return Problem{403, "当前密码不正确"}
		}
		if a.Mail == nil && (secret.SMTPHost == "" || secret.SMTPFrom == "") {
			return Problem{503, "尚未配置邮箱验证，原邮箱保持有效"}
		}
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM customers WHERE email=$1)", email).Scan(&exists); e != nil {
			return e
		}
		if exists {
			return bad("该邮箱已被使用")
		}
		if _, e = tx.Exec(ctx, "UPDATE email_tokens SET used_at=now() WHERE customer_id=$1 AND purpose='change_email' AND used_at IS NULL", u.ID); e != nil {
			return e
		}
		if e = a.queueToken(ctx, tx, u.ID, email, "change_email", 30*time.Minute); e != nil {
			return e
		}
		message = "昵称已保存；请验证新邮箱，验证前仍使用原邮箱登录"
	}
	if _, e = tx.Exec(ctx, "UPDATE customers SET name=$2 WHERE id=$1", u.ID, b.Name); e != nil {
		return e
	}
	if e = audit(ctx, tx, u.ID, "account.profile", u.ID); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	u.Name = b.Name
	send(w, 200, map[string]any{"user": u, "message": message})
	return nil
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "password", 8); e != nil {
		return e
	}
	var b struct {
		Current  string `json:"current_password"`
		Password string `json:"password"`
		Confirm  string `json:"password_confirm"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if b.Password != b.Confirm {
		return bad("两次输入的密码不一致")
	}
	encoded, e := passwordHash(b.Password)
	if e != nil {
		return e
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
	var old string
	if e = tx.QueryRow(ctx, "SELECT password_hash FROM customers WHERE id=$1 FOR UPDATE", current(r).ID).Scan(&old); e != nil {
		return e
	}
	if !passwordOK(old, b.Current) {
		return Problem{403, "当前密码不正确"}
	}
	if _, e = tx.Exec(ctx, "UPDATE customers SET password_hash=$2 WHERE id=$1", current(r).ID, encoded); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM sessions WHERE customer_id=$1", current(r).ID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE email_tokens SET used_at=now() WHERE customer_id=$1 AND purpose IN ('reset','change_email') AND used_at IS NULL", current(r).ID); e != nil {
		return e
	}
	if e = audit(ctx, tx, current(r).ID, "account.password", current(r).ID); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": "密码已修改，所有会话已注销，请重新登录"})
	return nil
}
func (a *App) verifyNewEmail(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "verify-email", 15); e != nil {
		return e
	}
	var b struct {
		Token string `json:"token"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
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
	var id, email string
	if e = tx.QueryRow(ctx, `UPDATE email_tokens SET used_at=$2 WHERE token_hash=$1 AND purpose='change_email' AND used_at IS NULL AND expires_at>$2 RETURNING customer_id,pending_email`, hash(b.Token), a.now()).Scan(&id, &email); e != nil {
		return bad("邮箱验证链接无效或已过期")
	}
	tag, e := tx.Exec(ctx, "UPDATE customers SET email=$2,email_verified_at=now(),admin_version=admin_version+1 WHERE id=$1 AND status='active'", id, email)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return bad("账户当前不可变更邮箱")
	}
	if _, e = tx.Exec(ctx, "DELETE FROM sessions WHERE customer_id=$1", id); e != nil {
		return e
	}
	if e = audit(ctx, tx, id, "account.email", id); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": "新邮箱已验证，请使用新邮箱重新登录"})
	return nil
}
