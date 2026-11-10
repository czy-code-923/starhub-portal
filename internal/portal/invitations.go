package portal

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var inviteCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func (a *App) manageInvitations(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if r.Method == "GET" {
		rows, e := a.DB.Query(ctx, `SELECT to_jsonb(x),i.sealed_code FROM invitations i CROSS JOIN LATERAL(SELECT i.id,i.version,i.prefix,i.email,i.enabled,i.starts_at,i.ends_at,i.max_uses,i.claimed,GREATEST(0,i.max_uses-i.claimed) remaining_uses,(SELECT count(*) FROM invitation_claims ic WHERE ic.invitation_id=i.id AND ic.finalized AND NOT ic.released) verified_uses,(SELECT count(*) FROM invitation_claims ic WHERE ic.invitation_id=i.id AND NOT ic.finalized AND NOT ic.released) reserved_uses,i.created_at)x ORDER BY i.created_at DESC LIMIT 200`)
		if e != nil {
			return e
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var data []byte
			var sealed []byte
			if e = rows.Scan(&data, &sealed); e != nil {
				return e
			}
			var item map[string]any
			if e = json.Unmarshal(data, &item); e != nil {
				return e
			}
			code := ""
			if len(sealed) > 0 {
				if e = a.unseal(sealed, &code); e != nil {
					return e
				}
			}
			item["code"] = code
			item["legacy_unrecoverable"] = len(sealed) == 0
			items = append(items, item)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		send(w, 200, items)
		return nil
	}
	if r.Method != "POST" && r.Method != "PUT" {
		return Problem{405, "使用编辑停用邀请码"}
	}
	var b struct {
		ID         string    `json:"id"`
		Version    int       `json:"version"`
		Code       string    `json:"code"`
		Regenerate bool      `json:"regenerate"`
		Email      string    `json:"email"`
		Enabled    bool      `json:"enabled"`
		Starts     time.Time `json:"starts_at"`
		Ends       time.Time `json:"ends_at"`
		Max        int       `json:"max_uses"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	var e error
	if b.Email != "" {
		b.Email, e = emailAddress(b.Email)
		if e != nil {
			return e
		}
	}
	if b.Max < 1 || b.Max > 10000 || !b.Ends.After(b.Starts) {
		return bad("邀请码时间或名额无效")
	}
	code := strings.TrimSpace(b.Code)
	if b.Regenerate || b.ID == "" && code == "" {
		code = ID() + ID()
	}
	if code != "" && !inviteCodePattern.MatchString(code) {
		return bad("邀请码须为 8–128 位字母、数字、下划线或连字符")
	}
	var sealed []byte
	if code != "" {
		sealed, e = a.seal(code)
		if e != nil {
			return e
		}
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return e
	}
	id := b.ID
	if id == "" {
		id = ID()
		_, e = tx.Exec(ctx, `INSERT INTO invitations(id,code_hash,prefix,email,enabled,starts_at,ends_at,max_uses,sealed_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, hash(code), code[:8], b.Email, b.Enabled, b.Starts, b.Ends, b.Max, sealed)
	} else {
		tag, err := tx.Exec(ctx, `UPDATE invitations SET email=$2,enabled=$3,starts_at=$4,ends_at=$5,max_uses=$6,code_hash=CASE WHEN $7='' THEN code_hash ELSE $7 END,prefix=CASE WHEN $7='' THEN prefix ELSE $8 END,sealed_code=CASE WHEN $7='' THEN sealed_code ELSE $9 END,version=version+1 WHERE id=$1 AND version=$10 AND claimed<=$6`, id, b.Email, b.Enabled, b.Starts, b.Ends, b.Max, func() string {
			if code == "" {
				return ""
			}
			return hash(code)
		}(), func() string {
			if len(code) < 8 {
				return ""
			}
			return code[:8]
		}(), sealed, b.Version)
		e = err
		if e == nil && tag.RowsAffected() != 1 {
			return Problem{409, "邀请码已变化，或新名额少于已使用数量"}
		}
	}
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, current(r).ID, "invitation.save", id); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]any{"id": id, "saved": true, "message": "邀请码已保存，可在列表中反复查看和复制"})
	return nil
}
