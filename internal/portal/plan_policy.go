package portal

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strings"
)

func queuePlanPolicy(ctx context.Context, tx pgx.Tx, plan string) error {
	_, e := tx.Exec(ctx, `INSERT INTO jobs(id,kind,subject) VALUES($1,'policy',$2) ON CONFLICT(kind,subject) DO UPDATE SET state='queued',attempts=0,available_at=now(),detail='',lease_until=NULL,lease_generation=NULL,updated_at=now()`, ID(), plan)
	return e
}

// Authorization follows the current plan, while monetary and accounting fields
// remain in the immutable purchase snapshot. No credential or expiry is changed.
func (a *App) syncServicePolicy(ctx context.Context, root string) error {
	var p Plan
	var data []byte
	var user, sub, external string
	var applied int
	var legacy bool
	e := a.DB.QueryRow(ctx, `SELECT to_jsonb(p),s.control_user_id,s.subscription_id,s.external_id,s.policy_version,s.legacy_combined FROM service_accounts s JOIN plans p ON p.id=s.plan_id WHERE s.order_id=$1`, root).Scan(&data, &user, &sub, &external, &applied, &legacy)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(data, &p); e != nil {
		return e
	}
	if legacy || p.Simulation || applied >= p.PolicyRevision {
		return nil
	}
	if !strings.HasPrefix(external, "portal:") || !portalID.MatchString(strings.TrimPrefix(external, "portal:")) {
		return Problem{503, "订阅身份映射无效，请管理员检查"}
	}
	body := map[string]any{"external_id": external, "user_id": user, "subscription_id": sub, "source_id": p.ID, "revision": p.PolicyRevision, "group_ids": p.Groups, "protocols": p.Protocols}
	var response struct {
		Saved    bool `json:"saved"`
		Revision int  `json:"revision"`
	}
	e = a.control(ctx, "PUT", "commerce/accounts/"+external[len("portal:"):]+"/policy", body, &response)
	if e == nil && (!response.Saved || response.Revision != p.PolicyRevision) {
		e = Problem{503, "控制面尚不支持套餐权限同步，请升级星枢至 0.25.2+"}
	}
	if e != nil {
		_, _ = a.DB.Exec(ctx, "UPDATE service_accounts SET policy_error=$2,policy_checked_at=now() WHERE order_id=$1", root, safeJobError(e))
		return e
	}
	_, e = a.DB.Exec(ctx, `UPDATE service_accounts SET policy_version=GREATEST(policy_version,$2),policy_error='',policy_checked_at=now() WHERE order_id=$1`, root, p.PolicyRevision)
	return e
}

func (a *App) syncPlanPolicy(ctx context.Context, plan string) error {
	_, secret, e := a.settings(ctx)
	if e != nil {
		return e
	}
	ctx = context.WithValue(ctx, controlSecretKey{}, secret)
	rows, e := a.DB.Query(ctx, `SELECT order_id FROM service_accounts WHERE plan_id=$1 AND NOT legacy_combined AND remote_state NOT IN ('deleted','invalid') ORDER BY order_id`, plan)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var last error
	for _, id := range ids {
		if e = a.syncServicePolicy(ctx, id); e != nil {
			last = e
		}
	}
	return last
}
