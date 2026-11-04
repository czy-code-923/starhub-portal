package portal

import (
	"net/http"
	"strings"
)

func (a *App) tickets(w http.ResponseWriter, r *http.Request) error {
	if r.Method == "GET" {
		data, e := jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT id,subject,state,created_at FROM tickets WHERE customer_id=$1 ORDER BY created_at DESC LIMIT 100)x`, current(r).ID)
		if e != nil {
			return e
		}
		send(w, 200, data)
		return nil
	}
	if e := a.limit(r, "ticket", 15); e != nil {
		return e
	}
	var b struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if strings.TrimSpace(b.Subject) == "" || !cleanText(b.Subject, 120) || strings.TrimSpace(b.Body) == "" || !cleanText(b.Body, 6000) {
		return bad("工单标题或内容无效")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	id := ID()
	if _, e = tx.Exec(r.Context(), "INSERT INTO tickets(id,customer_id,subject) VALUES($1,$2,$3)", id, current(r).ID, b.Subject); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), "INSERT INTO ticket_messages(id,ticket_id,customer_id,body) VALUES($1,$2,$3,$4)", ID(), id, current(r).ID, b.Body); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 201, map[string]string{"id": id})
	return nil
}
func (a *App) ticket(w http.ResponseWriter, r *http.Request) error {
	u := current(r)
	var state string
	if e := a.DB.QueryRow(r.Context(), "SELECT state FROM tickets WHERE id=$1 AND (customer_id=$2 OR $3)", r.PathValue("id"), u.ID, u.can("support")).Scan(&state); e != nil {
		return e
	}
	if r.Method == "GET" {
		data, e := jsonRows(r.Context(), a.DB, `SELECT COALESCE(jsonb_agg(x),'[]') FROM(SELECT m.id,m.body,m.created_at,c.role,c.name FROM ticket_messages m JOIN customers c ON c.id=m.customer_id WHERE m.ticket_id=$1 ORDER BY m.created_at,m.id)x`, r.PathValue("id"))
		if e != nil {
			return e
		}
		send(w, 200, map[string]any{"state": state, "messages": data})
		return nil
	}
	var b struct {
		Body  string `json:"body"`
		Close bool   `json:"close"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if !cleanText(b.Body, 6000) || b.Body == "" && !b.Close {
		return bad("回复内容无效")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if b.Body != "" {
		if _, e = tx.Exec(r.Context(), "INSERT INTO ticket_messages(id,ticket_id,customer_id,body) VALUES($1,$2,$3,$4)", ID(), r.PathValue("id"), u.ID, b.Body); e != nil {
			return e
		}
	}
	if b.Close {
		if _, e = tx.Exec(r.Context(), "UPDATE tickets SET state='closed' WHERE id=$1", r.PathValue("id")); e != nil {
			return e
		}
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	send(w, 200, map[string]bool{"saved": true})
	return nil
}
