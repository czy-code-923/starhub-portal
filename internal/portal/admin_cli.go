package portal

import (
	"context"
	"strings"
)

// ResetAdmin is reachable only from the local CLI with database access.
func (a *App) ResetAdmin(ctx context.Context, email, password string) error {
	email, e := emailAddress(email)
	if e != nil {
		return e
	}
	encoded, e := passwordHash(password)
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
	var id string
	if e = tx.QueryRow(ctx, "UPDATE customers SET password_hash=$2,status='active' WHERE email=$1 AND role='admin' RETURNING id", email, encoded).Scan(&id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM sessions WHERE customer_id=$1", id); e != nil {
		return e
	}
	if e = audit(ctx, tx, "local-cli", "admin.password-reset", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// ConfigureControl is a local-only bootstrap path: restricted key on stdin,
// never an administrator token in a browser or command argument.
func (a *App) ConfigureControl(ctx context.Context, origin, key string) error {
	u, e := validOrigin(origin)
	if e != nil || u.Scheme != "https" || !strings.HasPrefix(key, "xs_") || len(key) != 67 {
		return bad("有效 HTTPS 地址与受限 API 密钥为必填")
	}
	s, secrets, e := a.settings(ctx)
	if e != nil {
		return e
	}
	secrets.ControlOrigin = strings.TrimRight(origin, "/")
	secrets.ControlKey = key
	ctx = context.WithValue(ctx, controlSecretKey{}, secrets)
	var check struct {
		Feature string `json:"feature"`
	}
	if e = a.control(ctx, "GET", "commerce/groups", nil, &check); e != nil {
		return e
	}
	if check.Feature != "commerce_v1" {
		return bad("控制面商业接口未就绪")
	}
	sealed, e := a.seal(secrets)
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
	tag, e := tx.Exec(ctx, "UPDATE settings SET secrets=$2,version=version+1 WHERE id=true AND version=$1", s.Version, sealed)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return Problem{409, "设置已经变化，请重试"}
	}
	if e = audit(ctx, tx, "local-cli", "control.configure", "site"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
