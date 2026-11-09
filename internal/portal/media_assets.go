package portal

import (
	"context"
	"net/http"
	"strings"
)

const mediaReferenced = `(EXISTS(SELECT 1 FROM ticket_messages tm WHERE tm.images ? m.id) OR EXISTS(SELECT 1 FROM announcements n WHERE n.images ? m.id) OR EXISTS(SELECT 1 FROM settings s WHERE s.public->>'background_desktop'='/portal/v1/media/'||m.id OR s.public->>'background_mobile'='/portal/v1/media/'||m.id))`

func (a *App) mediaUpload(w http.ResponseWriter, r *http.Request) error {
	u := current(r)
	if e := a.limit(r, "media/"+u.ID, 30); e != nil {
		return e
	}
	imageSlots <- struct{}{}
	defer func() { <-imageSlots }()
	var b struct {
		Scope string `json:"scope"`
		Image string `json:"image_data"`
	}
	if e := decodeMedia(w, r, &b); e != nil {
		return e
	}
	if !includes([]string{"ticket", "announcement", "background"}, b.Scope) {
		return bad("图片用途无效")
	}
	if b.Scope == "announcement" && !u.can("announcements") || b.Scope == "background" && !u.Owner {
		return Problem{403, "没有上传此类图片的权限"}
	}
	data, mime, e := parseImage(b.Image)
	if e != nil {
		return e
	}
	if len(data) == 0 {
		return bad("请选择图片")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), `DELETE FROM media_assets m WHERE m.created_at<now()-interval '24 hours' AND NOT `+mediaReferenced); e != nil {
		return e
	}
	var own, total, ownCount, totalCount int64
	if e = tx.QueryRow(r.Context(), `SELECT COALESCE(sum(octet_length(content)) FILTER(WHERE owner_id=$1),0),COALESCE(sum(octet_length(content)),0),count(*) FILTER(WHERE owner_id=$1),count(*) FROM media_assets`, u.ID).Scan(&own, &total, &ownCount, &totalCount); e != nil {
		return e
	}
	if own+int64(len(data)) > 64<<20 || total+int64(len(data)) > 512<<20 || ownCount >= 1000 || totalCount >= 10000 {
		return Problem{409, "图片存储额度已满，请联系管理员清理不再使用的内容"}
	}
	id := ID()
	if _, e = tx.Exec(r.Context(), `INSERT INTO media_assets(id,owner_id,scope,mime,content) VALUES($1,$2,$3,$4,$5)`, id, u.ID, b.Scope, mime, data); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 201, map[string]string{"id": id, "url": "/portal/v1/media/" + id})
	return nil
}

func validateMediaRefs(ctx context.Context, q rowQueries, u actor, scope string, ids []string, existing string) error {
	if len(ids) > 6 {
		return bad("每条内容最多 6 张图片")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !portalID.MatchString(id) || seen[id] {
			return bad("图片编号无效或重复")
		}
		seen[id] = true
		var ok bool
		e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM media_assets m WHERE m.id=$1 AND m.scope=$2 AND (m.owner_id=$3 OR ($2='announcement' AND EXISTS(SELECT 1 FROM announcements n WHERE n.id=$4 AND n.images ? m.id))))`, id, scope, u.ID, existing).Scan(&ok)
		if e != nil {
			return e
		}
		if !ok {
			return Problem{403, "图片不存在或不属于当前内容"}
		}
	}
	return nil
}

func (a *App) mediaImage(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !portalID.MatchString(id) {
		return Problem{404, "图片不存在"}
	}
	var public bool
	e := a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM media_assets m WHERE m.id=$1 AND (EXISTS(SELECT 1 FROM announcements n WHERE n.enabled AND n.images ? m.id) OR (m.scope='background' AND EXISTS(SELECT 1 FROM settings s WHERE s.public->>'background_desktop'='/portal/v1/media/'||m.id OR s.public->>'background_mobile'='/portal/v1/media/'||m.id))))`, id).Scan(&public)
	if e != nil {
		return e
	}
	if !public {
		a.auth(a.privateMedia, false)(w, r)
		return nil
	}
	var data []byte
	var mime string
	if e = a.DB.QueryRow(r.Context(), "SELECT content,mime FROM media_assets WHERE id=$1", id).Scan(&data, &mime); e != nil {
		return e
	}
	return sendImage(w, data, mime)
}
func (a *App) privateMedia(w http.ResponseWriter, r *http.Request) error {
	u := current(r)
	var data []byte
	var mime string
	e := a.DB.QueryRow(r.Context(), `SELECT m.content,m.mime FROM media_assets m WHERE m.id=$1 AND (m.owner_id=$2 OR (m.scope='ticket' AND EXISTS(SELECT 1 FROM ticket_messages tm JOIN tickets t ON t.id=tm.ticket_id WHERE tm.images ? m.id AND (t.customer_id=$2 OR $3))) OR (m.scope='announcement' AND $4 AND EXISTS(SELECT 1 FROM announcements n WHERE n.images ? m.id)))`, r.PathValue("id"), u.ID, u.can("support"), u.can("announcements")).Scan(&data, &mime)
	if e != nil {
		return e
	}
	return sendImage(w, data, mime)
}
func (a *App) mediaDelete(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !portalID.MatchString(id) {
		return bad("图片编号无效")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if e = lock(r.Context(), tx); e != nil {
		return e
	}
	tag, e := tx.Exec(r.Context(), `DELETE FROM media_assets m WHERE m.id=$1 AND m.owner_id=$2 AND NOT `+mediaReferenced, id, current(r).ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return Problem{409, "图片仍在使用或不属于当前用户，请先编辑对应内容"}
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]bool{"removed": true})
	return nil
}

func mediaID(path string) string { return strings.TrimPrefix(path, "/portal/v1/media/") }
func mediaList(ids []string) []byte {
	if ids == nil {
		ids = []string{}
	}
	return raw(ids)
}
