package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This interface keeps the RED tests runnable before the streaming seam exists.
// The implementation must keep credentials inside auth while returning a real reader.
type relayStreamingManager interface {
	ExecuteRelayStream(context.Context, RelayRequest, RelayRequestPolicy) (RelayResponse, io.ReadCloser, error)
	RelayStatus(string) RelayPoolStatus
}

func requireRelayStreaming(t *testing.T, manager *Manager) relayStreamingManager {
	t.Helper()
	streaming, ok := any(manager).(relayStreamingManager)
	if !ok {
		t.Fatal("auth Manager is missing the token-free relay streaming/status contract")
	}
	return streaming
}

func TestRelayStreamIncrementalRedactionAndEOF(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	streaming := requireRelayStreaming(t, manager)
	const token = "relay-stream-secret"
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("relay did not inject the selected credential")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: harmless\n\necho relay-stream-")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = fmt.Fprintf(w, "secret end\n\n")
	}))
	t.Cleanup(server.Close)
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: server.Client().Transport})
	registerRelayTestAuths(t, manager, relayOAuthAuth("private-stream-account", token))
	response, body, errOpen := streaming.ExecuteRelayStream(context.Background(), RelayRequest{
		TargetBaseURL: server.URL + "/v1", Model: "test-model", Method: http.MethodPost,
		RelativePath: "/responses", Body: []byte(`{"model":"test-model","stream":true}`),
	}, RelayRequestPolicy{})
	if errOpen != nil {
		t.Fatalf("stream open: %v", errOpen)
	}
	if response.StatusCode != http.StatusOK || body == nil {
		t.Fatalf("stream response = %#v, body = %v", response, body)
	}
	t.Cleanup(func() { _ = body.Close() })
	type readResult struct {
		data []byte
		err  error
	}
	first := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 4096)
		n, errRead := body.Read(buf)
		first <- readResult{data: buf[:n], err: errRead}
	}()
	var payload []byte
	select {
	case got := <-first:
		if got.err != nil || string(got.data) != "data: harmless\n\necho " {
			t.Fatalf("first incremental read = %q, error = %v; want safe prefix before next upstream write", got.data, got.err)
		}
		payload = append(payload, got.data...)
	case <-time.After(time.Second):
		t.Fatal("relay buffered the healthy stream instead of delivering its safe prefix")
	}
	close(release)
	remainder, errRead := io.ReadAll(body)
	if errRead != nil {
		t.Fatalf("stream remainder: %v", errRead)
	}
	payload = append(payload, remainder...)
	if string(payload) != "data: harmless\n\necho [REDACTED] end\n\n" {
		t.Fatalf("sanitized stream = %q, want split-token redaction and EOF flush", payload)
	}
	if errClose := body.Close(); errClose != nil {
		t.Fatalf("idempotent stream close: %v", errClose)
	}
}

func TestRelayStreamCancellationClosesUpstreamWithoutRead(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	streaming := requireRelayStreaming(t, manager)
	canceled := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(server.Close)
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: server.Client().Transport})
	registerRelayTestAuths(t, manager, relayOAuthAuth("cancel-stream-account", "cancel-stream-token"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, body, errOpen := streaming.ExecuteRelayStream(ctx, RelayRequest{
		TargetBaseURL: server.URL + "/v1", Model: "test-model", Method: http.MethodPost, RelativePath: "/responses",
	}, RelayRequestPolicy{})
	if errOpen != nil || body == nil {
		t.Fatalf("stream open body = %v, error = %v", body, errOpen)
	}
	t.Cleanup(func() { _ = body.Close() })
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("execution cancellation did not close an unread upstream stream")
	}
}

func TestRelayExecuteStreamRefreshReplaySharesSendBudget(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	streaming := requireRelayStreaming(t, manager)
	manager.RegisterExecutor(&relayCountingRefreshExecutor{countingRefreshExecutor: countingRefreshExecutor{id: "codex"}})
	attempts := 0
	manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
	})})
	first := relayOAuthAuth("stream-budget-a", "stream-old-token")
	first.Metadata["refresh_token"] = "stream-refresh-token"
	registerRelayTestAuths(t, manager, first, relayOAuthAuth("stream-budget-b", "stream-second-token"))
	response, body, errOpen := streaming.ExecuteRelayStream(context.Background(), RelayRequest{
		TargetBaseURL: "https://relay.example/v1", Method: http.MethodGet, RelativePath: "/models", ReplaySafe: true,
	}, RelayRequestPolicy{Retry401: true, MaxRetries: 1})
	if body != nil {
		_ = body.Close()
	}
	if errOpen != nil || attempts != 2 || response.StatusCode != http.StatusUnauthorized || response.RetryOutcome != relayRetryOutcomeExhausted || string(response.Body) != "unauthorized" {
		t.Fatalf("stream status = %d, outcome = %q, error = %v after %d sends, want retained 401 and exhausted initial plus refreshed replay only", response.StatusCode, response.RetryOutcome, errOpen, attempts)
	}
}

type relayTrackedErrorBody struct {
	io.Reader
	bytesRead int
	closed    bool
}

func (b *relayTrackedErrorBody) Read(dst []byte) (int, error) {
	n, errRead := b.Reader.Read(dst)
	b.bytesRead += n
	return n, errRead
}

func (b *relayTrackedErrorBody) Close() error {
	b.closed = true
	return nil
}

func TestRelayStreamErrorBodyIsBoundedAndClosed(t *testing.T) {
	const limit = 1 << 20
	for _, tc := range []struct {
		name      string
		size      int
		wantError bool
	}{
		{"at limit", limit, false},
		{"one byte over", limit + 1, true},
		{"oversized", 2 * limit, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			upstreamBody := &relayTrackedErrorBody{Reader: strings.NewReader(strings.Repeat("x", tc.size))}
			manager.SetRoundTripperProvider(&relayCaptureRoundTripperProvider{transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: upstreamBody,
					ContentLength: -1, Request: req,
				}, nil
			})})
			registerRelayTestAuths(t, manager, relayOAuthAuth("error-body-account", "error-body-token"))
			response, body, errOpen := manager.ExecuteRelayStream(context.Background(), RelayRequest{
				TargetBaseURL: "https://relay.example/v1", Model: "test-model", Method: http.MethodPost,
				RelativePath: "/responses", Body: []byte(`{"model":"test-model","stream":true}`),
			}, RelayRequestPolicy{})
			if body != nil {
				_ = body.Close()
				t.Error("non-success response returned a live stream")
			}
			if !upstreamBody.closed {
				t.Error("non-success upstream body was not closed")
			}
			if got := upstreamBody.bytesRead; got > limit+1 {
				t.Errorf("consumed %d error-body bytes, want at most %d including overflow detection", got, limit+1)
			}
			if tc.wantError {
				if errOpen == nil || len(response.Body) != 0 {
					t.Errorf("oversized error response returned error %v and %d buffered bytes; want failure without payload", errOpen, len(response.Body))
				}
			} else if errOpen != nil || response.StatusCode != http.StatusBadRequest || len(response.Body) != tc.size {
				t.Errorf("bounded error response status = %d, bytes = %d, error = %v; want original error response", response.StatusCode, len(response.Body), errOpen)
			}
		})
	}
}

func TestRelayStatusIsAggregateAndCredentialFree(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	streaming := requireRelayStreaming(t, manager)
	plus := relayOAuthAuth("private-status-auth", "private-status-token")
	plus.Attributes["plan_type"] = "plus"
	plus.Attributes["path"] = "/private/credential.json"
	plus.Metadata["email"] = "private-user@example.com"
	disabled := relayOAuthAuth("disabled-status-auth", "disabled-status-token")
	disabled.Disabled = true
	nonOAuth := &Auth{
		ID: "non-oauth-status-auth", Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey},
		Metadata:   map[string]any{"access_token": "non-oauth-access", "refresh_token": "non-oauth-refresh"},
	}
	registerRelayTestAuths(t, manager, plus, disabled, nonOAuth)
	status := streaming.RelayStatus("")
	if status.Total != 2 || status.Eligible != 1 || status.Plus != 1 || status.Disabled != 1 {
		t.Fatalf("relay pool status = %+v, want total=2 eligible=1 plus=1 disabled=1", status)
	}
	raw, errMarshal := json.Marshal(status)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, secret := range []string{"private-status-auth", "private-status-token", "/private/credential.json", "private-user@example.com"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("status exposed credential metadata: %s", raw)
		}
	}
}
