package portal

import (
	"net/http"
	"regexp"
	"strings"
)

var couponPattern = regexp.MustCompile(`^[A-Z0-9_-]{4,32}$`)
var domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

func (a *App) admin(w http.ResponseWriter, r *http.Request, kind string) error {
	if kind == "settings" || kind == "version" || kind == "capacity" || kind == "service-migrations" {
		if e := requireOwner(r); e != nil {
			return e
		}
	} else {
		if e := requireAdminScope(r, adminResourceScope(kind)); e != nil {
			return e
		}
	}
	if kind == "plans" && r.Method == "DELETE" {
		return a.planLifecycle(w, r)
	}
	if kind == "customers" && r.Method == "DELETE" {
		r.SetPathValue("kind", kind)
		return a.deleteAccount(w, r)
	}
	if kind == "customers" && r.Method == "GET" {
		data, e := jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,email,name,role,status,admin_version,deleted_at,created_at,email_verified_at FROM customers WHERE role='customer' AND (deleted_at IS NOT NULL)=$1 ORDER BY created_at DESC LIMIT 500)x`, r.URL.Query().Get("deleted") == "1")
		if e != nil {
			return e
		}
		send(w, 200, data)
		return nil
	}
	if kind == "plans" && r.Method == "GET" {
		data, e := jsonRows(r.Context(), a.DB, "SELECT COALESCE(jsonb_agg(x ORDER BY x.sort,x.name),'[]') FROM(SELECT p.*,(SELECT count(*) FROM portal_effective_seats(p.id,now())) effective_members,(SELECT count(*) FROM service_accounts s WHERE s.plan_id=p.id AND s.policy_version<p.policy_revision AND NOT s.legacy_combined AND s.remote_state NOT IN ('deleted','invalid')) policy_pending,(SELECT count(*) FROM service_accounts s WHERE s.plan_id=p.id AND s.policy_version<p.policy_revision AND NOT s.legacy_combined AND s.remote_state NOT IN ('deleted','invalid') AND s.policy_error<>'') policy_failed FROM plans p WHERE (deleted_at IS NOT NULL)=$1)x", r.URL.Query().Get("deleted") == "1")
		if e != nil {
			return e
		}
		send(w, 200, data)
		return nil
	}
	if kind == "invitations" {
		return a.manageInvitations(w, r)
	}
	if kind == "settings" {
		return a.adminSettings(w, r)
	}
	if kind == "version" {
		return a.updates(w, r)
	}
	if kind == "service-migrations" {
		if r.Method != "GET" {
			return Problem{405, "method not allowed"}
		}
		data, e := jsonRows(r.Context(), a.DB, `SELECT jsonb_build_object('migrations',(SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT customer_id,state,response,created_at,applied_at FROM service_migrations ORDER BY created_at DESC LIMIT 50)x),'services',(SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT s.order_id,s.customer_id,s.control_user_id,s.subscription_id,s.legacy_combined,o.snapshot->'plan'->'quota_bytes' quota_bytes,o.snapshot->'plan'->>'quota_unit' quota_unit FROM service_accounts s JOIN orders o ON o.id=s.order_id ORDER BY s.updated_at DESC LIMIT 100)x))`)
		if e != nil {
			return e
		}
		send(w, 200, data)
		return nil
	}
	if kind == "capacity" {
		if r.Method != "GET" {
			return Problem{405, "method not allowed"}
		}
		result, e := a.capacity(r.Context())
		if e != nil {
			return e
		}
		syncErr := a.refreshServiceStates(r.Context())
		var registered, services, invalid, unknown, paid int
		var checked any
		e = a.DB.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM customers WHERE role='customer' AND deleted_at IS NULL),count(*) FILTER(WHERE s.remote_state='active' AND c.status='active' AND c.deleted_at IS NULL),count(*) FILTER(WHERE s.remote_state IN ('deleted','invalid','expired','disabled','exhausted') OR c.status<>'active' OR c.deleted_at IS NOT NULL),count(*) FILTER(WHERE s.remote_state='unknown'),(SELECT count(*) FROM orders WHERE state='paid'),min(s.remote_checked_at) FROM service_accounts s JOIN orders o ON o.id=s.order_id JOIN customers c ON c.id=s.customer_id WHERE o.state='paid'`).Scan(&registered, &services, &invalid, &unknown, &paid, &checked)
		if e != nil {
			return e
		}
		result["registered_customers"] = registered
		result["portal_services"] = services
		result["invalid_services"] = invalid
		result["unchecked_services"] = unknown
		result["paid_orders"] = paid
		result["services_checked_at"] = checked
		if syncErr != nil {
			result["sync_warning"] = "星枢状态暂时未能同步，以下为最近一次验证结果"
		}

		send(w, 200, result)
		return nil
	}
	if kind == "control-groups" {
		if r.Method != "GET" {
			return Problem{405, "method not allowed"}
		}
		var result any
		if e := a.control(r.Context(), "GET", "commerce/groups", nil, &result); e != nil {
			return e
		}
		send(w, 200, result)
		return nil
	}
	if r.Method == "GET" {
		if kind == "orders" {
			_ = a.refreshServiceStates(r.Context())
		}
		queries := map[string]string{
			"categories":    `SELECT COALESCE(jsonb_agg(c ORDER BY c.sort,c.name),'[]') FROM categories c`,
			"plans":         `SELECT COALESCE(jsonb_agg(p ORDER BY p.sort,p.name),'[]') FROM plans p`,
			"coupons":       couponListSQL,
			"invitations":   `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,prefix,email,enabled,starts_at,ends_at,max_uses,claimed,created_at FROM invitations ORDER BY created_at DESC LIMIT 200)x`,
			"customers":     `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,email,name,role,status,created_at,email_verified_at FROM customers WHERE role='customer' ORDER BY created_at DESC LIMIT 500)x`,
			"orders":        `SELECT COALESCE(jsonb_agg(to_jsonb(x)-'fulfillment_payload'),'[]') FROM(SELECT o.*,(SELECT s.remote_state FROM service_accounts s WHERE s.order_id=COALESCE(o.renewal_of,o.id)) service_state,(SELECT s.remote_checked_at FROM service_accounts s WHERE s.order_id=COALESCE(o.renewal_of,o.id)) service_checked_at,(SELECT m.state FROM manual_payments m WHERE m.order_id=o.id) manual_state,c.email FROM orders o JOIN customers c ON c.id=o.customer_id WHERE (o.archived_at IS NOT NULL)=$1 ORDER BY o.created_at DESC LIMIT 300)x`,
			"jobs":          `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,kind,subject,state,attempts,available_at,detail,updated_at FROM jobs ORDER BY created_at DESC LIMIT 200)x`,
			"announcements": `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT * FROM announcements ORDER BY pinned DESC,pin_order,created_at DESC,id LIMIT 100)x`,
			"tickets":       `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT t.*,c.email FROM tickets t JOIN customers c ON c.id=t.customer_id ORDER BY t.created_at DESC LIMIT 200)x`,
			"audit":         `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT * FROM audit ORDER BY id DESC LIMIT 300)x`}
		q, ok := queries[kind]
		if !ok {
			return bad("未知资源")
		}
		args := []any{}
		if kind == "orders" {
			args = append(args, r.URL.Query().Get("archived") == "1")
		}
		data, e := jsonRows(r.Context(), a.DB, q, args...)
		if e != nil {
			return e
		}
		send(w, 200, data)
		return nil
	}
	if r.Method == "DELETE" {
		return bad("请使用停用或下架操作，历史订单与审计保留")
	}
	if kind == "plans" {
		var err error
		r, err = a.withControlSecrets(r)
		if err != nil {
			return err
		}
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	id := ""
	var response any = map[string]bool{"saved": true}
	switch kind {
	case "categories":
		var b struct {
			ID          string `json:"id"`
			Version     int    `json:"version"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Sort        int    `json:"sort"`
			Enabled     bool   `json:"enabled"`
		}
		if e = decode(w, r, &b); e != nil {
			return e
		}
		if strings.TrimSpace(b.Name) == "" || !cleanText(b.Name, 60) || !cleanText(b.Description, 500) {
			return bad("分类名称或说明无效")
		}
		id = b.ID
		if id == "" {
			id = ID()
			_, e = tx.Exec(r.Context(), "INSERT INTO categories(id,name,description,sort,enabled) VALUES($1,$2,$3,$4,$5)", id, b.Name, b.Description, b.Sort, b.Enabled)
		} else {
			tag, err := tx.Exec(r.Context(), "UPDATE categories SET name=$3,description=$4,sort=$5,enabled=$6,version=version+1 WHERE id=$1 AND version=$2", id, b.Version, b.Name, b.Description, b.Sort, b.Enabled)
			e = err
			if e == nil && tag.RowsAffected() != 1 {
				return Problem{409, "分类已变化，请刷新"}
			}
		}
	case "plans":
		var b Plan
		if e = decode(w, r, &b); e != nil {
			return e
		}
		if strings.TrimSpace(b.Name) == "" || !cleanText(b.Name, 80) || !cleanText(b.Description, 2000) || b.CategoryID == "" || b.Quota < 1 || b.Quota > 9000000000000000 || (!b.Simulation && (len(b.Groups) < 1 || len(b.Protocols) < 1)) || len(b.Groups) > 8 || len(b.Protocols) > 32 || len(b.Prices) < 1 || len(b.Prices) > 12 || b.MaxDevices < 0 || b.MaxDevices > 20 || b.Multiplier < 1 || b.Multiplier > 100000 || !includes([]string{"both", "uplink", "downlink", "max"}, b.Billing) || !b.Ends.After(b.Starts) || !b.RecommendEnds.After(b.RecommendStarts) || b.Stock < 0 {
			return bad("套餐参数无效，请检查额度、价格、销售期与用户组")
		}
		if len(b.DisplayFeatures) > 12 {
			return bad("套餐展示信息最多 12 条")
		}
		for i, f := range b.DisplayFeatures {
			b.DisplayFeatures[i].Label = strings.TrimSpace(f.Label)
			b.DisplayFeatures[i].Value = strings.TrimSpace(f.Value)
			if b.DisplayFeatures[i].Label == "" || b.DisplayFeatures[i].Value == "" || !cleanText(f.Label, 24) || !cleanText(f.Value, 160) {
				return bad("展示信息需要名称与内容，名称不超过 24 字，内容不超过 160 字")
			}
		}
		if b.DisplayFeatures == nil {
			b.DisplayFeatures = []PlanFeature{}
		}
		seen := map[string]bool{}
		for _, p := range b.Prices {
			if !couponPattern.MatchString(p.ID) || p.Months < 1 || p.Months > 12 || p.Amount < 1 || p.Amount > 100000000 || seen[p.ID] {
				return bad("价格选项需要唯一 ID、1–12 个月和有效金额（分）")
			}
			seen[p.ID] = true
		}
		if !b.Simulation {
			if e = a.checkProtocolSelection(r.Context(), b.Groups, b.Protocols); e != nil {
				return e
			}
		}
		if b.Enabled && !b.Simulation {
			if e = a.checkGroups(r.Context(), b.Groups, b.Protocols); e != nil {
				return e
			}
		}
		if b.QuotaUnit == "" {
			b.QuotaUnit = "GB"
		}
		if !includes([]string{"GB", "GiB"}, b.QuotaUnit) {
			return bad("无效流量单位")
		}
		id = b.ID
		if id == "" {
			id = ID()
			_, e = tx.Exec(r.Context(), `INSERT INTO plans(id,category_id,name,description,enabled,recommended,sort,starts_at,ends_at,recommend_starts_at,recommend_ends_at,quota_bytes,group_ids,protocols,prices,max_devices,billing_mode,multiplier_milli,stock,allow_new,allow_renew,simulation,quota_unit,display_features) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`, id, b.CategoryID, b.Name, b.Description, b.Enabled, b.Recommended, b.Sort, b.Starts, b.Ends, b.RecommendStarts, b.RecommendEnds, b.Quota, raw(b.Groups), raw(b.Protocols), raw(b.Prices), b.MaxDevices, b.Billing, b.Multiplier, b.Stock, b.AllowNew, b.AllowRenew, b.Simulation, b.QuotaUnit, raw(b.DisplayFeatures))
		} else {
			tag, err := tx.Exec(r.Context(), `UPDATE plans SET policy_revision=CASE WHEN group_ids<>$14::jsonb OR protocols<>$15::jsonb THEN policy_revision+1 ELSE policy_revision END,category_id=$3,name=$4,description=$5,enabled=$6,recommended=$7,sort=$8,starts_at=$9,ends_at=$10,recommend_starts_at=$11,recommend_ends_at=$12,quota_bytes=$13,group_ids=$14,protocols=$15,prices=$16,max_devices=$17,billing_mode=$18,multiplier_milli=$19,stock=$20,allow_new=$21,allow_renew=$22,simulation=$23,quota_unit=$24,display_features=$25,version=version+1 WHERE id=$1 AND version=$2 AND deleted_at IS NULL`, id, b.Version, b.CategoryID, b.Name, b.Description, b.Enabled, b.Recommended, b.Sort, b.Starts, b.Ends, b.RecommendStarts, b.RecommendEnds, b.Quota, raw(b.Groups), raw(b.Protocols), raw(b.Prices), b.MaxDevices, b.Billing, b.Multiplier, b.Stock, b.AllowNew, b.AllowRenew, b.Simulation, b.QuotaUnit, raw(b.DisplayFeatures))
			e = err
			if e == nil && tag.RowsAffected() != 1 {
				return Problem{409, "套餐已变化，请刷新"}
			}
		}
		if e == nil {
			e = queuePlanPolicy(r.Context(), tx, id)
		}
		response = map[string]any{"saved": true, "notice": "套餐已保存，已购订阅的用户组与协议正在同步；金额、额度、用量和到期时间保留"}

	case "coupons":
		var b Coupon
		if e = decode(w, r, &b); e != nil {
			return e
		}
		b.Code = strings.ToUpper(strings.TrimSpace(b.Code))
		if !couponPattern.MatchString(b.Code) || strings.TrimSpace(b.Name) == "" || !cleanText(b.Name, 80) || !includes([]string{"fixed", "percent"}, b.Kind) || b.Amount < 1 || b.Amount > 100000000 || b.Kind == "percent" && b.Amount > 10000 || b.Minimum < 0 || b.Maximum < 0 || b.MaxUses < 0 || b.PerCustomer < 1 || b.PerCustomer > 10000 || !b.Ends.After(b.Starts) || len(b.Plans) > 100 || len(b.Prices) > 100 || len(b.Customers) > 500 {
			return bad("优惠码参数无效；比例为减免基点，1000 表示减免 10%")
		}
		id = b.ID
		if id == "" {
			id = ID()
			_, e = tx.Exec(r.Context(), `INSERT INTO coupons(id,code,name,enabled,kind,amount,minimum,maximum_discount,starts_at,ends_at,plan_ids,price_ids,customer_ids,new_only,max_uses,per_customer) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, id, b.Code, b.Name, b.Enabled, b.Kind, b.Amount, b.Minimum, b.Maximum, b.Starts, b.Ends, raw(b.Plans), raw(b.Prices), raw(b.Customers), b.NewOnly, b.MaxUses, b.PerCustomer)
		} else {
			tag, err := tx.Exec(r.Context(), `UPDATE coupons SET code=$3,name=$4,enabled=$5,kind=$6,amount=$7,minimum=$8,maximum_discount=$9,starts_at=$10,ends_at=$11,plan_ids=$12,price_ids=$13,customer_ids=$14,new_only=$15,max_uses=$16,per_customer=$17,version=version+1 WHERE id=$1 AND version=$2`, id, b.Version, b.Code, b.Name, b.Enabled, b.Kind, b.Amount, b.Minimum, b.Maximum, b.Starts, b.Ends, raw(b.Plans), raw(b.Prices), raw(b.Customers), b.NewOnly, b.MaxUses, b.PerCustomer)
			e = err
			if e == nil && tag.RowsAffected() != 1 {
				return Problem{409, "优惠码已变化，请刷新"}
			}
		}
	case "customers":
		var b struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if e = decode(w, r, &b); e != nil {
			return e
		}
		if !includes([]string{"active", "disabled"}, b.Status) {
			return bad("无效账户状态")
		}
		id = b.ID
		tag, err := tx.Exec(r.Context(), `UPDATE customers SET status=$2,admin_version=admin_version+1 WHERE id=$1 AND role='customer' AND deleted_at IS NULL AND email_verified_at IS NOT NULL AND status IN ('active','pending','disabled')`, id, b.Status)
		e = err
		if e == nil && tag.RowsAffected() != 1 {
			return bad("账户未验证或不允许修改管理员")
		}
		if b.Status == "disabled" {
			if _, e = tx.Exec(r.Context(), "DELETE FROM sessions WHERE customer_id=$1", id); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(r.Context(), "INSERT INTO jobs(id,kind,subject) VALUES($1,'access',$2) ON CONFLICT(kind,subject) DO UPDATE SET state='queued',available_at=now()", ID(), id); e != nil {
			return e
		}
		response = map[string]any{"saved": true, "notice": "账户状态已变更，代理服务权限正在等待同步，请查看任务结果"}
	case "jobs":
		var b struct {
			ID string `json:"id"`
		}
		if e = decode(w, r, &b); e != nil {
			return e
		}
		id = b.ID
		_, e = tx.Exec(r.Context(), "UPDATE jobs SET state='queued',available_at=now(),detail='' WHERE id=$1 AND state='failed' AND kind IN ('fulfill','mail','access','migrate','policy')", id)
	case "announcements":
		var b struct {
			ID       string   `json:"id"`
			Title    string   `json:"title"`
			Pinned   bool     `json:"pinned"`
			PinOrder int      `json:"pin_order"`
			Body     string   `json:"body"`
			Images   []string `json:"images"`
			Enabled  bool     `json:"enabled"`
		}
		if e = decode(w, r, &b); e != nil {
			return e
		}
		if b.PinOrder < -100000 || b.PinOrder > 100000 || strings.TrimSpace(b.Title) == "" || !cleanText(b.Title, 100) || !cleanText(b.Body, 12000) {
			return bad("公告内容无效")
		}
		id = b.ID
		if id == "" {
			id = ID()
			if e = validateMediaRefs(r.Context(), tx, current(r), "announcement", b.Images, ""); e != nil {
				return e
			}
			_, e = tx.Exec(r.Context(), "INSERT INTO announcements(id,title,body,enabled,pinned,pin_order,images) VALUES($1,$2,$3,$4,$5,$6,$7)", id, b.Title, b.Body, b.Enabled, b.Pinned, b.PinOrder, mediaList(b.Images))
		} else {
			if e = validateMediaRefs(r.Context(), tx, current(r), "announcement", b.Images, id); e != nil {
				return e
			}
			_, e = tx.Exec(r.Context(), "UPDATE announcements SET title=$2,body=$3,enabled=$4,pinned=$5,pin_order=$6,images=$7,updated_at=now() WHERE id=$1", id, b.Title, b.Body, b.Enabled, b.Pinned, b.PinOrder, mediaList(b.Images))
		}
	default:
		return Problem{405, "该资源仅支持查询"}
	}
	if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, current(r).ID, kind+".save", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, response)
	return nil
}
func (a *App) adminSettings(w http.ResponseWriter, r *http.Request) error {
	s, secrets, e := a.settings(r.Context())
	if e != nil {
		return e
	}
	if r.Method == "GET" {
		var payment any
		s.CatalogGuideTitle, s.CatalogGuideBody = s.catalogGuide()
		if v := secrets.Alipay; v != nil {
			payment = map[string]any{"enabled": v.Enabled, "sandbox": v.Sandbox, "app_id": v.AppID, "seller_id": v.SellerID, "mobile": v.Mobile, "private_configured": v.PrivateKey != "", "public_configured": v.PublicKey != ""}
		}
		send(w, 200, map[string]any{"settings": s, "secrets": map[string]any{"control_origin": secrets.ControlOrigin, "control_configured": secrets.ControlKey != "", "smtp_host": secrets.SMTPHost, "smtp_port": secrets.SMTPPort, "smtp_user": secrets.SMTPUser, "smtp_from": secrets.SMTPFrom, "smtp_configured": secrets.SMTPPassword != "", "alipay": payment}})
		return nil
	}
	if r.Method != "PUT" {
		return Problem{405, "method not allowed"}
	}
	var b struct {
		Settings     Settings `json:"settings"`
		Secrets      *Secrets `json:"secrets"`
		ClearControl bool     `json:"clear_control"`
		ClearSMTP    bool     `json:"clear_smtp"`
	}
	if e = decode(w, r, &b); e != nil {
		return e
	}
	n := b.Settings
	if !cleanText(n.CatalogGuideTitle, 80) || !cleanText(n.CatalogGuideBody, 6000) {
		return bad("套餐说明标题最多 80 字，正文最多 6000 字")
	}
	if n.Theme != "" && !includes([]string{"mint", "ocean", "rose", "midnight"}, n.Theme) || n.BackgroundDim < 0 || n.BackgroundDim > 85 {
		return bad("主题或背景遮罩设置无效")
	}
	if n.BackgroundDesktop, e = backgroundURL(n.BackgroundDesktop); e != nil {
		return e
	}
	if n.BackgroundMobile, e = backgroundURL(n.BackgroundMobile); e != nil {
		return e
	}
	if strings.TrimSpace(n.Title) == "" || !cleanText(n.Title, 40) || !includes([]string{"closed", "invite", "allowlist", "email"}, n.RegistrationMode) || !includes([]string{"GB", "GiB", "auto"}, n.TrafficUnit) || n.RegistrationLimit < 1 || n.RegistrationLimit > 500 || len(n.Allowlist) > 100 {
		return bad("站点设置无效")
	}
	if n.Domain != "" && (!domainPattern.MatchString(n.Domain) || !strings.Contains(n.Domain, ".") || strings.Contains(n.Domain, "..")) {
		return bad("域名应填写完整主机名，不带协议或端口")
	}
	if n.PublicOrigin != "" {
		if _, e = validOrigin(n.PublicOrigin); e != nil {
			return bad(e.Error())
		}
	}
	if n.Domain != "" {
		scheme := "http"
		if n.HTTPS {
			scheme = "https"
		}
		n.PublicOrigin = scheme + "://" + n.Domain
	}
	if n.PublicOrigin == "" {
		n.PublicOrigin = a.Origin
	}
	for _, entry := range n.Allowlist {
		if strings.HasPrefix(entry, "@") {
			if len(entry) < 4 || !domainPattern.MatchString(strings.TrimPrefix(entry, "@")) {
				return bad("邮箱域名白名单格式无效")
			}
		} else {
			if _, e = emailAddress(entry); e != nil {
				return e
			}
		}
	}
	if b.Secrets != nil {
		v := b.Secrets
		if v.ControlOrigin != "" {
			u, err := validOrigin(v.ControlOrigin)
			if err != nil || u.Scheme != "https" {
				return bad("星枢 API 必须使用有效 HTTPS origin")
			}
			secrets.ControlOrigin = strings.TrimRight(v.ControlOrigin, "/")
		}
		if v.ControlKey != "" {
			if !strings.HasPrefix(v.ControlKey, "xs_") || len(v.ControlKey) != 67 {
				return bad("请使用星枢受限 API 密钥，不使用管理员令牌")
			}
			secrets.ControlKey = v.ControlKey
		}
		if v.SMTPHost != "" {
			if strings.ContainsAny(v.SMTPHost, "/\\\r\n :") {
				return bad("SMTP 主机格式无效")
			}
			secrets.SMTPHost = v.SMTPHost
		}
		if v.SMTPPort != 0 {
			if v.SMTPPort != 465 && v.SMTPPort != 587 {
				return bad("SMTP 仅支持 TLS 465 或 STARTTLS 587")
			}
			secrets.SMTPPort = v.SMTPPort
		}
		if v.SMTPUser != "" {
			secrets.SMTPUser = v.SMTPUser
		}
		if v.SMTPPassword != "" {
			secrets.SMTPPassword = v.SMTPPassword
		}
		if v.SMTPFrom != "" {
			if _, e = emailAddress(v.SMTPFrom); e != nil {
				return e
			}
			secrets.SMTPFrom = v.SMTPFrom
		}
		if v.Alipay != nil {
			p := *v.Alipay
			if old := secrets.Alipay; old != nil {
				if p.PrivateKey == "" {
					p.PrivateKey = old.PrivateKey
				}
				if p.PublicKey == "" {
					p.PublicKey = old.PublicKey
				}
			}
			if p.Enabled {
				if e = validateAlipay(p); e != nil {
					return e
				}
				if !strings.HasPrefix(n.PublicOrigin, "https://") {
					return bad("启用支付宝前需设置可达的 HTTPS 公网入口")
				}
			}
			secrets.Alipay = &p
		}
	}
	if b.ClearControl {
		secrets.ControlKey = ""
		secrets.ControlOrigin = ""
	}
	if b.ClearSMTP {
		secrets.SMTPHost = ""
		secrets.SMTPPassword = ""
	}
	sealed, e := a.seal(secrets)
	if e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	for _, path := range []string{n.BackgroundDesktop, n.BackgroundMobile} {
		if strings.HasPrefix(path, "/portal/v1/media/") {
			if e = validateMediaRefs(r.Context(), tx, current(r), "background", []string{mediaID(path)}, ""); e != nil {
				return e
			}
		}
	}
	tag, e := tx.Exec(r.Context(), "UPDATE settings SET public=$2,secrets=$3,version=version+1 WHERE id=true AND version=$1", n.Version, raw(n), sealed)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return Problem{409, "设置已变化，请刷新后重试"}
	}
	if p := secrets.Alipay; p != nil && p.Enabled {
		c, e := a.seal(*p)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), "INSERT INTO payment_channels(id,config) VALUES($1,$2)", ID(), c); e != nil {
			return e
		}
	}
	payload, e := a.seal(map[string]any{"domain": n.Domain, "https": n.HTTPS, "origin": n.PublicOrigin})
	if e != nil {
		return e
	}
	if n.Domain != s.Domain || n.HTTPS != s.HTTPS || n.PublicOrigin != s.PublicOrigin {
		if _, e = tx.Exec(r.Context(), "INSERT INTO jobs(id,kind,subject,payload) VALUES($1,'gateway',$2,$3)", ID(), ID(), payload); e != nil {
			return e
		}
	}
	if e = audit(r.Context(), tx, current(r).ID, "settings.save", "site"); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]any{"saved": true, "notice": "设置已保存。域名代理应用结果请查看任务状态；IP 入口保留。"})
	return nil
}
