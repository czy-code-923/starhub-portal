package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type controlSecretKey struct{}

// Load settings before acquiring a transaction/lock: waiting requests must not
// exhaust the small pool while a lock holder needs a second connection.
func (a *App) withControlSecrets(r *http.Request) (*http.Request, error) {
	_, secret, e := a.settings(r.Context())
	if e != nil {
		return r, e
	}
	return r.WithContext(context.WithValue(r.Context(), controlSecretKey{}, secret)), nil
}

func (a *App) controlRequest(ctx context.Context, method, path string, body any) (*http.Response, error) {
	secret, ok := ctx.Value(controlSecretKey{}).(Secrets)
	var e error
	if !ok {
		_, secret, e = a.settings(ctx)
		if e != nil {
			return nil, e
		}
	}
	origin := secret.ControlOrigin
	if a.TestControlURL != "" {
		origin = a.TestControlURL
	}
	if origin == "" || secret.ControlKey == "" {
		return nil, Problem{503, "管理员尚未配置星枢 API 连接"}
	}
	var data io.Reader
	if body != nil {
		data = bytes.NewReader(raw(body))
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(origin, "/")+"/api/v1/"+path, data)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+secret.ControlKey)
	req.Header.Set("Content-Type", "application/json")
	res, e := a.Client.Do(req)
	if e != nil {
		return nil, Problem{503, "星枢 API 暂时无法连接"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		var problem map[string]string
		_ = json.Unmarshal(data, &problem)
		message := problem["error"]
		if len(message) > 240 {
			message = "请求未完成"
		}
		if res.StatusCode == 404 {
			if strings.HasPrefix(path, "commerce/accounts/") {
				return nil, Problem{404, "商业账户尚未创建"}
			}
			message = "星枢尚未支持商业接口，请先更新控制面"
		}
		return nil, Problem{503, fmt.Sprintf("星枢返回 %d：%s", res.StatusCode, message)}
	}
	return res, nil
}
func (a *App) control(ctx context.Context, method, path string, body, out any) error {
	res, e := a.controlRequest(ctx, method, path, body)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if e != nil {
		return e
	}
	if len(data) > 2<<20 {
		return errors.New("control response too large")
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
func (a *App) checkGroups(ctx context.Context, groups, protocols []string) error {
	var result struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}
	if e := a.control(ctx, "POST", "commerce/validate", map[string]any{"group_ids": groups, "protocols": protocols}, &result); e != nil {
		return e
	}
	if !result.Ready {
		return Problem{409, "节点组不满足独立计费要求：" + result.Reason}
	}
	return nil
}
func (a *App) account(ctx context.Context, user string) (string, string, error) {
	var id, subscription string
	e := a.DB.QueryRow(ctx, "SELECT control_user_id,subscription_id FROM service_accounts WHERE customer_id=$1 ORDER BY updated_at DESC,order_id DESC LIMIT 1", user).Scan(&id, &subscription)
	return id, subscription, e
}
func (a *App) service(w http.ResponseWriter, r *http.Request) error {
	selected, e := a.selectedService(r)
	id := selected.User
	if e == pgx.ErrNoRows {
		send(w, 200, map[string]any{"service": nil})
		return nil
	}
	if e != nil {
		return e
	}
	var u map[string]any
	if e = a.control(r.Context(), "GET", "proxy-users/"+id, nil, &u); e != nil {
		return e
	}
	safe := map[string]any{}
	for _, field := range []string{"active", "enabled", "expires_at", "quota_bytes", "used_bytes", "remaining_bytes", "lifetime_bytes", "upload_bytes", "download_bytes", "next_reset_at", "reset_day", "reset_strategy", "billing_mode", "multiplier_milli", "effective_max_devices", "device_count", "metered_nodes"} {
		safe[field] = u[field]
	}
	var name string
	_ = a.DB.QueryRow(r.Context(), "SELECT snapshot->'plan'->>'name' FROM orders WHERE id=$1", selected.Order).Scan(&name)
	safe["plan_name"] = name
	safe["order_id"] = selected.Order
	safe["quota_unit"] = selected.Unit
	safe["legacy_combined"] = selected.Legacy
	send(w, 200, map[string]any{"service": safe})
	return nil
}
func (a *App) clients(w http.ResponseWriter, r *http.Request) error {
	var list any
	if e := a.control(r.Context(), "GET", "client-catalog", nil, &list); e != nil {
		return e
	}
	send(w, 200, list)
	return nil
}
func (a *App) subscription(w http.ResponseWriter, r *http.Request) error {
	selected, e := a.selectedService(r)
	subscription := selected.Subscription
	if e == pgx.ErrNoRows {
		return Problem{404, "尚未开通套餐"}
	}
	if e != nil {
		return e
	}
	view := r.PathValue("view")
	if !includes([]string{"share", "qr", "content", "nodes", "check"}, view) {
		return bad("不支持的订阅操作")
	}
	client := r.URL.Query().Get("client")
	if view == "content" && (client == "" || client == "auto") {
		client = "mihomo"
	}
	if len(client) > 40 || strings.ContainsAny(client, "/\\\r\n&?#") {
		return bad("客户端参数无效")
	}
	path := "subscriptions/" + subscription + "/" + view
	if client != "" && client != "auto" {
		path += "?client=" + url.QueryEscape(client)
	}
	res, e := a.controlRequest(r.Context(), "GET", path, nil)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if e != nil || len(data) > 2<<20 {
		return errors.New("subscription response too large")
	}
	for _, h := range []string{"Content-Type", "Subscription-Userinfo", "Profile-Title", "X-Starhub-Excluded-Nodes"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if view == "content" {
		ext := "txt"
		if strings.Contains(client, "sing-box") || client == "hiddify" || client == "hiddify-app" {
			ext = "json"
		}
		if client == "mihomo" || client == "clash" {
			ext = "yaml"
		}
		w.Header().Set("Content-Disposition", `attachment; filename="starhub-`+selected.Order+`.`+ext+`"`)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, e = w.Write(data)
	return e
}
func (a *App) devices(w http.ResponseWriter, r *http.Request) error {
	selected, e := a.selectedService(r)
	id := selected.User
	if e == pgx.ErrNoRows {
		send(w, 200, []any{})
		return nil
	}
	if e != nil {
		return e
	}
	var list any
	if e = a.control(r.Context(), "GET", "proxy-users/"+id+"/devices", nil, &list); e != nil {
		return e
	}
	send(w, 200, list)
	return nil
}
