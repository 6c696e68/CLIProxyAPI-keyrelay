package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

type keyShopGateHost struct {
	handlerInterceptorTestHost
	yaml []byte
}

func (h *keyShopGateHost) KeyShopConfigYAML() []byte {
	return h.yaml
}

type keyShopNotifyingHost struct {
	keyShopGateHost
	secret string
	id     string
	code   string
	status int
}

func (h *keyShopNotifyingHost) NoteKeyShopRejection(_ context.Context, secret, requestID, code string, status int) {
	h.secret = secret
	h.id = requestID
	h.code = code
	h.status = status
}

func TestKeyShopModelGateRecordsDenialBeforeInterceptor(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopNotifyingHost{keyShopGateHost: keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels:\n  - name: gpt-5-codex\n    alias: codex-latest\n")}}
	handler.SetPluginHost(host)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5"}`), "")
	assertModelNotAllowed(t, errMsg)
	if host.secret != "ck_customer" || host.code != "model_not_allowed" || host.status != http.StatusForbidden || !strings.HasPrefix(host.id, "gate-") {
		t.Fatalf("denial was not recorded: %+v", host)
	}
}

func TestKeyShopModelGateDeniesBeforeProviderLookup(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels:\n  - name: gpt-5-codex\n    alias: codex-latest\n")})
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5"}`), "")
	assertModelNotAllowed(t, errMsg)
}

func TestKeyShopModelGateRewritesAliasBeforeProviderLookup(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels:\n  - name: gpt-5-codex\n    alias: codex-latest\n")})
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "codex-latest(high)", []byte(`{"model":"codex-latest(high)"}`), "")
	if errMsg == nil || !strings.Contains(errMsg.Error.Error(), "gpt-5-codex(high)") || strings.Contains(errMsg.Error.Error(), "codex-latest") {
		t.Fatalf("alias was not rewritten before routing: %+v", errMsg)
	}
}

func TestKeyShopModelGateRejectsMismatchedBodyModel(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels:\n  - name: gpt-5-codex\n    alias: codex-latest\n")})
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "codex-latest", []byte(`{"model":"gpt-5.5"}`), "")
	assertModelNotAllowed(t, errMsg)
}

func TestKeyShopModelGateFailsClosedOnMalformedPolicy(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels: [\n")})
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "codex-latest", []byte(`{"model":"codex-latest"}`), "")
	if errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("malformed policy was not fail-closed: %+v", errMsg)
	}
}

func TestKeyShopModelGateIgnoresOperatorKey(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels:\n  - name: only-model\n")})
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "operator-smoke-key"), "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5"}`), "")
	if errMsg != nil && errMsg.StatusCode == http.StatusForbidden {
		t.Fatalf("operator key was denied by key-shop policy: %+v", errMsg)
	}
}

func TestKeyShopModelGateDeniesStreamAndCount(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels: []\n")})
	ctx := keyShopContext(t, "ck_customer")
	_, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5","stream":true}`), "")
	assertModelNotAllowed(t, <-errChan)
	_, _, errMsg := handler.ExecuteCountWithAuthManager(ctx, "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5"}`), "")
	assertModelNotAllowed(t, errMsg)
}

func keyShopContext(t *testing.T, secret string) context.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("Authorization", "Bearer "+secret)
	return context.WithValue(context.Background(), "gin", ctx)
}

func assertModelNotAllowed(t *testing.T, errMsg *interfaces.ErrorMessage) {
	t.Helper()
	if errMsg == nil || errMsg.StatusCode != http.StatusForbidden || !strings.Contains(string(errMsg.Body), `"model_not_allowed"`) {
		t.Fatalf("expected model_not_allowed, got %+v", errMsg)
	}
}
