package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type relayRefreshExecutor struct {
	countingRefreshExecutor
}

func (*relayRefreshExecutor) PrepareRequest(req *http.Request, auth *Auth) error {
	token := auth.Attributes[AttributeAPIKey]
	if token == "" {
		token = authAccessToken(auth)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

type relayRoundTripFunc func(*http.Request) (*http.Response, error)

func (f relayRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type relayTestRoundTripper struct {
	attempts int
}

func (r *relayTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.attempts++
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ambiguous")),
		Request:    req,
	}, errors.New("ambiguous transport outcome after POST body was sent")
}

type relayTestRoundTripperProvider struct {
	transport *relayTestRoundTripper
}

func (p *relayTestRoundTripperProvider) RoundTripperFor(*Auth) http.RoundTripper {
	return p.transport
}

func TestExecuteRelayRequestPlusOnlyUsesEligibleCodexAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: "relay-non-plus", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{"plan_type": "pro", AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "synthetic-non-plus-token"},
	}); errRegister != nil {
		t.Fatalf("register non-Plus Codex auth: %v", errRegister)
	}

	_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodGet,
		RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{PlusPreference: "only", RotationMode: "round-robin"})
	if errExecute == nil || errExecute.Error() != "no_plus_codex_auth_available" {
		t.Fatalf("ExecuteRelayRequest() error = %v, want no_plus_codex_auth_available", errExecute)
	}
}

func TestExecuteRelayRequestDoesNotReplaySentPost(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	transport := &relayTestRoundTripper{}
	manager.SetRoundTripperProvider(&relayTestRoundTripperProvider{transport: transport})
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: "relay-plus", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{"plan_type": "plus", AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "synthetic-plus-token"},
	}); errRegister != nil {
		t.Fatalf("register Plus Codex auth: %v", errRegister)
	}

	_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodPost,
		RelativePath: "/responses", Body: []byte(`{"model":"gpt-5.4"}`), ReplaySafe: false,
	}, RelayRequestPolicy{RotationMode: "round-robin", MaxRetries: 2})
	if errExecute == nil {
		t.Fatal("ExecuteRelayRequest() error = nil, want ambiguous transport failure")
	}
	if transport.attempts != 1 {
		t.Fatalf("transport attempts = %d, want 1 after sent POST body", transport.attempts)
	}
}

type relayCaptureRoundTripper struct {
	request *http.Request
	body    []byte
}

func (r *relayCaptureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.request = req.Clone(req.Context())
	r.request.Header = req.Header.Clone()
	r.body, _ = io.ReadAll(req.Body)
	token := req.Header.Get("Authorization")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Upstream-Echo": []string{token}, "Set-Cookie": []string{token}},
		Body:       io.NopCloser(strings.NewReader("echo " + token)),
		Request:    req,
	}, nil
}

func TestExecuteRelayRequestValidatesTargetAndSanitizesHeadersAndResponse(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	transport := &relayCaptureRoundTripper{}
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: transport})
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: "relay-plus", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{"plan_type": "plus", AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "relay-secret-token"},
	}); errRegister != nil {
		t.Fatalf("register Plus Codex auth: %v", errRegister)
	}

	response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/configured/v1/", Model: "gpt-5.4", Method: http.MethodPost,
		RelativePath: "/responses", Headers: http.Header{
			"Authorization": []string{"Bearer caller-token"},
			"Content-Type":  []string{"application/json"},
			"Cookie":        []string{"caller-cookie"},
			"X-Private":     []string{"not-forwarded"},
		}, Body: []byte(`{"model":"gpt-5.4"}`),
	}, RelayRequestPolicy{})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}
	if transport.request.URL.String() != "https://relay.example/configured/v1/responses" {
		t.Fatalf("request URL = %q, want configured base path joined with relative path", transport.request.URL.String())
	}
	if got := transport.request.Header.Get("Authorization"); got != "Bearer relay-secret-token" {
		t.Fatalf("upstream Authorization = %q, want manager-injected token", got)
	}
	if transport.request.Header.Get("Cookie") != "" || transport.request.Header.Get("X-Private") != "" {
		t.Fatalf("unsafe caller headers forwarded: %v", transport.request.Header)
	}
	if string(transport.body) != `{"model":"gpt-5.4"}` {
		t.Fatalf("request body = %q", transport.body)
	}
	if strings.Contains(string(response.Body), "relay-secret-token") || strings.Contains(response.Headers.Get("X-Upstream-Echo"), "relay-secret-token") {
		t.Fatalf("response leaked selected token: body=%q headers=%v", response.Body, response.Headers)
	}
	if response.Headers.Get("Set-Cookie") != "" {
		t.Fatalf("response forwarded Set-Cookie: %v", response.Headers)
	}
}

type relayCaptureRoundTripperProvider struct {
	transport http.RoundTripper
}

func (p *relayCaptureRoundTripperProvider) RoundTripperFor(*Auth) http.RoundTripper {
	return p.transport
}

func TestExecuteRelayRequestRejectsUnsafeTargetsAndPathsBeforeTransport(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	transport := &relayTestRoundTripper{}
	manager.SetRoundTripperProvider(&relayTestRoundTripperProvider{transport: transport})
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: "relay-plus", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{"plan_type": "plus", AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "synthetic-plus-token"},
	}); errRegister != nil {
		t.Fatalf("register Plus Codex auth: %v", errRegister)
	}
	for _, tc := range []struct {
		name, target, method, path string
	}{
		{"http", "http://relay.example/v1", http.MethodGet, "/models"},
		{"userinfo", "https://user:pass@relay.example/v1", http.MethodGet, "/models"},
		{"query", "https://relay.example/v1?override=1", http.MethodGet, "/models"},
		{"fragment", "https://relay.example/v1#fragment", http.MethodGet, "/models"},
		{"traversal", "https://relay.example/v1", http.MethodGet, "/../admin"},
		{"query-path", "https://relay.example/v1", http.MethodGet, "/models?url=bad"},
		{"method", "https://relay.example/v1", http.MethodPost, "/models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
				TargetBaseURL: tc.target, Model: "gpt-5.4", Method: tc.method, RelativePath: tc.path,
			}, RelayRequestPolicy{})
			if errExecute == nil {
				t.Fatal("ExecuteRelayRequest() error = nil, want rejected target or path")
			}
		})
	}
	if transport.attempts != 0 {
		t.Fatalf("transport attempts = %d, want no request for invalid targets", transport.attempts)
	}
	_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodPost,
		RelativePath: "/responses", ReplaySafe: true,
	}, RelayRequestPolicy{})
	if errExecute == nil {
		t.Fatal("ExecuteRelayRequest() error = nil, want rejection of unsafe replay flag")
	}
}

func TestExecuteRelayRequest401RefreshRetry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		wantSends     int
		wantRefreshes int32
		wantStatus    int
		wantOutcome   string
	}{
		{"401 refresh", http.StatusUnauthorized, 2, 1, http.StatusOK, "retried"},
		{"403 no refresh or fallback", http.StatusForbidden, 1, 0, http.StatusForbidden, relayRetryOutcomeNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			executor := &countingRefreshExecutor{id: "codex"}
			manager.RegisterExecutor(executor)
			for _, id := range []string{"relay-refresh-a", "relay-refresh-b"} {
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID: id, Provider: "codex", Status: StatusActive,
					Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
					Metadata:   map[string]any{"access_token": "old-access-token", "refresh_token": "refresh-token"},
				}); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}
			}
			attempts := 0
			transport := relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				status := http.StatusOK
				if attempts == 1 {
					status = tc.status
				}
				if attempts == 2 && req.Header.Get("Authorization") != "Bearer refreshed-token" {
					t.Error("retry did not use refreshed credentials")
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: transport})
			response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
				TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet,
				RelativePath: "/models", ReplaySafe: true,
			}, RelayRequestPolicy{Retry401: true, MaxRetries: 1})
			if attempts != tc.wantSends {
				t.Errorf("transport attempts = %d, want %d", attempts, tc.wantSends)
			}
			if got := executor.refreshCalls.Load(); got != tc.wantRefreshes {
				t.Errorf("refresh calls = %d, want %d", got, tc.wantRefreshes)
			}
			if errExecute != nil {
				t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
			}
			if response.StatusCode != tc.wantStatus || response.RetryOutcome != tc.wantOutcome || string(response.Body) != "ok" {
				t.Errorf("status = %d, outcome = %q, want %d and %q with original body", response.StatusCode, response.RetryOutcome, tc.wantStatus, tc.wantOutcome)
			}
		})
	}
}

func TestExecuteRelayRequest401DoesNotReplayPost(t *testing.T) {
	for _, consumer := range []string{"request", "stream"} {
		t.Run(consumer, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			executor := &countingRefreshExecutor{id: "codex"}
			manager.RegisterExecutor(executor)
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID: "relay-post-unauthorized", Provider: "codex", Status: StatusActive,
				Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
				Metadata:   map[string]any{"access_token": "old-access-token", "refresh_token": "refresh-token"},
			}); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			attempts := 0
			transport := relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
			})
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: transport})
			request := RelayRequest{
				TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodPost,
				RelativePath: "/responses", Body: []byte(`{"model":"gpt-5.4"}`), ReplaySafe: false,
			}
			policy := RelayRequestPolicy{Retry401: true, MaxRetries: 2}
			var response RelayResponse
			var errExecute error
			if consumer == "stream" {
				var body io.ReadCloser
				response, body, errExecute = manager.ExecuteRelayStream(context.Background(), request, policy)
				if body != nil {
					body.Close()
					t.Error("unauthorized response returned a stream body")
				}
			} else {
				response, errExecute = manager.ExecuteRelayRequest(context.Background(), request, policy)
			}
			if attempts != 1 {
				t.Errorf("transport attempts = %d, want exactly one POST", attempts)
			}
			if got := executor.refreshCalls.Load(); got != 0 {
				t.Errorf("refresh calls = %d, want 0", got)
			}
			if errExecute != nil {
				t.Fatalf("ExecuteRelay%s() error = %v", consumer, errExecute)
			}
			if response.StatusCode != http.StatusUnauthorized || string(response.Body) != "unauthorized" || !response.BodySent || response.RetryOutcome != relayRetryOutcomeNone {
				t.Errorf("response = %#v, want original unauthorized response without replay", response)
			}
		})
	}
}

func TestExecuteRelayRequestRedactsFinalInjectedCredential(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&relayRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}})
	transport := relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer injected-api-key" {
			t.Errorf("Authorization = %q, want preparer-injected credential", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"X-Request-Id": []string{"injected-api-key", req.Header.Get("Authorization")}},
			Body:       io.NopCloser(strings.NewReader("echo injected-api-key " + req.Header.Get("Authorization"))),
		}, nil
	})
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: transport})
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: "relay-redaction", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth, AttributeAPIKey: "injected-api-key"},
		Metadata:   map[string]any{"access_token": "metadata-access-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}
	if string(response.Body) != "echo [REDACTED] [REDACTED]" {
		t.Errorf("response body = %q, want injected credential redacted", response.Body)
	}
	for _, value := range response.Headers.Values("X-Request-Id") {
		if strings.Contains(value, "injected-api-key") {
			t.Errorf("response header leaked injected credential: %q", value)
		}
	}
	if len(response.Headers.Values("X-Request-Id")) != 2 {
		t.Errorf("response headers = %v, want allowed headers retained", response.Headers)
	}
}

func TestExecuteRelayRequestUsesValidatedTrimmedTarget(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	transport := relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != "https://relay.example/v1/models" {
			t.Errorf("request URL = %q, want normalized target", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("models"))}, nil
	})
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: transport})
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: "relay-target", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "target-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: " https://relay.example/v1\r\n", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v, want trimmed target to execute", errExecute)
	}
}

type relayCountingRefreshExecutor struct {
	countingRefreshExecutor
}

func (e *relayCountingRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-" + auth.ID
	auth.Metadata["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

type relayDowngradingExecutor struct {
	relayCountingRefreshExecutor
	phase string
}

func (e *relayDowngradingExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	updated, err := e.relayCountingRefreshExecutor.Refresh(ctx, auth)
	if e.phase == "refresh" {
		updated.Attributes["plan_type"] = "free"
	}
	return updated, err
}

func (*relayDowngradingExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *relayDowngradingExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	if e.phase == "prepare" || e.phase == "retry prepare" && e.refreshCalls.Load() > 0 {
		auth.Attributes["plan_type"] = "free"
	}
	return auth, nil
}

func TestExecuteRelayRequestPlusOnlyRejectsDowngradedAuth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		phase     string
		expired   bool
		wantSends int
	}{
		{"before send refresh", "refresh", true, 0},
		{"before send preparation", "prepare", false, 0},
		{"401 refresh", "refresh", false, 1},
		{"401 preparation", "retry prepare", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			executor := &relayDowngradingExecutor{relayCountingRefreshExecutor: relayCountingRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}}, phase: tc.phase}
			manager.RegisterExecutor(executor)
			auth := relayOAuthAuth("relay-downgraded-plus", "plus-token")
			auth.Attributes["plan_type"] = "plus"
			auth.Metadata["refresh_token"] = "plus-refresh"
			if tc.expired {
				auth.Metadata["expires_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
			}
			registerRelayTestAuths(t, manager, auth)
			attempts := 0
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts++
				return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
			})})
			response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
				TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
			}, RelayRequestPolicy{PlusPreference: "only", Retry401: true, MaxRetries: 1})
			if attempts != tc.wantSends {
				t.Errorf("sends = %d, want %d; downgraded auth must not be sent", attempts, tc.wantSends)
			}
			if tc.wantSends == 0 && (errExecute == nil || errExecute.Error() != "no_plus_codex_auth_available") {
				t.Errorf("error = %v, want no_plus_codex_auth_available", errExecute)
			}
			if tc.wantSends == 1 && response.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want original 401", response.StatusCode)
			}
		})
	}
}

func TestExecuteRelayRequestOAuthAliasUsesRawModelCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, nil, nil)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "native-model", Alias: "relay-alias", Fork: true}},
	})
	registerRelayTestAuths(t, manager, relayOAuthAuth("relay-alias-auth", "alias-token"))
	attempts := 0
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("rate limited"))}, nil
	})})
	request := RelayRequest{TargetBaseURL: "https://relay.example/v1", Model: "relay-alias", Method: http.MethodPost, RelativePath: "/responses"}
	response, errExecute := manager.ExecuteRelayRequest(context.Background(), request, RelayRequestPolicy{})
	if errExecute != nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("initial response status = %d, error = %v, want 429", response.StatusCode, errExecute)
	}
	status := manager.RelayStatus("relay-alias")
	if status.Eligible != 0 || status.Cooling != 1 {
		t.Errorf("pool status = %+v, want raw alias model cooling", status)
	}
	_, errExecute = manager.ExecuteRelayRequest(context.Background(), request, RelayRequestPolicy{})
	if errExecute == nil || attempts != 1 {
		t.Errorf("second request error = %v, total sends = %d, want cooled alias rejected without send", errExecute, attempts)
	}
}

func TestExecuteRelayRequestRetryRejectsChangedLiveCandidate(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, change := range []string{"disable", "remove", "cooldown", "replace"} {
		t.Run(change, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			registerRelayTestAuths(t, manager, relayOAuthAuth("live-a", "first-token"), relayOAuthAuth("live-b", "second-token"))
			entered, release := make(chan struct{}), make(chan struct{})
			attempts := 0
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					close(entered)
					<-release
				}
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("rate limited"))}, nil
			})})
			finished := make(chan struct{})
			var response RelayResponse
			var errExecute error
			go func() {
				defer close(finished)
				response, errExecute = manager.ExecuteRelayRequest(context.Background(), RelayRequest{
					TargetBaseURL: "https://relay.example/v1", Model: "raw-model", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
				}, RelayRequestPolicy{Retry429: true, MaxRetries: 1})
			}()
			<-entered
			second, _ := manager.GetByID("live-b")
			switch change {
			case "disable":
				second.Disabled = true
				if _, err := manager.Update(context.Background(), second); err != nil {
					close(release)
					<-finished
					t.Fatal(err)
				}
			case "remove":
				manager.Remove(context.Background(), second.ID)
			case "replace":
				manager.Remove(context.Background(), second.ID)
				registerRelayTestAuths(t, manager, relayOAuthAuth(second.ID, "replacement-token"))
			case "cooldown":
				manager.MarkResult(context.Background(), relayResult(second, "raw-model", relayStatusError(http.StatusTooManyRequests)))
			}
			close(release)
			<-finished
			if attempts != 1 || errExecute != nil || response.StatusCode != http.StatusTooManyRequests || response.RetryOutcome != relayRetryOutcomeNoCandidate {
				t.Errorf("sends = %d, status = %d, outcome = %q, error = %v; want no send to changed candidate and prior 429", attempts, response.StatusCode, response.RetryOutcome, errExecute)
			}
		})
	}
}

type relayFailingRefreshExecutor struct {
	countingRefreshExecutor
}

func (e *relayFailingRefreshExecutor) Refresh(_ context.Context, _ *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	return nil, errors.New("refresh unavailable")
}

type relayCustomHeaderExecutor struct {
	countingRefreshExecutor
}

func (*relayCustomHeaderExecutor) PrepareRequest(req *http.Request, auth *Auth) error {
	req.Header.Set("X-Api-Key", auth.Attributes["header:X-Api-Key"])
	return nil
}

func registerRelayTestAuths(t *testing.T, manager *Manager, auths ...*Auth) {
	t.Helper()
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register relay auth %q: %v", auth.ID, errRegister)
		}
	}
}

func relayOAuthAuth(id, token string) *Auth {
	return &Auth{
		ID: id, Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Metadata:   map[string]any{"access_token": token},
	}
}

type relayFailingRequestAuthPreparer struct {
	countingRefreshExecutor
	failID string
}

func (*relayFailingRequestAuthPreparer) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *relayFailingRequestAuthPreparer) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	if e.failID == "" || strings.HasPrefix(auth.ID, e.failID) {
		return nil, errors.New("request auth preparation failed")
	}
	return auth, nil
}

type relayFailingRequestPreparer struct {
	countingRefreshExecutor
}

func (*relayFailingRequestPreparer) PrepareRequest(req *http.Request, auth *Auth) error {
	if strings.HasSuffix(auth.ID, "-a") || strings.HasSuffix(auth.ID, "-b") {
		return errors.New("raw request preparation failed")
	}
	req.Header.Set("Authorization", "Bearer "+authAccessToken(auth))
	return nil
}

type relayRetryPreparationExecutor struct {
	countingRefreshExecutor
}

func (*relayRetryPreparationExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (*relayRetryPreparationExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if authAccessToken(auth) == "refreshed-token" {
		auth.Metadata["access_token"] = "prepared-refreshed-token"
	} else {
		auth.Metadata["access_token"] = "prepared-initial-token"
	}
	return auth, nil
}

func (*relayRetryPreparationExecutor) PrepareRequest(req *http.Request, auth *Auth) error {
	req.Header.Set("Authorization", "Bearer "+authAccessToken(auth))
	return nil
}

func TestExecuteRelayRequestPreSendFailuresDoNotConsumeSendBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		executor ProviderExecutor
		expired  bool
	}{
		{name: "refresh", executor: &relayFailingRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}}, expired: true},
		{name: "auth preparation", executor: &relayFailingRequestAuthPreparer{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}, failID: "relay-presend-budget-failed"}},
		{name: "request preparation", executor: &relayFailingRequestPreparer{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(tc.executor)
			var tokens []string
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				tokens = append(tokens, req.Header.Get("Authorization"))
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})})
			first := relayOAuthAuth("relay-presend-budget-a", "token-a")
			second := relayOAuthAuth("relay-presend-budget-b", "token-b")
			third := relayOAuthAuth("relay-presend-budget-c", "token-c")
			for _, auth := range []*Auth{first, second, third} {
				auth.Attributes["plan_type"] = "plus"
			}
			if tc.expired {
				for _, auth := range []*Auth{first, second} {
					auth.Metadata["refresh_token"] = "refresh-" + auth.ID
					auth.Metadata["expires_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
				}
			}
			if preparer, ok := tc.executor.(*relayFailingRequestAuthPreparer); ok {
				first.ID = preparer.failID + "-a"
				second.ID = preparer.failID + "-b"
				third.ID = "relay-presend-budget-usable"
			}
			registerRelayTestAuths(t, manager, first, second, third)

			response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
				TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
			}, RelayRequestPolicy{PlusPreference: "only", RotationMode: "round-robin", MaxRetries: 0})
			if errExecute != nil {
				t.Fatalf("ExecuteRelayRequest() error = %v, want successful send after pre-send failures", errExecute)
			}
			if response.StatusCode != http.StatusOK || response.RetryOutcome != relayRetryOutcomeNone {
				t.Fatalf("response = %#v, want successful first outbound send", response)
			}
			if !reflect.DeepEqual(tokens, []string{"Bearer token-c"}) {
				t.Fatalf("transport tokens = %v, want only the third usable candidate", tokens)
			}
		})
	}
}

func TestExecuteRelayRequestPreparesRefreshedAuthBefore401Replay(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&relayRetryPreparationExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}})
	var tokens []string
	attempts := 0
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		tokens = append(tokens, req.Header.Get("Authorization"))
		status := http.StatusOK
		if attempts == 1 {
			status = http.StatusUnauthorized
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})})
	auth := relayOAuthAuth("relay-prepared-retry", "initial-token")
	auth.Metadata["refresh_token"] = "refresh-token"
	registerRelayTestAuths(t, manager, auth)

	_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{Retry401: true, MaxRetries: 1})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}
	if len(tokens) != 2 || tokens[0] != "Bearer prepared-initial-token" {
		t.Fatalf("relay did not use request-prepared credentials on initial send")
	}
	if tokens[1] != "Bearer prepared-refreshed-token" {
		t.Fatalf("relay did not prepare refreshed credentials before 401 replay")
	}
}

func TestExecuteRelayRequestMaxRetriesCoversUnauthorizedRefreshReplay(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	executor := &relayCountingRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}}
	manager.RegisterExecutor(executor)

	var tokens []string
	attempts := 0
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		tokens = append(tokens, req.Header.Get("Authorization"))
		status := http.StatusUnauthorized
		if attempts >= 3 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})})
	first := relayOAuthAuth("relay-budget-a", "old-budget-a")
	first.Metadata["refresh_token"] = "refresh-budget-a"
	registerRelayTestAuths(t, manager, first, relayOAuthAuth("relay-budget-b", "budget-b"))

	response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{RotationMode: "round-robin", Retry401: true, MaxRetries: 1})
	if errExecute != nil || response.StatusCode != http.StatusUnauthorized || response.RetryOutcome != relayRetryOutcomeExhausted {
		t.Fatalf("response status = %d, outcome = %q, error = %v, want retained 401 and exhausted budget", response.StatusCode, response.RetryOutcome, errExecute)
	}
	if attempts != 2 {
		t.Fatalf("transport attempts = %d, want initial 401 plus one refreshed retry; distinct fallback exceeded MaxRetries", attempts)
	}
	if len(tokens) != 2 || tokens[1] != "Bearer refreshed-relay-budget-a" {
		t.Fatalf("authorization sequence = %v, want initial token followed by refreshed same-auth retry", tokens)
	}
}

func TestExecuteRelayRequestReplayMarkersDependOnReplaySafety(t *testing.T) {
	t.Run("nonreplayable POST strips HTTP replay markers", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		var captured *http.Request
		manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req.Clone(req.Context())
			captured.Header = req.Header.Clone()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
		})})
		registerRelayTestAuths(t, manager, relayOAuthAuth("relay-post-markers", "post-markers-token"))

		_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
			TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodPost,
			RelativePath: "/responses", Headers: http.Header{"Idempotency-Key": []string{"client-key"}},
			Body: []byte(`{"model":"gpt-5.4"}`), ReplaySafe: false,
		}, RelayRequestPolicy{})
		if errExecute != nil {
			t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
		}
		if captured == nil {
			t.Fatal("transport did not receive request")
		}
		if captured.GetBody != nil {
			t.Fatal("nonreplayable POST exposed GetBody, allowing net/http automatic replay")
		}
		if got := captured.Header.Get("Idempotency-Key"); got != "" {
			t.Fatalf("nonreplayable POST Idempotency-Key = %q, want stripped replay marker", got)
		}
	})

	t.Run("replay-safe GET preserves Idempotency-Key", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		var captured *http.Request
		manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req.Clone(req.Context())
			captured.Header = req.Header.Clone()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
		})})
		registerRelayTestAuths(t, manager, relayOAuthAuth("relay-get-marker", "get-marker-token"))

		_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
			TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
			Headers: http.Header{"Idempotency-Key": []string{"get-key"}},
		}, RelayRequestPolicy{})
		if errExecute != nil {
			t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
		}
		if captured == nil {
			t.Fatal("transport did not receive request")
		}
		if got := captured.Header.Get("Idempotency-Key"); got != "get-key" {
			t.Fatalf("replay-safe GET Idempotency-Key = %q, want caller header preserved", got)
		}
	})
}

func TestExecuteRelayRequestPlusOnlyExhaustionReturnsStableError(t *testing.T) {
	tests := []struct {
		name     string
		executor ProviderExecutor
		prepare  func(*Auth)
	}{
		{
			name:     "refresh failure",
			executor: &relayFailingRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}},
			prepare: func(auth *Auth) {
				auth.Metadata["access_token"] = "expired-plus-token"
				auth.Metadata["refresh_token"] = "plus-refresh-token"
				auth.Metadata["expires_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
			},
		},
		{
			name:     "request auth preparation failure",
			executor: &relayFailingRequestAuthPreparer{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}},
			prepare:  func(*Auth) {},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(tc.executor)
			transportCalls := 0
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
				transportCalls++
				return nil, errors.New("unexpected transport call after Plus auth exhaustion")
			})})
			auth := relayOAuthAuth("relay-plus-exhausted-"+strings.ReplaceAll(tc.name, " ", "-"), "plus-token")
			auth.Attributes["plan_type"] = "plus"
			tc.prepare(auth)
			registerRelayTestAuths(t, manager, auth)

			_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
				TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
			}, RelayRequestPolicy{PlusPreference: "only", MaxRetries: 0})
			if errExecute == nil || errExecute.Error() != "no_plus_codex_auth_available" {
				t.Fatalf("ExecuteRelayRequest() error = %v, want stable no_plus_codex_auth_available", errExecute)
			}
			if transportCalls != 0 {
				t.Fatalf("transport calls = %d, want no fallback transport after Plus pool exhaustion", transportCalls)
			}
		})
	}
}
func TestExecuteRelayRequestPlusOnlyRetryExhaustionPreservesOutcome(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	attempts := 0
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("rate limited")),
		}, nil
	})})
	plus := relayOAuthAuth("relay-plus-retry-exhausted", "plus-retry-token")
	plus.Attributes["plan_type"] = "plus"
	registerRelayTestAuths(t, manager, plus)

	response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{PlusPreference: "only", Retry429: true, MaxRetries: 1})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v, want retained upstream response", errExecute)
	}
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusTooManyRequests)
	}
	if response.RetryOutcome != relayRetryOutcomeNoCandidate {
		t.Fatalf("response retry outcome = %q, want %q", response.RetryOutcome, relayRetryOutcomeNoCandidate)
	}
	if attempts != 1 {
		t.Fatalf("transport attempts = %d, want 1 after distinct-auth exhaustion", attempts)
	}
}

func TestExecuteRelayRequestPlusOnlyRetryFailurePreservesPriorResponse(t *testing.T) {
	tests := []struct {
		name     string
		executor ProviderExecutor
		prepare  func(*Auth, *Auth)
	}{
		{
			name:     "refresh failure",
			executor: &relayFailingRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}},
			prepare: func(_, second *Auth) {
				second.Metadata["access_token"] = "expired-plus-token"
				second.Metadata["refresh_token"] = "plus-refresh-token"
				second.Metadata["expires_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
			},
		},
		{
			name: "request auth preparation failure",
			executor: &relayFailingRequestAuthPreparer{
				countingRefreshExecutor: countingRefreshExecutor{id: "codex"},
				failID:                  "relay-plus-retry-failure-b",
			},
			prepare: func(*Auth, *Auth) {},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(tc.executor)
			transportCalls := 0
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
				transportCalls++
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("rate limited")),
				}, nil
			})})
			first := relayOAuthAuth("relay-plus-retry-failure-a", "plus-first-token")
			second := relayOAuthAuth("relay-plus-retry-failure-b", "plus-second-token")
			first.Attributes["plan_type"] = "plus"
			second.Attributes["plan_type"] = "plus"
			tc.prepare(first, second)
			registerRelayTestAuths(t, manager, first, second)

			response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
				TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
			}, RelayRequestPolicy{PlusPreference: "only", Retry429: true, MaxRetries: 1})
			if errExecute != nil {
				t.Fatalf("ExecuteRelayRequest() error = %v, want retained prior upstream response", errExecute)
			}
			if response.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("response status = %d, want %d from prior upstream response", response.StatusCode, http.StatusTooManyRequests)
			}
			if response.RetryOutcome != relayRetryOutcomeNoCandidate {
				t.Fatalf("response retry outcome = %q, want %q after Plus retry candidate failure", response.RetryOutcome, relayRetryOutcomeNoCandidate)
			}
			if transportCalls != 1 {
				t.Fatalf("transport calls = %d, want only the initial upstream response", transportCalls)
			}
		})
	}
}

func TestPickRelayAuthRoundRobinTracksSelectedIdentityAcrossSubsets(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	policy := RelayRequestPolicy{RotationMode: "round-robin", PlusPreference: "only"}
	candidates := []*Auth{
		relayOAuthAuth("relay-round-robin-a", "round-robin-a-token"),
		relayOAuthAuth("relay-round-robin-b", "round-robin-b-token"),
		relayOAuthAuth("relay-round-robin-c", "round-robin-c-token"),
	}
	for _, auth := range candidates {
		auth.Attributes["plan_type"] = "plus"
	}
	registerRelayTestAuths(t, manager, candidates...)
	for i, candidate := range candidates {
		candidates[i], _ = manager.GetByID(candidate.ID)
	}

	first, errPick := manager.pickRelayAuth("gpt-5.4", policy, candidates, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("first pick error = %v", errPick)
	}
	if first.ID != "relay-round-robin-a" {
		t.Fatalf("first pick = %q, want relay-round-robin-a", first.ID)
	}

	second, errPick := manager.pickRelayAuth("gpt-5.4", policy, candidates, map[string]struct{}{first.ID: {}})
	if errPick != nil {
		t.Fatalf("subset pick error = %v", errPick)
	}
	if second.ID != "relay-round-robin-b" {
		t.Fatalf("subset pick = %q, want relay-round-robin-b after %q", second.ID, first.ID)
	}

	next, errPick := manager.pickRelayAuth("gpt-5.4", policy, candidates, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("next full pick error = %v", errPick)
	}
	if next.ID != "relay-round-robin-c" {
		t.Fatalf("next full pick = %q, want relay-round-robin-c after selected identities %q then %q", next.ID, first.ID, second.ID)
	}
}

func TestExecuteRelayRequestAcceptsAllRotationModes(t *testing.T) {
	for _, mode := range []string{"round-robin", "sticky", "fill-first", "least-recent"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			var gotToken string
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				gotToken = req.Header.Get("Authorization")
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})})
			registerRelayTestAuths(t, manager, relayOAuthAuth("relay-mode-a", "mode-a-token"), relayOAuthAuth("relay-mode-b", "mode-b-token"))
			_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
				TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
			}, RelayRequestPolicy{RotationMode: mode, AffinityKey: "mode-test"})
			if errExecute != nil {
				t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
			}
			if gotToken == "" {
				t.Fatal("relay request did not select an auth")
			}
		})
	}
}
func TestExecuteRelayRequestStickyAffinityPersistsSelectedAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	var tokens []string
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		tokens = append(tokens, req.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})})
	registerRelayTestAuths(t, manager,
		relayOAuthAuth("relay-sticky-a", "sticky-a-token"),
		relayOAuthAuth("relay-sticky-b", "sticky-b-token"),
	)
	request := RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", Model: "gpt-5.4", ReplaySafe: true,
	}
	policy := RelayRequestPolicy{RotationMode: "sticky", AffinityKey: "same-client"}
	for range 2 {
		if _, errExecute := manager.ExecuteRelayRequest(context.Background(), request, policy); errExecute != nil {
			t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
		}
	}
	if len(tokens) != 2 {
		t.Fatalf("transport tokens = %v, want two requests", tokens)
	}
	if tokens[0] != tokens[1] {
		t.Fatalf("sticky auth bearers = %v, want the same bearer across requests", tokens)
	}
}

func TestExecuteRelayRequestPlusPreferenceFallsBackToNonPlus(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	var attempts int
	var tokens []string
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		tokens = append(tokens, req.Header.Get("Authorization"))
		status := http.StatusOK
		if attempts == 1 {
			status = http.StatusTooManyRequests
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})})
	plus := relayOAuthAuth("relay-plus-preferred", "plus-preferred-token")
	plus.Attributes["plan_type"] = "plus"
	pro := relayOAuthAuth("relay-pro-fallback", "pro-fallback-token")
	pro.Attributes["plan_type"] = "pro"
	registerRelayTestAuths(t, manager, plus, pro)
	response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{RotationMode: "round-robin", PlusPreference: "prefer", Retry429: true, MaxRetries: 1})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}
	if attempts != 2 || len(tokens) != 2 || tokens[0] != "Bearer plus-preferred-token" || tokens[1] != "Bearer pro-fallback-token" {
		t.Fatalf("attempts=%d tokens=%v, want Plus then non-Plus fallback", attempts, tokens)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want 200", response.StatusCode)
	}
}

func TestPickRelayAuthStickyAffinityInvalidatesUnavailableOrCoolingAuth(t *testing.T) {
	model := "gpt-5.4"
	for _, tc := range []struct {
		name string
		auth *Auth
	}{
		{name: "unavailable", auth: &Auth{ID: "relay-sticky-a", Provider: "codex", Status: StatusActive, Unavailable: true, Metadata: map[string]any{"access_token": "sticky-a"}}},
		{name: "cooling", auth: &Auth{ID: "relay-sticky-a", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "sticky-a"}, ModelStates: map[string]*ModelState{
			model: {Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			key := "sticky-invalidation-" + tc.name
			relayAffinitySet(manager, model, key, tc.auth.ID)
			tc.auth.Attributes = map[string]string{AttributeAuthKind: AuthKindOAuth}
			registerRelayTestAuths(t, manager, tc.auth, relayOAuthAuth("relay-sticky-b", "sticky-b"))
			first, _ := manager.GetByID(tc.auth.ID)
			second, _ := manager.GetByID("relay-sticky-b")
			picked, errPick := manager.pickRelayAuth(model, RelayRequestPolicy{RotationMode: "sticky", AffinityKey: key}, []*Auth{first, second}, map[string]struct{}{})
			if errPick != nil {
				t.Fatalf("pickRelayAuth() error = %v", errPick)
			}
			if picked == nil || picked.ID != "relay-sticky-b" {
				gotID := "<nil>"
				if picked != nil {
					gotID = picked.ID
				}
				t.Fatalf("picked auth = %q, want fallback relay-sticky-b", gotID)
			}
		})
	}
}

func TestExecuteRelayRequestStickyAffinityDoesNotPersistFailedAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	executor := &relayFailingRequestAuthPreparer{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}, failID: "relay-sticky-failed"}
	manager.RegisterExecutor(executor)
	var tokens []string
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if len(tokens) == 0 {
			if got := relayAffinityGet(manager, "", "failed-auth-test"); got != "" {
				t.Fatalf("sticky affinity before first usable response = %q, want empty", got)
			}
		}
		tokens = append(tokens, req.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})})
	failed := relayOAuthAuth("relay-sticky-failed", "failed-token")
	registerRelayTestAuths(t, manager, failed)
	request := RelayRequest{TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true}
	policy := RelayRequestPolicy{RotationMode: "sticky", AffinityKey: "failed-auth-test", MaxRetries: 0}
	if _, errExecute := manager.ExecuteRelayRequest(context.Background(), request, policy); errExecute == nil {
		t.Fatal("first ExecuteRelayRequest() error = nil, want failed preparation")
	}
	if len(tokens) != 0 {
		t.Fatalf("transport tokens after failed preparation = %v, want none", tokens)
	}
	if got := relayAffinityGet(manager, "", policy.AffinityKey); got != "" {
		t.Fatalf("sticky affinity after failed preparation = %q, want empty", got)
	}
	registerRelayTestAuths(t, manager, relayOAuthAuth("relay-sticky-usable", "usable-token"))
	if _, errExecute := manager.ExecuteRelayRequest(context.Background(), request, policy); errExecute != nil {
		t.Fatalf("second ExecuteRelayRequest() error = %v, want usable replacement", errExecute)
	}
	if got := relayAffinityGet(manager, "", policy.AffinityKey); got != "relay-sticky-usable" {
		t.Fatalf("sticky affinity after successful replacement = %q, want usable auth", got)
	}
	if _, errExecute := manager.ExecuteRelayRequest(context.Background(), request, policy); errExecute != nil {
		t.Fatalf("third ExecuteRelayRequest() error = %v, want sticky replacement", errExecute)
	}
	want := []string{"Bearer usable-token", "Bearer usable-token"}
	if !reflect.DeepEqual(tokens, want) {
		t.Fatalf("transport tokens = %v, want %v", tokens, want)
	}
}

func TestExecuteRelayRequestRefreshesOnlySelectedAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	executor := &relayCountingRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}}
	manager.RegisterExecutor(executor)
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})})
	expired := time.Now().Add(-time.Minute).Format(time.RFC3339)
	first := relayOAuthAuth("relay-refresh-a", "stale-a")
	first.Metadata["refresh_token"] = "refresh-a"
	first.Metadata["expires_at"] = expired
	second := relayOAuthAuth("relay-refresh-b", "stale-b")
	second.Metadata["refresh_token"] = "refresh-b"
	second.Metadata["expires_at"] = expired
	registerRelayTestAuths(t, manager, first, second)
	_, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{RotationMode: "round-robin"})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want only selected auth refreshed", got)
	}
	updatedSecond, ok := manager.GetByID(second.ID)
	if !ok || authAccessToken(updatedSecond) != "stale-b" {
		t.Fatalf("unselected auth token = %q, want stale-b", authAccessToken(updatedSecond))
	}
}

func TestExecuteRelayRequestPostStatusDoesNotFallbackReplay(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	attempts := 0
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("error"))}, nil
	})})
	registerRelayTestAuths(t, manager, relayOAuthAuth("relay-post-a", "post-a"), relayOAuthAuth("relay-post-b", "post-b"))
	_, _ = manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Model: "gpt-5.4", Method: http.MethodPost,
		RelativePath: "/responses", Body: []byte(`{"model":"gpt-5.4"}`), ReplaySafe: false,
	}, RelayRequestPolicy{RotationMode: "round-robin", Retry429: true, MaxRetries: 2})
	if attempts != 1 {
		t.Fatalf("transport attempts = %d, want 1", attempts)
	}
}

func TestExecuteRelayRequestRetryOutcomeIsInitializedAndCarried(t *testing.T) {
	t.Run("not retried", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
		})})
		registerRelayTestAuths(t, manager, relayOAuthAuth("relay-outcome-single", "outcome-token"))
		response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
			TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
		}, RelayRequestPolicy{})
		if errExecute != nil || response.RetryOutcome != relayRetryOutcomeNone {
			t.Fatalf("response=%#v error=%v, want successful not_retried response", response, errExecute)
		}
	})

	t.Run("carried after retry", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		attempts := 0
		manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			status := http.StatusOK
			if attempts == 1 {
				status = http.StatusBadGateway
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
		})})
		registerRelayTestAuths(t, manager, relayOAuthAuth("relay-outcome-a", "outcome-a"), relayOAuthAuth("relay-outcome-b", "outcome-b"))
		response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
			TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
		}, RelayRequestPolicy{Retry5xx: true, MaxRetries: 1})
		if errExecute != nil || response.RetryOutcome != "retried" {
			t.Fatalf("response=%#v error=%v, want successful retried response", response, errExecute)
		}
	})
}

func TestExecuteRelayRequestRedactsConfiguredCustomHeader(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&relayCustomHeaderExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}})
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		secret := req.Header.Get("X-Api-Key")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"X-Request-Id": []string{secret}},
			Body:       io.NopCloser(strings.NewReader("echo " + secret)),
		}, nil
	})})
	auth := relayOAuthAuth("relay-custom-header", "custom-header-access")
	auth.Attributes["header:X-Api-Key"] = "custom-header-secret"
	registerRelayTestAuths(t, manager, auth)
	response, errExecute := manager.ExecuteRelayRequest(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{})
	if errExecute != nil {
		t.Fatalf("ExecuteRelayRequest() error = %v", errExecute)
	}
	if strings.Contains(string(response.Body), "custom-header-secret") || strings.Contains(response.Headers.Get("X-Request-Id"), "custom-header-secret") {
		t.Fatalf("custom header credential leaked: body=%q headers=%v", response.Body, response.Headers)
	}
}
