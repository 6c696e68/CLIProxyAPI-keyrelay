package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// tokenRevokedBody mirrors the real upstream 401 body returned by Codex when
// an OAuth access token has been permanently revoked.
const tokenRevokedBody = `{"error":{"message":"Encountered invalidated oauth token for user, failing request","type":null,"code":"token_revoked","param":null},"status":401}`

func assertTokenRevokedDisabled(t *testing.T, auth *Auth) {
	t.Helper()
	if auth == nil {
		t.Fatalf("auth is nil")
	}
	if !auth.Disabled {
		t.Fatalf("Disabled = false, want true")
	}
	if auth.Status != StatusDisabled {
		t.Fatalf("Status = %v, want StatusDisabled", auth.Status)
	}
	if !auth.Unavailable {
		t.Fatalf("Unavailable = false, want true")
	}
	if !auth.NextRefreshAfter.IsZero() || !auth.NextRetryAfter.IsZero() {
		t.Fatalf("refresh/retry schedule = (%v, %v), want zero", auth.NextRefreshAfter, auth.NextRetryAfter)
	}
	if auth.RefreshFailures != 0 {
		t.Fatalf("RefreshFailures = %d, want 0", auth.RefreshFailures)
	}
	if disabled, ok := auth.Metadata["disabled"].(bool); !ok || !disabled {
		t.Fatalf("Metadata[disabled] = %v, want true", auth.Metadata["disabled"])
	}
	if auth.StatusMessage != "disabled (oauth token revoked)" {
		t.Fatalf("StatusMessage = %q, want %q", auth.StatusMessage, "disabled (oauth token revoked)")
	}
	if auth.LastError == nil {
		t.Fatalf("LastError is nil")
	}
	if auth.LastError.Code != "token_revoked" {
		t.Fatalf("LastError.Code = %q, want token_revoked", auth.LastError.Code)
	}
	if auth.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("LastError.HTTPStatus = %d, want 401", auth.LastError.HTTPStatus)
	}
}

func tokenRevokedAuth(id string, status Status, lastError *Error) *Auth {
	now := time.Now()
	return &Auth{
		ID:        id,
		Provider:  "codex",
		Status:    status,
		LastError: lastError,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-revoked",
			"expires_at":    now.Add(-time.Hour).Format(time.RFC3339),
		},
	}
}

func TestTokenRevoked_RefreshErrorDisablesAndPersistsAuth(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{
		id:          "codex",
		errToReturn: oauthStatusError{code: http.StatusUnauthorized, msg: tokenRevokedBody},
	}
	manager.RegisterExecutor(executor)

	if _, errRegister := manager.Register(ctx, tokenRevokedAuth("token-revoked-refresh", StatusActive, nil)); errRegister != nil {
		t.Fatalf("Register error: %v", errRegister)
	}

	if _, errRefresh := manager.refreshAuthForRequest(ctx, "token-revoked-refresh", ""); errRefresh == nil {
		t.Fatalf("expected refresh error, got nil")
	}

	manager.mu.RLock()
	current := manager.auths["token-revoked-refresh"]
	_, hasJob := manager.refreshJobs["token-revoked-refresh"]
	manager.mu.RUnlock()

	assertTokenRevokedDisabled(t, current)
	if hasJob {
		t.Fatalf("token-revoked auth still has a pending refresh job")
	}
	assertTokenRevokedDisabled(t, loadStoredRevokedTestAuth(t, store, "token-revoked-refresh"))
}

func TestTokenRevoked_ForceRefreshTerminalUnauthorizedDisablesAndPersistsAuth(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{
		id:          "codex",
		errToReturn: oauthStatusError{code: http.StatusUnauthorized, msg: tokenRevokedBody},
	}
	manager.RegisterExecutor(executor)

	auth := tokenRevokedAuth("token-revoked-terminal", StatusError, &Error{
		Code:       "unauthorized",
		Message:    "unauthorized",
		HTTPStatus: http.StatusUnauthorized,
	})
	auth.Unavailable = true
	registered, errRegister := manager.Register(ctx, auth)
	if errRegister != nil {
		t.Fatalf("Register error: %v", errRegister)
	}
	if !HasUnauthorizedAuthFailure(registered) {
		t.Fatalf("test setup: auth is not terminal unauthorized")
	}

	if _, errRefresh := manager.ForceRefreshAuth(ctx, auth.ID); errRefresh == nil {
		t.Fatalf("expected refresh error, got nil")
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()

	assertTokenRevokedDisabled(t, current)
	assertTokenRevokedDisabled(t, loadStoredRevokedTestAuth(t, store, auth.ID))
}

func TestRelayStatusError_TokenRevokedBodyClassifies(t *testing.T) {
	statusErr := relayStatusError(http.StatusUnauthorized, []byte(tokenRevokedBody))
	if statusErr == nil {
		t.Fatalf("relayStatusError() = nil, want a 401 error")
	}
	if statusErr.Code != "relay_upstream_status" {
		t.Fatalf("Code = %q, want relay_upstream_status", statusErr.Code)
	}
	if statusErr.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("HTTPStatus = %d, want 401", statusErr.HTTPStatus)
	}
	if !strings.Contains(statusErr.Message, "token_revoked") {
		t.Fatalf("Message = %q, want the upstream token_revoked code", statusErr.Message)
	}
	if !strings.Contains(statusErr.Message, "Encountered invalidated oauth token for user") {
		t.Fatalf("Message = %q, want the upstream error message", statusErr.Message)
	}
	if strings.Contains(statusErr.Message, "401}") {
		t.Fatalf("Message = %q, must not echo the raw upstream body", statusErr.Message)
	}
	if !isRevokedRefreshTokenResultError(statusErr) {
		t.Fatalf("isRevokedRefreshTokenResultError() = false, want true for token_revoked 401")
	}
	if statusCodeFromResult(statusErr) != http.StatusUnauthorized || !isUnauthorizedError(statusErr) {
		t.Fatalf("status classification lost: %d %v", statusCodeFromResult(statusErr), isUnauthorizedError(statusErr))
	}

	for name, body := range map[string]string{
		"empty":          "",
		"non-json":       "oops not json",
		"plain":          `{"error":{"message":"unauthorized","code":"unauthorized"}}`,
		"null-code":      `{"error":{"message":null,"code":null,"type":null}}`,
		"nested-code":    `{"error":{"code":{"bad":"object"},"message":"unauthorized"}}`,
		"missing-error":  `{"message":"unauthorized"}`,
		"whitespace-pad": "\n\t  {\"error\":{\"code\":\"token_revoked\",\"message\":\"invalidated oauth token\"}}\n",
	} {
		errStatus := relayStatusError(http.StatusUnauthorized, []byte(body))
		if errStatus == nil {
			t.Fatalf("%s: relayStatusError() = nil", name)
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "whitespace-pad":
			if !isRevokedRefreshTokenResultError(errStatus) {
				t.Fatalf("%s: expected revoked classification, Code=%q Message=%q", name, errStatus.Code, errStatus.Message)
			}
		default:
			if isRevokedRefreshTokenResultError(errStatus) {
				t.Fatalf("%s: unexpected revoked classification, Code=%q Message=%q", name, errStatus.Code, errStatus.Message)
			}
		}
	}
}

func TestRelayStatusError_OversizedBodyIsBounded(t *testing.T) {
	body := `{"error":{"code":"token_revoked","message":"` + strings.Repeat("x", 64<<10) + `"}}`
	if len(body) <= relayUpstreamErrorBodyLimit {
		t.Fatalf("test setup: body is not oversized")
	}
	errStatus := relayStatusError(http.StatusUnauthorized, []byte(body))
	if errStatus == nil {
		t.Fatalf("relayStatusError() = nil, want a 401 error")
	}
	if isRevokedRefreshTokenResultError(errStatus) {
		t.Fatalf("oversized body unexpectedly classified as revoked, Message=%q", errStatus.Message)
	}
}

func TestExecuteRelayRequest_TokenRevoked401WithoutRefreshCredentialDisablesAuth(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, nil, nil)
	if _, errRegister := manager.Register(ctx, &Auth{
		ID: "relay-token-revoked", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "revoked-access-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	attempts := 0
	transport := relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(tokenRevokedBody)),
		}, nil
	})
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: transport})

	response, errExecute := manager.ExecuteRelayRequest(ctx, RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodPost,
		RelativePath: "/responses", Body: []byte(`{"model":"gpt-5.4"}`), ReplaySafe: false,
	}, RelayRequestPolicy{Retry401: true, MaxRetries: 2})
	if attempts != 1 {
		t.Errorf("transport attempts = %d, want exactly one send", attempts)
	}
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("response status = %d, want 401 passthrough", response.StatusCode)
	}

	manager.mu.RLock()
	current := manager.auths["relay-token-revoked"]
	manager.mu.RUnlock()
	assertTokenRevokedDisabled(t, current)
	assertTokenRevokedDisabled(t, loadStoredRevokedTestAuth(t, store, "relay-token-revoked"))
}

func TestExecuteRelayRequest_TokenRevoked401RefreshSuccessKeepsCredential(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, nil, nil)
	executor := &countingRefreshExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(ctx, &Auth{
		ID: "relay-token-revoked-refresh-ok", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "old-access-token", "refresh_token": "refresh-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	attempts := 0
	transport := relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		status := http.StatusOK
		if attempts == 1 {
			status = http.StatusUnauthorized
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(tokenRevokedBody)),
			}, nil
		}
		if req.Header.Get("Authorization") != "Bearer refreshed-token" {
			t.Errorf("retry did not use refreshed credentials")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: transport})

	response, errExecute := manager.ExecuteRelayRequest(ctx, RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodGet,
		RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{Retry401: true, MaxRetries: 1})
	if attempts != 2 {
		t.Errorf("transport attempts = %d, want 2 (401 then retry)", attempts)
	}
	if executor.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls = %d, want 1", executor.refreshCalls.Load())
	}
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != "ok" {
		t.Errorf("response = %#v, want the 200 retry response", response)
	}

	manager.mu.RLock()
	current := manager.auths["relay-token-revoked-refresh-ok"]
	manager.mu.RUnlock()
	if current == nil {
		t.Fatalf("auth missing after successful retry")
	}
	if current.Disabled || current.Status == StatusDisabled {
		t.Fatalf("Disabled = %v Status = %v, want enabled after successful refresh retry", current.Disabled, current.Status)
	}
	stored := loadStoredRevokedTestAuth(t, store, "relay-token-revoked-refresh-ok")
	if stored.Disabled || stored.Status == StatusDisabled {
		t.Fatalf("stored auth disabled after successful refresh retry")
	}
}

// tokenRevokedExecuteExecutor always answers Execute with the upstream revoked
// token 401 body while Refresh keeps issuing new tokens, simulating a revoked
// account that upstream rejects on every fresh access token.
type tokenRevokedExecuteExecutor struct {
	id           string
	refreshOk    bool
	refreshErr   error
	refreshCalls atomic.Int32
}

func (e *tokenRevokedExecuteExecutor) Identifier() string { return e.id }

func (e *tokenRevokedExecuteExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: tokenRevokedBody}
}

func (e *tokenRevokedExecuteExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: tokenRevokedBody}
}

func (e *tokenRevokedExecuteExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if !e.refreshOk {
		return nil, e.refreshErr
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "fresh-but-revoked-token"
	return auth, nil
}

func (e *tokenRevokedExecuteExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *tokenRevokedExecuteExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestExecute_TokenRevoked401AfterRefreshRetryStillRevokedDisablesAuth(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("exec-token-revoked", "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	defer reg.UnregisterClient("exec-token-revoked")
	store := newMemoryAuthTestStore()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &tokenRevokedExecuteExecutor{id: "codex", refreshOk: true}
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(ctx, &Auth{
		ID: "exec-token-revoked", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "revoked-access-token", "refresh_token": "refresh-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errExec := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.4"}, cliproxyexecutor.Options{}); errExec == nil {
		t.Fatalf("expected upstream unauthorized error, got nil")
	}

	if executor.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls = %d, want exactly one inline refresh", executor.refreshCalls.Load())
	}
	manager.mu.RLock()
	current := manager.auths["exec-token-revoked"]
	manager.mu.RUnlock()
	assertTokenRevokedDisabled(t, current)
	assertTokenRevokedDisabled(t, loadStoredRevokedTestAuth(t, store, "exec-token-revoked"))
}

func TestExecute_TokenRevoked401AfterFailedRefreshInvocationIsNotDisabled(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("exec-token-revoked-transient-refresh", "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	defer reg.UnregisterClient("exec-token-revoked-transient-refresh")
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &tokenRevokedExecuteExecutor{
		id:         "codex",
		refreshOk:  false,
		refreshErr: oauthStatusError{code: http.StatusInternalServerError, msg: "upstream refresh unavailable"},
	}
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(ctx, &Auth{
		ID: "exec-token-revoked-transient-refresh", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "revoked-access-token", "refresh_token": "refresh-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errExec := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.4"}, cliproxyexecutor.Options{}); errExec == nil {
		t.Fatalf("expected upstream unauthorized error, got nil")
	}

	manager.mu.RLock()
	current := manager.auths["exec-token-revoked-transient-refresh"]
	manager.mu.RUnlock()
	if current == nil {
		t.Fatalf("auth missing after failed execution")
	}
	if current.Disabled || current.Status == StatusDisabled {
		t.Fatalf("Disabled = %v Status = %v, want not disabled while refresh may still recover", current.Disabled, current.Status)
	}
}

func TestExecuteRelayRequest_PlainUnauthorized401NotDisabled(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, nil, nil)
	if _, errRegister := manager.Register(ctx, &Auth{
		ID: "relay-plain-unauthorized", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "plain-access-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	transport := relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unauthorized","code":"unauthorized"}}`)),
		}, nil
	})
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: transport})

	_, errExecute := manager.ExecuteRelayRequest(ctx, RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodPost,
		RelativePath: "/responses", Body: []byte(`{"model":"gpt-5.4"}`), ReplaySafe: false,
	}, RelayRequestPolicy{})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}

	manager.mu.RLock()
	current := manager.auths["relay-plain-unauthorized"]
	manager.mu.RUnlock()
	if current == nil {
		t.Fatalf("auth missing after plain unauthorized result")
	}
	if current.Disabled || current.Status == StatusDisabled {
		t.Fatalf("Disabled = %v Status = %v, want plain 401 to keep the credential enabled", current.Disabled, current.Status)
	}
	stored := loadStoredRevokedTestAuth(t, store, "relay-plain-unauthorized")
	if stored.Disabled || stored.Status == StatusDisabled {
		t.Fatalf("stored auth disabled by a plain 401")
	}
}

func TestMarkResult_TokenRevoked401NonCodexProviderNotDisabled(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(ctx, &Auth{
		ID: "claude-token-revoked", Provider: "claude", Status: StatusActive,
		Metadata: map[string]any{"access_token": "revoked-access-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID: "claude-token-revoked", Provider: "claude", Model: "claude-sonnet", RouteModel: "claude-sonnet",
		Success: false,
		Error:   &Error{Code: "relay_upstream_status", HTTPStatus: http.StatusUnauthorized, Message: "upstream: token_revoked: invalidated oauth token"},
	})

	manager.mu.RLock()
	current := manager.auths["claude-token-revoked"]
	manager.mu.RUnlock()
	if current == nil {
		t.Fatalf("auth missing after marked result")
	}
	if current.Disabled || current.Status == StatusDisabled {
		t.Fatalf("Disabled = %v Status = %v, want non-codex revoked 401 to keep the credential enabled", current.Disabled, current.Status)
	}
}
