package portal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var imageSlots = make(chan struct{}, 2)
var transferReference = regexp.MustCompile(`^[A-Za-z0-9_-]{6,100}$`)

func decodeMedia(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1500<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return bad("图片或字段过大/格式错误，请使用 1MB 以内 PNG/JPEG")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return bad("仅接受一个 JSON 对象")
	}
	return nil
}
func parseImage(data string) ([]byte, string, error) {
	if data == "" {
		return nil, "", nil
	}
	parts := strings.SplitN(data, ",", 2)
	if len(parts) != 2 || !includes([]string{"data:image/png;base64", "data:image/jpeg;base64"}, parts[0]) {
		return nil, "", bad("仅支持 PNG 或 JPEG 图片，不支持 SVG、外链或动态内容")
	}
	raw, e := base64.StdEncoding.DecodeString(parts[1])
	if e != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return nil, "", bad("图片应小于 1MB")
	}
	mime := http.DetectContentType(raw)
	if "data:"+mime+";base64" != parts[0] {
		return nil, "", bad("图片内容与类型不一致")
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || !includes([]string{"png", "jpeg"}, format) || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 12000000 {
		return nil, "", bad("图片无法识别或像素过大")
	}
	return raw, mime, nil
}
func (a *App) paymentMethods(w http.ResponseWriter, r *http.Request) error {
	_, secret, e := a.settings(r.Context())
	if e != nil {
		return e
	}
	data, e := jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x ORDER BY x.id),'[]') FROM(SELECT id,provider,name,CASE WHEN id='alipay_redpacket' THEN 'redpacket' ELSE 'manual' END type,instructions FROM manual_payment_methods WHERE enabled AND (qr_image IS NOT NULL OR id='alipay_redpacket'))x`)
	if e != nil {
		return e
	}
	var methods []map[string]any
	if e = json.Unmarshal(data, &methods); e != nil {
		return e
	}
	if secret.Alipay != nil && secret.Alipay.Enabled {
		methods = append(methods, map[string]any{"id": "alipay_official", "provider": "alipay", "type": "official", "name": "支付宝在线支付"})
	}
	send(w, 200, methods)
	return nil
}
func (a *App) configurePaymentMethods(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	ctx := r.Context()
	if r.Method == "GET" {
		rows, e := jsonRows(ctx, a.DB, `SELECT COALESCE(jsonb_agg(x ORDER BY x.id),'[]') FROM(SELECT id,provider,name,instructions,enabled,(qr_image IS NOT NULL) image_configured,version,updated_at FROM manual_payment_methods)x`)
		if e != nil {
			return e
		}
		send(w, 200, rows)
		return nil
	}
	imageSlots <- struct{}{}
	defer func() { <-imageSlots }()
	var b struct {
		ID           string `json:"id"`
		Version      int    `json:"version"`
		Name         string `json:"name"`
		Instructions string `json:"instructions"`
		Enabled      bool   `json:"enabled"`
		Image        string `json:"image_data"`
		Clear        bool   `json:"clear_image"`
	}
	if e := decodeMedia(w, r, &b); e != nil {
		return e
	}
	if !includes([]string{"alipay_static", "wechat_static", "alipay_redpacket"}, b.ID) || strings.TrimSpace(b.Name) == "" || !cleanText(b.Name, 60) || !cleanText(b.Instructions, 2000) || b.Clear && b.Image != "" {
		return bad("收款方式参数无效")
	}
	img, mime, e := parseImage(b.Image)
	if e != nil {
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
	var old []byte
	var oldMime string
	var version int
	if e = tx.QueryRow(ctx, "SELECT qr_image,qr_mime,version FROM manual_payment_methods WHERE id=$1 FOR UPDATE", b.ID).Scan(&old, &oldMime, &version); e != nil {
		return e
	}
	if version != b.Version {
		return Problem{409, "收款码已变化，请刷新"}
	}
	if b.Image == "" {
		img, mime = old, oldMime
	}
	if b.Clear {
		img = nil
		mime = ""
	}
	if b.ID == "alipay_redpacket" && b.Image != "" {
		return bad("口令红包方式不使用收款码图片")
	}
	if b.Enabled && len(img) == 0 && b.ID != "alipay_redpacket" {
		return bad("启用前请上传真实收款码")
	}
	if _, e = tx.Exec(ctx, "UPDATE manual_payment_methods SET name=$2,instructions=$3,enabled=$4,qr_image=$5,qr_mime=$6,version=version+1,updated_at=now() WHERE id=$1", b.ID, b.Name, b.Instructions, b.Enabled, img, mime); e != nil {
		return e
	}
	if e = audit(ctx, tx, current(r).ID, "manual-method.save", b.ID); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]bool{"saved": true})
	return nil
}
func (a *App) methodImage(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	var img []byte
	var mime string
	if e := a.DB.QueryRow(r.Context(), "SELECT qr_image,qr_mime FROM manual_payment_methods WHERE id=$1 AND qr_image IS NOT NULL", r.PathValue("id")).Scan(&img, &mime); e != nil {
		return e
	}
	return sendImage(w, img, mime)
}
func sendImage(w http.ResponseWriter, img []byte, mime string) error {
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, e := w.Write(img)
	return e
}

// An attempt snapshots the receiving QR and exact discounted order amount.
func (a *App) beginManualPayment(w http.ResponseWriter, r *http.Request, method string) error {
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	var state string
	var expiry time.Time
	if e = tx.QueryRow(ctx, "SELECT state,expires_at FROM orders WHERE id=$1 AND customer_id=$2 FOR UPDATE", r.PathValue("id"), current(r).ID).Scan(&state, &expiry); e != nil {
		return e
	}
	if state != "pending" || !expiry.After(a.now()) {
		return bad("订单已过期或不可支付")
	}
	if e = checkRenewalInvoice(ctx, tx, r.PathValue("id"), a.now()); e != nil {
		return e
	}
	var official bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payments WHERE order_id=$1)", r.PathValue("id")).Scan(&official); e != nil {
		return e
	}
	if official {
		return Problem{409, "此订单已使用在线支付，请先取消并确认渠道关闭后重新下单"}
	}
	var oldMethod string
	e = tx.QueryRow(ctx, "SELECT method_id FROM manual_payments WHERE order_id=$1", r.PathValue("id")).Scan(&oldMethod)
	if e == nil {
		if method != oldMethod {
			return Problem{409, "订单已绑定收款方式，请使用原方式或重新下单"}
		}
	} else if e == pgx.ErrNoRows {
		tag, err := tx.Exec(ctx, `INSERT INTO manual_payments(id,order_id,method_id,provider,method_name,instructions,qr_image,qr_mime,review_deadline) SELECT $1,$2,id,provider,name,instructions,qr_image,qr_mime,$4 FROM manual_payment_methods WHERE id=$3 AND enabled AND (qr_image IS NOT NULL OR id='alipay_redpacket')`, ID(), r.PathValue("id"), method, expiry.Add(48*time.Hour))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return bad("所选收款码尚未启用")
		}
	} else {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return a.manualStatus(w, r)
}
func (a *App) manualStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var exists bool
	if e := a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1 AND customer_id=$2)", r.PathValue("id"), current(r).ID).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return pgx.ErrNoRows
	}
	data, e := jsonRows(ctx, a.DB, `SELECT COALESCE((SELECT to_jsonb(x) FROM(SELECT m.id,m.method_id,m.method_name,m.instructions,(m.redpacket_sealed IS NOT NULL) redpacket_submitted,o.id payment_reference,m.state,m.reference,m.payer_note,(m.proof_image IS NOT NULL) proof_available,m.version,m.review_note,m.submitted_at,m.review_deadline,o.payable,o.expires_at,o.state order_state FROM manual_payments m JOIN orders o ON o.id=m.order_id WHERE m.order_id=$1 AND o.customer_id=$2)x),'null')`, r.PathValue("id"), current(r).ID)
	if e != nil {
		return e
	}
	send(w, 200, map[string]any{"attempt": data})
	return nil
}
func (a *App) manualImage(w http.ResponseWriter, r *http.Request) error {
	var img []byte
	var mime string
	column, mimeColumn := "qr_image", "qr_mime"
	if strings.HasSuffix(r.URL.Path, "/receipt") {
		column, mimeColumn = "proof_image", "proof_mime"
	}
	e := a.DB.QueryRow(r.Context(), "SELECT m."+column+",m."+mimeColumn+" FROM manual_payments m JOIN orders o ON o.id=m.order_id WHERE o.id=$1 AND (o.customer_id=$2 OR $3) AND m."+column+" IS NOT NULL", r.PathValue("id"), current(r).ID, current(r).can("payments")).Scan(&img, &mime)
	if e != nil {
		return e
	}
	return sendImage(w, img, mime)
}
func (a *App) submitReceipt(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "receipt", 20); e != nil {
		return e
	}
	imageSlots <- struct{}{}
	defer func() { <-imageSlots }()
	var b struct {
		Redpacket string `json:"redpacket_password"`
		Reference string `json:"reference"`
		Note      string `json:"note"`
		Image     string `json:"image_data"`
		Version   int    `json:"version"`
	}
	if e := decodeMedia(w, r, &b); e != nil {
		return e
	}
	b.Reference = strings.TrimSpace(b.Reference)
	if !cleanText(b.Reference, 100) || !cleanText(b.Note, 1000) {
		return bad("请填写付款流水或上传付款截图")
	}
	img, mime, e := parseImage(b.Image)
	if e != nil {
		return e
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	var id, state, orderState, method string
	var version int
	var expiry time.Time
	var oldImage, sealed []byte
	var oldMime string
	if e = tx.QueryRow(ctx, `SELECT m.id,m.state,m.version,o.state,o.expires_at,m.proof_image,m.proof_mime,m.method_id,m.redpacket_sealed FROM manual_payments m JOIN orders o ON o.id=m.order_id WHERE o.id=$1 AND o.customer_id=$2 FOR UPDATE OF m,o`, r.PathValue("id"), current(r).ID).Scan(&id, &state, &version, &orderState, &expiry, &oldImage, &oldMime, &method, &sealed); e != nil {
		return e
	}
	if version != b.Version {
		return Problem{409, "付款记录已变化，请刷新"}
	}
	if orderState != "pending" || !expiry.After(a.now()) || state == "approved" || state == "cancelled" {
		return Problem{409, "付款窗口已关闭；已转账但未提交的请通过工单联系管理员核实"}
	}
	if e = checkRenewalInvoice(ctx, tx, r.PathValue("id"), a.now()); e != nil {
		return e
	}
	if b.Image == "" {
		img, mime = oldImage, oldMime
	}
	if method == "alipay_redpacket" {
		if b.Redpacket != "" {
			sealed, e = a.saveRedpacket(ctx, tx, r.PathValue("id"), b.Redpacket)
			if e != nil {
				return e
			}
		}
		if len(sealed) == 0 {
			return bad("请提交本订单的红包口令")
		}
	} else if b.Redpacket != "" {
		return bad("当前订单未选择口令红包方式")
	} else if b.Reference == "" && b.Image == "" {
		return bad("请填写付款流水或上传付款截图")
	}
	if _, e = tx.Exec(ctx, `UPDATE manual_payments SET state='submitted',reference=$2,payer_note=$3,proof_image=$4,proof_mime=$5,redpacket_sealed=$6,submitted_at=now(),version=version+1 WHERE id=$1`, id, b.Reference, b.Note, img, mime, sealed); e != nil {
		return e
	}
	if e = queueTelegram(ctx, tx, "receipt:"+id+":"+fmt.Sprint(version+1), r.PathValue("id"), "payment.submitted"); e != nil {
		return e
	}
	if e = audit(ctx, tx, current(r).ID, "manual-payment.submit", id); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": "凭证已提交，管理员核实实际到账后开通；请勿重复付款"})
	return nil
}
func (a *App) reviewPayments(w http.ResponseWriter, r *http.Request) error {
	if e := requireAdminScope(r, "payments"); e != nil {
		return e
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if r.Method == "GET" {
		data, e := jsonRows(ctx, a.DB, `SELECT COALESCE(jsonb_agg(x ORDER BY x.created_at DESC),'[]') FROM(SELECT m.id,m.order_id,m.method_id,m.provider,m.method_name,(m.redpacket_sealed IS NOT NULL) redpacket_submitted,m.state,m.reference,m.payer_note,(m.proof_image IS NOT NULL) proof_available,m.verified_reference,m.received_amount,m.received_at,m.review_note,m.reviewed_at,m.review_deadline,m.submitted_at,m.created_at,m.version,o.payable,o.state order_state,o.customer_id,c.email,c.name FROM manual_payments m JOIN orders o ON o.id=m.order_id JOIN customers c ON c.id=o.customer_id WHERE ($1='' OR m.id=$1) ORDER BY m.created_at DESC LIMIT 300)x`, id)
		if e != nil {
			return e
		}
		if id != "" {
			var items []json.RawMessage
			if e = json.Unmarshal(data, &items); e != nil {
				return e
			}
			if len(items) == 0 {
				return pgx.ErrNoRows
			}
			send(w, 200, items[0])
		} else {
			send(w, 200, data)
		}
		return nil
	}
	var b manualReview
	if e := decode(w, r, &b); e != nil {
		return e
	}
	message, e := a.reviewManual(ctx, current(r), id, b)
	if e != nil {
		return e
	}
	send(w, 200, map[string]string{"message": message})
	return nil
}

type manualReview struct {
	Decision  string    `json:"decision"`
	Version   int       `json:"version"`
	Reference string    `json:"verified_reference"`
	Amount    int64     `json:"received_amount"`
	At        time.Time `json:"received_at"`
	Note      string    `json:"note"`
	Confirmed bool      `json:"confirmed"`
}

func (a *App) reviewManual(ctx context.Context, reviewer actor, id string, b manualReview) (string, error) {
	if !reviewer.can("payments") {
		return "", Problem{403, "没有核款权限"}
	}
	b.Reference = strings.ToUpper(strings.TrimSpace(b.Reference))
	if !includes([]string{"approve", "reject"}, b.Decision) || !cleanText(b.Note, 1000) {
		return "", bad("审核参数无效")
	}
	if b.Decision == "approve" && (!b.Confirmed || b.At.IsZero() || b.At.After(a.now().Add(5*time.Minute))) {
		return "", bad("请核实真实到账金额、时间，并勾选确认")
	}
	if b.Decision == "reject" && strings.TrimSpace(b.Note) == "" {
		return "", bad("请填写驳回原因")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return "", e
	}
	fresh, e := actorRow(tx.QueryRow(ctx, "SELECT id,email,name,role,is_owner,admin_scopes FROM customers WHERE id=$1 AND status='active' AND deleted_at IS NULL FOR UPDATE", reviewer.ID))
	if e != nil || !fresh.can("payments") {
		return "", Problem{403, "核款管理员已停用、已删除或权限已撤销"}
	}
	reviewer = fresh
	var order, user, state, orderState, verified, provider, method string
	var version int
	var amount int64
	var expiry, created, deadline time.Time
	if e = tx.QueryRow(ctx, `SELECT m.order_id,o.customer_id,m.state,m.version,o.payable,o.state,o.expires_at,o.created_at,m.review_deadline,COALESCE(m.verified_reference,''),m.provider,m.method_id FROM manual_payments m JOIN orders o ON o.id=m.order_id WHERE m.id=$1 FOR UPDATE OF m,o`, id).Scan(&order, &user, &state, &version, &amount, &orderState, &expiry, &created, &deadline, &verified, &provider, &method); e != nil {
		return "", e
	}
	if b.Decision == "approve" {
		var deleted bool
		if e = tx.QueryRow(ctx, "SELECT deleted_at IS NOT NULL FROM customers WHERE id=$1", user).Scan(&deleted); e != nil {
			return "", e
		}
		if deleted {
			return "", Problem{409, "购买用户已删除，请先核对恢复账户或人工处理退款"}
		}
	}
	if b.Decision == "approve" {
		if method == "alipay_redpacket" && b.Reference == "" {
			b.Reference = "REDPACK-" + hash(order)[:32]
		}
		if !transferReference.MatchString(b.Reference) {
			return "", bad("静态收款码核验需填写有效交易流水；口令红包可留空")
		}
	}
	if user == reviewer.ID {
		return "", Problem{403, "不能审核自己提交的付款，请由其他有权限的管理员处理"}
	}
	if state == "approved" && b.Decision == "approve" && verified == b.Reference && amount == b.Amount {
		return "该付款已确认，不会重复开通", nil
	}
	if version != b.Version || state != "submitted" || orderState != "pending" || !deadline.After(a.now()) {
		return "", Problem{409, "付款状态已变化、未提交凭证或审核期已过，请刷新核对"}
	}
	if b.Decision == "reject" {
		_, e = tx.Exec(ctx, "UPDATE manual_payments SET state='rejected',review_note=$2,reviewed_by=$3,reviewed_at=now(),version=version+1 WHERE id=$1", id, b.Note, reviewer.ID)
	} else {
		var usedOnline bool
		if provider == "alipay" {
			if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payments WHERE UPPER(trade_no)=$1 AND state='paid')", b.Reference).Scan(&usedOnline); e != nil {
				return "", e
			}
			if usedOnline {
				return "", Problem{409, "此交易流水已用于在线支付订单"}
			}
		}
		if b.Amount != amount {
			return "", bad("实收金额必须与订单优惠后应付金额一致")
		}
		paymentEnd := expiry
		if method == "alipay_redpacket" {
			paymentEnd = deadline
		}
		if !b.At.Before(paymentEnd) || b.At.Before(created.Add(-time.Minute)) {
			return "", bad("到账时间不在本订单付款/核验窗口内，不能直接开通，请人工处理退款或新订单")
		}
		if e = checkRenewalInvoice(ctx, tx, order, b.At); e != nil {
			return "", e
		}
		_, e = tx.Exec(ctx, `UPDATE manual_payments SET state='approved',verified_reference=$2,received_amount=$3,received_at=$4,review_note=$5,reviewed_by=$6,reviewed_at=now(),version=version+1 WHERE id=$1`, id, b.Reference, b.Amount, b.At, b.Note, reviewer.ID)
		if e != nil {
			return "", e
		}
		if _, e = tx.Exec(ctx, "UPDATE orders SET state='paid',paid_at=$2,fulfillment='queued' WHERE id=$1", order, b.At); e != nil {
			return "", e
		}
		_, e = tx.Exec(ctx, "INSERT INTO jobs(id,kind,subject) VALUES($1,'fulfill',$2) ON CONFLICT(kind,subject) DO NOTHING", ID(), order)
	}
	if e != nil {
		return "", e
	}
	if e = queueTelegram(ctx, tx, "review:"+id+":"+fmt.Sprint(version+1), order, "payment."+b.Decision); e != nil {
		return "", e
	}
	if e = audit(ctx, tx, reviewer.ID, "manual-payment."+b.Decision, id); e != nil {
		return "", e
	}
	if e = tx.Commit(ctx); e != nil {
		return "", e
	}
	if b.Decision == "approve" {
		return "到账已核实，已排队开通；重复提交不会重复发放权益", nil
	}
	return "已驳回，用户可在付款窗口内补充凭证", nil
}
