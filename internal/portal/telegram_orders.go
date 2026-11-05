package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	if includes([]string{"alipay_redpacket", "alipay_static", "wechat_static"}, method) && review == "submitted" && state == "pending" && tgCanApprove(p, method) {
		buttons = append(buttons, []tgButton{{"核验到账并开通", "review:" + order}})
	}
	return a.tgButtons(ctx, c, p.UserID, fmt.Sprintf("星枢 · 订单 %s\n%s\n应付 ¥%.2f\n订单 %s / 核款 %s\n%s", order, name, float64(amount)/100, state, review, map[bool]string{true: "口令红包无需交易流水，确认实际领取金额后才能开通。", false: "静态码需填写真实到账流水、金额和时间并二次确认；创建订单不代表付款。"}[method == "alipay_redpacket"]), buttons)
}
func (a *App) tgReviewer(ctx context.Context, p tgPrincipal) (actor, error) {
	var u actor
	var status string
	var scopes []byte
	e := a.DB.QueryRow(ctx, `SELECT id,role,is_owner,admin_scopes,status FROM customers WHERE id=$1 AND deleted_at IS NULL`, p.Administrator).Scan(&u.ID, &u.Role, &u.Owner, &scopes, &status)
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
	if !c.Enabled || c.Mode != "polling" || !ok || chat != user {
		return nil
	}
	reply := func(text string) error {
		runes := []rune(text)
		if len(runes) > 200 {
			text = string(runes[:200])
		}
		_ = a.tgCall(ctx, c.Token, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text, "show_alert": true}, nil)
		return nil
	}
	parts := strings.SplitN(data, ":", 2)
	if len(parts) != 2 || !portalID.MatchString(parts[1]) {
		return reply("按钮无效，请重新打开订单")
	}
	action, id := parts[0], parts[1]
	if action == "order" {
		_ = a.tgCall(ctx, c.Token, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID}, nil)
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
	return a.tgReviewCallback(ctx, c, p, user, action, id, reply)
}
