package portal

import (
	"context"
	"net/http"
	"time"
)

// Keep payment/order/idempotency journals. Only disposable operational history
// is pruned; queued, working and retryable failed jobs are never removed.
func (a *App) pruneHistory(ctx context.Context, manual bool, kind string) error {
	if !a.retentionMu.TryLock() {
		return Problem{409, "记录清理正在进行，请稍后重试"}
	}
	defer a.retentionMu.Unlock()
	if !manual && a.now().Before(a.nextRetention) {
		return nil
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(90234011)"); e != nil {
		return e
	}
	if kind == "" || kind == "audit" {
		if manual {
			_, e = tx.Exec(ctx, "DELETE FROM audit")
		} else {
			_, e = tx.Exec(ctx, `DELETE FROM audit WHERE created_at<$1 OR id IN(SELECT id FROM audit WHERE created_at>=$1 ORDER BY id DESC OFFSET 10000)`, a.now().Add(-30*24*time.Hour))
		}
		if e != nil {
			return e
		}
	}
	if kind == "" || kind == "jobs" {
		if manual {
			_, e = tx.Exec(ctx, "DELETE FROM jobs WHERE state IN ('succeeded','rolled_back')")
		} else {
			_, e = tx.Exec(ctx, `DELETE FROM jobs WHERE state IN ('succeeded','rolled_back') AND (updated_at<$1 OR id IN(SELECT id FROM jobs WHERE state IN ('succeeded','rolled_back') ORDER BY updated_at DESC,id DESC OFFSET 2000))`, a.now().Add(-30*24*time.Hour))
		}
		if e != nil {
			return e
		}
	}
	if kind == "" && !manual {
		var acquired bool
		if e = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(90234002)").Scan(&acquired); e != nil {
			return e
		}
		if acquired {
			if _, e = tx.Exec(ctx, `DELETE FROM media_assets m WHERE m.created_at<now()-interval '24 hours' AND NOT `+mediaReferenced); e != nil {
				return e
			}
		}
	}
	if e = tx.Commit(ctx); e == nil {
		a.nextRetention = a.now().Add(5 * time.Minute)
	}
	return e
}
func (a *App) recordRetention(w http.ResponseWriter, r *http.Request) error {
	kind := r.URL.Query().Get("kind")
	if kind != "jobs" && kind != "audit" {
		return bad("请选择任务或操作记录")
	}
	if e := requireAdminScope(r, kind); e != nil {
		return e
	}
	if r.Method == "POST" {
		if e := requireOwner(r); e != nil {
			return e
		}
		var b struct {
			Confirmed bool `json:"confirmed"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if !b.Confirmed {
			return bad("请确认清理历史记录")
		}
		if e := a.pruneHistory(r.Context(), true, kind); e != nil {
			return e
		}
		// Retain a new audit marker even when the old audit history was cleared.
		if _, e := a.DB.Exec(r.Context(), "INSERT INTO audit(actor,action,subject) VALUES($1,'history.clear',$2)", current(r).ID, kind); e != nil {
			return e
		}
	}
	var audits, completed, protected int
	if e := a.DB.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM audit),(SELECT count(*) FROM jobs WHERE state IN ('succeeded','rolled_back')),(SELECT count(*) FROM jobs WHERE state NOT IN ('succeeded','rolled_back'))`).Scan(&audits, &completed, &protected); e != nil {
		return e
	}
	send(w, 200, map[string]any{"audit_days": 30, "audit_limit": 10000, "completed_job_days": 30, "completed_job_limit": 2000, "audit_count": audits, "completed_jobs": completed, "protected_jobs": protected, "can_clear": current(r).Owner})
	return nil
}
