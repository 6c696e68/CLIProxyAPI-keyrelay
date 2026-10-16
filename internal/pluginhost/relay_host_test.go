package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

const (
	relayExecute     = "host.relay.execute"
	relayStreamOpen  = "host.relay.stream_open"
	relayStreamRead  = "host.relay.stream_read"
	relayStreamClose = "host.relay.stream_close"
	relayCancel      = "host.relay.cancel"
	relayStatus      = "host.relay.status"
)

type relayContractRequest struct {
	Provider    string              `json:"provider"`
	Path        string              `json:"path,omitempty"`
	Method      string              `json:"method,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	Body        []byte              `json:"body,omitempty"`
	StreamID    string              `json:"stream_id,omitempty"`
	AffinityKey string              `json:"affinity_key,omitempty"`
}

type relayContractResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       []byte              `json:"body,omitempty"`
	StreamID   string              `json:"stream_id,omitempty"`
	Payload    []byte              `json:"payload,omitempty"`
	Done       bool                `json:"done,omitempty"`
}

func newRelayContractHost(t *testing.T, baseURL, pluginConfig string) *Host {
	t.Helper()
	var raw yaml.Node
	persisted := fmt.Sprintf("base_url: %q\nmodels: [test-model]\n%s", baseURL, pluginConfig)
	if errDecode := yaml.Unmarshal([]byte(persisted), &raw); errDecode != nil {
		t.Fatalf("decode persisted relay config: %v", errDecode)
	}
	enabled := true
	host := New()
	host.runtimeConfig = &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Configs: map[string]config.PluginInstanceConfig{
			"ch-relay": {Enabled: &enabled, Raw: *raw.Content[0]},
		},
	}}
	return host
}

func relayContractAuth(id, token, plan string) *coreauth.Auth {
	return &coreauth.Auth{
		ID: id, Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"plan_type": strings.ToLower(plan)},
		Metadata:   map[string]any{"access_token": token},
	}
}

func addRelayContractAuth(t *testing.T, host *Host, auths ...*coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register Codex auth: %v", errRegister)
		}
	}
	host.SetAuthManager(manager)
}

func relayTLSContext(server *httptest.Server) context.Context {
	transport := server.Client().Transport.(*http.Transport).Clone()
	return context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
}

func callRelayContract(t *testing.T, host *Host, pluginID, method string, ctx context.Context, request any) ([]byte, error) {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	payload, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatalf("marshal relay request: %v", errMarshal)
	}
	return host.callFromPlugin(withHostCallbackPluginID(ctx, pluginID), method, payload)
}

func decodeRelayContractResponse(t *testing.T, raw []byte) relayContractResponse {
	t.Helper()
	response, errDecode := decodeRPCEnvelope[relayContractResponse](raw)
	if errDecode != nil {
		t.Fatalf("decode relay response: %v", errDecode)
	}
	return response
}

func TestRelayExecuteUsesPersistedHTTPSConfigAndAllowedOperations(t *testing.T) {
	var got []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer relay-test-token" {
			t.Errorf("Authorization = %q, want host-injected Codex token", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	host := newRelayContractHost(t, server.URL+"/configured/v1", "")
	addRelayContractAuth(t, host, relayContractAuth("codex-account", "relay-test-token", "Plus"))
	ctx := relayTLSContext(server)

	for _, operation := range []struct{ path, method string }{
		{"/models", http.MethodGet},
		{"/chat/completions", http.MethodPost},
		{"/responses", http.MethodPost},
	} {
		raw, errCall := callRelayContract(t, host, "ch-relay", relayExecute, ctx, relayContractRequest{
			Provider: "codex", Path: operation.path, Method: operation.method, Body: []byte(`{"model":"test-model"}`),
		})
		if errCall != nil {
			t.Fatalf("relay %s %s: %v", operation.method, operation.path, errCall)
		}
		response := decodeRelayContractResponse(t, raw)
		if response.StatusCode != http.StatusOK || string(response.Body) != "ok" {
			t.Fatalf("relay response = %#v, want upstream 200", response)
		}
	}
	want := []string{"GET /configured/v1/models", "POST /configured/v1/chat/completions", "POST /configured/v1/responses"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("upstream operations = %v, want %v", got, want)
	}
}

func TestRelayExecuteRejectsUnsafePathProviderPluginAndHTTPConfig(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	host := newRelayContractHost(t, server.URL+"/v1", "")
	addRelayContractAuth(t, host, relayContractAuth("codex-account", "token", "Plus"))
	ctx := relayTLSContext(server)

	for _, test := range []struct {
		name, path, method, provider, plugin string
	}{
		{"absolute URL", "https://attacker.invalid/models", http.MethodGet, "codex", "ch-relay"},
		{"HTTP URL", "http://attacker.invalid/models", http.MethodGet, "codex", "ch-relay"},
		{"traversal", "/../admin", http.MethodGet, "codex", "ch-relay"},
		{"query override", "/models?url=https://attacker.invalid", http.MethodGet, "codex", "ch-relay"},
		{"unlisted path", "/admin", http.MethodGet, "codex", "ch-relay"},
		{"wrong method", "/models", http.MethodPost, "codex", "ch-relay"},
		{"wrong provider", "/models", http.MethodGet, "openai", "ch-relay"},
		{"wrong plugin", "/models", http.MethodGet, "codex", "other-plugin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, errCall := callRelayContract(t, host, test.plugin, relayExecute, ctx, relayContractRequest{
				Provider: test.provider, Path: test.path, Method: test.method,
			})
			if errCall == nil {
				t.Fatal("relay accepted an unauthorized or unallowlisted request")
			}
		})
	}
	for _, override := range []struct {
		name    string
		request map[string]any
	}{
		{"absolute URL override", map[string]any{"provider": "codex", "path": "/models", "method": http.MethodGet, "url": "https://attacker.invalid/override"}},
		{"base URL override", map[string]any{"provider": "codex", "path": "/models", "method": http.MethodGet, "base_url": "https://attacker.invalid"}},
		{"auth identity override", map[string]any{"provider": "codex", "path": "/models", "method": http.MethodGet, "auth_id": "attacker-auth"}},
		{"authorization header override", map[string]any{"provider": "codex", "path": "/models", "method": http.MethodGet, "headers": map[string][]string{"Authorization": {"Bearer attacker-token"}}}},
	} {
		t.Run(override.name, func(t *testing.T) {
			if _, errCall := callRelayContract(t, host, "ch-relay", relayExecute, ctx, override.request); errCall == nil {
				t.Fatalf("relay accepted supplied %s", override.name)
			}
		})
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("invalid relay requests reached upstream %d times", upstreamCalls.Load())
	}

	httpHost := newRelayContractHost(t, "http://relay.example/v1", "")
	addRelayContractAuth(t, httpHost, relayContractAuth("codex-account", "token", "Plus"))
	_, errCall := callRelayContract(t, httpHost, "ch-relay", relayExecute, context.Background(), relayContractRequest{
		Provider: "codex", Path: "/models", Method: http.MethodGet,
	})
	if errCall == nil {
		t.Fatal("relay accepted persisted HTTP base URL")
	}
}

func TestRelayRejectsAmbiguousModelBeforeUpstream(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	host := newRelayContractHost(t, server.URL+"/v1", "")
	addRelayContractAuth(t, host, relayContractAuth("model-account", "model-token", "Plus"))
	ctx := relayTLSContext(server)

	for _, method := range []string{relayExecute, relayStreamOpen} {
		t.Run(method, func(t *testing.T) {
			for _, tc := range []struct {
				name, body string
			}{
				{"casefold override", `{"model":"unlisted","Model":"test-model"}`},
				{"duplicate override", `{"model":"unlisted","model":"test-model"}`},
				{"escaped duplicate", `{"model":"unlisted","\u006dodel":"test-model"}`},
				{"duplicate equal models", `{"model":"test-model","model":"test-model"}`},
				{"casefold equal models", `{"model":"test-model","MODEL":"test-model"}`},
				{"uppercase only", `{"Model":"test-model"}`},
				{"unlisted lowercase", `{"model":"unlisted"}`},
				{"padded model", `{"model":" test-model "}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before := upstreamCalls.Load()
					raw, errCall := callRelayContract(t, host, "ch-relay", method, ctx, relayContractRequest{
						Provider: "codex", Path: "/responses", Method: http.MethodPost, Body: []byte(tc.body),
					})
					if errCall == nil {
						response := decodeRelayContractResponse(t, raw)
						if response.StreamID != "" {
							if _, errClose := callRelayContract(t, host, "ch-relay", relayStreamClose, ctx, map[string]string{"stream_id": response.StreamID}); errClose != nil {
								t.Fatalf("close unexpected stream: %v", errClose)
							}
						}
						t.Errorf("relay accepted ambiguous or unallowlisted model body %s", tc.body)
					}
					if got := upstreamCalls.Load() - before; got != 0 {
						t.Errorf("rejected model body reached upstream %d times", got)
					}
				})
			}
			t.Run("exact lowercase model", func(t *testing.T) {
				before := upstreamCalls.Load()
				raw, errCall := callRelayContract(t, host, "ch-relay", method, ctx, relayContractRequest{
					Provider: "codex", Path: "/responses", Method: http.MethodPost,
					Body: []byte(`{"model":"test-model","input":{"model":"unlisted","Model":"unlisted"}}`),
				})
				if errCall != nil {
					t.Fatalf("relay rejected exact allowlisted model: %v", errCall)
				}
				response := decodeRelayContractResponse(t, raw)
				if response.StreamID != "" {
					if _, errClose := callRelayContract(t, host, "ch-relay", relayStreamClose, ctx, map[string]string{"stream_id": response.StreamID}); errClose != nil {
						t.Fatalf("close allowed stream: %v", errClose)
					}
				}
				if response.StatusCode != http.StatusOK || upstreamCalls.Load()-before != 1 {
					t.Fatalf("exact model response = %#v, upstream sends = %d; want one successful send", response, upstreamCalls.Load()-before)
				}
			})
		})
	}
}

func TestRelayResponseAndStatusDoNotExposeTokenWhenUpstreamEchoesIt(t *testing.T) {
	const token = "relay-secret-must-not-leak"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Echo", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "upstream echoed %s", r.Header.Get("Authorization"))
	}))
	t.Cleanup(server.Close)
	host := newRelayContractHost(t, server.URL+"/v1", "")
	addRelayContractAuth(t, host, relayContractAuth("private-auth-id", token, "Plus"))
	rawResponse, errCall := callRelayContract(t, host, "ch-relay", relayExecute, relayTLSContext(server), relayContractRequest{
		Provider: "codex", Path: "/models", Method: http.MethodGet,
	})
	if errCall != nil {
		t.Fatalf("relay execute: %v", errCall)
	}
	rawStatus, errStatus := callRelayContract(t, host, "ch-relay", relayStatus, context.Background(), nil)
	if errStatus != nil {
		t.Fatalf("relay status: %v", errStatus)
	}
	for label, raw := range map[string][]byte{"response": rawResponse, "status": rawStatus} {
		if strings.Contains(string(raw), token) || strings.Contains(string(raw), "private-auth-id") {
			t.Errorf("relay %s serialized credential material: %s", label, raw)
		}
	}
}

func TestRelayAffinityIsHostDerivedAndSessionSticky(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	host := newRelayContractHost(t, server.URL+"/v1", "rotation_mode: sticky\n")
	manager := coreauth.NewManager(nil, nil, nil)
	for i, token := range []string{"affinity-a", "affinity-b"} {
		if _, errRegister := manager.Register(context.Background(), relayContractAuth(fmt.Sprintf("codex-%d", i), token, "Plus")); errRegister != nil {
			t.Fatalf("register Codex auth: %v", errRegister)
		}
	}
	host.SetAuthManager(manager)
	base := relayTLSContext(server)
	for _, session := range []string{"session-A", "session-B", "session-A", "session-B"} {
		ctx := logging.WithClientRequestMetadata(base, logging.ClientRequestMetadata{SessionID: session})
		_, errCall := callRelayContract(t, host, "ch-relay", relayExecute, ctx, relayContractRequest{
			Provider: "codex", Path: "/models", Method: http.MethodGet,
			AffinityKey: "plugin-controlled-key",
		})
		if errCall != nil {
			t.Fatalf("relay execute for %s: %v", session, errCall)
		}
	}
	a, b, aAgain, bAgain := <-seen, <-seen, <-seen, <-seen
	if a != aAgain || b != bAgain || a == b {
		t.Fatalf("selected auth tokens = [%q %q %q %q], want distinct sessions sticky to distinct auths", a, b, aAgain, bAgain)
	}
}

func TestRelayStreamReadWaitsForHealthyUpstreamAndEOFCloses(t *testing.T) {
	firstChunkSent := make(chan struct{})
	releaseSecondChunk := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(finished)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		close(firstChunkSent)
		<-releaseSecondChunk
		_, _ = w.Write([]byte("second"))
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(server.Close)
	host := newRelayContractHost(t, server.URL+"/v1", "")
	addRelayContractAuth(t, host, relayContractAuth("codex-account", "unique-credential", "Plus"))
	ctx, cancel := context.WithCancel(relayTLSContext(server))
	defer cancel()
	raw, errOpen := callRelayContract(t, host, "ch-relay", relayStreamOpen, ctx, relayContractRequest{
		Provider: "codex", Path: "/responses", Method: http.MethodPost, Body: []byte(`{"model":"test-model","stream":true}`),
	})
	if errOpen != nil {
		t.Fatalf("relay stream open: %v", errOpen)
	}
	opened := decodeRelayContractResponse(t, raw)
	if opened.StreamID == "" {
		t.Fatalf("stream open response = %#v, want opaque stream ID", opened)
	}
	select {
	case <-firstChunkSent:
	case <-time.After(time.Second):
		t.Fatal("upstream did not flush initial stream chunk")
	}
	read := func() relayContractResponse {
		rawRead, errRead := callRelayContract(t, host, "ch-relay", relayStreamRead, ctx, map[string]string{"stream_id": opened.StreamID})
		if errRead != nil {
			t.Fatalf("relay stream read: %v", errRead)
		}
		return decodeRelayContractResponse(t, rawRead)
	}
	if got := string(read().Payload); got != "first" {
		t.Fatalf("first stream payload = %q, want first", got)
	}
	readResult := make(chan relayContractResponse, 1)
	go func() { readResult <- read() }()
	select {
	case <-readResult:
		t.Fatal("stream read returned before upstream released the next chunk")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseSecondChunk)
	select {
	case got := <-readResult:
		if string(got.Payload) != "second" {
			t.Fatalf("second stream payload = %q, want second", got.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy stream read remained blocked after release")
	}
	if !read().Done {
		t.Fatal("stream read did not report upstream EOF")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("upstream stream was not closed after EOF")
	}
}

func TestRelayStreamCloseIsIdempotentAndCancelPropagates(t *testing.T) {
	started, canceled := make(chan struct{}, 2), make(chan struct{}, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	t.Cleanup(server.Close)
	host := newRelayContractHost(t, server.URL+"/v1", "")
	addRelayContractAuth(t, host, relayContractAuth("codex-account", "token", "Plus"))
	ctx, cancel := context.WithTimeout(relayTLSContext(server), 5*time.Second)
	defer cancel()
	open := func() string {
		raw, errOpen := callRelayContract(t, host, "ch-relay", relayStreamOpen, ctx, relayContractRequest{
			Provider: "codex", Path: "/responses", Method: http.MethodPost, Body: []byte(`{"model":"test-model","stream":true}`),
		})
		if errOpen != nil {
			t.Fatalf("relay stream open: %v", errOpen)
		}
		id := decodeRelayContractResponse(t, raw).StreamID
		if id == "" {
			t.Fatal("relay stream ID is empty")
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("upstream stream did not start")
		}
		return id
	}
	closeRequest := func(id string) {
		if _, errClose := callRelayContract(t, host, "ch-relay", relayStreamClose, ctx, map[string]string{"stream_id": id}); errClose != nil {
			t.Fatalf("relay stream close: %v", errClose)
		}
	}
	first := open()
	closeRequest(first)
	closeRequest(first)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("stream close did not cancel upstream")
	}
	second := open()
	if _, errCancel := callRelayContract(t, host, "ch-relay", relayCancel, ctx, map[string]string{"stream_id": second}); errCancel != nil {
		t.Fatalf("relay cancel: %v", errCancel)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("relay cancel did not cancel upstream")
	}
}

func TestRelayRetry401And429UseDistinctAccountsAndStayBounded(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			seen := make(chan string, 3)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Get("Authorization")
				if attempts.Add(1) < 3 {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("recovered"))
			}))
			t.Cleanup(server.Close)
			host := newRelayContractHost(t, server.URL+"/v1", "max_retries: 2\n")
			addRelayContractAuth(t, host,
				relayContractAuth("codex-a", "token-a", "Plus"),
				relayContractAuth("codex-b", "token-b", "Plus"),
				relayContractAuth("codex-c", "token-c", "Plus"),
			)
			raw, errCall := callRelayContract(t, host, "ch-relay", relayExecute, relayTLSContext(server), relayContractRequest{
				Provider: "codex", Path: "/models", Method: http.MethodGet,
			})
			if errCall != nil {
				t.Fatalf("relay retry: %v", errCall)
			}
			response := decodeRelayContractResponse(t, raw)
			if response.StatusCode != http.StatusOK || string(response.Body) != "recovered" || attempts.Load() != 3 {
				t.Fatalf("relay response = %#v after %d attempts, want recovered after 3", response, attempts.Load())
			}
			first, second, third := <-seen, <-seen, <-seen
			if first == second || second == third || first == third {
				t.Fatalf("retry used accounts [%q %q %q], want three distinct eligible accounts", first, second, third)
			}
		})
	}
}

func TestRelayCallbacksPreserve429WhenNoRetryCandidateRemains(t *testing.T) {
	for _, consumer := range []struct{ name, method string }{
		{"execute", relayExecute},
		{"stream-open", relayStreamOpen},
	} {
		t.Run(consumer.name, func(t *testing.T) {
			const token = "terminal-relay-credential"
			var attempts atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprintf(w, `{"error":{"message":"rate limited for %s"}}`, token)
			}))
			t.Cleanup(server.Close)
			host := newRelayContractHost(t, server.URL+"/v1", "retry_on_rate_limit: true\nmax_retries: 2\n")
			addRelayContractAuth(t, host, relayContractAuth("sole-codex-account", token, "Plus"))
			raw, errCall := callRelayContract(t, host, "ch-relay", consumer.method, relayTLSContext(server), relayContractRequest{
				Provider: "codex", Path: "/models", Method: http.MethodGet,
			})
			if errCall != nil {
				t.Fatalf("relay callback lost received upstream 429: %v", errCall)
			}
			response := decodeRelayContractResponse(t, raw)
			if response.StatusCode != http.StatusTooManyRequests || attempts.Load() != 1 {
				t.Fatalf("relay response = %#v after %d sends, want received 429 after one eligible account", response, attempts.Load())
			}
			if got := string(response.Body); got != `{"error":{"message":"rate limited for [REDACTED]"}}` {
				t.Fatalf("relay error body = %q, want received body with credential redacted", got)
			}
			if got := http.Header(response.Headers).Get("Retry-After"); got != "17" {
				t.Fatalf("Retry-After = %q, want upstream retry hint 17", got)
			}
			if consumer.method == relayStreamOpen && (response.StreamID != "" || !response.Done) {
				t.Fatalf("terminal stream-open response = %#v, want completed HTTP error without live stream", response)
			}
		})
	}
}

func TestRelayDoesNotReplayPOSTWithoutIdempotencyAfterBodyIsSent(t *testing.T) {
	var attempts atomic.Int32
	var receivedBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	host := newRelayContractHost(t, server.URL+"/v1", "max_retries: 2\n")
	addRelayContractAuth(t, host,
		relayContractAuth("codex-a", "token-a", "Plus"),
		relayContractAuth("codex-b", "token-b", "Plus"),
	)
	body := []byte(`{"model":"test-model","input":"must not be duplicated"}`)
	raw, errCall := callRelayContract(t, host, "ch-relay", relayExecute, relayTLSContext(server), relayContractRequest{
		Provider: "codex", Path: "/responses", Method: http.MethodPost, Body: body,
	})
	if errCall != nil {
		t.Fatalf("relay POST: %v", errCall)
	}
	response := decodeRelayContractResponse(t, raw)
	if response.StatusCode != http.StatusServiceUnavailable || attempts.Load() != 1 || string(receivedBody) != string(body) {
		t.Fatalf("POST response = %#v, attempts = %d, received body = %q; want one sent request without replay", response, attempts.Load(), receivedBody)
	}
}

func TestRelayRetryLimitAndEligibleCodexAuthFailures(t *testing.T) {
	t.Run("retry-limit", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Retry-After", "23")
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "persistent rate limit")
		}))
		t.Cleanup(server.Close)
		host := newRelayContractHost(t, server.URL+"/v1", "max_retries: 1\n")
		addRelayContractAuth(t, host,
			relayContractAuth("codex-a", "token-a", "Plus"),
			relayContractAuth("codex-b", "token-b", "Plus"),
			relayContractAuth("codex-c", "token-c", "Plus"),
		)
		raw, errCall := callRelayContract(t, host, "ch-relay", relayExecute, relayTLSContext(server), relayContractRequest{
			Provider: "codex", Path: "/models", Method: http.MethodGet,
		})
		if errCall != nil {
			t.Fatalf("persistent 429 lost received response: %v", errCall)
		}
		response := decodeRelayContractResponse(t, raw)
		if response.StatusCode != http.StatusTooManyRequests || attempts.Load() != 2 {
			t.Fatalf("persistent 429 response = %#v after %d attempts, want retained 429 after exact two sends", response, attempts.Load())
		}
		if string(response.Body) != "persistent rate limit" {
			t.Fatalf("persistent 429 body = %q, want retained upstream body", response.Body)
		}
		if got := http.Header(response.Headers).Get("Retry-After"); got != "23" {
			t.Fatalf("persistent 429 Retry-After = %q, want retained upstream hint 23", got)
		}
	})
	for _, tc := range []struct {
		name, pluginConfig string
		auths              []*coreauth.Auth
	}{
		{"missing-codex-auth", "", nil},
		{"plus-only-without-plus-auth", "plus_preference: only\n", []*coreauth.Auth{relayContractAuth("codex-team", "team-token", "Team")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var forwarded atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				forwarded.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(server.Close)
			host := newRelayContractHost(t, server.URL+"/v1", tc.pluginConfig)
			addRelayContractAuth(t, host, tc.auths...)
			_, errCall := callRelayContract(t, host, "ch-relay", relayExecute, relayTLSContext(server), relayContractRequest{
				Provider: "codex", Path: "/models", Method: http.MethodGet,
			})
			if errCall == nil || forwarded.Load() != 0 {
				t.Fatalf("request without eligible Codex auth = %v, forwarded %d requests; want auth failure before upstream", errCall, forwarded.Load())
			}
		})
	}
}
