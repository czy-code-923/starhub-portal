package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type tgButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

func (a *App) tgButtons(ctx context.Context, c tgConfig, user int64, text string, buttons [][]tgButton) error {
	if c.Mode != "polling" {
		return a.tgSend(ctx, c, user, text+"\n通知模式：请在购买面板后台处理。")
	}
	return a.tgCall(ctx, c.Token, "sendMessage", map[string]any{"chat_id": user, "text": text, "protect_content": true, "link_preview_options": map[string]any{"is_disabled": true}, "reply_markup": map[string]any{"inline_keyboard": buttons}}, nil)
}
func (a *App) tgOrderID(ctx context.Context, short string) (string, error) {
	if len(short) < 8 || len(short) > 32 {
		return "", bad("请输入至少 8 位订单号，或点击订单按钮")
	}
	for _, c := range short {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", bad("订单编号无效")
		}
	}
	rows, e := a.DB.Query(ctx, "SELECT id FROM orders WHERE id LIKE $1||'%' LIMIT 2", short)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return "", e
		}
		ids = append(ids, id)
	}
	if e = rows.Err(); e != nil {
		return "", e
	}
	if len(ids) != 1 {
		return "", bad("订单不存在或短编号不唯一，请选择列表中的订单")
	}
	return ids[0], nil
}
func (a *App) tgOrderCard(ctx context.Context, c tgConfig, p tgPrincipal, order string) error {
	if !includes(p.Scopes, "orders") && !includes(p.Scopes, "payments") {
		return nil
	}
	var state, review, method, name string
	var amount int64
	e := a.DB.QueryRow(ctx, `SELECT o.state,o.payable,COALESCE(o.snapshot->'plan'->>'name','套餐'),COALESCE(m.state,''),COALESCE(m.method_id,'') FROM orders o LEFT JOIN manual_payments m ON m.order_id=o.id WHERE o.id=$1`, order).Scan(&state, &amount, &name, &review, &method)
	if e != nil {
		return e
	}
	buttons := [][]tgButton{}
	if method == "alipay_redpacket" && includes(p.Scopes, "redpacket") && includes(p.Scopes, "payments") {
		buttons = append(buttons, []tgButton{{"查看此订单红包口令", "packet:" + order}})
	}
	if method == "alipay_redpacket" && review == "submitted" && state == "pending" && includes(p.Scopes, "approve") {
		buttons = append(buttons, []tgButton{{"我已领取，开始核验", "review:" + order}})
	}
	return a.tgButtons(ctx, c, p.UserID, fmt.Sprintf("星枢 · 订单 %s\n%s\n应付 ¥%.2f\n订单 %s / 核款 %s\n%s", order, name, float64(amount)/100, state, review, map[bool]string{true: "口令红包无需交易流水，确认实际领取金额后才能开通。", false: "静态码或在线支付请在后台核验真实流水；创建订单不代表付款。"}[method == "alipay_redpacket"]), buttons)
}
func (a *App) tgReviewer(ctx context.Context, p tgPrincipal) (actor, error) {
	var u actor
	var status string
	var scopes []byte
	e := a.DB.QueryRow(ctx, `SELECT id,role,is_owner,admin_scopes,status FROM customers WHERE id=$1`, p.Administrator).Scan(&u.ID, &u.Role, &u.Owner, &scopes, &status)
	if e != nil {
		return u, Problem{403, "绑定的核款管理员不可用"}
	}
	if e = json.Unmarshal(scopes, &u.Scopes); e != nil {
		return u, e
	}
	if status != "active" || !u.can("payments") {
		return u, Problem{403, "后台管理员的核款权限已撤销"}
	}
	return u, nil
}
func (a *App) tgCallback(ctx context.Context, user, chat int64, callbackID, data string) error {
	c, _, e := a.tgLoad(ctx)
	if e != nil {
		return e
	}
	p, ok := tgPrincipalFor(c, user)
	if !c.Enabled || !ok || chat != user {
		return nil
	}
	reply := func(text string) error {
		runes := []rune(text)
		if len(runes) > 200 {
			text = string(runes[:200])
		}
		return a.tgCall(ctx, c.Token, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text, "show_alert": true}, nil)
	}
	parts := strings.SplitN(data, ":", 2)
	if len(parts) != 2 || !portalID.MatchString(parts[1]) {
		return reply("按钮无效，请重新打开订单")
	}
	action, id := parts[0], parts[1]
	if action == "order" {
		if e = a.tgCall(ctx, c.Token, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID}, nil); e != nil {
			return e
		}
		return a.tgOrderCard(ctx, c, p, id)
	}
	if action == "packet" {
		if !includes(p.Scopes, "redpacket") || !includes(p.Scopes, "payments") {
			return reply("没有口令查看权限")
		}
		value, err := a.redpacketValue(ctx, id)
		if err != nil {
			return reply("此订单尚未提交红包口令")
		}
		if _, err = a.DB.Exec(ctx, "INSERT INTO audit(actor,action,subject) VALUES($1,'telegram.redpacket.view',$2)", fmt.Sprint("telegram:", user), id); err != nil {
			return err
		}
		if e = reply("口令仅在本私聊显示，请在支付宝核实领取"); e != nil {
			return e
		}
		return a.tgSend(ctx, c, user, "订单 "+id+"\n红包口令："+value)
	}
	if !includes(p.Scopes, "approve") || !includes(p.Scopes, "payments") || !includes(p.Scopes, "redpacket") {
		return reply("没有机器人核款权限")
	}
	reviewer, err := a.tgReviewer(ctx, p)
	if err != nil {
		return reply(err.Error())
	}
	if action == "review" {
		var payment, owner string
		var version int
		var amount int64
		err = a.DB.QueryRow(ctx, `SELECT m.id,m.version,o.payable,o.customer_id FROM manual_payments m JOIN orders o ON o.id=m.order_id WHERE o.id=$1 AND o.state='pending' AND m.state='submitted' AND m.method_id='alipay_redpacket' AND m.review_deadline>now()`, id).Scan(&payment, &version, &amount, &owner)
		if err != nil {
			return reply("订单尚未提交红包、已处理或核验期已过")
		}
		if owner == reviewer.ID {
			return reply("不能核验自己的订单")
		}
		nonce := ID()
		if _, e = a.DB.Exec(ctx, `INSERT INTO telegram_confirmations(id,user_id,administrator_id,token_hash,payment_id,payment_version,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, nonce, user, reviewer.ID, hash(c.Token), payment, version, a.now().Add(5*time.Minute)); e != nil {
			return e
		}
		if e = reply("请核对实际领取金额，再点击第二次确认"); e != nil {
			return e
		}
		return a.tgButtons(ctx, c, user, fmt.Sprintf("核款二次确认\n订单 %s\n我已在支付宝成功领取此红包，实际收到 ¥%.2f，确认后排队开通。\n仅提交口令不代表到账。本次按钮 5 分钟内有效。", id, float64(amount)/100), [][]tgButton{{{"已领取且金额一致，确认开通", "confirm:" + nonce}}})
	}
	if action == "confirm" {
		var payment string
		var version int
		err = a.DB.QueryRow(ctx, `UPDATE telegram_confirmations SET used_at=$5 WHERE id=$1 AND user_id=$2 AND administrator_id=$3 AND token_hash=$4 AND used_at IS NULL AND expires_at>$5 RETURNING payment_id,payment_version`, id, user, reviewer.ID, hash(c.Token), a.now()).Scan(&payment, &version)
		if err == pgx.ErrNoRows {
			return reply("确认已使用或已过期，请重新查看订单")
		}
		if err != nil {
			return err
		}
		var amount int64
		if err = a.DB.QueryRow(ctx, "SELECT o.payable FROM manual_payments m JOIN orders o ON o.id=m.order_id WHERE m.id=$1", payment).Scan(&amount); err != nil {
			return err
		}
		message, err := a.reviewManual(ctx, reviewer, payment, manualReview{Decision: "approve", Version: version, Amount: amount, At: a.now(), Confirmed: true, Note: fmt.Sprint("Telegram 用户 ", user, " 二次确认实际领取")})
		if err != nil {
			return reply(safeJobError(err))
		}
		if _, e = a.DB.Exec(ctx, "INSERT INTO audit(actor,action,subject) VALUES($1,'telegram.payment.approve',$2)", fmt.Sprint("telegram:", user), payment); e != nil {
			return e
		}
		return reply(message)
	}
	return reply("不支持此操作")
}
