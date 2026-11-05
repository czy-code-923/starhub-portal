package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Settings struct {
	Title             string   `json:"title"`
	RegistrationMode  string   `json:"registration_mode"`
	Approval          bool     `json:"approval_required"`
	Allowlist         []string `json:"email_allowlist"`
	ShowCatalog       bool     `json:"show_catalog"`
	TrafficUnit       string   `json:"traffic_unit"`
	RegistrationLimit int      `json:"registration_limit"`
	PublicOrigin      string   `json:"public_origin"`
	Domain            string   `json:"domain"`
	HTTPS             bool     `json:"https"`
	Version           int      `json:"version"`
}
type Secrets struct {
	ControlOrigin string        `json:"control_origin"`
	ControlKey    string        `json:"control_key"`
	SMTPHost      string        `json:"smtp_host"`
	SMTPPort      int           `json:"smtp_port"`
	SMTPUser      string        `json:"smtp_user"`
	SMTPPassword  string        `json:"smtp_password"`
	SMTPFrom      string        `json:"smtp_from"`
	Alipay        *AlipayConfig `json:"alipay,omitempty"`
}
type AlipayConfig struct {
	Enabled    bool   `json:"enabled"`
	Sandbox    bool   `json:"sandbox"`
	AppID      string `json:"app_id"`
	SellerID   string `json:"seller_id"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	Mobile     bool   `json:"mobile"`
}
type Price struct {
	ID      string `json:"id"`
	Months  int    `json:"months"`
	Amount  int64  `json:"amount"`
	Enabled bool   `json:"enabled"`
}
type Plan struct {
	DisplayFeatures []PlanFeature `json:"display_features"`
	Simulation      bool          `json:"simulation"`
	ID              string        `json:"id"`
	Version         int           `json:"version"`
	CategoryID      string        `json:"category_id"`
	Name            string        `json:"name"`
	Description     string        `json:"description"`
	Enabled         bool          `json:"enabled"`
	Recommended     bool          `json:"recommended"`
	Sort            int           `json:"sort"`
	Starts          time.Time     `json:"starts_at"`
	Ends            time.Time     `json:"ends_at"`
	RecommendStarts time.Time     `json:"recommend_starts_at"`
	RecommendEnds   time.Time     `json:"recommend_ends_at"`
	Quota           int64         `json:"quota_bytes"`
	QuotaUnit       string        `json:"quota_unit"`
	Groups          []string      `json:"group_ids"`
	Protocols       []string      `json:"protocols"`
	Prices          []Price       `json:"prices"`
	MaxDevices      int           `json:"max_devices"`
	Billing         string        `json:"billing_mode"`
	Multiplier      int           `json:"multiplier_milli"`
	Stock           int           `json:"stock"`
	AllowNew        bool          `json:"allow_new"`
	AllowRenew      bool          `json:"allow_renew"`
}
type PlanFeature struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
type Coupon struct {
	ID          string    `json:"id"`
	Version     int       `json:"version"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Enabled     bool      `json:"enabled"`
	Kind        string    `json:"kind"`
	Amount      int64     `json:"amount"`
	Minimum     int64     `json:"minimum"`
	Maximum     int64     `json:"maximum_discount"`
	Starts      time.Time `json:"starts_at"`
	Ends        time.Time `json:"ends_at"`
	Plans       []string  `json:"plan_ids"`
	Prices      []string  `json:"price_ids"`
	Customers   []string  `json:"customer_ids"`
	NewOnly     bool      `json:"new_only"`
	MaxUses     int       `json:"max_uses"`
	PerCustomer int       `json:"per_customer"`
}

func (a *App) settings(ctx context.Context) (Settings, Secrets, error) {
	var s Settings
	var v Secrets
	var raw json.RawMessage
	var sealed []byte
	var version int
	e := a.DB.QueryRow(ctx, "SELECT public,secrets,version FROM settings WHERE id=true").Scan(&raw, &sealed, &version)
	if e != nil {
		return s, v, e
	}
	if e = json.Unmarshal(raw, &s); e != nil {
		return s, v, e
	}
	s.Version = version
	if len(sealed) > 0 {
		e = a.unseal(sealed, &v)
	}
	return s, v, e
}
func (a *App) public(w http.ResponseWriter, r *http.Request) error {
	s, secrets, e := a.settings(r.Context())
	if e != nil {
		return e
	}
	var announcements json.RawMessage
	announcements, e = jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,title,body,pinned,pin_order,created_at FROM announcements WHERE enabled ORDER BY pinned DESC,pin_order,created_at DESC,id LIMIT 100)x`)
	if e != nil {
		return e
	}
	send(w, 200, map[string]any{"title": s.Title, "registration_mode": s.RegistrationMode, "approval_required": s.Approval, "show_catalog": s.ShowCatalog, "traffic_unit": s.TrafficUnit, "email_ready": secrets.SMTPHost != "" || a.Mail != nil, "alipay_ready": secrets.Alipay != nil && secrets.Alipay.Enabled, "announcements": announcements, "version": Version})
	return nil
}
func raw(v any) []byte { b, _ := json.Marshal(v); return b }
func includes(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func eligibleScope(xs []string, s string) bool { return len(xs) == 0 || includes(xs, s) }
func cleanText(s string, max int) bool {
	return len([]rune(s)) <= max && !strings.ContainsAny(s, "\x00\r")
}
