package portal

import (
	_ "embed"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/url"
	"strings"
)

//go:embed content/client_help.json
var defaultClientHelp []byte
var helpPlatforms = []string{"Android", "iOS", "Windows", "macOS", "Linux"}

type helpApp struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Platforms []string `json:"platforms"`
	Format    string   `json:"format"`
	URL       string   `json:"url"`
	Steps     []string `json:"steps"`
}
type helpFAQ struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}
type helpDocument struct {
	Title string    `json:"title"`
	Intro string    `json:"intro"`
	Apps  []helpApp `json:"apps"`
	FAQs  []helpFAQ `json:"faqs"`
}

func validateHelp(d helpDocument) error {
	if strings.TrimSpace(d.Title) == "" || !cleanText(d.Title, 100) || !cleanText(d.Intro, 2000) || len(d.Apps) > 30 || len(d.FAQs) > 30 {
		return bad("教程标题、说明或条目数量无效")
	}
	ids := map[string]bool{}
	for _, a := range d.Apps {
		if a.ID == "" || !cleanText(a.ID, 64) || ids[a.ID] || strings.TrimSpace(a.Name) == "" || !cleanText(a.Name, 80) || !cleanText(a.Format, 120) || len(a.Platforms) < 1 || len(a.Platforms) > 5 || len(a.Steps) < 1 || len(a.Steps) > 15 {
			return bad("客户端名称、平台或步骤无效")
		}
		ids[a.ID] = true
		seen := map[string]bool{}
		for _, p := range a.Platforms {
			if !includes(helpPlatforms, p) || seen[p] {
				return bad("客户端平台无效或重复")
			}
			seen[p] = true
		}
		if a.URL != "" {
			u, e := url.Parse(a.URL)
			if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(a.URL) > 1000 {
				return bad("教程链接需为不含登录凭据的 HTTPS 地址")
			}
		}
		for _, s := range a.Steps {
			if strings.TrimSpace(s) == "" || !cleanText(s, 1500) {
				return bad("教程步骤不能为空或超过 1500 字")
			}
		}
	}
	for _, f := range d.FAQs {
		if strings.TrimSpace(f.Question) == "" || !cleanText(f.Question, 180) || strings.TrimSpace(f.Answer) == "" || !cleanText(f.Answer, 3000) {
			return bad("常见问题需要标题和说明")
		}
	}
	if len(raw(d)) > 48<<10 {
		return bad("教程总内容请控制在 48 KiB 内")
	}
	return nil
}
func (a *App) clientHelp(w http.ResponseWriter, r *http.Request) error {
	if r.Method == "GET" {
		var data json.RawMessage
		var version int
		e := a.DB.QueryRow(r.Context(), "SELECT document,version FROM portal_content WHERE name='client-help'").Scan(&data, &version)
		if e == pgx.ErrNoRows {
			data = defaultClientHelp
			version = 0
		} else if e != nil {
			return e
		}
		send(w, 200, map[string]any{"document": data, "version": version, "platforms": helpPlatforms})
		return nil
	}
	if e := requireAdminScope(r, "content"); e != nil {
		return e
	}
	var b struct {
		Version  int          `json:"version"`
		Document helpDocument `json:"document"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if b.Document.Apps == nil {
		b.Document.Apps = []helpApp{}
	}
	if b.Document.FAQs == nil {
		b.Document.FAQs = []helpFAQ{}
	}
	if e := validateHelp(b.Document); e != nil {
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
	var n int
	if b.Version == 0 {
		e = tx.QueryRow(r.Context(), `INSERT INTO portal_content(name,document,updated_by) VALUES('client-help',$1,$2) ON CONFLICT(name) DO NOTHING RETURNING version`, raw(b.Document), current(r).ID).Scan(&n)
	} else {
		e = tx.QueryRow(r.Context(), `UPDATE portal_content SET document=$1,updated_by=$2,version=version+1,updated_at=now() WHERE name='client-help' AND version=$3 RETURNING version`, raw(b.Document), current(r).ID, b.Version).Scan(&n)
	}
	if e == pgx.ErrNoRows {
		return Problem{409, "教程已被修改，请重新读取后保存"}
	}
	if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, current(r).ID, "client-help.save", "client-help"); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]any{"version": n, "message": "客户端教程已保存"})
	return nil
}
