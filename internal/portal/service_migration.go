package portal

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"math/big"
	"time"
)

func toBinaryQuota(n int64) int64 {
	v := new(big.Int).Mul(big.NewInt(n), big.NewInt(1073741824))
	v.Quo(v, big.NewInt(1000000000))
	return v.Int64()
}
func grantName(name, plan, order string) string {
	a, b := []rune(name), []rune(plan)
	return string(a[:min(len(a), 35)]) + " · " + string(b[:min(len(b), 20)]) + " · " + order
}

func (a *App) migrateCustomer(ctx context.Context, user string) error {
	_, secret, e := a.settings(ctx)
	if e != nil {
		return e
	}
	ctx = context.WithValue(ctx, controlSecretKey{}, secret)
	conn, e := a.DB.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", "portal-user:"+user); e != nil {
		return e
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", "portal-user:"+user)
	var sealed []byte
	var savedState string
	e = conn.QueryRow(ctx, "SELECT payload,state FROM service_migrations WHERE customer_id=$1", user).Scan(&sealed, &savedState)
	if e == nil && savedState == "applied" {
		var status string
		if e = conn.QueryRow(ctx, "SELECT status FROM customers WHERE id=$1", user).Scan(&status); e != nil {
			return e
		}
		if status != "active" {
			return a.syncAccess(ctx, user)
		}
		return nil
	}
	if e != nil && e != pgx.ErrNoRows {
		return e
	}
	if len(sealed) == 0 {
		// Let in-flight legacy grants converge first. Otherwise a delayed old
		// grant could re-enable the shared identity after the split revoked it.
		var pending bool
		if e = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE customer_id=$1 AND state='paid' AND NOT per_order AND fulfillment<>'active')", user).Scan(&pending); e != nil {
			return e
		}
		if pending {
			return Problem{409, "等待旧版已付款订单开通完成，再拆分独立订阅"}
		}
		var old, last, oldID string
		e = conn.QueryRow(ctx, "SELECT external_id,order_id,control_user_id FROM service_accounts WHERE customer_id=$1 AND legacy_combined", user).Scan(&old, &last, &oldID)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		var legacy map[string]any
		if e = a.control(ctx, "GET", "proxy-users/"+oldID, nil, &legacy); e != nil {
			return e
		}
		expiryValue, ok := legacy["expires_at"].(string)
		if !ok {
			return Problem{503, "旧权益到期信息暂不可用"}
		}
		expires, err := time.Parse(time.RFC3339Nano, expiryValue)
		if err != nil {
			return err
		}
		if !expires.After(a.now()) {
			return nil
		}
		var name string
		if e = conn.QueryRow(ctx, "SELECT name FROM customers WHERE id=$1", user).Scan(&name); e != nil {
			return e
		}
		rows, err := conn.Query(ctx, `SELECT id,snapshot,COALESCE(paid_at,created_at) FROM orders WHERE customer_id=$1 AND state='paid' AND NOT per_order AND fulfillment='active' ORDER BY created_at,id`, user)
		if err != nil {
			return err
		}
		type candidate struct {
			ID   string
			Data []byte
			Paid time.Time
		}
		all := []candidate{}
		for rows.Next() {
			var c candidate
			if e = rows.Scan(&c.ID, &c.Data, &c.Paid); e != nil {
				rows.Close()
				return e
			}
			all = append(all, c)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		requests := []map[string]any{}
		for _, c := range all {
			var snap struct {
				Plan  Plan  `json:"plan"`
				Price Price `json:"price"`
			}
			if e = json.Unmarshal(c.Data, &snap); e != nil {
				return e
			}
			if !c.Paid.AddDate(0, snap.Price.Months, 0).After(a.now()) && c.ID != last {
				continue
			}
			quota := snap.Plan.Quota
			if snap.Plan.QuotaUnit != "GiB" {
				quota = toBinaryQuota(quota)
			}
			requests = append(requests, map[string]any{"external_id": "portal:" + c.ID, "order_id": c.ID, "name": grantName(name, snap.Plan.Name, c.ID), "months": snap.Price.Months, "quota_bytes": quota, "group_ids": snap.Plan.Groups, "protocols": snap.Plan.Protocols, "max_devices": snap.Plan.MaxDevices, "billing_mode": snap.Plan.Billing, "multiplier_milli": snap.Plan.Multiplier})
		}
		if len(requests) == 0 {
			return Problem{409, "旧权益未找到可迁移订单，请核对历史订单"}
		}
		sealed, e = a.seal(map[string]any{"legacy_external_id": old, "orders": requests})
		if e != nil {
			return e
		}
		if _, e = conn.Exec(ctx, "INSERT INTO service_migrations(customer_id,legacy_external_id,payload) VALUES($1,$2,$3) ON CONFLICT(customer_id) DO NOTHING", user, old, sealed); e != nil {
			return e
		}
		if e = conn.QueryRow(ctx, "SELECT payload FROM service_migrations WHERE customer_id=$1", user).Scan(&sealed); e != nil {
			return e
		}
	}
	var body map[string]any
	if e = a.unseal(sealed, &body); e != nil {
		return e
	}
	var result struct {
		LegacyUser         string    `json:"legacy_user_id"`
		Expiry             time.Time `json:"expires_at"`
		Remaining          int64     `json:"remaining_bytes"`
		CredentialsRevoked bool      `json:"credentials_revoked"`
		Orders             []struct {
			Order        string `json:"order_id"`
			User         string `json:"user_id"`
			Subscription string `json:"subscription_id"`
			Operation    string `json:"operation_id"`
			External     string `json:"external_id"`
			Quota        int64  `json:"quota_bytes"`
			Used         int64  `json:"used_bytes"`
			Remaining    int64  `json:"remaining_bytes"`
		} `json:"orders"`
	}
	if e = a.control(ctx, "POST", "commerce/migrate-orders", body, &result); e != nil {
		return e
	}
	if len(result.Orders) == 0 {
		return Problem{503, "控制面未返回独立订阅迁移结果"}
	}
	requests, ok := body["orders"].([]any)
	if !ok || len(requests) != len(result.Orders) || !result.CredentialsRevoked {
		return Problem{503, "迁移结果不完整或旧身份尚未撤销"}
	}
	expected := map[string]int64{}
	for _, v := range requests {
		m, ok := v.(map[string]any)
		if !ok {
			return bad("无效迁移请求")
		}
		id, _ := m["order_id"].(string)
		q, _ := m["quota_bytes"].(float64)
		expected[id] = int64(q)
	}
	for _, o := range result.Orders {
		q, ok := expected[o.Order]
		if !ok || q != o.Quota {
			return bad("迁移结果与原订单不符")
		}
		delete(expected, o.Order)
	}
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "DELETE FROM service_accounts WHERE customer_id=$1 AND legacy_combined", user); e != nil {
		return e
	}
	for _, o := range result.Orders {
		if !portalID.MatchString(o.Order) || !portalID.MatchString(o.User) || !portalID.MatchString(o.Subscription) || o.External != "portal:"+o.Order {
			return bad("无效迁移结果")
		}
		tag, err := tx.Exec(ctx, `INSERT INTO service_accounts(customer_id,order_id,control_user_id,subscription_id,plan_id,last_order_id,external_id,legacy_combined) SELECT $1,id,$3,$4,plan_id,id,$5,false FROM orders WHERE id=$2 AND customer_id=$1 AND state='paid' ON CONFLICT(order_id) DO NOTHING`, user, o.Order, o.User, o.Subscription, o.External)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return Problem{409, "迁移映射不一致，请勿重复创建订阅"}
		}
		if _, e = tx.Exec(ctx, `UPDATE orders SET per_order=true,snapshot=jsonb_set(jsonb_set(snapshot,'{plan,quota_bytes}',to_jsonb($2::bigint)),'{plan,quota_unit}','"GiB"') WHERE id=$1`, o.Order, o.Quota); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE entitlement_ledger SET control_operation=$2 WHERE order_id=$1", o.Order, o.Operation); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE service_migrations SET state='applied',response=$2,applied_at=now() WHERE customer_id=$1", user, raw(result)); e != nil {
		return e
	}
	if e = audit(ctx, tx, "system", "services.split", user); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	var status string
	if e = conn.QueryRow(ctx, "SELECT status FROM customers WHERE id=$1", user).Scan(&status); e != nil {
		return e
	}
	if status != "active" {
		return a.syncAccess(ctx, user)
	}
	return nil
}

func (a *App) capacity(ctx context.Context) (map[string]any, error) {
	var result map[string]any
	e := a.control(ctx, "GET", "commerce/capacity", nil, &result)
	return result, e
}
