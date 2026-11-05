package portal

import (
	"net/http"
	"net/netip"
	"strings"
)

// Restricted local helper credential; only typed settings and a gateway job.
// Never accepts a command, file path, SQL or a new network listener.
func (a *App) localDomain(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Domain    string `json:"domain"`
		HTTPS     bool   `json:"https"`
		IPOrigin  string `json:"ip_origin"`
		AllowHTTP bool   `json:"allow_http"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	b.Domain = strings.ToLower(strings.TrimSpace(b.Domain))
	if b.Domain != "" && (!domainPattern.MatchString(b.Domain) || !strings.Contains(b.Domain, ".") || strings.Contains(b.Domain, "..")) {
		return bad("域名格式无效，不应包含协议或端口")
	}
	origin := b.IPOrigin
	if b.Domain != "" {
		if !b.HTTPS && !b.AllowHTTP {
			return bad("HTTP 域名需明确允许明文访问")
		}
		origin = "https://" + b.Domain
		if !b.HTTPS {
			origin = "http://" + b.Domain
		}
	} else {
		u, e := validOrigin(origin)
		if e != nil {
			return bad("需要有效 IP 入口")
		}
		if _, e = netip.ParseAddr(u.Hostname()); e != nil || u.Port() == "" {
			return bad("IP 入口必须是 IP 加端口")
		}
		if u.Scheme != "https" && !b.AllowHTTP {
			return bad("IP HTTP 需明确添加 --allow-http")
		}
	}
	settings, secret, e := a.settings(r.Context())
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
	var busy bool
	if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM jobs WHERE kind IN ('update','gateway') AND state IN ('queued','working'))").Scan(&busy); e != nil {
		return e
	}
	if busy {
		return Problem{409, "更新或域名任务正在处理，请完成后重试"}
	}
	// Do not change secrets or payment callback channels through a recovery CLI.
	if secret.Alipay != nil && secret.Alipay.Enabled && !strings.HasPrefix(origin, "https://") {
		return Problem{409, "已启用官方支付宝回调，请先在后台停用该支付渠道，再切换到 HTTP IP 入口"}
	}
	tag, e := tx.Exec(r.Context(), "UPDATE settings SET public=public||$1::jsonb,version=version+1 WHERE id=true AND version=$2", raw(map[string]any{"domain": b.Domain, "https": b.HTTPS && b.Domain != "", "public_origin": origin}), settings.Version)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return Problem{409, "设置在操作期间已变更，请检查支付与域名配置后重试"}
	}
	sealed, e := a.seal(map[string]any{"domain": b.Domain, "https": b.HTTPS, "origin": origin})
	if e != nil {
		return e
	}
	id := ID()
	if _, e = tx.Exec(r.Context(), "INSERT INTO jobs(id,kind,subject,payload) VALUES($1,'gateway',$1,$2)", id, sealed); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, "local-helper", "domain.configure", b.Domain); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 202, map[string]any{"id": id, "origin": origin, "message": "域名设置已保存，入口任务已排队；请用 status 查询结果"})
	return nil
}
