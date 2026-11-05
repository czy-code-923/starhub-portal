package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
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
