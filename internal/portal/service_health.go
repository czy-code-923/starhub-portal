package portal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"
)

// Only public accounting fields are cached. Never store UUIDs, passwords or
// subscription URLs in the health cache. Financial records remain authoritative.
func serviceSummary(u map[string]any) map[string]any {
	safe := map[string]any{}
	for _, field := range []string{"active", "enabled", "expires_at", "quota_bytes", "used_bytes", "remaining_bytes", "lifetime_bytes", "upload_bytes", "download_bytes", "next_reset_at", "reset_day", "reset_strategy", "billing_mode", "multiplier_milli", "effective_max_devices", "device_count", "metered_nodes"} {
		safe[field] = u[field]
	}
	return safe
}
func (a *App) identityState(u map[string]any) (string, error) {
	enabled, ok := u["enabled"].(bool)
	if !ok {
		return "", errors.New("invalid control-plane user response")
	}
	expiry, ok := u["expires_at"].(string)
	if !ok {
		return "", errors.New("missing control-plane expiry")
	}
	expires, e := time.Parse(time.RFC3339Nano, expiry)
	if e != nil {
		return "", e
	}
	if !enabled {
		return "disabled", nil
	}
	if !expires.After(a.now()) {
		return "expired", nil
	}
	quota, quotaOK := u["quota_bytes"].(float64)
	used, usedOK := u["used_bytes"].(float64)
	if !quotaOK || !usedOK || quota < 0 || used < 0 || math.IsNaN(quota) || math.IsNaN(used) || math.IsInf(quota, 0) || math.IsInf(used, 0) {
		return "", errors.New("invalid control-plane accounting")
	}
	if quota > 0 && used >= quota {
		return "exhausted", nil
	}
	return "active", nil
}
func (a *App) saveServiceHealth(ctx context.Context, user, state string, u map[string]any, checked time.Time) error {
	_, e := a.DB.Exec(ctx, `UPDATE service_accounts SET remote_state=$2,remote_summary=$3,remote_checked_at=$4 WHERE control_user_id=$1 AND (remote_checked_at IS NULL OR remote_checked_at<=$4)`, user, state, raw(serviceSummary(u)), checked)
	return e
}
func (a *App) liveService(ctx context.Context, s serviceSelection) (map[string]any, error) {
	started := a.now()
	var u map[string]any
	e := a.control(ctx, "GET", "proxy-users/"+s.User, nil, &u)
	if e != nil {
		var p Problem
		if !errors.As(e, &p) || p.Status != 404 {
			return nil, e
		}
		if e = a.saveServiceHealth(ctx, s.User, "deleted", nil, started); e != nil {
			return nil, e
		}
		return map[string]any{"state": "deleted", "enabled": false, "active": false}, nil
	}
	state, e := a.identityState(u)
	if e != nil {
		return nil, Problem{503, "星枢身份状态响应不完整，请稍后重试"}
	}
	if e = a.saveServiceHealth(ctx, s.User, state, u, started); e != nil {
		return nil, e
	}
	safe := serviceSummary(u)
	safe["state"] = state
	safe["checked_at"] = started
	return safe, nil
}

// A complete, validated snapshot is required before inferring deletion. An
// unavailable server, denied API key or partial page never invalidates service.
// This single-process deployment makes at most one bounded refresh per minute.
func (a *App) refreshServiceStates(ctx context.Context) error {
	if !a.healthMu.TryLock() {
		return nil
	}
	defer a.healthMu.Unlock()
	if a.now().Before(a.nextHealthSync) {
		return nil
	}
	a.nextHealthSync = a.now().Add(time.Minute)
	var count int
	if e := a.DB.QueryRow(ctx, "SELECT count(*) FROM service_accounts").Scan(&count); e != nil {
		return e
	}
	if count == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	started := a.now()
	all := map[string]map[string]any{}
	after := ""
	for page := 0; page < 50; page++ {
		var result struct {
			Items []map[string]any `json:"items"`
			Next  string           `json:"next_cursor"`
		}
		if e := a.control(ctx, "GET", "proxy-users/page?limit=200&after="+after, nil, &result); e != nil {
			return e
		}
		if result.Items == nil {
			return errors.New("invalid control-plane user page")
		}
		for _, u := range result.Items {
			id, ok := u["id"].(string)
			if !ok || !portalID.MatchString(id) {
				return errors.New("invalid control-plane user ID")
			}
			if _, e := a.identityState(u); e != nil {
				return e
			}
			all[id] = u
		}
		if result.Next == "" {
			break
		}
		if !portalID.MatchString(result.Next) || result.Next <= after || page == 49 {
			return errors.New("invalid control-plane pagination")
		}
		after = result.Next
	}
	rows, e := a.DB.Query(ctx, "SELECT control_user_id FROM service_accounts")
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
	// Updates are atomic: readers cannot observe a half-applied capacity snapshot.
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	for _, id := range ids {
		state := "deleted"
		u, exists := all[id]
		if exists {
			state, e = a.identityState(u)
			if e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE service_accounts SET remote_state=$2,remote_summary=$3,remote_checked_at=$4 WHERE control_user_id=$1 AND updated_at<=$4 AND (remote_checked_at IS NULL OR remote_checked_at<=$4)`, id, state, raw(serviceSummary(u)), started); e != nil {
			return fmt.Errorf("save service health: %w", e)
		}
	}
	return tx.Commit(ctx)
}
func inactiveService(state string) bool {
	return state != "" && state != "active" && state != "unknown"
}
func serviceUnavailable(state string) error {
	return Problem{http.StatusGone, "此订单服务已失效（" + state + "），请联系管理员；不会自动重新开通或扣款"}
}
