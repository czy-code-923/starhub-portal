package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalDomainSwitchRequiresHelperAndExplicitHTTP(t *testing.T) {
	f := setup(t)
	f.a.WorkerToken = strings.Repeat("d", 64)
	call := func(body any, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://portal.test/portal/v1/local/domain", bytes.NewReader(raw(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w
	}
	b := map[string]any{"domain": "", "ip_origin": "http://192.0.2.9:18088", "https": false}
	expect(t, call(b, "wrong"), 401)
	expect(t, call(b, f.a.WorkerToken), 400)
	b["allow_http"] = true
	expect(t, call(b, f.a.WorkerToken), 202)
	var data []byte
	f.a.DB.QueryRow(context.Background(), "SELECT public FROM settings").Scan(&data)
	var s Settings
	json.Unmarshal(data, &s)
	if s.Domain != "" || s.PublicOrigin != "http://192.0.2.9:18088" {
		t.Fatal(string(data))
	}
	b["domain"] = "evil.test/command"
	expect(t, call(b, f.a.WorkerToken), 400)
}

func TestLocalDomainRejectsConcurrentSettingChanges(t *testing.T) {
	f := setup(t)
	f.a.WorkerToken = strings.Repeat("d", 64)
	ctx := context.Background()
	tx, e := f.a.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		t.Fatal(e)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "http://portal.test/portal/v1/local/domain", bytes.NewReader(raw(map[string]any{"domain": "", "ip_origin": "http://192.0.2.9:18088", "allow_http": true})))
		r.Header.Set("Authorization", "Bearer "+f.a.WorkerToken)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		result <- w
	}()
	waiting := false
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if e = f.a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%pg_advisory_xact_lock%')").Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("domain action did not reach serialization lock")
	}
	if _, e = tx.Exec(ctx, "UPDATE settings SET version=version+1,public=public||'{\"domain\":\"operator.example.test\"}'::jsonb"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case w := <-result:
		expect(t, w, 409)
	case <-time.After(5 * time.Second):
		t.Fatal("domain action blocked")
	}
	var domain string
	f.a.DB.QueryRow(ctx, "SELECT public->>'domain' FROM settings").Scan(&domain)
	if domain != "operator.example.test" {
		t.Fatal("stale CLI overwrote settings")
	}
}
