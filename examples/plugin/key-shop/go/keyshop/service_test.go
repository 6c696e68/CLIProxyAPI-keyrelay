package keyshop

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testService(t *testing.T) *Service {
	t.Helper()
	store, err := Open(Config{DataFile: filepath.Join(t.TempDir(), "keys.db"), Now: func() time.Time {
		return time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store, []byte("<admin>"), []byte("<dash>"))
}

func call(t *testing.T, svc *Service, method, body string) []byte {
	t.Helper()
	raw, err := svc.Call(method, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func createSecret(t *testing.T, svc *Service, ttl, limit int64) string {
	t.Helper()
	raw := call(t, svc, "management.handle", `{"Method":"POST","Path":"/v0/management/plugins/key-shop/keys","Body":`+string(mustJSON(t, map[string]any{"ttl_seconds": ttl, "token_limit": limit}))+`}`)
	var env struct {
		Result struct {
			Body []byte
		}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Secret string
	}
	if err := json.Unmarshal(env.Result.Body, &created); err != nil || created.Secret == "" {
		t.Fatal("create did not return a secret")
	}
	return created.Secret
}

func TestRegisterDeclaresAdmissionCapabilities(t *testing.T) {
	raw := call(t, testService(t), "plugin.register", `{}`)
	if !strings.Contains(string(raw), `"schema_version":6`) || !strings.Contains(string(raw), `"request_interceptor":true`) {
		t.Fatalf("register missing capabilities")
	}
}

func TestAuthenticateRejectsExpiredAndUnsupportedPaths(t *testing.T) {
	svc := testService(t)
	expired := createSecret(t, svc, -60, 100)
	raw := call(t, svc, "frontend_auth.authenticate", authBody(expired, "/v1/chat/completions"))
	if strings.Contains(string(raw), `"Authenticated":true`) {
		t.Fatal("expired key authenticated")
	}
	secret := createSecret(t, svc, 3600, 100)
	for _, path := range []string{"/v1/realtime", "/v1/live", "/v1/live/abc", "/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		raw = call(t, svc, "frontend_auth.authenticate", authBody(secret, path))
		if strings.Contains(string(raw), `"Authenticated":true`) {
			t.Fatalf("unsupported path authenticated: %s", path)
		}
	}
	raw = call(t, svc, "frontend_auth.authenticate", authBody(secret, "/v1/chat/completions"))
	if !strings.Contains(string(raw), `"Authenticated":true`) {
		t.Fatal("active key was rejected")
	}
}

func TestInterceptBeforeDeniesExhaustedAndExpired(t *testing.T) {
	svc := testService(t)
	exhausted := createSecret(t, svc, 3600, 1)
	raw := call(t, svc, "request.intercept_before", interceptBody(exhausted, "one", "/v1/chat/completions", true))
	if strings.Contains(string(raw), `"Terminate":true`) {
		t.Fatal("first admit terminated")
	}
	_ = call(t, svc, "usage.handle", `{"RequestID":"one","Failed":false,"Detail":{"InputTokens":1}}`)
	_ = call(t, svc, "request.complete", `{"RequestID":"one","Outcome":"succeeded"}`)
	raw = call(t, svc, "request.intercept_before", interceptBody(exhausted, "two", "/v1/messages/count_tokens", false))
	if !strings.Contains(string(raw), `"Terminate":true`) || !strings.Contains(string(raw), `"StatusCode":402`) {
		t.Fatal("exhausted count was not denied")
	}
	expired := createSecret(t, svc, -60, 100)
	raw = call(t, svc, "request.intercept_before", interceptBody(expired, "old", "/v1/messages/count_tokens", false))
	if !strings.Contains(string(raw), `"StatusCode":403`) {
		t.Fatal("expired count was not denied")
	}
}

func TestManagementCreateAndPublicSession(t *testing.T) {
	svc := testService(t)
	raw := call(t, svc, "management.handle", `{"Method":"POST","Path":"/plugins/key-shop/keys","Body":{"label":"buyer","token_limit":50}}`)
	if strings.Contains(string(raw), "secret_hash") {
		t.Fatal("create leaked hash")
	}
	secret := createSecret(t, svc, 3600, 50)
	listed := call(t, svc, "management.handle", `{"Method":"GET","Path":"/plugins/key-shop/keys"}`)
	if strings.Contains(string(listed), secret) || strings.Contains(string(listed), "secret_hash") {
		t.Fatal("list leaked secret material")
	}
	session := call(t, svc, "management.handle", `{"Method":"GET","Path":"/api/session","Headers":{"Authorization":["Bearer `+secret+`"]}}`)
	if !strings.Contains(string(session), `"StatusCode":200`) {
		t.Fatal("session was not authorized")
	}
	missing := call(t, svc, "management.handle", `{"Method":"GET","Path":"/api/session","Query":{"key":["ck_query"]}}`)
	if !strings.Contains(string(missing), `"StatusCode":401`) {
		t.Fatal("query key was accepted")
	}
}

func authBody(secret, path string) string {
	body, _ := json.Marshal(map[string]any{"Path": path, "Headers": map[string][]string{"Authorization": {"Bearer " + secret}}})
	return string(body)
}

func interceptBody(secret, id, path string, generate bool) string {
	body, _ := json.Marshal(map[string]any{
		"RequestID": id,
		"Headers":   map[string][]string{"Authorization": {"Bearer " + secret}},
		"Metadata":  map[string]any{"request_path": path, "generate": generate},
	})
	return string(body)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
