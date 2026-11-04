package portal

import "context"

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
