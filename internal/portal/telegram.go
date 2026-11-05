package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type tgPrincipal struct {
	Administrator string   `json:"administrator_id,omitempty"`
	UserID        int64    `json:"user_id"`
	Label         string   `json:"label"`
	Scopes        []string `json:"scopes"`
	Notify        bool     `json:"notify"`
}
type tgConfig struct {
	Enabled    bool          `json:"enabled"`
	Mode       string        `json:"mode"`
	Token      string        `json:"token"`
	Principals []tgPrincipal `json:"principals"`
}

var tgScopes = map[string]string{"overview": "运营概览", "orders": "订单查询 / 新订单通知", "payments": "付款核验查询 / 状态通知", "redpacket": "私聊查看完整红包口令（另需付款权限）", "approve": "核验红包 / 收款码并开通（需绑定核款管理员）"}
var tgToken = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{25,100}$`)
var portalID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (a *App) tgLoad(ctx context.Context) (tgConfig, int, error) {
	var c tgConfig
	var sealed []byte
	var version int
	e := a.DB.QueryRow(ctx, "SELECT sealed,version FROM telegram_settings WHERE id=true").Scan(&sealed, &version)
	if e == nil && len(sealed) > 0 {
		e = a.unseal(sealed, &c)
	}
	if c.Mode == "" {
		c.Mode = "notify_only"
	}
	return c, version, e
}
func validateTG(c tgConfig) error {
	if c.Mode != "notify_only" && c.Mode != "polling" {
		return bad("请选择仅通知或独立机器人命令模式")
	}
	if c.Token != "" && !tgToken.MatchString(c.Token) {
		return bad("机器人令牌格式无效")
	}
	if len(c.Principals) > 20 {
		return bad("最多 20 个授权私聊用户")
	}
	seen := map[int64]bool{}
	for _, p := range c.Principals {
		if p.UserID <= 0 || seen[p.UserID] || !cleanText(p.Label, 60) || len(p.Scopes) == 0 || len(p.Scopes) > len(tgScopes) {
			return bad("授权对象需使用不重复的私聊数字用户 ID 和明确权限")
		}
		seen[p.UserID] = true
		scopes := map[string]bool{}
		for _, s := range p.Scopes {
			if _, ok := tgScopes[s]; !ok || scopes[s] {
				return bad("未知或重复的机器人权限")
			}
			scopes[s] = true
		}
		if scopes["redpacket"] && !scopes["payments"] {
			return bad("查看红包口令同时需要付款核验权限")
		}
		if scopes["approve"] && (!scopes["payments"] || !portalID.MatchString(p.Administrator)) {
			return bad("核款开通需要付款权限并选择后台管理员；核验红包另需口令查看权限")
		}
	}
	if c.Enabled && (c.Token == "" || len(c.Principals) == 0) {
		return bad("启用前请填写令牌并授权至少一位私聊用户")
	}
	return nil
}
func (a *App) telegramSettings(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	ctx := r.Context()
	if r.Method == "GET" {
		c, v, e := a.tgLoad(ctx)
		if e != nil {
			return e
		}
		configured := c.Token != ""
		c.Token = ""
		status, e := jsonRows(ctx, a.DB, `SELECT jsonb_build_object('username',username,'last_poll_at',last_poll_at,'last_error',last_error,'queued',(SELECT count(*) FROM telegram_events WHERE state='queued'),'failed',(SELECT count(*) FROM telegram_events WHERE state='failed'),'sent',(SELECT count(*) FROM telegram_events WHERE state='sent')) FROM telegram_settings WHERE id=true`)
		if e != nil {
			return e
		}
		send(w, 200, map[string]any{"config": c, "configured": configured, "version": v, "scopes": tgScopes, "status": status})
		return nil
	}
	var b struct {
		tgConfig
		Version int  `json:"version"`
		Clear   bool `json:"clear_token"`
	}
	if e := decode(w, r, &b); e != nil {
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
	var sealed []byte
	var v int
	if e = tx.QueryRow(ctx, "SELECT sealed,version FROM telegram_settings WHERE id=true FOR UPDATE").Scan(&sealed, &v); e != nil {
		return e
	}
	if v != b.Version {
		return Problem{409, "机器人配置已变化，请刷新"}
	}
	var old tgConfig
	if len(sealed) > 0 {
		if e = a.unseal(sealed, &old); e != nil {
			return e
		}
	}
	if b.Token == "" && !b.Clear {
		b.Token = old.Token
	}
	if b.Clear {
		b.Token = ""
		b.Enabled = false
	}
	if e = validateTG(b.tgConfig); e != nil {
		return e
	}
	for _, p := range b.Principals {
		if includes(p.Scopes, "approve") {
			var allowed bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers WHERE id=$1 AND role='admin' AND status='active' AND deleted_at IS NULL AND (is_owner OR admin_scopes ? 'payments'))`, p.Administrator).Scan(&allowed); e != nil {
				return e
			}
			if !allowed {
				return bad("请选择仍启用且具有核款权限的后台管理员")
			}
		}
	}
	sealed, e = a.seal(b.tgConfig)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE telegram_settings SET sealed=$1,enabled=$2,version=version+1,cursor=CASE WHEN $3 THEN 0 ELSE cursor END,username=CASE WHEN $3 THEN '' ELSE username END,last_error='',updated_at=now() WHERE id=true", sealed, b.Enabled, b.Token != old.Token); e != nil {
		return e
	}
	if e = audit(ctx, tx, current(r).ID, "telegram.configure", "settings"); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": "Telegram 配置已保存；默认只发送订单与状态，完整口令仅供获授权的私聊命令查询"})
	return nil
}
func (a *App) tgCall(ctx context.Context, token, method string, body any, result any) error {
	endpoint := "https://api.telegram.org"
	if a.TestTelegramURL != "" {
		endpoint = a.TestTelegramURL
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint+"/bot"+token+"/"+method, bytes.NewReader(raw(body)))
	if e != nil {
		return Problem{503, "Telegram 请求无法建立"}
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := a.Client.Do(req)
	if e != nil {
		return Problem{503, "Telegram 暂时无法连接，请检查 VPS 到 Telegram 的 HTTPS 网络"}
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(data) > 1<<20 {
		return Problem{503, "Telegram 响应无效"}
	}
	var response struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Code   int             `json:"error_code"`
	}
	if json.Unmarshal(data, &response) != nil || !response.OK {
		message := map[int]string{401: "Telegram 令牌无效或已撤销", 403: "机器人被接收者阻止；请先在私聊启动机器人", 409: "机器人已有其他轮询实例或 Webhook；请改用仅通知模式或独立机器人", 429: "Telegram 请求限流，通知将稍后重试"}[response.Code]
		if message == "" {
			message = "Telegram 返回异常，稍后重试"
		}
		return Problem{503, message}
	}
	if result != nil {
		return json.Unmarshal(response.Result, result)
	}
	return nil
}
func (a *App) tgProbe(ctx context.Context, c tgConfig) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if e := a.tgCall(ctx, c.Token, "getMe", map[string]any{}, &me); e != nil {
		return "", e
	}
	if c.Mode == "polling" {
		var wh struct {
			URL string `json:"url"`
		}
		if e := a.tgCall(ctx, c.Token, "getWebhookInfo", map[string]any{}, &wh); e != nil {
			return "", e
		}
		if wh.URL != "" {
			return "", Problem{409, "机器人已有 Webhook；不会自动删除，请使用独立机器人或仅通知模式"}
		}
	}
	return me.Username, nil
}
func (a *App) telegramCheck(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	c, _, e := a.tgLoad(r.Context())
	if e != nil {
		return e
	}
	if c.Token == "" {
		return bad("请先保存机器人令牌")
	}
	name, e := a.tgProbe(r.Context(), c)
	if e != nil {
		return e
	}
	_, e = a.DB.Exec(r.Context(), "UPDATE telegram_settings SET username=$1,last_error='' WHERE id=true", name)
	if e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": "令牌验证成功；未发送消息、未更改 Webhook", "username": name})
	return nil
}
func (a *App) telegramRetry(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	_, e := a.DB.Exec(r.Context(), `UPDATE telegram_events SET state='queued',attempts=0,available_at=now(),last_error='' WHERE id IN(SELECT id FROM telegram_events WHERE state='failed' ORDER BY created_at DESC LIMIT 100)`)
	if e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": "失败通知已排队重试，已确认投递的接收者不会再次发送"})
	return nil
}
func (a *App) telegramCommands(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	c, _, e := a.tgLoad(r.Context())
	if e != nil {
		return e
	}
	if c.Mode != "polling" || c.Token == "" {
		return bad("仅独立机器人命令模式允许设置菜单，避免覆盖其他机器人的命令")
	}
	if _, e = a.tgProbe(r.Context(), c); e != nil {
		return e
	}
	commands := []map[string]string{{"command": "help", "description": "查看获授权的命令"}, {"command": "overview", "description": "运营概览（需权限）"}, {"command": "orders", "description": "最近订单（需权限）"}, {"command": "order", "description": "查看订单：订单前 8 位（需权限）"}, {"command": "payments", "description": "待核款订单（需权限）"}, {"command": "redpacket", "description": "订单口令查询（需额外权限）"}}
	if e = a.tgCall(r.Context(), c.Token, "setMyCommands", map[string]any{"commands": commands}, nil); e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": "查询菜单已设置；显示命令不代表拥有执行权限"})
	return nil
}
func queueTelegram(ctx context.Context, tx pgx.Tx, event, order, kind string) error {
	_, e := tx.Exec(ctx, `INSERT INTO telegram_events(id,order_id,kind) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM telegram_settings WHERE enabled) ON CONFLICT(id) DO NOTHING`, event, order, kind)
	return e
}
func (a *App) tgSend(ctx context.Context, c tgConfig, id int64, text string) error {
	runes := []rune(text)
	if len(runes) > 3500 {
		text = string(runes[:3500])
	}
	return a.tgCall(ctx, c.Token, "sendMessage", map[string]any{"chat_id": id, "text": text, "protect_content": true, "link_preview_options": map[string]any{"is_disabled": true}}, nil)
}
func tgPrincipalFor(c tgConfig, id int64) (tgPrincipal, bool) {
	for _, p := range c.Principals {
		if p.UserID == id {
			return p, true
		}
	}
	return tgPrincipal{}, false
}
func (a *App) tgCommand(ctx context.Context, c tgConfig, user, chat int64, text string) error {
	p, ok := tgPrincipalFor(c, user)
	if !ok || chat != user {
		return nil
	}
	tag, e := a.DB.Exec(ctx, `INSERT INTO telegram_command_limits(user_id) VALUES($1) ON CONFLICT(user_id) DO UPDATE SET last_at=now() WHERE telegram_command_limits.last_at<now()-interval '2 seconds'`, user)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	command := strings.Split(words[0], "@")[0]
	var message string
	buttons := [][]tgButton{}
	switch command {
	case "/start", "/help":
		message = "星枢购买面板\n支持获授权的私聊查询与核款二次确认；请先在真实收款账户核对到账。\n"
		for _, item := range []struct{ scope, cmd string }{{"overview", "/overview 运营概览"}, {"orders", "/orders 最近订单 /order 订单前8位"}, {"payments", "/payments 待核款订单"}, {"redpacket", "/redpacket 订单前8位 查看红包口令"}} {
			if includes(p.Scopes, item.scope) {
				message += item.cmd + "\n"
			}
		}
	case "/order":
		if len(words) != 2 {
			return a.tgSend(ctx, c, user, "请选择订单按钮，或输入 /order 订单前8位")
		}
		id, err := a.tgOrderID(ctx, words[1])
		if err != nil {
			return a.tgSend(ctx, c, user, safeJobError(err))
		}
		return a.tgOrderCard(ctx, c, p, id)
	case "/overview":
		if !includes(p.Scopes, "overview") {
			return nil
		}
		var users, pending, review int
		var paid int64
		e := a.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM customers WHERE role='customer' AND deleted_at IS NULL),(SELECT count(*) FROM orders WHERE state='pending'),(SELECT count(*) FROM manual_payments WHERE state='submitted'),(SELECT COALESCE(sum(payable),0) FROM orders WHERE state='paid' AND paid_at>now()-interval '24 hours')`).Scan(&users, &pending, &review, &paid)
		if e != nil {
			return e
		}
		message = fmt.Sprintf("星枢购买面板\n用户 %d · 待支付 %d · 待核款 %d\n近 24 小时已付 ¥%.2f", users, pending, review, float64(paid)/100)
	case "/orders", "/payments":
		scope := "orders"
		if command == "/payments" {
			scope = "payments"
		}
		if !includes(p.Scopes, scope) {
			return nil
		}
		query := `SELECT o.id,o.payable,o.state,COALESCE(m.state,''),COALESCE(m.method_name,'在线支付') FROM orders o LEFT JOIN manual_payments m ON m.order_id=o.id`
		if command == "/payments" {
			query += ` WHERE m.state='submitted' AND o.state='pending'`
		}
		query += ` ORDER BY o.created_at DESC LIMIT 10`
		rows, e := a.DB.Query(ctx, query)
		if e != nil {
			return e
		}
		defer rows.Close()
		message = "星枢购买面板 · " + command + "\n"
		for rows.Next() {
			var id, state, review, method string
			var amount int64
			if e = rows.Scan(&id, &amount, &state, &review, &method); e != nil {
				return e
			}
			message += fmt.Sprintf("%s\n¥%.2f · %s · %s / %s\n", id, float64(amount)/100, method, state, review)
			buttons = append(buttons, []tgButton{{"订单 " + id[:8] + " · " + method, "order:" + id}})
		}
		if e = rows.Err(); e != nil {
			return e
		}
	case "/redpacket":
		if !includes(p.Scopes, "redpacket") || !includes(p.Scopes, "payments") {
			return nil
		}
		if len(words) != 2 {
			message = "格式：/redpacket 订单前8位（或选择订单按钮）"
			break
		}
		orderID, err := a.tgOrderID(ctx, words[1])
		if err != nil {
			return a.tgSend(ctx, c, user, safeJobError(err))
		}
		words[1] = orderID
		value, e := a.redpacketValue(ctx, words[1])
		if e == pgx.ErrNoRows {
			message = "该订单没有已提交的红包口令"
			break
		}
		if e != nil {
			return e
		}
		if _, e = a.DB.Exec(ctx, "INSERT INTO audit(actor,action,subject) VALUES($1,'telegram.redpacket.view',$2)", fmt.Sprint("telegram:", user), words[1]); e != nil {
			return e
		}
		message = "订单 " + words[1] + "\n红包口令：" + value + "\n请仅在支付宝官方客户端领取并核对实际到账。机器人不代领；核对实际到账后可从订单按钮二次确认开通。"
	default:
		return nil
	}
	if len(buttons) > 0 {
		return a.tgButtons(ctx, c, user, message, buttons)
	}
	return a.tgSend(ctx, c, user, message)
}
func (a *App) tgDeliver(ctx context.Context, c tgConfig) error {
	var id, order, kind string
	var attempts int
	e := a.DB.QueryRow(ctx, `SELECT id,order_id,kind,attempts FROM telegram_events WHERE state='queued' AND available_at<=now() ORDER BY created_at LIMIT 1`).Scan(&id, &order, &kind, &attempts)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	scope := "payments"
	if kind == "order.created" {
		scope = "orders"
	}
	var amount int64
	var state, review, method string
	var renewal bool
	e = a.DB.QueryRow(ctx, `SELECT o.payable,o.state,COALESCE(m.state,''),COALESCE(m.method_name,'在线支付'),o.renewal_of IS NOT NULL FROM orders o LEFT JOIN manual_payments m ON m.order_id=o.id WHERE o.id=$1`, order).Scan(&amount, &state, &review, &method, &renewal)
	if e != nil {
		return e
	}
	labels := map[string]string{"order.created": "新订单", "payment.submitted": "付款凭证 / 红包口令已提交", "payment.approve": "核款通过", "payment.reject": "凭证被驳回", "service.active": "服务已开通"}
	if renewal {
		labels["order.created"] = "新续费订单"
		labels["service.active"] = "原订阅已续期"
	}
	message := fmt.Sprintf("星枢购买面板 · %s\n订单 %s\n应付 ¥%.2f · %s\n订单 %s / 核款 %s\n可点订单按钮核验；收款码需真实流水，红包需实际领取。通知不含完整口令。", labels[kind], order, float64(amount)/100, method, state, review)
	for _, p := range c.Principals {
		if !p.Notify || !includes(p.Scopes, scope) {
			continue
		}
		var sent bool
		if e = a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM telegram_deliveries WHERE event_id=$1 AND user_id=$2)", id, p.UserID).Scan(&sent); e != nil {
			return e
		}
		if sent {
			continue
		}
		// Re-read permissions immediately before sending; revoked recipients are skipped.
		latest, _, err := a.tgLoad(ctx)
		if err != nil {
			return err
		}
		fresh, exists := tgPrincipalFor(latest, p.UserID)
		if !latest.Enabled || latest.Token != c.Token {
			return nil // Preserve the queue while paused or rotating the bot token.
		}
		if !exists || !fresh.Notify || !includes(fresh.Scopes, scope) {
			continue
		}
		if e = a.tgButtons(ctx, latest, p.UserID, message, [][]tgButton{{{"查看订单 / 核验", "order:" + order}}}); e != nil {
			break
		}
		_, e = a.DB.Exec(ctx, "INSERT INTO telegram_deliveries(event_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING", id, p.UserID)
		if e != nil {
			return e
		}
		_, _ = a.DB.Exec(ctx, "UPDATE telegram_settings SET last_error='' WHERE id=true")
	}
	if e != nil {
		_, err := a.DB.Exec(ctx, "UPDATE telegram_events SET attempts=attempts+1,state=CASE WHEN attempts>=9 THEN 'failed' ELSE 'queued' END,available_at=now()+$2::interval,last_error=$3 WHERE id=$1", id, fmt.Sprintf("%d seconds", min(3600, 30*(1<<min(attempts, 6)))), safeJobError(e))
		return err
	}
	_, e = a.DB.Exec(ctx, "UPDATE telegram_events SET state='sent',last_error='' WHERE id=$1", id)
	return e
}
func (a *App) tgTick(ctx context.Context) error {
	c, version, e := a.tgLoad(ctx)
	if e != nil || !c.Enabled {
		return e
	}
	if e = a.tgDeliver(ctx, c); e != nil {
		return e
	}
	if c.Mode != "polling" {
		return nil
	}
	var checked int
	if e = a.DB.QueryRow(ctx, "SELECT checked_version FROM telegram_settings WHERE id=true").Scan(&checked); e != nil {
		return e
	}
	if checked != version {
		name, err := a.tgProbe(ctx, c)
		if err != nil {
			return err
		}
		if _, e = a.DB.Exec(ctx, "UPDATE telegram_settings SET username=$1,checked_version=$2 WHERE version=$2", name, version); e != nil {
			return e
		}
	}
	var cursor int64
	if e = a.DB.QueryRow(ctx, "SELECT cursor FROM telegram_settings WHERE id=true").Scan(&cursor); e != nil {
		return e
	}
	var updates []struct {
		ID       int64 `json:"update_id"`
		Callback *struct {
			ID   string `json:"id"`
			Data string `json:"data"`
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
			Message *struct {
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
		Message *struct {
			Text  string `json:"text"`
			Reply *struct {
				ID int64 `json:"message_id"`
			} `json:"reply_to_message"`
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
		} `json:"message"`
	}
	if e = a.tgCall(ctx, c.Token, "getUpdates", map[string]any{"offset": cursor, "timeout": 10, "limit": 20, "allowed_updates": []string{"message", "callback_query"}}, &updates); e != nil {
		return e
	}
	for _, u := range updates {
		latest, _, err := a.tgLoad(ctx)
		if err != nil {
			return err
		}
		if !latest.Enabled || latest.Mode != "polling" || latest.Token != c.Token {
			return nil
		}
		if u.Callback != nil && u.Callback.Message != nil && u.Callback.Message.Chat.Type == "private" && len(u.Callback.Data) <= 64 {
			if e = a.tgCallback(ctx, u.Callback.From.ID, u.Callback.Message.Chat.ID, u.Callback.ID, u.Callback.Data); e != nil {
				return e
			}
		}
		if u.Message != nil && u.Message.Chat.Type == "private" && len(u.Message.Text) <= 500 {
			handled := false
			if u.Message.Reply != nil {
				handled, e = a.tgPaymentReply(ctx, u.Message.From.ID, u.Message.Chat.ID, u.Message.Reply.ID, u.Message.Text)
				if e != nil {
					return e
				}
			}
			if !handled {
				if e = a.tgCommand(ctx, latest, u.Message.From.ID, u.Message.Chat.ID, u.Message.Text); e != nil {
					return e
				}
			}
		}
		if _, e = a.DB.Exec(ctx, "UPDATE telegram_settings SET cursor=GREATEST(cursor,$1) WHERE id=true", u.ID+1); e != nil {
			return e
		}
	}
	_, e = a.DB.Exec(ctx, "UPDATE telegram_settings SET last_poll_at=now(),last_error='' WHERE id=true")
	return e
}
func (a *App) RunTelegram(ctx context.Context) {
	// A session advisory lock prevents duplicate consumers across app processes.
	for ctx.Err() == nil {
		config, _, err := a.tgLoad(ctx)
		if err != nil || !config.Enabled {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}
		conn, e := a.DB.Acquire(ctx)
		if e != nil {
			return
		}
		var owned bool
		e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(90234003)").Scan(&owned)
		if e != nil || !owned {
			conn.Release()
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}
		func() {
			defer conn.Release()
			defer func() {
				stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, _ = conn.Exec(stop, "SELECT pg_advisory_unlock(90234003)")
			}()
			lastClean := time.Time{}
			for ctx.Err() == nil {
				config, _, err := a.tgLoad(ctx)
				if err != nil || !config.Enabled {
					return
				}
				if e = a.tgTick(ctx); e != nil && ctx.Err() == nil {
					_, _ = a.DB.Exec(ctx, "UPDATE telegram_settings SET last_error=$1 WHERE id=true", safeJobError(e))
				}
				if time.Since(lastClean) > time.Hour {
					_, _ = a.DB.Exec(ctx, "DELETE FROM telegram_events WHERE created_at<now()-interval '30 days' AND state IN ('sent','failed')")
					_, _ = a.DB.Exec(ctx, "DELETE FROM telegram_confirmations WHERE expires_at<now()-interval '1 day'")
					lastClean = time.Now()
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}()
	}
}
