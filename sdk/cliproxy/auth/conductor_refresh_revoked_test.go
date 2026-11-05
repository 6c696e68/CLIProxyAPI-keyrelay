package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

const revokedRefreshTokenMessage = `{"error":"invalid_grant","code":"refresh_token_invalidated","message":"Your session has ended. Please log in again."}`

func revokedRefreshAuth(id string, status Status, lastError *Error) *Auth {
	now := time.Now()
	return &Auth{
		ID:        id,
		Provider:  "test-provider",
		Status:    status,
		LastError: lastError,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-revoked",
			"expires_at":    now.Add(-time.Hour).Format(time.RFC3339),
		},
	}
}

func assertRevokedRefreshDisabled(t *testing.T, auth *Auth) {
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
	if auth.StatusMessage != "disabled (refresh token invalidated)" {
		t.Fatalf("StatusMessage = %q, want %q", auth.StatusMessage, "disabled (refresh token invalidated)")
	}
	if auth.LastError == nil {
		t.Fatalf("LastError is nil")
	}
	if auth.LastError.Code != "refresh_token_invalidated" {
		t.Fatalf("LastError.Code = %q, want refresh_token_invalidated", auth.LastError.Code)
	}
	if auth.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("LastError.HTTPStatus = %d, want 401", auth.LastError.HTTPStatus)
	}
}

func loadStoredRevokedTestAuth(t *testing.T, store *memoryAuthTestStore, id string) *Auth {
	t.Helper()
	if store == nil {
		t.Fatalf("store is nil")
	}
	store.mu.Lock()
	stored := store.auths[id]
	store.mu.Unlock()
	if stored == nil {
		t.Fatalf("auth %s not persisted", id)
	}
	return stored.Clone()
}

func TestRefreshAuthForRequest_RevokedRefreshTokenDisablesAndPersistsAuth(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: oauthStatusError{code: http.StatusUnauthorized, msg: revokedRefreshTokenMessage},
	}
	manager.RegisterExecutor(executor)

	auth := revokedRefreshAuth("revoked-refresh-token", StatusActive, nil)
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register error: %v", errRegister)
	}

	if _, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, ""); errRefresh == nil {
		t.Fatalf("expected refresh error, got nil")
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	_, hasJob := manager.refreshJobs[auth.ID]
	manager.mu.RUnlock()

	assertRevokedRefreshDisabled(t, current)
	if hasJob {
		t.Fatalf("revoked auth still has a pending refresh job")
	}
	if manager.shouldRefresh(current, time.Now()) {
		t.Fatalf("revoked auth should not be scheduled for refresh")
	}

	assertRevokedRefreshDisabled(t, loadStoredRevokedTestAuth(t, store, auth.ID))
}

func TestForceRefreshAuth_RevokedRefreshTokenDisablesTerminalUnauthorizedAuth(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: oauthStatusError{code: http.StatusUnauthorized, msg: revokedRefreshTokenMessage},
	}
	manager.RegisterExecutor(executor)

	auth := revokedRefreshAuth("revoked-terminal-unauthorized", StatusError, &Error{
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

	assertRevokedRefreshDisabled(t, current)
	assertRevokedRefreshDisabled(t, loadStoredRevokedTestAuth(t, store, auth.ID))
}

func TestRefreshAuthForRequest_ReusedRefreshTokenIsNotRevoked(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: oauthStatusError{code: http.StatusBadRequest, msg: `{"error":"invalid_grant","code":"refresh_token_reused"}`},
	}
	manager.RegisterExecutor(executor)

	auth := revokedRefreshAuth("reused-refresh-token", StatusActive, nil)
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register error: %v", errRegister)
	}

	if _, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, ""); errRefresh == nil {
		t.Fatalf("expected refresh error, got nil")
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if current == nil {
		t.Fatalf("auth not found in manager")
	}
	if current.Disabled || current.Status == StatusDisabled {
		t.Fatalf("reused refresh token must not disable the auth (disabled=%v status=%v)", current.Disabled, current.Status)
	}
	if disabled, ok := current.Metadata["disabled"]; ok {
		t.Fatalf("Metadata[disabled] = %v, want absent", disabled)
	}
}

func TestRefreshAuthForRequest_PlainInvalidGrantIsNotRevoked(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: oauthStatusError{code: http.StatusBadRequest, msg: `{"error":"invalid_grant","error_description":"Bad Request"}`},
	}
	manager.RegisterExecutor(executor)

	auth := revokedRefreshAuth("plain-invalid-grant", StatusActive, nil)
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register error: %v", errRegister)
	}

	if _, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, ""); errRefresh == nil {
		t.Fatalf("expected refresh error, got nil")
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if current == nil {
		t.Fatalf("auth not found in manager")
	}
	if current.Disabled || current.Status == StatusDisabled {
		t.Fatalf("plain invalid_grant must not disable the auth (disabled=%v status=%v)", current.Disabled, current.Status)
	}
	if current.RefreshFailures != 1 {
		t.Fatalf("RefreshFailures = %d, want 1", current.RefreshFailures)
	}
	if current.NextRefreshAfter.IsZero() {
		t.Fatalf("plain invalid_grant should keep the retry schedule")
	}
}
