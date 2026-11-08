package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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

type keyShopResolveHost struct {
	keyShopNotifyingHost
	result KeyShopResolveResult
	err    error
	called bool
	calls  int
	model  string
	body   string
}

func (h *keyShopResolveHost) KeyShopResolveModel(_ context.Context, secret, model, bodyModel string) (KeyShopResolveResult, error) {
	h.calls++
	h.called = true
	h.secret = secret
	h.model = model
	h.body = bodyModel
	return h.result, h.err
}

func TestKeyShopModelGateRecordsDenialBeforeInterceptor(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopNotifyingHost{keyShopGateHost: keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels:\n  - name: gpt-5-codex\n    alias: codex-latest\n")}}
	handler.SetPluginHost(host)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5"}`), "")
	assertModelNotAllowed(t, errMsg)
	if host.secret != "ck_customer" || host.code != "model_not_allowed" || host.status != http.StatusForbidden || host.id == "" || strings.HasPrefix(host.id, "gate-") {
		t.Fatalf("denial was not recorded with the lifecycle request id: %+v", host)
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

func TestKeyShopModelGateGroupAliasRewriteKeepsSuffix(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopResolveHost{result: KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: true, Limited: true, Model: "vippro/gpt-6-sol(high)"}}
	handler.SetPluginHost(host)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "gpt-6-sol(high)", []byte(`{"model":"gpt-6-sol(high)"}`), "")
	if !host.called || host.model != "gpt-6-sol(high)" || host.body != "gpt-6-sol(high)" {
		t.Fatalf("resolver was not called with the client model: %+v", host)
	}
	if errMsg == nil || !strings.Contains(errMsg.Error.Error(), "vippro/gpt-6-sol(high)") {
		t.Fatalf("group alias was not rewritten: %+v", errMsg)
	}
}

func TestKeyShopModelGateGroupDeny(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopResolveHost{result: KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: false, Limited: true}}
	host.yaml = []byte("model_access_mode: custom\nmodels:\n  - name: gpt-5.5\n")
	handler.SetPluginHost(host)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5"}`), "")
	assertModelNotAllowed(t, errMsg)
	if host.code != "model_not_allowed" || host.status != http.StatusForbidden {
		t.Fatalf("group denial was not recorded: %+v", host)
	}
}

func TestKeyShopModelGateLegacyPassthrough(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopResolveHost{result: KeyShopResolveResult{Loaded: true, Mode: "legacy"}}
	host.yaml = []byte("model_access_mode: custom\nmodels:\n  - name: gpt-5-codex\n    alias: codex-latest\n")
	handler.SetPluginHost(host)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "codex-latest(high)", []byte(`{"model":"codex-latest(high)"}`), "")
	if !host.called {
		t.Fatal("legacy key did not ask the plugin")
	}
	if errMsg == nil || !strings.Contains(errMsg.Error.Error(), "gpt-5-codex(high)") || strings.Contains(errMsg.Error.Error(), "codex-latest") {
		t.Fatalf("legacy key did not keep the global alias rewrite: %+v", errMsg)
	}
}

func TestKeyShopModelGateResolveErrorFailsClosed(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopResolveHost{err: context.DeadlineExceeded}
	host.yaml = []byte("model_access_mode: full\n")
	handler.SetPluginHost(host)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5"}`), "")
	if errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(errMsg.Body), `"model_policy_unavailable"`) {
		t.Fatalf("plugin error was not fail-closed: %+v", errMsg)
	}
}

func TestKeyShopModelGateResolverIgnoresOperatorKey(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopResolveHost{
		result: KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: false},
		err:    context.DeadlineExceeded,
	}
	host.yaml = []byte("model_access_mode: custom\nmodels:\n  - name: only-model\n")
	handler.SetPluginHost(host)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "operator-smoke-key"), "openai", "gpt-5.5", []byte(`{"model":"gpt-5.5"}`), "")
	if host.called {
		t.Fatal("operator key called resolve_model")
	}
	if errMsg != nil && errMsg.StatusCode == http.StatusForbidden {
		t.Fatalf("operator key was denied by key-shop policy: %+v", errMsg)
	}
	if errMsg != nil && errMsg.StatusCode == http.StatusServiceUnavailable && strings.Contains(string(errMsg.Body), `"model_policy_unavailable"`) {
		t.Fatalf("operator key hit the key-shop policy error: %+v", errMsg)
	}
}

func TestKeyShopModelGateAllModelsKeepsDifferingBody(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopResolveHost{result: KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: true, Model: "gpt-6-sol"}}
	handler.SetPluginHost(host)
	raw := []byte(`{"model":"other-model","stream":true}`)
	_, name, body, errMsg := handler.applyKeyShopModelGate(keyShopContext(t, "ck_customer"), "gpt-6-sol", raw)
	if errMsg != nil {
		t.Fatalf("unchanged model was denied: %+v", errMsg)
	}
	if name != "gpt-6-sol" || string(body) != string(raw) {
		t.Fatalf("unchanged model rewrote the body: name=%q body=%s", name, body)
	}
}

func TestPreparedStreamRouteUsesGroupUpstreamName(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopResolveHost{result: KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: true, Limited: true, Model: "vippro/gpt-6-sol(high)"}})
	calls := 0
	seen := ""
	router := &handlerModelRouterTestHost{hasRouters: true}
	router.route = func(_ context.Context, req pluginapi.ModelRouteRequest, _ string) (pluginapi.ModelRouteResponse, bool) {
		calls++
		seen = req.RequestedModel
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "codex", TargetModel: req.RequestedModel}, true
	}
	handler.SetModelRouterHost(router)
	body := []byte(`{"model":"gpt-6-sol(high)","stream":true}`)
	ctx, routed := handler.PrepareStreamModelRoute(keyShopContext(t, "ck_customer"), "openai-response", "gpt-6-sol(high)", body)
	if !routed {
		t.Fatal("prepared route did not override resolution")
	}
	if got := handler.StreamRouteModel(ctx, "gpt-6-sol(high)"); got != "vippro/gpt-6-sol(high)" {
		t.Fatalf("stream route model = %q, want upstream name", got)
	}
	if calls != 1 || seen != "vippro/gpt-6-sol(high)" {
		t.Fatalf("prepared route used %q after %d calls", seen, calls)
	}
}

type websocketDenialHost struct {
	keyShopResolveHost
	notes int
	rows  map[string]struct{}
}

func (h *websocketDenialHost) NoteKeyShopRejection(_ context.Context, secret, requestID, code string, status int) {
	h.notes++
	if h.rows == nil {
		h.rows = map[string]struct{}{}
	}
	h.rows[requestID] = struct{}{}
	h.secret = secret
	h.id = requestID
	h.code = code
	h.status = status
}

func TestResponsesWebsocketGateDenialRecordsOnce(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &websocketDenialHost{keyShopResolveHost: keyShopResolveHost{result: KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: false, Limited: true}}}
	handler.SetPluginHost(host)
	body := []byte(`{"model":"gpt-6-sol","stream":true}`)
	ctx := keyShopContext(t, "ck_customer")
	ctx, _ = handler.PrepareStreamModelRoute(ctx, "openai-response", "gpt-6-sol", body)
	_ = handler.StreamRouteModel(ctx, "gpt-6-sol")
	_, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai-response", "gpt-6-sol", body, "")
	assertModelNotAllowed(t, <-errChan)
	if host.calls != 1 {
		t.Fatalf("resolve_model calls = %d, want 1", host.calls)
	}
	if host.notes != 1 || len(host.rows) != 1 {
		t.Fatalf("denial notes = %d rejected rows = %d, want 1 and 1 quota unit", host.notes, len(host.rows))
	}
	if host.id == "" || strings.HasPrefix(host.id, "gate-") {
		t.Fatalf("denial id = %q, want the lifecycle request id", host.id)
	}
}

// keyShopGroupHost resolves through a function so tests can model a real group.
type keyShopGroupHost struct {
	keyShopGateHost
	resolve func(model, bodyModel string) (KeyShopResolveResult, error)
	calls   []string
	notes   int
	secret  string
	id      string
	code    string
	status  int
}

func (h *keyShopGroupHost) KeyShopResolveModel(_ context.Context, secret, model, bodyModel string) (KeyShopResolveResult, error) {
	h.calls = append(h.calls, model)
	h.secret = secret
	return h.resolve(model, bodyModel)
}

func (h *keyShopGroupHost) NoteKeyShopRejection(_ context.Context, secret, requestID, code string, status int) {
	h.notes++
	h.secret = secret
	h.id = requestID
	h.code = code
	h.status = status
}

// limitedGroupResolver mimics Group.Resolve for client alias -> upstream entries.
func limitedGroupResolver(aliases map[string]string) func(model, bodyModel string) (KeyShopResolveResult, error) {
	resolveOne := func(name string) (string, bool) {
		name = strings.TrimSpace(name)
		base, suffix := name, ""
		if open := strings.LastIndex(name, "("); open > 0 && strings.HasSuffix(name, ")") {
			base, suffix = name[:open], name[open:]
		}
		upstream, ok := aliases[strings.ToLower(base)]
		if !ok {
			return "", false
		}
		return upstream + suffix, true
	}
	return func(model, bodyModel string) (KeyShopResolveResult, error) {
		requested := strings.TrimSpace(model)
		if requested == "" {
			requested = strings.TrimSpace(bodyModel)
		}
		upstream, ok := resolveOne(requested)
		if !ok {
			return KeyShopResolveResult{Loaded: true, Mode: "group", Limited: true}, nil
		}
		if body := strings.TrimSpace(bodyModel); body != "" && !strings.EqualFold(body, requested) {
			bodyUpstream, bodyOK := resolveOne(body)
			if !bodyOK || bodyUpstream != upstream {
				return KeyShopResolveResult{Loaded: true, Mode: "group", Limited: true}, nil
			}
		}
		return KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: true, Limited: true, Model: upstream}, nil
	}
}

func assertRoutedModel(t *testing.T, errMsg *interfaces.ErrorMessage, want string) {
	t.Helper()
	if errMsg == nil {
		t.Fatalf("expected a provider routing error naming %q", want)
	}
	if errMsg.StatusCode == http.StatusForbidden {
		t.Fatalf("model %q was denied: %+v", want, errMsg)
	}
	if !strings.Contains(errMsg.Error.Error(), want) {
		t.Fatalf("routed model error = %v, want it to name %q", errMsg.Error, want)
	}
}

func TestKeyShopGroupAuthorizesRawNameBeforeGlobalMapping(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		ModelMapping: []config.ModelMappingRule{{Match: "*5.5", Target: "gpt-6-luna", Mode: config.ModelMappingModeWildcard}},
	}, nil)
	host := &keyShopGroupHost{resolve: limitedGroupResolver(map[string]string{
		"vippro/claude-opus-5.5": "kr/claude-opus-5.5-thinking",
	})}
	handler.SetPluginHost(host)
	const raw = "vippro/claude-opus-5.5(high)"
	const want = "kr/claude-opus-5.5-thinking(high)"
	body := []byte(`{"model":"vippro/claude-opus-5.5(high)","stream":true}`)

	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", raw, body, "")
	assertRoutedModel(t, errMsg, want)
	if strings.Contains(errMsg.Error.Error(), "gpt-6-luna") {
		t.Fatalf("global mapping overrode the group: %v", errMsg.Error)
	}

	_, _, errMsg = handler.ExecuteCountWithAuthManager(keyShopContext(t, "ck_customer"), "openai", raw, body, "")
	assertRoutedModel(t, errMsg, want)

	_, _, errChan := handler.ExecuteStreamWithAuthManager(keyShopContext(t, "ck_customer"), "openai", raw, body, "")
	assertRoutedModel(t, <-errChan, want)

	ctx := keyShopContext(t, "ck_customer")
	ctx, _ = handler.PrepareStreamModelRoute(ctx, "openai-response", raw, body)
	if got := handler.StreamRouteModel(ctx, raw); got != want {
		t.Fatalf("stream route model = %q, want %q", got, want)
	}
	if result := handler.mappingResultForOriginal(ctx, raw); result.Changed {
		t.Fatalf("authoritative group path applied a mapping echo: %+v", result)
	}
	_, _, errChan = handler.ExecuteStreamWithAuthManager(ctx, "openai-response", raw, body, "")
	assertRoutedModel(t, <-errChan, want)

	if len(host.calls) == 0 {
		t.Fatal("resolver was not consulted")
	}
	for _, seen := range host.calls {
		if seen != raw {
			t.Fatalf("resolver saw %q, want only the raw client model %q", seen, raw)
		}
	}
}

func TestKeyShopGroupKeepDecisionDoesNotGetRemapped(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		ModelMapping: []config.ModelMappingRule{{Match: "*5.5", Target: "gpt-6-luna", Mode: config.ModelMappingModeWildcard}},
	}, nil)
	host := &keyShopGroupHost{resolve: limitedGroupResolver(map[string]string{
		"kr/claude-opus-5.5": "kr/claude-opus-5.5",
	})}
	handler.SetPluginHost(host)
	const raw = "kr/claude-opus-5.5"
	body := []byte(`{"model":"kr/claude-opus-5.5","stream":true}`)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", raw, body, "")
	assertRoutedModel(t, errMsg, raw)
	ctx, _ := handler.PrepareStreamModelRoute(keyShopContext(t, "ck_customer"), "openai-response", raw, body)
	if got := handler.StreamRouteModel(ctx, raw); got != raw {
		t.Fatalf("stream route model = %q, want the raw group name %q", got, raw)
	}
	_, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai-response", raw, body, "")
	assertRoutedModel(t, <-errChan, raw)
}

func TestKeyShopAllModelsAndLegacyKeysStillUseGlobalMapping(t *testing.T) {
	cases := map[string]func(model, bodyModel string) (KeyShopResolveResult, error){
		"all-models": func(model, _ string) (KeyShopResolveResult, error) {
			return KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: true, Model: strings.TrimSpace(model)}, nil
		},
		"legacy": func(string, string) (KeyShopResolveResult, error) {
			return KeyShopResolveResult{Loaded: true, Mode: "legacy"}, nil
		},
	}
	const raw = "vippro/claude-opus-5.5"
	const want = "gpt-6-luna"
	for name, resolve := range cases {
		t.Run(name, func(t *testing.T) {
			handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
				ModelMapping: []config.ModelMappingRule{{Match: "*5.5", Target: want, Mode: config.ModelMappingModeWildcard}},
			}, nil)
			host := &keyShopGroupHost{resolve: resolve}
			handler.SetPluginHost(host)
			body := []byte(`{"model":"vippro/claude-opus-5.5"}`)
			_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", raw, body, "")
			assertRoutedModel(t, errMsg, want)
			ctx, _ := handler.PrepareStreamModelRoute(keyShopContext(t, "ck_customer"), "openai-response", raw, body)
			if got := handler.StreamRouteModel(ctx, raw); got != want {
				t.Fatalf("stream route model = %q, want %q", got, want)
			}
			if result := handler.mappingResultForOriginal(ctx, raw); !result.Changed || result.Original != raw {
				t.Fatalf("mapped path lost its mapping echo: %+v", result)
			}
		})
	}
}

func TestKeyShopOldPluginResolveResultStillUsesGlobalMapping(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		ModelMapping: []config.ModelMappingRule{{Match: "*5.5", Target: "gpt-6-luna", Mode: config.ModelMappingModeWildcard}},
	}, nil)
	// An old plugin sends no Limited field and keeps the requested model, which is
	// how an all-models group answers. That reply must not become authoritative.
	host := &keyShopGroupHost{resolve: func(model, _ string) (KeyShopResolveResult, error) {
		return KeyShopResolveResult{Loaded: true, Mode: "group", Allowed: true, Model: strings.TrimSpace(model)}, nil
	}}
	handler.SetPluginHost(host)
	const raw = "vippro/claude-opus-5.5"
	body := []byte(`{"model":"vippro/claude-opus-5.5"}`)
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", raw, body, "")
	assertRoutedModel(t, errMsg, "gpt-6-luna")
	ctx, _ := handler.PrepareStreamModelRoute(keyShopContext(t, "ck_customer"), "openai-response", raw, body)
	if got := handler.StreamRouteModel(ctx, raw); got != "gpt-6-luna" {
		t.Fatalf("stream route model = %q, want the mapped name", got)
	}
}

func TestKeyShopGroupFallsBackToMappedAlias(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		ModelMapping: []config.ModelMappingRule{{Match: "vippro/gpt-6-astra", Target: "gpt-6-astra"}},
	}, nil)
	host := &keyShopGroupHost{resolve: limitedGroupResolver(map[string]string{
		"gpt-6-astra": "vippro/gpt-6-astra",
	})}
	handler.SetPluginHost(host)
	const want = "vippro/gpt-6-astra"
	_, _, errMsg := handler.ExecuteWithAuthManager(keyShopContext(t, "ck_customer"), "openai", "vippro/gpt-6-astra", []byte(`{"model":"vippro/gpt-6-astra"}`), "")
	assertRoutedModel(t, errMsg, want)
	foundMapped := false
	for _, seen := range host.calls {
		if seen == "gpt-6-astra" {
			foundMapped = true
		}
	}
	if !foundMapped {
		t.Fatalf("resolver never saw the mapped alias: %v", host.calls)
	}
}

func TestKeyShopGroupDeniesUnknownModelOnce(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		ModelMapping: []config.ModelMappingRule{{Match: "vippro/gpt-6-astra", Target: "gpt-6-astra"}},
	}, nil)
	host := &keyShopGroupHost{resolve: limitedGroupResolver(map[string]string{
		"gpt-6-astra": "vippro/gpt-6-astra",
	})}
	handler.SetPluginHost(host)
	const raw = "vippro/not-in-group"
	body := []byte(`{"model":"vippro/not-in-group","stream":true}`)
	ctx, _ := handler.PrepareStreamModelRoute(keyShopContext(t, "ck_customer"), "openai-response", raw, body)
	_ = handler.StreamRouteModel(ctx, raw)
	_, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai-response", raw, body, "")
	assertModelNotAllowed(t, <-errChan)
	if len(host.calls) != 1 {
		t.Fatalf("resolver calls = %v, want one raw probe", host.calls)
	}
	if host.notes != 1 {
		t.Fatalf("denial notes = %d, want 1", host.notes)
	}
	if host.id == "" || strings.HasPrefix(host.id, "gate-") {
		t.Fatalf("denial id = %q, want the lifecycle request id", host.id)
	}
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
