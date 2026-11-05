package portal

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Version = "0.4.0"
const Repository = "czy-code-923/starhub-portal"

//go:embed migrations/*.sql
var migrations embed.FS

type App struct {
	IPOrigin        string
	WorkerToken     string
	TrustedProxies  []netip.Prefix
	DB              *pgxpool.Pool
	Key             []byte
	Origin, WebDir  string
	Client          *http.Client
	Mail            func(context.Context, string, string, string) error
	Now             func() time.Time
	TestControlURL  string
	TestTelegramURL string
}
type Problem struct {
	Status  int
	Message string
}

func (e Problem) Error() string { return e.Message }
func bad(s string) error        { return Problem{400, s} }
func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}
func Open(ctx context.Context, dsn string, key []byte, origin, web string) (*App, error) {
	if len(key) != 32 {
		return nil, errors.New("MASTER_KEY must contain 32 bytes")
	}
	if _, e := validOrigin(origin); e != nil {
		return nil, e
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 2 * time.Minute
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	a := &App{DB: db, Key: key, Origin: strings.TrimRight(origin, "/"), WebDir: web, Client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}
	for _, cidr := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if strings.TrimSpace(cidr) == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			db.Close()
			return nil, errors.New("invalid TRUSTED_PROXIES CIDR")
		}
		a.TrustedProxies = append(a.TrustedProxies, prefix)
	}
	if e = db.Ping(ctx); e != nil {
		db.Close()
		return nil, e
	}
	if e = a.migrate(ctx); e != nil {
		db.Close()
		return nil, e
	}
	return a, nil
}
func (a *App) migrate(ctx context.Context) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(90234001)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY)"); e != nil {
		return e
	}
	files, _ := migrations.ReadDir("migrations")
	for _, f := range files {
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", f.Name()).Scan(&exists); e != nil {
			return e
		}
		if exists {
			continue
		}
		data, _ := migrations.ReadFile("migrations/" + f.Name())
		if _, e = tx.Exec(ctx, string(data)); e != nil {
			return fmt.Errorf("migration %s: %w", f.Name(), e)
		}
		if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", f.Name()); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (a *App) seal(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(a.Key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return g.Seal(nonce, nonce, b, nil), nil
}
func (a *App) unseal(b []byte, v any) error {
	block, e := aes.NewCipher(a.Key)
	if e != nil {
		return e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return e
	}
	if len(b) < g.NonceSize() {
		return errors.New("invalid encrypted configuration")
	}
	raw, e := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], nil)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, v)
}
func validOrigin(v string) (*url.URL, error) {
	u, e := url.Parse(strings.TrimRight(v, "/"))
	if e != nil || u == nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("origin must be http(s)://host[:port] without credentials or path")
	}
	return u, nil
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return bad("请求字段或 JSON 格式错误")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return bad("仅接受一个 JSON 对象")
	}
	return nil
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type endpoint func(http.ResponseWriter, *http.Request) error

func wrap(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if e := fn(w, r); e != nil {
			code, msg := 500, "服务暂不可用"
			var p Problem
			var pg *pgconn.PgError
			if errors.As(e, &p) {
				code, msg = p.Status, p.Message
			} else if errors.Is(e, pgx.ErrNoRows) {
				code, msg = 404, "记录不存在"
			} else if errors.As(e, &pg) && (pg.Code == "23505" || pg.Code == "23503" || pg.Code == "23514") {
				code, msg = 409, "记录冲突，请刷新后重试"
			} else {
				slog.Error("portal request failed", "path", r.URL.Path, "error_type", fmt.Sprintf("%T", e))
			}
			send(w, code, map[string]string{"error": msg})
		}
	}
}

type actor struct {
	ID     string   `json:"id"`
	Email  string   `json:"email"`
	Name   string   `json:"name"`
	Role   string   `json:"role"`
	Owner  bool     `json:"is_owner"`
	Scopes []string `json:"admin_scopes"`
	CSRF   string   `json:"-"`
}
type actorKey struct{}

func current(r *http.Request) actor { v, _ := r.Context().Value(actorKey{}).(actor); return v }
func (a *App) auth(fn endpoint, admin bool) http.HandlerFunc {
	return wrap(func(w http.ResponseWriter, r *http.Request) error {
		c, e := r.Cookie("sp_session")
		if e != nil {
			return Problem{401, "请先登录"}
		}
		var u actor
		var csrf string
		e = a.DB.QueryRow(r.Context(), `SELECT c.id,c.email,c.name,c.role,c.is_owner,c.admin_scopes,s.csrf_hash FROM sessions s JOIN customers c ON c.id=s.customer_id WHERE s.token_hash=$1 AND s.expires_at>$2 AND c.status='active'`, hash(c.Value), a.now()).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Owner, &u.Scopes, &csrf)
		if e != nil {
			return Problem{401, "登录已失效"}
		}
		if admin && u.Role != "admin" {
			return Problem{403, "需要管理员权限"}
		}
		if r.Method != "GET" && r.Method != "HEAD" && hash(r.Header.Get("X-CSRF-Token")) != csrf {
			return Problem{403, "安全校验失效，请重新登录"}
		}
		_, secret, err := a.settings(r.Context())
		if err != nil {
			return err
		}
		ctx := context.WithValue(r.Context(), controlSecretKey{}, secret)
		return fn(w, r.WithContext(context.WithValue(ctx, actorKey{}, u)))
	})
}
func lock(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(90234002)")
	return e
}
func audit(ctx context.Context, tx pgx.Tx, who, action, subject string) error {
	_, e := tx.Exec(ctx, "INSERT INTO audit(actor,action,subject) VALUES($1,$2,$3)", who, action, subject)
	return e
}
func jsonRows(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, query string, args ...any) (json.RawMessage, error) {
	var raw json.RawMessage
	e := db.QueryRow(ctx, query, args...).Scan(&raw)
	return raw, e
}
func (a *App) limit(r *http.Request, key string, n int) error {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(ip); err == nil {
		for _, prefix := range a.TrustedProxies {
			if prefix.Contains(addr) {
				if forwarded := net.ParseIP(r.Header.Get("X-Real-IP")); forwarded != nil {
					ip = forwarded.String()
				}
				break
			}
		}
	}
	var count int
	e := a.DB.QueryRow(r.Context(), `INSERT INTO rate_limits(key,count,reset_at) VALUES($1,1,$2) ON CONFLICT(key) DO UPDATE SET count=CASE WHEN rate_limits.reset_at<=$3 THEN 1 ELSE rate_limits.count+1 END,reset_at=CASE WHEN rate_limits.reset_at<=$3 THEN $2 ELSE rate_limits.reset_at END RETURNING count`, hash(key+"/"+ip), a.now().Add(15*time.Minute), a.now()).Scan(&count)
	if e != nil {
		return e
	}
	if count > n {
		return Problem{429, "操作过于频繁，请稍后再试"}
	}
	return nil
}
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /portal/v1/local/{action}", wrap(a.localWorker))
	m.HandleFunc("GET /portal/v1/health", wrap(func(w http.ResponseWriter, r *http.Request) error {
		if e := a.DB.Ping(r.Context()); e != nil {
			return e
		}
		send(w, 200, map[string]string{"version": Version, "status": "ok", "installation_id": hash("installation/" + hex.EncodeToString(a.Key))[:24]})
		return nil
	}))
	m.HandleFunc("GET /portal/v1/public", wrap(a.public))
	m.HandleFunc("POST /portal/v1/auth/login", wrap(a.login))
	m.HandleFunc("POST /portal/v1/auth/register", wrap(a.register))
	m.HandleFunc("POST /portal/v1/auth/verify", wrap(a.verify))
	m.HandleFunc("POST /portal/v1/auth/forgot", wrap(a.forgot))
	m.HandleFunc("POST /portal/v1/auth/reset", wrap(a.reset))
	m.HandleFunc("GET /portal/v1/me", a.auth(a.me, false))
	m.HandleFunc("GET /portal/v1/me/profile", a.auth(a.profile, false))
	m.HandleFunc("PUT /portal/v1/me/profile", a.auth(a.profile, false))
	m.HandleFunc("PUT /portal/v1/me/password", a.auth(a.changePassword, false))
	m.HandleFunc("POST /portal/v1/auth/verify-email", wrap(a.verifyNewEmail))
	m.HandleFunc("GET /portal/v1/payment-methods", a.auth(a.paymentMethods, false))
	m.HandleFunc("GET /portal/v1/orders/{id}/payment", a.auth(a.manualStatus, false))
	m.HandleFunc("POST /portal/v1/orders/{id}/receipt", a.auth(a.submitReceipt, false))
	m.HandleFunc("GET /portal/v1/orders/{id}/payment-code", a.auth(a.manualImage, false))
	m.HandleFunc("GET /portal/v1/orders/{id}/receipt", a.auth(a.manualImage, false))
	for _, method := range []string{"GET", "POST", "PUT"} {
		m.HandleFunc(method+" /portal/v1/admin/administrators", a.auth(a.team, true))
	}
	m.HandleFunc("GET /portal/v1/admin/payment-methods", a.auth(a.configurePaymentMethods, true))
	m.HandleFunc("PUT /portal/v1/admin/payment-methods", a.auth(a.configurePaymentMethods, true))
	m.HandleFunc("GET /portal/v1/admin/payment-methods/{id}/image", a.auth(a.methodImage, true))
	m.HandleFunc("GET /portal/v1/admin/manual-payments", a.auth(a.reviewPayments, true))
	m.HandleFunc("GET /portal/v1/admin/manual-payments/{id}", a.auth(a.reviewPayments, true))
	m.HandleFunc("GET /portal/v1/admin/manual-payments/{id}/redpacket", a.auth(a.viewRedpacket, true))
	m.HandleFunc("GET /portal/v1/admin/telegram", a.auth(a.telegramSettings, true))
	m.HandleFunc("PUT /portal/v1/admin/telegram", a.auth(a.telegramSettings, true))
	m.HandleFunc("POST /portal/v1/admin/telegram/check", a.auth(a.telegramCheck, true))
	m.HandleFunc("POST /portal/v1/admin/telegram/retry", a.auth(a.telegramRetry, true))
	m.HandleFunc("POST /portal/v1/admin/telegram/commands", a.auth(a.telegramCommands, true))
	m.HandleFunc("PUT /portal/v1/admin/manual-payments/{id}", a.auth(a.reviewPayments, true))
	m.HandleFunc("POST /portal/v1/admin/demo-plans", a.auth(a.demoPlans, true))

	m.HandleFunc("POST /portal/v1/auth/logout", a.auth(a.logout, false))
	m.HandleFunc("GET /portal/v1/catalog", wrap(a.catalog))
	m.HandleFunc("POST /portal/v1/checkout/quote", a.auth(a.quote, false))
	m.HandleFunc("POST /portal/v1/orders", a.auth(a.createOrder, false))
	m.HandleFunc("GET /portal/v1/orders", a.auth(a.orders, false))
	m.HandleFunc("GET /portal/v1/orders/{id}", a.auth(a.order, false))
	m.HandleFunc("POST /portal/v1/orders/{id}/cancel", a.auth(a.cancelOrder, false))
	m.HandleFunc("POST /portal/v1/orders/{id}/payment", a.auth(a.pay, false))
	m.HandleFunc("POST /portal/v1/payments/alipay/notify", wrap(a.alipayNotify))
	m.HandleFunc("GET /portal/v1/me/service", a.auth(a.service, false))
	m.HandleFunc("GET /portal/v1/me/services", a.auth(a.services, false))
	m.HandleFunc("GET /portal/v1/me/subscription/{view}", a.auth(a.subscription, false))
	m.HandleFunc("GET /portal/v1/me/devices", a.auth(a.devices, false))
	m.HandleFunc("GET /portal/v1/clients", a.auth(a.clients, false))
	m.HandleFunc("GET /portal/v1/tickets", a.auth(a.tickets, false))
	m.HandleFunc("POST /portal/v1/tickets", a.auth(a.tickets, false))
	m.HandleFunc("GET /portal/v1/tickets/{id}", a.auth(a.ticket, false))
	m.HandleFunc("POST /portal/v1/tickets/{id}", a.auth(a.ticket, false))
	for _, path := range []string{"settings", "categories", "plans", "coupons", "invitations", "customers", "orders", "jobs", "announcements", "tickets", "audit", "control-groups", "version", "capacity", "service-migrations"} {
		p := path
		for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
			m.HandleFunc(method+" /portal/v1/admin/"+p, a.auth(func(w http.ResponseWriter, r *http.Request) error { return a.admin(w, r, p) }, true))
		}
	}
	m.HandleFunc("GET /portal/v1/openapi", a.auth(a.openapi, true))
	m.HandleFunc("POST /portal/v1/admin/gateway/retry", a.auth(a.retryGateway, true))
	m.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/portal/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "method not allowed", 405)
			return
		}
		path := filepath.Join(a.WebDir, filepath.Clean("/"+r.URL.Path))
		if f, e := os.Stat(path); e == nil && !f.IsDir() {
			http.ServeFile(w, r, path)
			return
		}
		http.ServeFile(w, r, filepath.Join(a.WebDir, "index.html"))
	}))
	slots := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			send(w, 429, map[string]string{"error": "服务繁忙，请稍后重试"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self' https://openapi.alipay.com https://openapi-sandbox.dl.alipaydev.com")
		if r.Method != "GET" && r.Method != "HEAD" && r.URL.Path != "/portal/v1/payments/alipay/notify" {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				send(w, 403, map[string]string{"error": "请求来源不匹配"})
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && !a.allowedOrigin(r.Context(), strings.TrimRight(origin, "/")) {
				send(w, 403, map[string]string{"error": "请求来源不匹配"})
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}

func (a *App) allowedOrigin(ctx context.Context, origin string) bool {
	if origin == a.Origin || a.IPOrigin != "" && origin == a.IPOrigin {
		return true
	}
	s, _, e := a.settings(ctx)
	return e == nil && s.PublicOrigin != "" && origin == strings.TrimRight(s.PublicOrigin, "/")
}
