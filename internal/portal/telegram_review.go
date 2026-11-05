package portal

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strconv"
	"strings"
	"time"
)

func tgCanApprove(p tgPrincipal, method string) bool {
	return includes(p.Scopes, "approve") && includes(p.Scopes, "payments") && (method != "alipay_redpacket" || includes(p.Scopes, "redpacket"))
}
func (a *App) tgReviewCallback(ctx context.Context, c tgConfig, p tgPrincipal, user int64, action, id string, reply func(string) error) error {
	if !includes(p.Scopes, "approve") || !includes(p.Scopes, "payments") {
		return reply("没有机器人核款权限")
	}
	reviewer, e := a.tgReviewer(ctx, p)
	if e != nil {
		return reply(e.Error())
	}
	if action == "review" {
		var payment, owner, method string
		var version int
		var amount int64
		e = a.DB.QueryRow(ctx, `SELECT m.id,m.version,o.payable,o.customer_id,m.method_id FROM manual_payments m JOIN orders o ON o.id=m.order_id JOIN customers c ON c.id=o.customer_id WHERE o.id=$1 AND o.state='pending' AND m.state='submitted' AND m.method_id IN ('alipay_redpacket','alipay_static','wechat_static') AND m.review_deadline>$2 AND c.deleted_at IS NULL`, id, a.now()).Scan(&payment, &version, &amount, &owner, &method)
		if e != nil {
			return reply("订单未提交凭证、已处理、用户已删除或核验期已过")
		}
		if owner == reviewer.ID {
			return reply("不能核验自己的订单")
		}
		if !tgCanApprove(p, method) {
			return reply("核验红包还需要口令查看权限")
		}
		if _, e = a.DB.Exec(ctx, "UPDATE telegram_confirmations SET used_at=$4 WHERE user_id=$1 AND token_hash=$2 AND payment_id=$3 AND used_at IS NULL", user, hash(c.Token), payment, a.now()); e != nil {
			return e
		}
		nonce := ID()
		_, e = a.DB.Exec(ctx, `INSERT INTO telegram_confirmations(id,user_id,administrator_id,token_hash,payment_id,payment_version,method_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, nonce, user, reviewer.ID, hash(c.Token), payment, version, method, a.now().Add(5*time.Minute))
		if e != nil {
			return e
		}
		if method == "alipay_redpacket" {
			_ = reply("请在支付宝实际领取并核对金额，再二次确认")
			return a.tgButtons(ctx, c, user, fmt.Sprintf("红包核款二次确认\n订单 %s\n应付 ¥%.2f\n请先在支付宝成功领取此红包，并确认实际到账金额一致，再点下方按钮。\n本次确认 5 分钟内有效；仅有口令不代表到账。", id, float64(amount)/100), [][]tgButton{{{"已领取且金额一致，确认开通", "confirm:" + nonce}}})
		}
		_ = reply("请回复真实到账流水、金额和北京时间")
		var msg struct {
			ID int64 `json:"message_id"`
		}
		e = a.tgCall(ctx, c.Token, "sendMessage", map[string]any{"chat_id": user, "text": fmt.Sprintf("收款码核验 · 订单 %s\n应付 ¥%.2f\n请在支付宝 / 微信核对实际到账后，直接回复本条消息：\n真实流水号 金额 YYYY-MM-DD HH:MM\n例如：TESTREF123 5.00 2026-11-05 12:30\n时间使用北京时间。只填写客户提供的流水或截图不能证明到账，核对后还需二次确认。5 分钟内有效。", id, float64(amount)/100), "protect_content": true, "reply_markup": map[string]any{"force_reply": true, "selective": true, "input_field_placeholder": "真实流水号 金额 YYYY-MM-DD HH:MM"}}, &msg)
		if e != nil {
			return e
		}
		if msg.ID <= 0 {
			return Problem{503, "Telegram 未返回核验消息编号，请重新开始"}
		}
		_, e = a.DB.Exec(ctx, "UPDATE telegram_confirmations SET reply_message_id=$2 WHERE id=$1", nonce, msg.ID)
		return e
	}
	if action == "confirm" {
		var payment, method, ref string
		var version int
		var entered *int64
		var at *time.Time
		// Consume once. Invalid permission/method or missing static receipt fields
		// never reach the shared, transactional payment reviewer.
		e = a.DB.QueryRow(ctx, `SELECT payment_id,payment_version,method_id,verified_reference,received_amount,reference_received_at FROM telegram_confirmations WHERE id=$1 AND user_id=$2 AND administrator_id=$3 AND token_hash=$4 AND used_at IS NULL AND expires_at>$5`, id, user, reviewer.ID, hash(c.Token), a.now()).Scan(&payment, &version, &method, &ref, &entered, &at)
		if e == pgx.ErrNoRows {
			return reply("确认已使用或已过期，请重新查看订单")
		}
		if e != nil {
			return e
		}
		if !tgCanApprove(p, method) {
			return reply("核验红包还需要口令查看权限")
		}
		var amount int64
		if method == "alipay_redpacket" {
			if e = a.DB.QueryRow(ctx, "SELECT o.payable FROM manual_payments m JOIN orders o ON o.id=m.order_id WHERE m.id=$1", payment).Scan(&amount); e != nil {
				return e
			}
			now := a.now()
			at = &now
		} else {
			if ref == "" || entered == nil || at == nil {
				return reply("请先回复真实流水、到账金额和时间")
			}
			amount = *entered
		}
		tag, e := a.DB.Exec(ctx, `UPDATE telegram_confirmations SET used_at=$5 WHERE id=$1 AND user_id=$2 AND administrator_id=$3 AND token_hash=$4 AND used_at IS NULL AND expires_at>$5`, id, user, reviewer.ID, hash(c.Token), a.now())
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return reply("确认已使用或已过期，请重新查看订单")
		}
		message, e := a.reviewManual(ctx, reviewer, payment, manualReview{Decision: "approve", Version: version, Reference: ref, Amount: amount, At: *at, Confirmed: true, Note: fmt.Sprint("Telegram 用户 ", user, " 二次确认实际到账")})
		if e != nil {
			return reply(safeJobError(e))
		}
		if _, e = a.DB.Exec(ctx, "INSERT INTO audit(actor,action,subject) VALUES($1,'telegram.payment.approve',$2)", fmt.Sprint("telegram:", user), payment); e != nil {
			return e
		}
		return reply(message)
	}
	return reply("不支持此操作")
}

func parseTGReceipt(text string) (string, int64, time.Time, error) {
	parts := strings.Fields(text)
	if len(parts) != 4 {
		return "", 0, time.Time{}, bad("格式：真实流水号 金额 YYYY-MM-DD HH:MM（北京时间）")
	}
	ref := strings.ToUpper(parts[0])
	if !transferReference.MatchString(ref) {
		return "", 0, time.Time{}, bad("真实流水号需 6–100 位字母、数字、下划线或连字符")
	}
	price := strings.Split(strings.TrimPrefix(parts[1], "¥"), ".")
	if len(price) > 2 || len(price[0]) < 1 || len(price[0]) > 7 {
		return "", 0, time.Time{}, bad("金额格式无效")
	}
	for _, s := range price {
		for _, c := range s {
			if c < '0' || c > '9' {
				return "", 0, time.Time{}, bad("金额格式无效")
			}
		}
	}
	whole, e := strconv.ParseInt(price[0], 10, 64)
	if e != nil {
		return "", 0, time.Time{}, bad("金额格式无效")
	}
	fraction := int64(0)
	if len(price) == 2 {
		if len(price[1]) < 1 || len(price[1]) > 2 {
			return "", 0, time.Time{}, bad("金额最多两位小数")
		}
		fraction, _ = strconv.ParseInt(price[1], 10, 64)
		if len(price[1]) == 1 {
			fraction *= 10
		}
	}
	amount := whole*100 + fraction
	if amount < 1 || amount > 100000000 {
		return "", 0, time.Time{}, bad("金额超出允许范围")
	}
	at, e := time.ParseInLocation("2006-01-02 15:04", parts[2]+" "+parts[3], time.FixedZone("CST", 8*3600))
	if e != nil {
		return "", 0, time.Time{}, bad("时间格式：YYYY-MM-DD HH:MM，使用北京时间")
	}
	return ref, amount, at, nil
}

func (a *App) tgPaymentReply(ctx context.Context, user, chat, replyID int64, text string) (bool, error) {
	c, _, e := a.tgLoad(ctx)
	if e != nil {
		return false, e
	}
	p, ok := tgPrincipalFor(c, user)
	if !c.Enabled || c.Mode != "polling" || !ok || chat != user || !includes(p.Scopes, "payments") || !includes(p.Scopes, "approve") {
		return false, nil
	}
	reviewer, e := a.tgReviewer(ctx, p)
	if e != nil {
		return true, a.tgSend(ctx, c, user, "绑定管理员的核款权限不可用，请联系主管理员")
	}
	var nonce, payment, order string
	var version int
	var expected int64
	var created, expires time.Time
	e = a.DB.QueryRow(ctx, `SELECT t.id,t.payment_id,t.payment_version,m.order_id,o.payable,o.created_at,o.expires_at FROM telegram_confirmations t JOIN manual_payments m ON m.id=t.payment_id JOIN orders o ON o.id=m.order_id WHERE t.user_id=$1 AND t.administrator_id=$2 AND t.token_hash=$3 AND t.reply_message_id=$4 AND t.used_at IS NULL AND t.expires_at>$5 AND t.method_id IN ('alipay_static','wechat_static') AND m.state='submitted' AND m.version=t.payment_version AND o.state='pending'`, user, reviewer.ID, hash(c.Token), replyID, a.now()).Scan(&nonce, &payment, &version, &order, &expected, &created, &expires)
	if e == pgx.ErrNoRows {
		var known bool
		if err := a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM telegram_confirmations WHERE user_id=$1 AND administrator_id=$2 AND token_hash=$3 AND reply_message_id=$4)", user, reviewer.ID, hash(c.Token), replyID).Scan(&known); err != nil {
			return true, err
		}
		if known {
			return true, a.tgSend(ctx, c, user, "此核验已过期或已处理，请重新打开订单并点击核验按钮。")
		}
		return false, nil
	}
	if e != nil {
		return true, e
	}
	ref, amount, at, e := parseTGReceipt(text)
	if e != nil {
		return true, a.tgSend(ctx, c, user, e.Error()+"\n请回复同一条核验消息重新填写。")
	}
	if amount != expected {
		return true, a.tgSend(ctx, c, user, "实际到账金额必须与订单应付金额一致，请核对后重新回复；金额不符请在后台人工处理。")
	}
	if at.Before(created.Add(-time.Minute)) || !at.Before(expires) || at.After(a.now().Add(5*time.Minute)) {
		return true, a.tgSend(ctx, c, user, "实际到账时间不在本订单付款窗口内，请在后台核对处理。")
	}
	tag, e := a.DB.Exec(ctx, `UPDATE telegram_confirmations SET verified_reference=$2,received_amount=$3,reference_received_at=$4 WHERE id=$1 AND used_at IS NULL AND expires_at>$5 AND verified_reference=''`, nonce, ref, amount, at, a.now())
	if e != nil {
		return true, e
	}
	if tag.RowsAffected() != 1 {
		var saved string
		var paid int64
		var when time.Time
		if e = a.DB.QueryRow(ctx, "SELECT verified_reference,received_amount,reference_received_at FROM telegram_confirmations WHERE id=$1 AND used_at IS NULL AND expires_at>$2", nonce, a.now()).Scan(&saved, &paid, &when); e != nil || saved != ref || paid != amount || !when.Equal(at) {
			return true, a.tgSend(ctx, c, user, "当前核验已填写或已过期。修改流水请重新从订单按钮开始，避免旧确认内容与新流水不一致。")
		}
	}
	return true, a.tgButtons(ctx, c, user, fmt.Sprintf("收款码核款二次确认\n订单 %s\n真实流水 %s\n实收 ¥%.2f\n到账时间 %s（北京时间）\n确认已在收款账户核实，且没有用于其他订单，才可开通。", order, ref, float64(amount)/100, at.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04")), [][]tgButton{{{"已核实流水、金额和时间，确认开通", "confirm:" + nonce}}})
}
