package portal

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

var passwordSlots = make(chan struct{}, 2)

func passwordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 256 {
		return "", bad("密码长度需为 12–256 个字节")
	}
	passwordSlots <- struct{}{}
	defer func() { <-passwordSlots }()
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	sum := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return "argon2id$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(sum), nil
}
func passwordOK(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id" || len(password) > 256 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[1])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[2])
	if e != nil || len(want) != 32 {
		return false
	}
	passwordSlots <- struct{}{}
	defer func() { <-passwordSlots }()
	got := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return subtle.ConstantTimeCompare(want, got) == 1
}
func emailAddress(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	m, e := mail.ParseAddress(s)
	if e != nil || m.Address != s || len(s) > 254 {
		return "", bad("邮箱格式不正确")
	}
	return s, nil
}
func (a *App) Bootstrap(ctx context.Context, email, password string) error {
	email, e := emailAddress(email)
	if e != nil {
		return e
	}
	encoded, e := passwordHash(password)
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
	var count int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM customers WHERE role='admin'").Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return errorsNew("管理员已存在，请使用受控密码恢复命令")
	}
	_, e = tx.Exec(ctx, `INSERT INTO customers(id,email,name,password_hash,role,status,email_verified_at,is_owner) VALUES($1,$2,'管理员',$3,'admin','active',now(),true)`, ID(), email, encoded)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func errorsNew(s string) error { return fmt.Errorf("%s", s) }
func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "login", 20); e != nil {
		return e
	}
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	email, _ := emailAddress(b.Email)
	var u actor
	var encoded, status string
	e := a.DB.QueryRow(r.Context(), "SELECT id,email,name,role,is_owner,admin_scopes,password_hash,status FROM customers WHERE email=$1", email).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Owner, &u.Scopes, &encoded, &status)
	if e != nil {
		_, _ = passwordHash("invalid-login-dummy-credential")
		return Problem{401, "邮箱或密码不正确"}
	}
	if !passwordOK(encoded, b.Password) {
		return Problem{401, "邮箱或密码不正确"}
	}
	// Serialize session creation with account changes. An in-flight login using an
	// old password must not create a fresh session after password reset/disable.
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	var latestHash string
	e = tx.QueryRow(r.Context(), "SELECT password_hash,status,is_owner,admin_scopes,email,name,role FROM customers WHERE id=$1 FOR UPDATE", u.ID).Scan(&latestHash, &status, &u.Owner, &u.Scopes, &u.Email, &u.Name, &u.Role)
	if e != nil || latestHash != encoded {
		return Problem{401, "账户信息已变更，请重新登录"}
	}
	if status != "active" {
		return Problem{403, map[string]string{"unverified": "请先验证邮箱", "pending": "账户正在等待审核", "disabled": "账户已停用", "expired": "注册验证已过期，请联系管理员"}[status]}
	}
	token := ID() + ID()
	csrf := a.csrf(token)
	_, e = tx.Exec(r.Context(), "INSERT INTO sessions(token_hash,customer_id,csrf_hash,expires_at) VALUES($1,$2,$3,$4)", hash(token), u.ID, hash(csrf), a.now().Add(24*time.Hour))
	if e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "sp_session", Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(r.Header.Get("Origin"), "https://") || r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	send(w, 200, map[string]any{"user": u, "csrf": csrf})
	return nil
}
func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	u := current(r)
	cookie, _ := r.Cookie("sp_session")
	csrf := a.csrf(cookie.Value)
	send(w, 200, map[string]any{"user": u, "csrf": csrf})
	return nil
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	cookie, _ := r.Cookie("sp_session")
	if _, e := a.DB.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", hash(cookie.Value)); e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "sp_session", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	send(w, 200, map[string]bool{"ok": true})
	return nil
}
func (a *App) register(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "register", 6); e != nil {
		return e
	}
	var b struct {
		Email      string `json:"email"`
		Name       string `json:"name"`
		Password   string `json:"password"`
		Confirm    string `json:"password_confirm"`
		Invitation string `json:"invitation"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	email, e := emailAddress(b.Email)
	if e != nil {
		return e
	}
	if b.Password != b.Confirm {
		return bad("两次输入的密码不一致")
	}
	if len(strings.TrimSpace(b.Name)) == 0 || !cleanText(b.Name, 80) {
		return bad("请填写有效昵称")
	}
	s, secret, e := a.settings(r.Context())
	if e != nil {
		return e
	}
	if s.RegistrationMode == "closed" {
		return Problem{403, "当前不开放注册"}
	}
	if a.Mail == nil && secret.SMTPHost == "" {
		return Problem{503, "管理员尚未配置邮箱验证服务"}
	}
	if s.RegistrationMode == "allowlist" || len(s.Allowlist) > 0 {
		allowed := false
		for _, entry := range s.Allowlist {
			if strings.EqualFold(entry, email) || strings.HasPrefix(entry, "@") && strings.HasSuffix(email, strings.ToLower(entry)) {
				allowed = true
			}
		}
		if !allowed {
			return Problem{403, "该邮箱不在注册白名单中"}
		}
	}
	encoded, e := passwordHash(b.Password)
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
	var count int
	if e = tx.QueryRow(r.Context(), "SELECT count(*) FROM customers").Scan(&count); e != nil {
		return e
	}
	if count >= s.RegistrationLimit {
		return Problem{409, "注册名额已满"}
	}
	invite := ""
	if s.RegistrationMode == "invite" {
		if e = tx.QueryRow(r.Context(), `SELECT id FROM invitations WHERE code_hash=$1 AND enabled AND starts_at<=$2 AND ends_at>$2 AND claimed<max_uses AND (email='' OR email=$3) FOR UPDATE`, hash(strings.TrimSpace(b.Invitation)), a.now(), email).Scan(&invite); e != nil {
			return Problem{403, "邀请码无效、已过期或名额已用完"}
		}
	}
	id := ID()
	if _, e = tx.Exec(r.Context(), `INSERT INTO customers(id,email,name,password_hash,status) VALUES($1,$2,$3,$4,'unverified')`, id, email, b.Name, encoded); e != nil {
		return e
	}
	if invite != "" {
		if _, e = tx.Exec(r.Context(), "UPDATE invitations SET claimed=claimed+1 WHERE id=$1", invite); e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), "INSERT INTO invitation_claims(customer_id,invitation_id,expires_at) VALUES($1,$2,$3)", id, invite, a.now().Add(24*time.Hour)); e != nil {
			return e
		}
	}
	if e = a.queueToken(r.Context(), tx, id, email, "verify", 24*time.Hour); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 201, map[string]string{"status": "unverified", "message": "验证邮件已排队，请验证邮箱后登录"})
	return nil
}
func (a *App) queueToken(ctx context.Context, tx pgx.Tx, id, email, purpose string, ttl time.Duration) error {
	token := ID() + ID()
	pending := ""
	if purpose == "change_email" {
		pending = email
	}
	if _, e := tx.Exec(ctx, "INSERT INTO email_tokens(id,customer_id,purpose,token_hash,expires_at,pending_email) VALUES($1,$2,$3,$4,$5,$6)", ID(), id, purpose, hash(token), a.now().Add(ttl), pending); e != nil {
		return e
	}
	payload, e := a.seal(map[string]string{"email": email, "purpose": purpose, "token": token})
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO jobs(id,kind,subject,payload) VALUES($1,'mail',$2,$3)", ID(), ID(), payload)
	return e
}
func (a *App) verify(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "verify", 20); e != nil {
		return e
	}
	var b struct {
		Token string `json:"token"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	s, _, e := a.settings(r.Context())
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
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	var id string
	if e = tx.QueryRow(r.Context(), `UPDATE email_tokens SET used_at=$2 WHERE token_hash=$1 AND purpose='verify' AND used_at IS NULL AND expires_at>$2 RETURNING customer_id`, hash(b.Token), a.now()).Scan(&id); e != nil {
		return bad("验证链接无效或已过期")
	}
	status := "active"
	if s.Approval {
		status = "pending"
	}
	tag, e := tx.Exec(r.Context(), `UPDATE customers SET status=$2,email_verified_at=$3 WHERE id=$1 AND status='unverified'`, id, status, a.now())
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return bad("账户状态已变化")
	}
	if _, e = tx.Exec(r.Context(), "UPDATE invitation_claims SET finalized=true WHERE customer_id=$1 AND NOT released", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]string{"status": status})
	return nil
}
func (a *App) forgot(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "forgot", 5); e != nil {
		return e
	}
	var b struct {
		Email string `json:"email"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	email, _ := emailAddress(b.Email)
	var id string
	e := a.DB.QueryRow(r.Context(), "SELECT id FROM customers WHERE email=$1 AND status IN ('active','pending')", email).Scan(&id)
	if e == nil {
		tx, e := a.DB.Begin(r.Context())
		if e != nil {
			return e
		}
		defer tx.Rollback(r.Context())
		if e = a.queueToken(r.Context(), tx, id, email, "reset", 30*time.Minute); e != nil {
			return e
		}
		if e = tx.Commit(r.Context()); e != nil {
			return e
		}
	} else if e != pgx.ErrNoRows {
		return e
	}
	send(w, 200, map[string]string{"message": "若账户存在，密码恢复邮件将发送到该邮箱"})
	return nil
}
func (a *App) reset(w http.ResponseWriter, r *http.Request) error {
	if e := a.limit(r, "reset", 10); e != nil {
		return e
	}
	var b struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		Confirm  string `json:"password_confirm"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if b.Password != b.Confirm {
		return bad("两次输入的密码不一致")
	}
	encoded, e := passwordHash(b.Password)
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
	var id string
	if e = tx.QueryRow(r.Context(), `UPDATE email_tokens SET used_at=$2 WHERE token_hash=$1 AND purpose='reset' AND used_at IS NULL AND expires_at>$2 RETURNING customer_id`, hash(b.Token), a.now()).Scan(&id); e != nil {
		return bad("恢复链接无效或已过期")
	}
	if _, e = tx.Exec(r.Context(), "UPDATE customers SET password_hash=$2 WHERE id=$1 AND status IN ('active','pending')", id, encoded); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), "DELETE FROM sessions WHERE customer_id=$1", id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]bool{"ok": true})
	return nil
}

func (a *App) csrf(token string) string {
	h := hmac.New(sha256.New, a.Key)
	h.Write([]byte("portal-csrf/" + token))
	return hex.EncodeToString(h.Sum(nil))
}
