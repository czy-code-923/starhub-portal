package portal

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var releaseTag = regexp.MustCompile(`^v([0-9]{1,5})\.([0-9]{1,5})\.([0-9]{1,5})$`)

type Release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	URL        string `json:"html_url"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"assets"`
}

func newer(a, b string) bool {
	aa, bb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	if len(aa) != 3 || len(bb) != 3 {
		return false
	}
	for i := range aa {
		x, _ := strconv.Atoi(aa[i])
		y, _ := strconv.Atoi(bb[i])
		if x != y {
			return x > y
		}
	}
	return false
}
func (a *App) latest(r *http.Request) (Release, error) {
	var c Release
	var data []byte
	var checked time.Time
	e := a.DB.QueryRow(r.Context(), "SELECT payload,checked_at FROM release_cache WHERE id=true").Scan(&data, &checked)
	if e == nil && a.now().Sub(checked) < 10*time.Minute {
		e = json.Unmarshal(data, &c)
		return c, e
	}
	if e != nil && e != pgx.ErrNoRows {
		return c, e
	}
	req, e := http.NewRequestWithContext(r.Context(), "GET", "https://api.github.com/repos/"+Repository+"/releases/latest", nil)
	if e != nil {
		return c, e
	}
	req.Header.Set("User-Agent", "Starhub-Portal/"+Version)
	res, e := a.Client.Do(req)
	if e != nil {
		return c, Problem{503, "暂时无法读取 GitHub 正式发布"}
	}
	defer res.Body.Close()
	data, e = io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(data) > 1<<20 || res.StatusCode != 200 {
		return c, Problem{503, "尚无可用正式版或 GitHub 暂不可用"}
	}
	if e = json.Unmarshal(data, &c); e != nil {
		return c, e
	}
	if !releaseTag.MatchString(c.Tag) || c.Draft || c.Prerelease || c.URL != "https://github.com/"+Repository+"/releases/tag/"+c.Tag {
		return c, errors.New("invalid official release")
	}
	if _, e = a.DB.Exec(r.Context(), "INSERT INTO release_cache(id,payload,checked_at) VALUES(true,$1,$2) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,checked_at=excluded.checked_at", data, a.now()); e != nil {
		return c, e
	}
	return c, nil
}
func (a *App) updates(w http.ResponseWriter, r *http.Request) error {
	if r.Method == "GET" {
		release, e := a.latest(r)
		var message string
		if e != nil {
			message = safeJobError(e)
		}
		jobs, err := jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,kind,subject,state,detail,created_at,updated_at FROM jobs WHERE kind IN ('update','gateway') ORDER BY created_at DESC LIMIT 20)x`)
		if err != nil {
			return err
		}
		var ready bool
		var status json.RawMessage
		_ = a.DB.QueryRow(r.Context(), "SELECT last_seen>now()-interval '90 seconds',status FROM local_worker WHERE id=true").Scan(&ready, &status)
		send(w, 200, map[string]any{"current": Version, "latest": strings.TrimPrefix(release.Tag, "v"), "available": e == nil && newer(release.Tag, Version), "release_url": release.URL, "helper_ready": ready, "helper_status": status, "jobs": jobs, "message": message})
		return nil
	}
	if r.Method != "POST" {
		return Problem{405, "method not allowed"}
	}
	var b struct {
		Version string `json:"version"`
		Current string `json:"current"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	release, e := a.latest(r)
	if e != nil {
		return e
	}
	if b.Current != Version || release.Tag != "v"+b.Version || !newer(b.Version, Version) {
		return Problem{409, "版本已变化，请重新检查更新"}
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	var ready, busy bool
	if e = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM local_worker WHERE last_seen>now()-interval '90 seconds'),EXISTS(SELECT 1 FROM jobs WHERE kind='update' AND state IN ('queued','working'))").Scan(&ready, &busy); e != nil {
		return e
	}
	if !ready || busy {
		return Problem{409, "更新助手未就绪或已有更新任务"}
	}
	payload, e := a.seal(map[string]string{"version": b.Version, "current": Version})
	if e != nil {
		return e
	}
	id := ID()
	if e = tx.QueryRow(r.Context(), `INSERT INTO jobs(id,kind,subject,payload) VALUES($1,'update',$2,$3)
ON CONFLICT(kind,subject) DO UPDATE SET payload=excluded.payload,state='queued',attempts=0,available_at=now(),lease_until=NULL,detail='',updated_at=now()
WHERE jobs.state IN ('failed','rolled_back') RETURNING id`, id, b.Version, payload).Scan(&id); e == pgx.ErrNoRows {
		return Problem{409, "该版本更新任务已完成，请刷新版本状态"}
	} else if e != nil {
		return e
	}
	if e = audit(r.Context(), tx, current(r).ID, "update.request", b.Version); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 202, map[string]string{"id": id})
	return nil
}
func (a *App) localWorker(w http.ResponseWriter, r *http.Request) error {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(a.WorkerToken) < 64 || subtle.ConstantTimeCompare([]byte(hash(token)), []byte(hash(a.WorkerToken))) != 1 {
		return Problem{401, "unauthorized worker"}
	}
	switch r.PathValue("action") {
	case "domain":
		return a.localDomain(w, r)
	case "heartbeat":
		var b struct {
			Status map[string]string `json:"status"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if len(b.Status) > 8 {
			return bad("worker status too large")
		}
		for k, v := range b.Status {
			if len(k) > 50 || len(v) > 250 {
				return bad("worker status too large")
			}
		}
		_, e := a.DB.Exec(r.Context(), "INSERT INTO local_worker(id,last_seen,status) VALUES(true,now(),$1) ON CONFLICT(id) DO UPDATE SET last_seen=now(),status=excluded.status", raw(b.Status))
		if e != nil {
			return e
		}
		send(w, 200, map[string]bool{"ok": true})
		return nil
	case "claim":
		var id, kind, subject string
		var payload []byte
		e := a.DB.QueryRow(r.Context(), `UPDATE jobs SET state='working',attempts=attempts+1,lease_until=now()+interval '30 minutes',updated_at=now() WHERE id=(SELECT id FROM jobs WHERE kind IN ('gateway','update') AND ((state='queued' AND available_at<=now()) OR (state='working' AND lease_until<now())) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,kind,subject,payload`).Scan(&id, &kind, &subject, &payload)
		if e == pgx.ErrNoRows {
			send(w, 200, map[string]any{"job": nil})
			return nil
		}
		if e != nil {
			return e
		}
		var body any
		if e = a.unseal(payload, &body); e != nil {
			return e
		}
		send(w, 200, map[string]any{"job": map[string]any{"id": id, "kind": kind, "subject": subject, "payload": body}})
		return nil
	case "report":
		var b struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Detail string `json:"detail"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if !includes([]string{"succeeded", "failed", "rolled_back"}, b.State) || len(b.Detail) > 500 {
			return bad("invalid worker report")
		}
		tag, e := a.DB.Exec(r.Context(), "UPDATE jobs SET state=$2,detail=$3,lease_until=NULL,updated_at=now() WHERE id=$1 AND kind IN ('update','gateway') AND state='working'", b.ID, b.State, b.Detail)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return Problem{409, "worker job state changed"}
		}
		send(w, 200, map[string]bool{"ok": true})
		return nil
	}
	return bad("unknown worker action")
}

func (a *App) retryGateway(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	ctx := r.Context()
	s, _, e := a.settings(ctx)
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
	var busy bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE kind='gateway' AND state IN ('queued','working'))").Scan(&busy); e != nil {
		return e
	}
	if busy {
		return Problem{409, "已有域名任务排队或处理中，请先查看任务状态"}
	}
	payload, e := a.seal(map[string]any{"domain": s.Domain, "https": s.HTTPS, "origin": s.PublicOrigin})
	if e != nil {
		return e
	}
	id := ID()
	if _, e = tx.Exec(ctx, "INSERT INTO jobs(id,kind,subject,payload) VALUES($1,'gateway',$2,$3)", id, ID(), payload); e != nil {
		return e
	}
	if e = audit(ctx, tx, current(r).ID, "gateway.retry", s.Domain); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]string{"id": id, "message": "域名检查与反代同步已排队；证书与真实应用入口分别校验，IP 入口保留"})
	return nil
}
