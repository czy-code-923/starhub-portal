package portal

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) Run(ctx context.Context) {
	go a.RunTelegram(ctx)
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		_ = a.WorkOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (a *App) WorkOnce(ctx context.Context) error {
	if e := a.cleanup(ctx); e != nil {
		return e
	}
	if e := a.reconcile(ctx); e != nil {
		return e
	}
	for i := 0; i < 5; i++ {
		var id, kind, subject string
		var payload []byte
		var attempts int
		lease := ID()
		e := a.DB.QueryRow(ctx, `UPDATE jobs SET lease_generation=$1,state='working',attempts=attempts+1,lease_until=now()+interval '2 minutes',updated_at=now() WHERE id=(SELECT id FROM jobs WHERE kind IN ('mail','fulfill','access','migrate') AND ((state='queued' AND available_at<=now()) OR (state='working' AND lease_until<now())) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,kind,subject,payload,attempts`, lease).Scan(&id, &kind, &subject, &payload, &attempts)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		switch kind {
		case "migrate":
			e = a.migrateCustomer(ctx, subject)
		case "fulfill":
			e = a.fulfill(ctx, subject)
		case "mail":
			e = a.sendJobMail(ctx, payload)
		case "access":
			e = a.syncAccess(ctx, subject)
		}
		state, detail, next := "succeeded", "", a.now()
		if e != nil {
			state = "queued"
			if attempts >= 10 {
				state = "failed"
			}
			detail = safeJobError(e)
			next = next.Add(time.Duration(min(3600, 30*(1<<min(attempts, 6)))) * time.Second)
		}
		completed, err := a.DB.Exec(ctx, "UPDATE jobs SET state=$2,detail=$3,available_at=$4,lease_until=NULL,lease_generation=NULL,updated_at=now() WHERE id=$1 AND state='working' AND lease_generation=$5", id, state, detail, next, lease)
		if err != nil {
			return err
		}
		if completed.RowsAffected() == 0 {
			continue
		}
		if kind == "fulfill" && state == "failed" {
			_, _ = a.DB.Exec(ctx, "UPDATE orders SET fulfillment='failed' WHERE id=$1 AND state='paid' AND fulfillment<>'active'", subject)
		}
	}
	return nil
}
func safeJobError(e error) string {
	var p Problem
	if errors.As(e, &p) && len(p.Message) <= 240 {
		return p.Message
	}
	return "任务暂未完成，请检查连接配置、商业接口版本及服务状态"
}
func (a *App) fulfill(ctx context.Context, order string) error {
	var user, state, fulfillment string
	var snap []byte
	var created time.Time
	var perOrder bool
	var savedPayload []byte
	if e := a.DB.QueryRow(ctx, "SELECT customer_id,state,snapshot,created_at,per_order,fulfillment_payload,fulfillment FROM orders WHERE id=$1", order).Scan(&user, &state, &snap, &created, &perOrder, &savedPayload, &fulfillment); e != nil {
		return e
	}
	if state != "paid" || fulfillment == "active" {
		return nil
	}
	_, secret, secretErr := a.settings(ctx)
	if secretErr != nil {
		return secretErr
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
	var earlier bool
	if e = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE customer_id=$1 AND state='paid' AND fulfillment<>'active' AND NOT per_order AND (created_at,id)<($2,$3))`, user, created, order).Scan(&earlier); e != nil {
		return e
	}
	if earlier && !perOrder {
		return Problem{409, "等待该用户更早的已支付订单开通"}
	}
	var snapshot struct {
		Plan  Plan  `json:"plan"`
		Price Price `json:"price"`
	}
	if e = json.Unmarshal(snap, &snapshot); e != nil {
		return e
	}
	var name, status string
	if e = conn.QueryRow(ctx, "SELECT name,status FROM customers WHERE id=$1", user).Scan(&name, &status); e != nil {
		return e
	}
	if status != "active" {
		if err := a.syncAccess(ctx, user); err != nil {
			return err
		}
		return Problem{409, "账户未激活或已停用，请管理员处理已付款订单"}
	}
	if e = a.checkGroups(ctx, snapshot.Plan.Groups, snapshot.Plan.Protocols); e != nil {
		return e
	}
	var result struct {
		UserID         string `json:"user_id"`
		SubscriptionID string `json:"subscription_id"`
		Operation      string `json:"operation_id"`
	}
	body := map[string]any{"external_id": "portal:" + order, "order_id": order, "name": grantName(name, snapshot.Plan.Name, order), "months": snapshot.Price.Months, "quota_bytes": snapshot.Plan.Quota, "group_ids": snapshot.Plan.Groups, "protocols": snapshot.Plan.Protocols, "max_devices": snapshot.Plan.MaxDevices, "billing_mode": snapshot.Plan.Billing, "multiplier_milli": snapshot.Plan.Multiplier}
	if !perOrder {
		body["external_id"] = "portal:" + user
		body["name"] = name
	}
	if len(savedPayload) == 0 {
		sealed, err := a.seal(body)
		if err != nil {
			return err
		}
		if err = conn.QueryRow(ctx, "UPDATE orders SET fulfillment_payload=COALESCE(fulfillment_payload,$2) WHERE id=$1 RETURNING fulfillment_payload", order, sealed).Scan(&savedPayload); err != nil {
			return err
		}
	}
	if e = a.unseal(savedPayload, &body); e != nil {
		return e
	}
	if e = a.control(ctx, "POST", "commerce/entitlements", body, &result); e != nil {
		return e
	}
	if len(result.UserID) != 32 || len(result.SubscriptionID) != 32 {
		return errors.New("invalid entitlement response")
	}
	// Persist the mapping before waiting for Agent convergence. A concurrent
	// disable job can then revoke the actual identity, even if this job retries.
	if _, e = conn.Exec(ctx, `INSERT INTO service_accounts(customer_id,control_user_id,subscription_id,plan_id,last_order_id,order_id,external_id,legacy_combined) VALUES($1,$2,$3,$4,$5,$5,$6,$7) ON CONFLICT(order_id) DO UPDATE SET updated_at=now()`, user, result.UserID, result.SubscriptionID, snapshot.Plan.ID, order, body["external_id"], !perOrder); e != nil {
		return e
	}
	if e = conn.QueryRow(ctx, "SELECT status FROM customers WHERE id=$1", user).Scan(&status); e != nil {
		return e
	}
	if status != "active" {
		if e = a.syncAccess(ctx, user); e != nil {
			return e
		}
		return Problem{409, "账户已停用，已请求撤销代理身份"}
	}
	var ready struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}
	if e = a.control(ctx, "GET", "subscriptions/"+result.SubscriptionID+"/check?client=sing-box-latest", nil, &ready); e != nil {
		return e
	}
	if !ready.Ready {
		return Problem{409, "权益已保存，等待节点配置和独立身份同步就绪"}
	}
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `INSERT INTO service_accounts(customer_id,control_user_id,subscription_id,plan_id,last_order_id,order_id,external_id,legacy_combined) VALUES($1,$2,$3,$4,$5,$5,$6,$7) ON CONFLICT(order_id) DO UPDATE SET updated_at=now()`, user, result.UserID, result.SubscriptionID, snapshot.Plan.ID, order, body["external_id"], !perOrder); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO entitlement_ledger(order_id,customer_id,control_operation) VALUES($1,$2,$3) ON CONFLICT(order_id) DO NOTHING", order, user, result.Operation); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE orders SET fulfillment='active' WHERE id=$1 AND state='paid'", order); e != nil {
		return e
	}
	if e = queueTelegram(ctx, tx, "active:"+order, order, "service.active"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *App) syncAccess(ctx context.Context, user string) error {
	for pass := 0; pass < 3; pass++ {
		var status string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM customers WHERE id=$1", user).Scan(&status); e != nil {
			return e
		}
		// External ID remains authoritative if a grant succeeded remotely but its
		// response was lost before the local account mapping could be persisted.
		rows, e := a.DB.Query(ctx, `SELECT external_id FROM service_accounts WHERE customer_id=$1 UNION SELECT 'portal:'||id FROM orders WHERE customer_id=$1 AND state='paid' UNION SELECT 'portal:'||$1::text WHERE NOT EXISTS(SELECT 1 FROM service_migrations WHERE customer_id=$1 AND state='applied')`, user)
		if e != nil {
			return e
		}
		external := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			external = append(external, id)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		for _, id := range external {
			err := a.control(ctx, "PUT", "commerce/accounts/"+strings.TrimPrefix(id, "portal:")+"/state", map[string]any{"external_id": id, "enabled": status == "active"}, nil)
			var p Problem
			if errors.As(err, &p) && p.Status == 404 {
				continue
			}
			if err != nil {
				return err
			}
		}
		var latest string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM customers WHERE id=$1", user).Scan(&latest); e != nil {
			return e
		}
		if latest == status {
			return nil
		}
	}
	return Problem{409, "账户权限持续变化，等待重试同步"}
}
func (a *App) sendJobMail(ctx context.Context, payload []byte) error {
	var p map[string]string
	if e := a.unseal(payload, &p); e != nil {
		return e
	}
	var valid bool
	if e := a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM email_tokens t JOIN customers c ON c.id=t.customer_id WHERE t.token_hash=$1 AND t.used_at IS NULL AND t.expires_at>$2 AND c.deleted_at IS NULL)", hash(p["token"]), a.now()).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return nil
	}
	s, secrets, e := a.settings(ctx)
	if e != nil {
		return e
	}
	origin := s.PublicOrigin
	if origin == "" {
		origin = a.Origin
	}
	purpose := p["purpose"]
	subject, fragment := "验证你的星枢账户邮箱", "verify"
	if purpose == "reset" {
		subject, fragment = "重设你的星枢账户密码", "reset"
	}
	if purpose == "change_email" {
		subject = "验证你的新邮箱"
		fragment = "verify-email"
	}
	body := "请打开以下链接完成操作。请勿转发此链接：\r\n" + origin + "/#" + fragment + "=" + p["token"] + "\r\n如果不是你本人发起，请忽略此邮件。"
	if a.Mail != nil {
		return a.Mail(ctx, p["email"], subject, body)
	}
	if secrets.SMTPHost == "" || secrets.SMTPFrom == "" {
		return Problem{503, "邮箱服务尚未配置"}
	}
	port := secrets.SMTPPort
	if port == 0 {
		port = 587
	}
	address := net.JoinHostPort(secrets.SMTPHost, fmt.Sprint(port))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, e := dialer.DialContext(ctx, "tcp", address)
	if e != nil {
		return errors.New("SMTP 连接失败")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	cfg := &tls.Config{ServerName: secrets.SMTPHost, MinVersion: tls.VersionTLS12}
	if port == 465 {
		secure := tls.Client(conn, cfg)
		if e = secure.HandshakeContext(ctx); e != nil {
			return errors.New("SMTP TLS 验证失败")
		}
		conn = secure
	}
	c, e := smtp.NewClient(conn, secrets.SMTPHost)
	if e != nil {
		return errors.New("SMTP 握手失败")
	}
	defer c.Close()
	if port == 587 {
		if e = c.StartTLS(cfg); e != nil {
			return errors.New("SMTP 必须支持 STARTTLS")
		}
	}
	if secrets.SMTPUser != "" {
		if e = c.Auth(smtp.PlainAuth("", secrets.SMTPUser, secrets.SMTPPassword, secrets.SMTPHost)); e != nil {
			return errors.New("SMTP 鉴权失败")
		}
	}
	if e = c.Mail(secrets.SMTPFrom); e != nil {
		return errors.New("SMTP 发件地址不可用")
	}
	if e = c.Rcpt(p["email"]); e != nil {
		return errors.New("SMTP 收件地址不可用")
	}
	stream, e := c.Data()
	if e != nil {
		return e
	}
	message := "From: " + secrets.SMTPFrom + "\r\nTo: " + p["email"] + "\r\nSubject: =?UTF-8?B?" + encodeHeader(subject) + "?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	if _, e = stream.Write([]byte(message)); e != nil {
		return e
	}
	if e = stream.Close(); e != nil {
		return e
	}
	return c.Quit()
}
func (a *App) cleanup(ctx context.Context) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, "SELECT customer_id,invitation_id FROM invitation_claims WHERE NOT finalized AND NOT released AND expires_at<=$1 FOR UPDATE", a.now())
	if e != nil {
		return e
	}
	type claim struct{ user, invite string }
	due := []claim{}
	for rows.Next() {
		var c claim
		if e = rows.Scan(&c.user, &c.invite); e != nil {
			rows.Close()
			return e
		}
		due = append(due, c)
	}
	rows.Close()
	for _, c := range due {
		if _, e = tx.Exec(ctx, "UPDATE invitations SET claimed=GREATEST(0,claimed-1) WHERE id=$1", c.invite); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE invitation_claims SET released=true WHERE customer_id=$1", c.user); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE customers SET status='expired' WHERE id=$1 AND status='unverified'", c.user); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "DELETE FROM sessions WHERE expires_at<$1", a.now()); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "DELETE FROM rate_limits WHERE reset_at<$1", a.now().Add(-time.Hour)); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE orders o SET state='cancelled' WHERE state='pending' AND expires_at<=$1 AND NOT EXISTS(SELECT 1 FROM payments p WHERE p.order_id=o.id) AND NOT EXISTS(SELECT 1 FROM manual_payments m WHERE m.order_id=o.id AND m.state='submitted' AND m.review_deadline>$1)", a.now()); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE manual_payments m SET state='cancelled',review_note=CASE WHEN m.state='submitted' THEN '审核期已过，请通过工单联系管理员核实' ELSE m.review_note END,version=m.version+1 FROM orders o WHERE o.id=m.order_id AND o.state='cancelled' AND m.state IN ('awaiting','submitted','rejected')"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *App) reconcile(ctx context.Context) error {
	rows, e := a.DB.Query(ctx, `SELECT p.id,p.out_trade_no,c.config,o.expires_at FROM orders o JOIN payments p ON p.order_id=o.id JOIN payment_channels c ON c.id=p.channel_id WHERE o.state='pending' AND o.expires_at<=$1 ORDER BY o.expires_at LIMIT 10`, a.now())
	if e != nil {
		return e
	}
	type pending struct {
		id, out string
		sealed  []byte
		expiry  time.Time
	}
	items := []pending{}
	for rows.Next() {
		var p pending
		if e = rows.Scan(&p.id, &p.out, &p.sealed, &p.expiry); e != nil {
			rows.Close()
			return e
		}
		items = append(items, p)
	}
	rows.Close()
	for _, p := range items {
		var cfg AlipayConfig
		if e = a.unseal(p.sealed, &cfg); e != nil {
			return e
		}
		result, err := a.queryPayment(ctx, cfg, p.out)
		if err != nil {
			continue
		}
		if result.Status == "TRADE_SUCCESS" || result.Status == "TRADE_FINISHED" {
			_ = a.recordPayment(ctx, p.id, result, hash("query/"+p.id+"/"+result.Trade))
			continue
		}
		closed := result.Status == "TRADE_CLOSED" || result.Status == "NOT_CREATED"
		if result.Status == "WAIT_BUYER_PAY" {
			response, err := a.alipayAPI(ctx, cfg, "alipay.trade.close", map[string]string{"out_trade_no": p.out})
			closed = err == nil && field(response, "code") == "10000"
		}
		if closed {
			_, _ = a.DB.Exec(ctx, `UPDATE orders SET state='cancelled' WHERE state='pending' AND id=(SELECT order_id FROM payments WHERE id=$1)`, p.id)
		}
	}
	return nil
}
func encodeHeader(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
