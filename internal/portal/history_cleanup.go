package portal

import (
	"net/http"
	"strconv"
	"time"
)

type historyRequest struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	ID        string   `json:"id"`
	Days      int      `json:"days"`
	Confirmed bool     `json:"confirmed"`
	IDs       []string `json:"ids"`
}

func historySpec(kind, action string) (table, condition, stamp string) {
	stamp = "x.created_at"
	switch kind + "/" + action {
	case "tickets/purge":
		return "tickets", `x.state='closed' AND NOT EXISTS(SELECT 1 FROM ticket_messages m WHERE m.ticket_id=x.id AND m.created_at>=$2)`, "x.closed_at"
	case "announcements/purge":
		return "announcements", "NOT x.enabled", "x.updated_at"
	case "orders/archive":
		return "orders", `x.archived_at IS NULL AND (x.state IN ('cancelled','refunded') OR x.state='paid' AND EXISTS(SELECT 1 FROM service_accounts s WHERE s.order_id=COALESCE(x.renewal_of,x.id) AND s.remote_state IN ('expired','deleted','disabled','invalid'))) AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.subject=x.id AND j.state NOT IN ('succeeded','rolled_back'))`, stamp
	case "orders/restore":
		return "orders", "x.archived_at IS NOT NULL", stamp
	case "manual-payments/restore":
		return "manual_payments", "x.archived_at IS NOT NULL", stamp
	case "orders/purge":
		return "orders", `x.state='cancelled' AND x.expires_at<$3 AND NOT EXISTS(SELECT 1 FROM payments p WHERE p.order_id=x.id) AND NOT EXISTS(SELECT 1 FROM manual_payments m WHERE m.order_id=x.id) AND NOT EXISTS(SELECT 1 FROM redpacket_claims r WHERE r.order_id=x.id) AND NOT EXISTS(SELECT 1 FROM entitlement_ledger l WHERE l.order_id=x.id) AND NOT EXISTS(SELECT 1 FROM service_accounts s WHERE s.order_id=x.id OR s.last_order_id=x.id) AND NOT EXISTS(SELECT 1 FROM orders r WHERE r.renewal_of=x.id) AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.subject=x.id) AND NOT EXISTS(SELECT 1 FROM telegram_events e WHERE e.order_id=x.id AND e.state<>'sent')`, stamp
	case "manual-payments/archive", "manual-payments/attachments":
		c := `x.state IN ('approved','rejected','cancelled') AND EXISTS(SELECT 1 FROM orders o WHERE o.id=x.order_id AND o.state IN ('paid','cancelled','refunded'))`
		if action == "archive" {
			c += " AND x.archived_at IS NULL"
		} else {
			c += " AND x.archived_at IS NOT NULL AND x.attachments_purged_at IS NULL"
		}
		return "manual_payments", c, stamp
	}
	return "", "", ""
}

func (a *App) historyCleanup(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	b := historyRequest{Days: 90}
	if r.Method == "GET" {
		b.Kind = r.URL.Query().Get("kind")
		b.Action = r.URL.Query().Get("action")
		b.ID = r.URL.Query().Get("id")
		if v := r.URL.Query().Get("days"); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil {
				return bad("清理天数无效")
			}
			b.Days = n
		}
	} else {
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if !b.Confirmed {
			return bad("请确认清理范围和不可恢复的内容")
		}
		if e := a.limit(r, "history-cleanup", 20); e != nil {
			return e
		}
	}
	table, condition, stamp := historySpec(b.Kind, b.Action)
	if table == "" || b.Days < 0 || b.Days > 3650 || b.ID != "" && !portalID.MatchString(b.ID) || b.ID == "" && b.Days < 7 {
		return bad("清理类型、操作、记录或时间范围无效")
	}
	if r.Method != "GET" {
		if b.ID != "" && len(b.IDs) == 0 {
			b.IDs = []string{b.ID}
		}
		if len(b.IDs) < 1 || len(b.IDs) > 100 {
			return bad("请先预览并确认本批记录")
		}
		for _, id := range b.IDs {
			if !portalID.MatchString(id) {
				return bad("清理记录 ID 无效")
			}
		}
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if r.Method != "GET" {
		if e = lock(ctx, tx); e != nil {
			return e
		}
	}
	query := "SELECT x.id FROM " + table + " x WHERE (" + condition + ") AND ($1='' OR x.id=$1) AND " + stamp + "<$2 AND x.created_at<=$3 ORDER BY " + stamp + ",x.id LIMIT 101"
	args := []any{b.ID, a.now().Add(-time.Duration(b.Days) * 24 * time.Hour), a.now()}
	if r.Method != "GET" {
		query = "SELECT x.id FROM " + table + " x WHERE (" + condition + ") AND ($1='' OR x.id=$1) AND " + stamp + "<$2 AND x.created_at<=$3 AND x.id=ANY($4) ORDER BY " + stamp + ",x.id LIMIT 100"
		args = append(args, b.IDs)
		query += " FOR UPDATE OF x"
	}
	rows, e := tx.Query(ctx, query, args...)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	more := len(ids) > 100
	if more {
		ids = ids[:100]
	}
	if r.Method == "GET" {
		send(w, 200, map[string]any{"eligible": len(ids), "ids": ids, "has_more": more, "batch_limit": 100, "kind": b.Kind, "action": b.Action})
		return nil
	}
	if len(ids) == 0 {
		return Problem{409, "没有符合条件的记录；状态可能已改变，待处理款项、有效服务和必要账本不会删除"}
	}
	var changed, images int64
	var freed int64
	media := []string{}
	if b.Action == "purge" && (b.Kind == "tickets" || b.Kind == "announcements") {
		q := "SELECT DISTINCT jsonb_array_elements_text(images) FROM announcements WHERE id=ANY($1)"
		if b.Kind == "tickets" {
			q = "SELECT DISTINCT jsonb_array_elements_text(images) FROM ticket_messages WHERE ticket_id=ANY($1)"
		}
		mr, err := tx.Query(ctx, q, ids)
		if err != nil {
			return err
		}
		for mr.Next() {
			var id string
			if err = mr.Scan(&id); err != nil {
				mr.Close()
				return err
			}
			media = append(media, id)
		}
		mr.Close()
		if err = mr.Err(); err != nil {
			return err
		}
	}
	switch b.Action {
	case "restore":
		tag, err := tx.Exec(ctx, "UPDATE "+table+" SET archived_at=NULL WHERE id=ANY($1)", ids)
		e = err
		changed = tag.RowsAffected()
	case "archive":
		tag, err := tx.Exec(ctx, "UPDATE "+table+" SET archived_at=$2 WHERE id=ANY($1)", ids, a.now())
		e = err
		changed = tag.RowsAffected()
	case "attachments":
		if e = tx.QueryRow(ctx, "SELECT COALESCE(sum(COALESCE(octet_length(qr_image),0)+COALESCE(octet_length(proof_image),0)+COALESCE(octet_length(redpacket_sealed),0)),0) FROM manual_payments WHERE id=ANY($1)", ids).Scan(&freed); e != nil {
			return e
		}
		tag, err := tx.Exec(ctx, "UPDATE manual_payments SET qr_image=NULL,proof_image=NULL,redpacket_sealed=NULL,attachments_purged_at=$2,version=version+1 WHERE id=ANY($1)", ids, a.now())
		e = err
		changed = tag.RowsAffected()
	case "purge":
		if b.Kind == "tickets" {
			if _, e = tx.Exec(ctx, "DELETE FROM ticket_messages WHERE ticket_id=ANY($1)", ids); e != nil {
				return e
			}
		}
		if b.Kind == "orders" {
			if _, e = tx.Exec(ctx, "DELETE FROM telegram_events WHERE order_id=ANY($1) AND state='sent'", ids); e != nil {
				return e
			}
		}
		tag, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE id=ANY($1)", ids)
		e = err
		changed = tag.RowsAffected()
	}
	if e != nil {
		return e
	}
	if len(media) > 0 {
		tag, err := tx.Exec(ctx, "DELETE FROM media_assets m WHERE m.id=ANY($1) AND NOT "+mediaReferenced, media)
		if err != nil {
			return err
		}
		images = tag.RowsAffected()
	}
	if e = audit(ctx, tx, current(r).ID, "history."+b.Action, b.Kind+":"+strconv.FormatInt(changed, 10)); e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" x WHERE ("+condition+") AND ($1='' OR x.id=$1) AND "+stamp+"<$2 AND x.created_at<=$3)", args[:3]...).Scan(&more); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	send(w, 200, map[string]any{"changed": changed, "images_removed": images, "attachment_bytes_removed": freed, "has_more": more, "message": "本批处理完成；订阅身份、付款防重和开通凭据保留。数据库可复用回收空间，数据文件不一定立即缩小。"})
	return nil
}
