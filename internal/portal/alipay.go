package portal

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func privateRSA(s string) (*rsa.PrivateKey, error) {
	p, _ := pem.Decode([]byte(s))
	if p == nil {
		return nil, bad("支付宝应用私钥需为 PEM 格式")
	}
	if v, e := x509.ParsePKCS8PrivateKey(p.Bytes); e == nil {
		if key, ok := v.(*rsa.PrivateKey); ok && key.N.BitLen() >= 2048 {
			return key, nil
		}
	}
	if key, e := x509.ParsePKCS1PrivateKey(p.Bytes); e == nil && key.N.BitLen() >= 2048 {
		return key, nil
	}
	return nil, bad("应用私钥须为至少 2048 位的 RSA 密钥")
}
func publicRSA(s string) (*rsa.PublicKey, error) {
	p, _ := pem.Decode([]byte(s))
	if p == nil {
		return nil, bad("支付宝公钥需为 PEM 格式")
	}
	if v, e := x509.ParsePKIXPublicKey(p.Bytes); e == nil {
		if key, ok := v.(*rsa.PublicKey); ok && key.N.BitLen() >= 2048 {
			return key, nil
		}
	}
	if cert, e := x509.ParseCertificate(p.Bytes); e == nil {
		if key, ok := cert.PublicKey.(*rsa.PublicKey); ok && key.N.BitLen() >= 2048 {
			return key, nil
		}
	}
	return nil, bad("支付宝公钥或公钥证书无效")
}
func validateAlipay(c AlipayConfig) error {
	if len(c.AppID) < 8 || len(c.AppID) > 32 || len(c.SellerID) < 8 || len(c.SellerID) > 32 {
		return bad("请填写支付宝 App ID 和收款商户 ID")
	}
	if _, e := privateRSA(c.PrivateKey); e != nil {
		return e
	}
	if _, e := publicRSA(c.PublicKey); e != nil {
		return e
	}
	return nil
}
func canonical(v url.Values, notification bool) string {
	keys := []string{}
	for k, vs := range v {
		if k == "sign" || notification && k == "sign_type" || len(vs) == 0 || vs[0] == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, k+"="+v.Get(k))
	}
	return strings.Join(parts, "&")
}
func signed(c AlipayConfig, v url.Values) error {
	key, e := privateRSA(c.PrivateKey)
	if e != nil {
		return e
	}
	digest := sha256.Sum256([]byte(canonical(v, false)))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if e != nil {
		return e
	}
	v.Set("sign", base64.StdEncoding.EncodeToString(sig))
	return nil
}
func verifyAlipay(c AlipayConfig, v url.Values) error {
	key, e := publicRSA(c.PublicKey)
	if e != nil {
		return e
	}
	sig, e := base64.StdEncoding.DecodeString(v.Get("sign"))
	if e != nil {
		return bad("支付签名无效")
	}
	h := sha256.Sum256([]byte(canonical(v, true)))
	if e = rsa.VerifyPKCS1v15(key, crypto.SHA256, h[:], sig); e != nil {
		return bad("支付签名校验失败")
	}
	return nil
}
func gateway(c AlipayConfig) string {
	if c.Sandbox {
		return "https://openapi-sandbox.dl.alipaydev.com/gateway.do"
	}
	return "https://openapi.alipay.com/gateway.do"
}

var china = time.FixedZone("Asia/Shanghai", 8*3600)

func alipayTime(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04:05", s, china)
}
func money(n int64) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }
func parseMoney(s string) (int64, error) {
	if len(s) > 16 || s == "" {
		return 0, bad("金额无效")
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, bad("金额无效")
	}
	for _, c := range strings.Join(parts, "") {
		if c < '0' || c > '9' {
			return 0, bad("金额无效")
		}
	}
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || whole > 10000000 {
		return 0, bad("金额无效")
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, bad("金额精度无效")
		}
		fraction, _ = strconv.ParseInt(parts[1], 10, 64)
		if len(parts[1]) == 1 {
			fraction *= 10
		}
	}
	return whole*100 + fraction, nil
}
func (a *App) params(c AlipayConfig, method string, biz any) url.Values {
	return url.Values{"app_id": {c.AppID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {a.now().In(china).Format("2006-01-02 15:04:05")}, "version": {"1.0"}, "biz_content": {string(raw(biz))}}
}
func (a *App) pay(w http.ResponseWriter, r *http.Request) error {
	var choice struct {
		Method string `json:"method_id"`
	}
	if e := decode(w, r, &choice); e != nil {
		return e
	}
	if choice.Method != "" && choice.Method != "alipay_official" {
		if !includes([]string{"alipay_static", "wechat_static", "alipay_redpacket"}, choice.Method) {
			return bad("未知收款方式")
		}
		return a.beginManualPayment(w, r, choice.Method)
	}
	var manual bool
	if e := a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM manual_payments m JOIN orders o ON o.id=m.order_id WHERE o.id=$1 AND o.customer_id=$2)", r.PathValue("id"), current(r).ID).Scan(&manual); e != nil {
		return e
	}
	if manual {
		return Problem{409, "此订单已绑定个人收款码，请使用原方式或重新下单"}
	}

	if e := a.limit(r, "pay", 30); e != nil {
		return e
	}
	s, secret, e := a.settings(r.Context())
	if e != nil {
		return e
	}
	if secret.Alipay == nil || !secret.Alipay.Enabled {
		return Problem{503, "支付宝尚未配置或暂不可用"}
	}
	origin := s.PublicOrigin
	if origin == "" {
		origin = a.Origin
	}
	if !strings.HasPrefix(origin, "https://") {
		return bad("支付宝需要配置 HTTPS 公网入口")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM manual_payments WHERE order_id=$1)", r.PathValue("id")).Scan(&manual); e != nil {
		return e
	}
	if manual {
		return Problem{409, "订单已经绑定个人收款码"}
	}
	var amount int64
	var state, name string
	var expires time.Time
	if e = tx.QueryRow(r.Context(), `SELECT payable,state,expires_at,snapshot->'plan'->>'name' FROM orders WHERE id=$1 AND customer_id=$2 FOR UPDATE`, r.PathValue("id"), current(r).ID).Scan(&amount, &state, &expires, &name); e != nil {
		return e
	}
	if state != "pending" || !expires.After(a.now()) {
		return bad("订单不可支付或已过期")
	}
	if e = checkRenewalInvoice(r.Context(), tx, r.PathValue("id"), a.now()); e != nil {
		return e
	}
	var channelID, paymentID, outNo string
	var encrypted []byte
	e = tx.QueryRow(r.Context(), "SELECT p.id,p.out_trade_no,p.channel_id,c.config FROM payments p JOIN payment_channels c ON c.id=p.channel_id WHERE p.order_id=$1", r.PathValue("id")).Scan(&paymentID, &outNo, &channelID, &encrypted)
	if e == pgx.ErrNoRows {
		if e = tx.QueryRow(r.Context(), "SELECT id,config FROM payment_channels ORDER BY created_at DESC LIMIT 1").Scan(&channelID, &encrypted); e != nil {
			return Problem{503, "支付渠道尚未保存"}
		}
		paymentID = ID()
		outNo = ID()
		if _, e = tx.Exec(r.Context(), "INSERT INTO payments(id,order_id,channel_id,out_trade_no) VALUES($1,$2,$3,$4)", paymentID, r.PathValue("id"), channelID, outNo); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	var c AlipayConfig
	if e = a.unseal(encrypted, &c); e != nil {
		return e
	}
	method, product := "alipay.trade.page.pay", "FAST_INSTANT_TRADE_PAY"
	if r.URL.Query().Get("mobile") == "1" {
		if !c.Mobile {
			return bad("管理员尚未启用手机网站支付，请在桌面浏览器完成付款")
		}
		method, product = "alipay.trade.wap.pay", "QUICK_WAP_WAY"
	}
	p := a.params(c, method, map[string]any{"out_trade_no": outNo, "product_code": product, "total_amount": money(amount), "subject": name, "time_expire": expires.In(china).Format("2006-01-02 15:04:05"), "quit_url": origin + "/#orders"})
	p.Set("notify_url", origin+"/portal/v1/payments/alipay/notify")
	p.Set("return_url", origin+"/#orders")
	if e = signed(c, p); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]any{"url": gateway(c) + "?" + p.Encode(), "expires_at": expires, "payable": amount})
	return nil
}

type paymentResult struct {
	Trade           string
	Amount          int64
	Status          string
	Paid            time.Time
	AppID, SellerID string
}

func (a *App) alipayNotify(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if e := r.ParseForm(); e != nil {
		return bad("支付通知格式无效")
	}
	v := r.PostForm
	for _, values := range v {
		if len(values) != 1 {
			return bad("重复支付通知字段")
		}
	}
	var c AlipayConfig
	var sealed []byte
	var id string
	if e := a.DB.QueryRow(r.Context(), "SELECT p.id,c.config FROM payments p JOIN payment_channels c ON c.id=p.channel_id WHERE p.out_trade_no=$1", v.Get("out_trade_no")).Scan(&id, &sealed); e != nil {
		return bad("支付单不存在")
	}
	if e := a.unseal(sealed, &c); e != nil {
		return e
	}
	if v.Get("sign_type") != "RSA2" {
		return bad("仅支持 RSA2 签名")
	}
	if e := verifyAlipay(c, v); e != nil {
		return e
	}
	amount, e := parseMoney(v.Get("total_amount"))
	if e != nil {
		return e
	}
	if v.Get("app_id") != c.AppID || v.Get("seller_id") != c.SellerID {
		return bad("支付应用或收款方不匹配")
	}
	result := paymentResult{Trade: v.Get("trade_no"), Amount: amount, Status: v.Get("trade_status"), AppID: c.AppID, SellerID: c.SellerID}
	if result.Status == "TRADE_SUCCESS" || result.Status == "TRADE_FINISHED" {
		result.Paid, e = alipayTime(v.Get("gmt_payment"))
		if e != nil {
			result, e = a.queryPayment(r.Context(), c, v.Get("out_trade_no"))
			if e != nil {
				return e
			}
		}
	}
	if e = a.recordPayment(r.Context(), id, result, hash(canonical(v, true))); e != nil {
		return e
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(200)
	_, e = io.WriteString(w, "success")
	return e
}
func (a *App) recordPayment(ctx context.Context, paymentID string, p paymentResult, event string) error {
	if !includes([]string{"TRADE_SUCCESS", "TRADE_FINISHED", "WAIT_BUYER_PAY", "TRADE_CLOSED"}, p.Status) {
		return bad("未知支付状态")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	var orderID, state string
	var amount int64
	var expires time.Time
	if e = tx.QueryRow(ctx, "SELECT o.id,o.state,o.payable,o.expires_at FROM payments p JOIN orders o ON o.id=p.order_id WHERE p.id=$1 FOR UPDATE OF o", paymentID).Scan(&orderID, &state, &amount, &expires); e != nil {
		return e
	}
	if p.Amount != amount {
		return bad("支付金额与优惠后订单实付不一致")
	}
	if _, e = tx.Exec(ctx, "INSERT INTO payment_events(id,payment_id,event_hash,state) VALUES($1,$2,$3,$4) ON CONFLICT(event_hash) DO NOTHING", ID(), paymentID, event, p.Status); e != nil {
		return e
	}
	if p.Status == "TRADE_SUCCESS" || p.Status == "TRADE_FINISHED" {
		if p.Trade == "" || len(p.Trade) > 80 || p.Paid.IsZero() || p.Paid.After(a.now().Add(5*time.Minute)) {
			return bad("支付交易号或付款时间无效")
		}
		tag, err := tx.Exec(ctx, "UPDATE payments SET trade_no=$2,state='paid' WHERE id=$1 AND (trade_no IS NULL OR trade_no=$2)", paymentID, p.Trade)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return bad("交易号不匹配")
		}
		if state == "pending" {
			renewalErr := checkRenewalInvoice(ctx, tx, orderID, p.Paid)
			var policy Problem
			if renewalErr != nil && !errors.As(renewalErr, &policy) {
				return renewalErr
			}
			if !p.Paid.Before(expires) || renewalErr != nil {
				_, e = tx.Exec(ctx, "UPDATE orders SET state='late_paid',paid_at=$2 WHERE id=$1", orderID, p.Paid)
			} else {
				if _, e = tx.Exec(ctx, "UPDATE orders SET state='paid',paid_at=$2,fulfillment='queued' WHERE id=$1", orderID, p.Paid); e != nil {
					return e
				}
				_, e = tx.Exec(ctx, "INSERT INTO jobs(id,kind,subject) VALUES($1,'fulfill',$2) ON CONFLICT(kind,subject) DO NOTHING", ID(), orderID)
			}
		} else if state == "cancelled" {
			_, e = tx.Exec(ctx, "UPDATE orders SET state='late_paid',paid_at=$2 WHERE id=$1", orderID, p.Paid)
		}
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *App) alipayAPI(ctx context.Context, c AlipayConfig, method string, biz any) (map[string]json.RawMessage, error) {
	p := a.params(c, method, biz)
	if e := signed(c, p); e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", gateway(c), strings.NewReader(p.Encode()))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, e := a.Client.Do(req)
	if e != nil {
		return nil, errors.New("支付宝请求暂不可用")
	}
	defer res.Body.Close()
	body, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(body) > 1<<20 || res.StatusCode != 200 {
		return nil, errors.New("支付宝响应无效")
	}
	var envelope map[string]json.RawMessage
	if e = json.Unmarshal(body, &envelope); e != nil {
		return nil, e
	}
	response := envelope[strings.ReplaceAll(method, ".", "_")+"_response"]
	var sig string
	if e = json.Unmarshal(envelope["sign"], &sig); e != nil {
		return nil, errors.New("支付宝响应缺少签名")
	}
	key, e := publicRSA(c.PublicKey)
	if e != nil {
		return nil, e
	}
	signature, e := base64.StdEncoding.DecodeString(sig)
	if e != nil {
		return nil, e
	}
	digest := sha256.Sum256(response)
	if e = rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); e != nil {
		return nil, errors.New("支付宝响应验签失败")
	}
	var out map[string]json.RawMessage
	e = json.Unmarshal(response, &out)
	return out, e
}
func field(v map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(v[k], &s)
	return s
}
func (a *App) queryPayment(ctx context.Context, c AlipayConfig, outNo string) (paymentResult, error) {
	v, e := a.alipayAPI(ctx, c, "alipay.trade.query", map[string]string{"out_trade_no": outNo})
	if e != nil {
		return paymentResult{}, e
	}
	if field(v, "code") != "10000" {
		if field(v, "sub_code") == "ACQ.TRADE_NOT_EXIST" {
			return paymentResult{Status: "NOT_CREATED"}, nil
		}
		return paymentResult{}, errors.New("支付宝查单尚未成功")
	}
	if field(v, "out_trade_no") != outNo {
		return paymentResult{}, errors.New("支付单号不匹配")
	}
	amount, e := parseMoney(field(v, "total_amount"))
	if e != nil {
		return paymentResult{}, e
	}
	p := paymentResult{Trade: field(v, "trade_no"), Amount: amount, Status: field(v, "trade_status"), AppID: c.AppID, SellerID: c.SellerID}
	if p.Status == "TRADE_SUCCESS" || p.Status == "TRADE_FINISHED" {
		p.Paid, e = alipayTime(field(v, "send_pay_date"))
		if e != nil {
			return p, errors.New("无法核实实际付款时间")
		}
	}
	return p, nil
}
