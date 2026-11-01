package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const (
	tokenA = "tok-account-a"
	tokenB = "tok-account-b"
)

type hostCall struct {
	method  string
	payload []byte
}

type httpScript struct {
	status   int
	body     []byte
	streamID string
	chunks   []readChunk
}

type readChunk struct {
	payload []byte
	err     string
	done    bool
}

type decodedHTTP struct {
	RPC            string
	HostCallbackID string              `json:"host_callback_id"`
	Method         string              `json:"method"`
	URL            string              `json:"url"`
	Headers        map[string][]string `json:"headers"`
	Body           []byte              `json:"body"`
}

type fakeHost struct {
	mu         sync.Mutex
	calls      []hostCall
	files      []pluginapi.HostAuthFileEntry
	authJSON   map[string]json.RawMessage
	scripts    []httpScript
	scriptUsed int
	pending    []readChunk
	readIndex  int

	holdFirstRead  bool
	readStarted    chan struct{}
	releaseRead    chan struct{}
	streamFinished chan struct{}
	startedOnce    sync.Once
	releaseOnce    sync.Once
	finishOnce     sync.Once
}

func setup(t *testing.T) *fakeHost {
	t.Helper()
	resetRuntime()
	accountCheckAutoStart = false
	host := &fakeHost{authJSON: map[string]json.RawMessage{}}
	hostCallOverride = host.call
	t.Cleanup(func() {
		host.release()
		stopAccountChecker()
		hostCallOverride = nil
	})
	return host
}

func (f *fakeHost) call(method string, payload []byte) (json.RawMessage, error) {
	copied := append([]byte(nil), payload...)
	if method == pluginabi.MethodHostHTTPStreamRead {
		return f.readStream(copied)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, hostCall{method: method, payload: copied})
	switch method {
	case pluginabi.MethodHostAuthList:
		return json.Marshal(map[string]any{"files": f.files})
	case pluginabi.MethodHostAuthGet:
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		if err := json.Unmarshal(copied, &req); err != nil {
			return nil, err
		}
		raw, ok := f.authJSON[req.AuthIndex]
		if !ok {
			return nil, errString("missing auth " + req.AuthIndex)
		}
		return json.Marshal(map[string]any{"auth_index": req.AuthIndex, "json": json.RawMessage(raw)})
	case pluginabi.MethodHostHTTPDo:
		return f.respondLocked(false)
	case pluginabi.MethodHostHTTPDoStream:
		return f.respondLocked(true)
	case pluginabi.MethodHostHTTPStreamClose:
		return json.RawMessage(`{}`), nil
	case pluginabi.MethodHostStreamClose:
		f.markFinishedLocked()
		return json.RawMessage(`{}`), nil
	case pluginabi.MethodHostStreamEmit:
		return json.RawMessage(`{}`), nil
	default:
		return nil, errString("unexpected host method " + method)
	}
}

func (f *fakeHost) respondLocked(stream bool) (json.RawMessage, error) {
	script := httpScript{status: http.StatusOK, body: []byte(`{"ok":true}`)}
	if f.scriptUsed < len(f.scripts) {
		script = f.scripts[f.scriptUsed]
		f.scriptUsed++
		if script.status == 0 {
			script.status = http.StatusOK
		}
	}
	if stream {
		if script.status < 200 || script.status >= 300 {
			if script.streamID != "" {
				if len(script.chunks) == 0 && len(script.body) > 0 {
					f.pending = []readChunk{{payload: script.body, done: true}}
				} else {
					f.pending = script.chunks
				}
				f.readIndex = 0
				return json.Marshal(map[string]any{"status_code": script.status, "stream_id": script.streamID})
			}
			return json.Marshal(map[string]any{"status_code": script.status, "body": script.body})
		}
		f.pending = script.chunks
		f.readIndex = 0
		id := script.streamID
		if id == "" {
			id = "http-stream"
		}
		return json.Marshal(map[string]any{"status_code": script.status, "stream_id": id})
	}
	return json.Marshal(map[string]any{
		"StatusCode": script.status,
		"Body":       script.body,
		"Headers":    map[string][]string{"Content-Type": {"application/json"}},
	})
}

func (f *fakeHost) readStream(payload []byte) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, hostCall{method: pluginabi.MethodHostHTTPStreamRead, payload: payload})
	idx := f.readIndex
	chunk := readChunk{done: true}
	if idx < len(f.pending) {
		chunk = f.pending[idx]
		f.readIndex++
	}
	hold := f.holdFirstRead && idx == 0
	f.mu.Unlock()
	if hold {
		f.startedOnce.Do(func() {
			if f.readStarted != nil {
				close(f.readStarted)
			}
		})
		if f.releaseRead != nil {
			<-f.releaseRead
		}
	}
	done := chunk.done || chunk.err != ""
	return json.Marshal(map[string]any{"payload": chunk.payload, "error": chunk.err, "done": done})
}

func (f *fakeHost) markFinishedLocked() {
	if f.streamFinished == nil {
		return
	}
	f.finishOnce.Do(func() { close(f.streamFinished) })
}

func (f *fakeHost) release() {
	f.releaseOnce.Do(func() {
		if f.releaseRead != nil {
			close(f.releaseRead)
		}
	})
}

func (f *fakeHost) add(entry pluginapi.HostAuthFileEntry, doc any) {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	if entry.ID == "" {
		entry.ID = entry.AuthIndex
	}
	if entry.Name == "" {
		entry.Name = entry.AuthIndex + ".json"
	}
	f.files = append(f.files, entry)
	f.authJSON[entry.AuthIndex] = raw
}

func unsignedPlanJWT(t *testing.T, plan string) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": plan},
	})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

func (f *fakeHost) codex(index, token, plan string, edit func(*pluginapi.HostAuthFileEntry)) {
	entry := pluginapi.HostAuthFileEntry{AuthIndex: index, Provider: "codex", Type: "codex"}
	if edit != nil {
		edit(&entry)
	}
	f.add(entry, map[string]any{"access_token": token, "plan_type": plan, "email": "secret-" + index + "@example.com"})
}

func (f *fakeHost) snapshot() []hostCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]hostCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeHost) httpCalls(t *testing.T) []decodedHTTP {
	t.Helper()
	var out []decodedHTTP
	for _, call := range f.snapshot() {
		if call.method != pluginabi.MethodHostHTTPDo && call.method != pluginabi.MethodHostHTTPDoStream {
			continue
		}
		var decoded decodedHTTP
		if err := json.Unmarshal(call.payload, &decoded); err != nil {
			t.Fatalf("decode %s: %v", call.method, err)
		}
		decoded.RPC = call.method
		out = append(out, decoded)
	}
	return out
}

func (f *fakeHost) saw(method string) bool {
	return f.count(method) > 0
}

func (f *fakeHost) count(method string) int {
	n := 0
	for _, call := range f.snapshot() {
		if call.method == method {
			n++
		}
	}
	return n
}

func (f *fakeHost) assertNoHTTP(t *testing.T) {
	t.Helper()
	if calls := f.httpCalls(t); len(calls) != 0 {
		t.Fatalf("host http calls = %d, want 0", len(calls))
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func yamlReq(yml string) []byte {
	raw, err := json.Marshal(map[string]any{"config_yaml": []byte(yml), "schema_version": pluginabi.SchemaVersion})
	if err != nil {
		panic(err)
	}
	return raw
}

func mustConfigure(t *testing.T, yml string) {
	t.Helper()
	if _, err := handleMethod(pluginabi.MethodPluginReconfigure, yamlReq(yml)); err != nil {
		t.Fatalf("configure: %v", err)
	}
}

type rpcEnv struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *envelopeError  `json:"error"`
}

func callEnv(t *testing.T, method string, req any) (rpcEnv, []byte) {
	t.Helper()
	var rawReq []byte
	if req != nil {
		var err error
		rawReq, err = json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal %s: %v", method, err)
		}
	}
	raw, err := handleMethod(method, rawReq)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var env rpcEnv
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", method, err)
	}
	return env, raw
}

func callOK(t *testing.T, method string, req any) json.RawMessage {
	t.Helper()
	env, raw := callEnv(t, method, req)
	if !env.OK {
		t.Fatalf("%s not ok: %s", method, raw)
	}
	return env.Result
}

func execReq(format, model string, payload []byte) executorCall {
	return executorCall{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:   model,
			Format:  format,
			Payload: payload,
			Headers: http.Header{},
		},
		HostCallbackID: "cb-test",
	}
}

func mustExec(t *testing.T, format, model string, payload []byte) pluginapi.ExecutorResponse {
	t.Helper()
	result := callOK(t, pluginabi.MethodExecutorExecute, execReq(format, model, payload))
	var resp pluginapi.ExecutorResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("decode execute: %v", err)
	}
	return resp
}

func configSignature() string {
	cfg := currentConfig()
	raw, err := json.Marshal(map[string]any{
		"enabled":  cfg.Enabled,
		"base":     cfg.BaseURL,
		"prefix":   cfg.Prefix,
		"retry":    cfg.retry(),
		"fallback": cfg.fallbackRetry(),
		"mode":     cfg.RotationMode,
		"plus":     cfg.PlusPreference,
		"sticky":   cfg.stickySeconds(),
		"stats":    cfg.StatsFile,
		"priority": cfg.Priority,
		"headers":  cfg.Headers,
		"models":   cfg.Models,
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func bearer(t *testing.T, call decodedHTTP) string {
	t.Helper()
	got := call.Headers["Authorization"]
	if len(got) != 1 {
		t.Fatalf("Authorization = %#v", call.Headers)
	}
	return got[0]
}

func waitCh(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestDefaults(t *testing.T) {
	setup(t)
	mustConfigure(t, "")
	cfg := currentConfig()
	if cfg.BaseURL != defaultBaseURL {
		t.Fatalf("base-url = %q", cfg.BaseURL)
	}
	if cfg.retry() != 3 {
		t.Fatalf("request-retry = %d", cfg.retry())
	}
	if cfg.fallbackRetry() != 0 {
		t.Fatalf("fallback-retry = %d", cfg.fallbackRetry())
	}
	if cfg.RotationMode != "round-robin" {
		t.Fatalf("rotation-mode = %q", cfg.RotationMode)
	}
	if cfg.PlusPreference != "prefer" {
		t.Fatalf("plus-preference = %q", cfg.PlusPreference)
	}
	if cfg.stickySeconds() != 300 {
		t.Fatalf("sticky-seconds = %d", cfg.stickySeconds())
	}
	if cfg.modelCooldown() != 24*time.Hour {
		t.Fatalf("model cooldown = %s", cfg.modelCooldown())
	}
	if cfg.Prefix != "" {
		t.Fatalf("prefix = %q", cfg.Prefix)
	}
	var listed pluginapi.ModelResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 0 {
		t.Fatalf("models = %#v, want none", listed.Models)
	}
}

func TestPrefixAndPlusPreference(t *testing.T) {
	setup(t)
	mustConfigure(t, "prefix: /vippro/\n")
	if got := currentConfig().Prefix; got != "vippro" {
		t.Fatalf("prefix /vippro/ = %q", got)
	}
	mustConfigure(t, "prefix: a/b\nmodels:\n  - name: gpt-5.4\n    alias: gpt-5.4\n")
	if got := currentConfig().Prefix; got != "" {
		t.Fatalf("prefix a/b = %q", got)
	}
	var route pluginapi.ModelRouteResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRoute, routeCall{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "gpt-5.4"}}), &route); err != nil {
		t.Fatal(err)
	}
	if !route.Handled || route.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("empty prefix route = %+v", route)
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRoute, routeCall{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "a/b/gpt-5.4"}}), &route); err != nil {
		t.Fatal(err)
	}
	if route.Handled {
		t.Fatal("a/b prefix still claimed a path")
	}

	mustConfigure(t, "plus-preference: ignore\n")
	if got := currentConfig().PlusPreference; got != "off" {
		t.Fatalf("ignore stored as %q", got)
	}
	mustConfigure(t, "plus-preference: ONLY\n")
	if got := currentConfig().PlusPreference; got != "only" {
		t.Fatalf("ONLY stored as %q", got)
	}
}

func TestRejectInvalidConfigKeepsPrevious(t *testing.T) {
	const seed = `
enabled: true
base-url: https://good.example/v1
prefix: vip
request-retry: 1
rotation-mode: round-robin
plus-preference: prefer
sticky-seconds: 120
models:
  - name: gpt-5.4
    alias: gpt-5.4
`
	cases := []string{
		"base-url: http://bad.example/v1\n",
		"base-url: https://user:pass@bad.example/v1\n",
		"base-url: https://bad.example/v1?q=1\n",
		"base-url: https://bad.example/v1#frag\n",
		"base-url: https://bad.example/v1\nrotation-mode: spin\n",
		"base-url: https://bad.example/v1\nmodels:\n  - alias: gpt-5.4\n",
		"base-url: https://bad.example/v1\nheaders:\n  Authorization: Bearer nope\n",
		"base-url: https://bad.example/v1\napi-key: nope\n",
		"base-url: https://bad.example/v1\nkeys:\n  - nope\n",
		"base-url: https://bad.example/v1\n---\nprefix: other\n",
	}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			f := setup(t)
			f.codex("1", tokenA, "plus", nil)
			mustConfigure(t, seed)
			before := configSignature()
			if _, err := handleMethod(pluginabi.MethodPluginReconfigure, yamlReq(bad)); err != nil {
				t.Fatalf("invalid config unregistered the plugin: %v", err)
			}
			if got := configSignature(); got != before {
				t.Fatalf("config changed\nbefore %s\nafter  %s", before, got)
			}
			mustExec(t, "openai", "vip/gpt-5.4", []byte(`{"model":"vip/gpt-5.4"}`))
			calls := f.httpCalls(t)
			if len(calls) != 1 || calls[0].URL != "https://good.example/v1/chat/completions" {
				t.Fatalf("live upstream = %#v", calls)
			}
		})
	}

	t.Run("out of range retries are clamped", func(t *testing.T) {
		setup(t)
		mustConfigure(t, "enabled: true\nrequest-retry: 10\nfallback-retry: 9\nsticky-seconds: -1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		cfg := currentConfig()
		if cfg.retry() != 10 || cfg.fallbackRetry() != maxFallbackRetries || cfg.stickySeconds() != 0 {
			t.Fatalf("clamped retry=%d fallback=%d sticky=%d", cfg.retry(), cfg.fallbackRetry(), cfg.stickySeconds())
		}
	})

	t.Run("shared http base url is accepted", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		mustConfigure(t, "enabled: true\nbase-url: http://100.105.177.35:11500/v1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || calls[0].URL != "http://100.105.177.35:11500/v1/chat/completions" {
			t.Fatalf("shared http = %#v", calls)
		}
		if !privateBaseHost("100.64.0.0") || !privateBaseHost("100.127.255.255") {
			t.Fatal("shared address rejected")
		}
		if privateBaseHost("100.63.255.255") || privateBaseHost("100.128.0.1") || privateBaseHost("8.8.8.8") {
			t.Fatal("public address accepted for http")
		}
		for _, raw := range []string{
			"http://user:pass@100.86.237.127:11500/v1",
			"http://100.86.237.127:11500/v1?q=1",
			"http://100.86.237.127:11500/v1#frag",
		} {
			if _, err := validateBaseURL(raw); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		}
		if _, err := validateBaseURL("https://example.com/v1"); err != nil {
			t.Fatalf("https rejected: %v", err)
		}
		if _, err := validateBaseURL("https://100.86.237.127:11500/v1"); err != nil {
			t.Fatalf("https shared rejected: %v", err)
		}
	})

	t.Run("private http base url is accepted", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		mustConfigure(t, "enabled: true\nbase-url: http://192.168.8.1:11500/v1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || calls[0].URL != "http://192.168.8.1:11500/v1/chat/completions" {
			t.Fatalf("private http = %#v", calls)
		}
	})
}

func TestDuplicateModelPairKeepsFirst(t *testing.T) {
	setup(t)
	mustConfigure(t, `
enabled: true
models:
  - {name: gpt-5.4, alias: gpt-5.4, display-name: first}
  - {name: gpt-5.4, alias: gpt-5.4, display-name: second}
  - {name: " gpt-5.4 ", alias: " gpt-5.4 "}
  - {name: gpt-5.4-mini, alias: gpt-5.4}
`)
	models := currentConfig().Models
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Name != "gpt-5.4" || models[0].Alias != "gpt-5.4" || models[0].DisplayName != "first" {
		t.Fatalf("first = %+v", models[0])
	}
	if models[1].Name != "gpt-5.4-mini" || models[1].Alias != "gpt-5.4" {
		t.Fatalf("pool mate = %+v", models[1])
	}
	pools := currentConfig().pools()
	if len(pools) != 1 || len(pools[0].names) != 2 || pools[0].names[0] != "gpt-5.4" || pools[0].names[1] != "gpt-5.4-mini" {
		t.Fatalf("pools = %+v", pools)
	}
}

func TestDisabled(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	mustConfigure(t, `
enabled: false
prefix: vip
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	var listed pluginapi.ModelResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 0 {
		t.Fatalf("disabled models = %#v", listed.Models)
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelForAuth, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 0 {
		t.Fatalf("for_auth models = %#v", listed.Models)
	}
	var route pluginapi.ModelRouteResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRoute, routeCall{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "vip/gpt-5.4"}}), &route); err != nil {
		t.Fatal(err)
	}
	if route.Handled {
		t.Fatal("disabled plugin handled the route")
	}
	env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "vip/gpt-5.4", []byte(`{"model":"vip/gpt-5.4"}`)))
	if env.OK || env.Error == nil || env.Error.Code != "disabled" {
		t.Fatalf("execute = %+v", env.Error)
	}
	f.assertNoHTTP(t)
}

func TestRouteModelsAndIdentifier(t *testing.T) {
	setup(t)
	mustConfigure(t, `
enabled: true
prefix: vippro
models:
  - name: gpt-5.4
    alias: gpt-5.4
    display-name: GPT-5.4
  - name: gpt-5.4-mini
    alias: gpt-5.4
`)
	var listed pluginapi.ModelResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 {
		t.Fatalf("catalog len = %d, want 1", len(listed.Models))
	}
	model := listed.Models[0]
	if model.ID != "vippro/gpt-5.4" || model.Name != "gpt-5.4" || model.OwnedBy != pluginID || !model.UserDefined || model.DisplayName != "GPT-5.4" {
		t.Fatalf("model = %+v", model)
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelForAuth, map[string]any{"AuthID": "codex-1"}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 0 {
		t.Fatalf("for_auth = %#v", listed.Models)
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRegister, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 || listed.Models[0].ID != "vippro/gpt-5.4" {
		t.Fatalf("register = %#v", listed.Models)
	}

	assertRoute := func(requested string, handled bool) {
		t.Helper()
		var route pluginapi.ModelRouteResponse
		if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRoute, routeCall{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: requested}}), &route); err != nil {
			t.Fatal(err)
		}
		if route.Handled != handled {
			t.Fatalf("route %s handled = %v", requested, route.Handled)
		}
		if handled {
			if route.TargetKind != pluginapi.ModelRouteTargetSelf || route.Target != "" {
				t.Fatalf("route %s = %+v", requested, route)
			}
		}
	}
	assertRoute("vippro/gpt-5.4", true)
	assertRoute("vippro/gpt-5.4(high)", true)
	assertRoute("gpt-5.4", false)
	assertRoute("codex/gpt-5.4", false)

	var ident struct {
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodExecutorIdentifier, map[string]any{}), &ident); err != nil {
		t.Fatal(err)
	}
	if ident.Identifier != pluginID {
		t.Fatalf("identifier = %q", ident.Identifier)
	}

	mustConfigure(t, `
enabled: true
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	assertRoute("gpt-5.4", true)
	assertRoute("gpt-5.4(high)", true)
	assertRoute("vippro/gpt-5.4", false)

	mustConfigure(t, `
enabled: true
prefix: vippro
models:
  - name: gpt-5.6
    display-name: GPT-5.6
    image: true
    thinking:
      levels: ["low", "high"]
`)
	assertRoute("vippro/gpt-5.6", true)
	assertRoute("gpt-5.6", false)
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 || listed.Models[0].Thinking == nil {
		t.Fatalf("thinking capability = %+v", listed.Models)
	}
	if got := listed.Models[0].Thinking.Levels; len(got) != 2 || got[0] != "low" || got[1] != "high" {
		t.Fatalf("thinking levels = %#v", got)
	}
	mustConfigure(t, `
enabled: true
prefix: vippro
models:
  - name: gpt-6-astra
`)
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 || listed.Models[0].Thinking == nil {
		t.Fatalf("gpt-6-astra thinking capability = %+v", listed.Models)
	}
}

func TestExplicitThinkingWithoutLevelsDoesNotInherit(t *testing.T) {
	setup(t)
	mustConfigure(t, `
enabled: true
models:
  - name: gpt-6-sol
    alias: gpt-6-sol
    max-context-length: 128000
    input-modalities: [text, image]
    output-modalities: [text]
    force-mapping: true
    thinking:
      min: 0
      max: 100
`)
	var listed pluginapi.ModelResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 {
		t.Fatalf("models = %+v", listed.Models)
	}
	got := listed.Models[0]
	if got.ContextLength != 128000 || strings.Join(got.SupportedInputModalities, ",") != "text,image" || strings.Join(got.SupportedOutputModalities, ",") != "text" {
		t.Fatalf("catalog fields = %+v", got)
	}
	if got.Thinking == nil || got.Thinking.Min != 0 || got.Thinking.Max != 100 || len(got.Thinking.Levels) != 0 {
		t.Fatalf("thinking = %+v", got.Thinking)
	}
}

func TestUnconfiguredModelInheritsCanonicalThinkingLevels(t *testing.T) {
	setup(t)
	mustConfigure(t, `
enabled: true
prefix: vippro
models:
  - name: gpt-6-sol
`)
	var listed pluginapi.ModelResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 || listed.Models[0].Thinking == nil {
		t.Fatalf("gpt-6-sol thinking capability = %+v", listed.Models)
	}
	got := listed.Models[0].Thinking.Levels
	// Static models.json levels first, then the extra "ultra" level the Codex
	// client catalog lists for gpt-6-sol.
	want := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("thinking levels = %#v, want %#v", got, want)
	}
	if params := listed.Models[0].SupportedParameters; len(params) != 1 || params[0] != "service_tier" {
		t.Fatalf("supported parameters = %#v, want [service_tier]", params)
	}
}

func TestUnionLevelsKeepsOrderAndSkipsDuplicates(t *testing.T) {
	got := unionLevels([]string{"low", "high"}, []string{"High", "", "ultra", "low", "ultra"})
	if strings.Join(got, ",") != "low,high,ultra" {
		t.Fatalf("unionLevels = %#v", got)
	}
	if levels := codexClientLevels("gpt-6-astra"); len(levels) == 0 || levels[len(levels)-1] != "ultra" {
		t.Fatalf("codexClientLevels(gpt-6-astra) = %#v, want ultra last", levels)
	}
	if levels := codexClientLevels("no-such-model"); levels != nil {
		t.Fatalf("codexClientLevels(unknown) = %#v, want nil", levels)
	}
}

func TestPayloadRewriteAndReject(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	mustConfigure(t, `
enabled: true
prefix: vippro
request-retry: 0
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	const original = `{"temperature":0.2,"model":"vippro/gpt-5.4(high)","messages":[{"content":"model"}]}`
	mustExec(t, "openai", "vippro/gpt-5.4(high)", []byte(original))
	calls := f.httpCalls(t)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	const want = `{"temperature":0.2,"model":"gpt-5.4","messages":[{"content":"model"}],"reasoning_effort":"high"}`
	if string(calls[0].Body) != want {
		t.Fatalf("body = %s", calls[0].Body)
	}

	f.calls = nil
	rejected := []string{
		`{"Model":"vippro/gpt-5.4"}`,
		`{"model":"vippro/gpt-5.4","model":"other"}`,
		`{"model":null}`,
		`{"model":["vippro/gpt-5.4"]}`,
		`{"model":1}`,
	}
	for _, payload := range rejected {
		t.Run(payload, func(t *testing.T) {
			before := len(f.snapshot())
			env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "vippro/gpt-5.4", []byte(payload)))
			if env.OK {
				t.Fatal("invalid payload succeeded")
			}
			if got := len(f.httpCalls(t)); got != 0 {
				t.Fatalf("http calls = %d", got)
			}
			if len(f.snapshot()) != before {
				t.Fatal("host was called for an invalid payload")
			}
		})
	}
}

func TestAliasPool(t *testing.T) {
	t.Run("retry stays on the first name then the next request rotates", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{{status: http.StatusInternalServerError}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, `
enabled: true
request-retry: 1
plus-preference: off
models:
  - name: alpha
    alias: gpt
  - name: beta
    alias: gpt
`)
		var listed pluginapi.ModelResponse
		if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
			t.Fatal(err)
		}
		if len(listed.Models) != 1 || listed.Models[0].ID != "gpt" || listed.Models[0].Name != "gpt" || !strings.Contains(listed.Models[0].Description, "alpha") || !strings.Contains(listed.Models[0].Description, "beta") {
			t.Fatalf("catalog = %+v", listed.Models)
		}
		mustExec(t, "openai", "gpt", []byte(`{"model":"gpt","n":1}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 {
			t.Fatalf("calls = %d", len(calls))
		}
		if !strings.Contains(string(calls[0].Body), `"model":"alpha"`) || !strings.Contains(string(calls[1].Body), `"model":"alpha"`) {
			t.Fatalf("bodies = %s | %s", calls[0].Body, calls[1].Body)
		}
		if !strings.Contains(string(calls[0].Body), `"n":1`) {
			t.Fatalf("other bytes changed: %s", calls[0].Body)
		}
		mustExec(t, "openai", "gpt", []byte(`{"model":"gpt"}`))
		calls = f.httpCalls(t)
		if len(calls) != 3 || !strings.Contains(string(calls[2].Body), `"model":"beta"`) {
			t.Fatalf("next start = %s", calls[len(calls)-1].Body)
		}
	})

	t.Run("one account retries the same upstream name", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.scripts = []httpScript{{status: http.StatusInternalServerError}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, `
enabled: true
request-retry: 1
plus-preference: off
models:
  - name: alpha
    alias: gpt
  - name: beta
    alias: gpt
`)
		mustExec(t, "openai", "gpt", []byte(`{"model":"gpt"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || requestModel(t, calls[0]) != "alpha" || requestModel(t, calls[1]) != "alpha" {
			t.Fatalf("bodies = %s | %s", calls[0].Body, calls[len(calls)-1].Body)
		}
	})

	t.Run("retry 0 stops after the first name", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.scripts = []httpScript{{status: http.StatusInternalServerError}}
		mustConfigure(t, `
enabled: true
request-retry: 0
plus-preference: off
models:
  - name: alpha
    alias: gpt
  - name: beta
    alias: gpt
`)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt", []byte(`{"model":"gpt"}`)))
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusInternalServerError {
			t.Fatalf("execute = %+v", env.Error)
		}
		calls := f.httpCalls(t)
		if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"model":"alpha"`) || strings.Contains(string(calls[0].Body), `"model":"beta"`) {
			t.Fatalf("calls = %#v", calls)
		}
	})
}

func TestPickerFiltersPriorityRotation(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	t.Run("skips ineligible accounts", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return now }
		f.codex("0", "tok-gemini", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Provider = "gemini" })
		f.codex("1", "tok-disabled", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Disabled = true })
		f.codex("2", "tok-unavailable", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Unavailable = true })
		f.codex("3", "tok-future", "plus", func(e *pluginapi.HostAuthFileEntry) { e.NextRetryAfter = now.Add(time.Minute) })
		f.codex("4", "", "plus", nil)
		f.codex("5", "tok-good", "free", func(e *pluginapi.HostAuthFileEntry) { e.NextRetryAfter = now.Add(-time.Minute) })
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer tok-good" {
			t.Fatalf("calls = %#v", calls)
		}
		blob := string(calls[0].Body) + bearer(t, calls[0])
		for _, secret := range []string{"tok-gemini", "tok-disabled", "tok-unavailable", "tok-future"} {
			if strings.Contains(blob, secret) {
				t.Fatalf("used ineligible token %s", secret)
			}
		}
	})

	t.Run("highest priority only", func(t *testing.T) {
		f := setup(t)
		f.codex("1", "tok-low", "free", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 0 })
		f.codex("2", "tok-high", "free", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 2 })
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer tok-high" || bearer(t, calls[1]) != "Bearer tok-high" {
			t.Fatalf("bearers = %s %s", bearer(t, calls[0]), bearer(t, calls[1]))
		}
	})

	t.Run("round-robin keeps a successful account", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		var got []string
		for i := 0; i < 3; i++ {
			mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
			calls := f.httpCalls(t)
			got = append(got, bearer(t, calls[len(calls)-1]))
		}
		want := []string{"Bearer " + tokenA, "Bearer " + tokenA, "Bearer " + tokenA}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("round-robin = %v", got)
		}
	})

	t.Run("round-robin switches after one upstream error and stays", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadGateway, body: []byte(`{"error":"bad"}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 3 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB || bearer(t, calls[2]) != "Bearer "+tokenB {
			t.Fatalf("switch bearers, calls=%d", len(calls))
		}
	})

	t.Run("client cancel does not switch account", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadGateway, body: []byte("execute host http request: context canceled")},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || len(f.httpCalls(t)) != 1 || bearer(t, f.httpCalls(t)[0]) != "Bearer "+tokenA {
			t.Fatalf("cancel switched, status=%v calls=%d", env.Error, len(f.httpCalls(t)))
		}
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[1]) != "Bearer "+tokenA {
			t.Fatalf("next bearer = %s", bearer(t, calls[1]))
		}
	})

	t.Run("fill-first keeps the first account", func(t *testing.T) {
		f := setup(t)
		early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		late := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		f.codex("1", tokenA, "free", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = late })
		f.codex("2", tokenB, "free", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = early })
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: fill-first\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenA {
			t.Fatalf("fill-first bearers changed")
		}
	})

	t.Run("least-recent uses the earliest UpdatedAt", func(t *testing.T) {
		f := setup(t)
		early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		late := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		f.codex("1", tokenA, "free", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = late })
		f.codex("2", tokenB, "free", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = early })
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: least-recent\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenB {
			t.Fatalf("least-recent bearer = %s", bearer(t, calls[0]))
		}
	})
}

func TestSticky(t *testing.T) {
	const yml = `
enabled: true
plus-preference: off
rotation-mode: sticky
sticky-seconds: 300
request-retry: 0
models:
  - name: gpt-5.4
    alias: gpt-5.4
`
	start := time.Date(2026, 1, 2, 15, 0, 0, 0, time.UTC)

	t.Run("same session sticks until the injected clock expires", func(t *testing.T) {
		f := setup(t)
		current := start
		nowFn = func() time.Time { return current }
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, yml)
		send := func() string {
			t.Helper()
			req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
			req.Metadata = map[string]any{"execution_session_id": "sess-a"}
			callOK(t, pluginabi.MethodExecutorExecute, req)
			calls := f.httpCalls(t)
			return bearer(t, calls[len(calls)-1])
		}
		if got := send(); got != "Bearer "+tokenA {
			t.Fatalf("first = %s", got)
		}
		current = start.Add(299 * time.Second)
		if got := send(); got != "Bearer "+tokenA {
			t.Fatalf("within ttl = %s", got)
		}
		current = start.Add(301 * time.Second)
		if got := send(); got != "Bearer "+tokenB {
			t.Fatalf("after ttl = %s", got)
		}
	})

	t.Run("different sessions can use different accounts", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, yml)
		send := func(session string) string {
			t.Helper()
			req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
			req.Metadata = map[string]any{"execution_session_id": session}
			callOK(t, pluginabi.MethodExecutorExecute, req)
			calls := f.httpCalls(t)
			return bearer(t, calls[len(calls)-1])
		}
		if send("sess-a") != "Bearer "+tokenA || send("sess-b") != "Bearer "+tokenB {
			t.Fatal("sessions did not diverge")
		}
	})

	t.Run("a failed first account does not stick", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{{status: http.StatusBadGateway, body: []byte(`{"error":"down"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, strings.Replace(yml, "request-retry: 0", "request-retry: 1", 1))
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.Metadata = map[string]any{"execution_session_id": "sess-a"}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		callOK(t, pluginabi.MethodExecutorExecute, req)
		calls := f.httpCalls(t)
		if len(calls) < 3 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[len(calls)-1]) != "Bearer "+tokenB {
			t.Fatalf("failed account stuck, calls=%d last=%s", len(calls), bearer(t, calls[len(calls)-1]))
		}
	})

	t.Run("zero sticky seconds leaves no bind", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "free", nil)
		mustConfigure(t, strings.Replace(yml, "sticky-seconds: 300", "sticky-seconds: 0", 1))
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.Metadata = map[string]any{"execution_session_id": "sess-a"}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		runtimeMu.Lock()
		binds := len(runtime.sticky)
		runtimeMu.Unlock()
		if binds != 0 {
			t.Fatalf("sticky binds = %d", binds)
		}
	})

	t.Run("unavailable sticky account is replaced without sleeping", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, yml)
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.Metadata = map[string]any{"execution_session_id": "sess-a"}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		for i := range f.files {
			if f.files[i].AuthIndex == "1" {
				f.files[i].Unavailable = true
			}
		}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("replacement bearers = %s %s", bearer(t, calls[0]), bearer(t, calls[1]))
		}
	})
}

func TestClassifyCountsModelCooldown(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	markModelCooldown("1", tokenA, "gpt-5.4", time.Hour, "")
	kind, plus := classifyEnabled(f.files[0], "cb", now)
	if kind != kindCooling || plus {
		t.Fatalf("cooled classify = %v plus=%v", kind, plus)
	}
}

func TestPlusPreference(t *testing.T) {
	const models = "models:\n  - {name: gpt-5.4, alias: gpt-5.4}\n"
	t.Run("off can select plus and non-plus", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "pro", nil)
		f.scripts = []httpScript{{status: http.StatusBadGateway, body: []byte(`{"error":"upstream"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 1\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatal("off mode did not use the pro account after an upstream error")
		}
	})

	t.Run("prefer does not use non-plus while a plus account exists", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "team", nil)
		f.scripts = []httpScript{{status: http.StatusBadGateway, body: []byte(`{"error":"upstream"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 1\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenA {
			t.Fatalf("prefer stayed on plus = %#v", calls)
		}
	})

	t.Run("prefer fails while enabled plus accounts are cooling", func(t *testing.T) {
		f := setup(t)
		now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.NextRetryAfter = now.Add(time.Hour) })
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 3\n"+models)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || env.Error == nil || env.Error.Code != "no_plus_account" {
			t.Fatalf("cooling plus = %+v", env.Error)
		}
		f.assertNoHTTP(t)
	})

	t.Run("prefer uses free only when no enabled plus exists", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Disabled = true })
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 0\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenB {
			t.Fatalf("prefer without plus = %#v", calls)
		}
	})

	t.Run("prefer skips a free bearer even when plan_type says plus", func(t *testing.T) {
		f := setup(t)
		dir := t.TempDir()
		plusPath := filepath.Join(dir, "plus.json")
		freePath := filepath.Join(dir, "free.json")
		if err := os.WriteFile(plusPath, []byte(`{"plan_type":"plus","access_token":"`+tokenA+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(freePath, []byte(`{"plan_type":"plus","access_token":"`+unsignedPlanJWT(t, "free")+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Path = plusPath })
		f.codex("2", tokenB, "free", func(e *pluginapi.HostAuthFileEntry) { e.Path = freePath })
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 3\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA {
			t.Fatalf("file plan prefer = %#v", calls)
		}
		for _, call := range f.snapshot() {
			if call.method == pluginabi.MethodHostAuthGet {
				t.Fatalf("path already had the token: %s", call.payload)
			}
		}
	})

	t.Run("only uses the access token plan and ignores id_token and plan_type", func(t *testing.T) {
		f := setup(t)
		freeBearer := unsignedPlanJWT(t, "free")
		plusBearer := unsignedPlanJWT(t, "plus")
		f.add(pluginapi.HostAuthFileEntry{AuthIndex: "1", Provider: "codex", Type: "codex"}, map[string]any{
			"access_token": freeBearer,
			"plan_type":    "plus",
			"id_token":     unsignedPlanJWT(t, "plus"),
		})
		f.add(pluginapi.HostAuthFileEntry{AuthIndex: "2", Provider: "codex", Type: "codex"}, map[string]any{
			"access_token": plusBearer,
			"plan_type":    "free",
			"id_token":     unsignedPlanJWT(t, "free"),
		})
		mustConfigure(t, "enabled: true\nplus-preference: only\nrequest-retry: 3\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+plusBearer {
			t.Fatalf("jwt plan = %#v", calls)
		}
	})

	t.Run("prefer retries another plus account", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.codex("3", "tok-free", "free", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 1\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("prefer plus retry = %#v", calls)
		}
	})

	t.Run("only does not call http without plus and pro is not plus", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "pro", nil)
		mustConfigure(t, "enabled: true\nplus-preference: only\nrequest-retry: 3\n"+models)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || env.Error == nil || env.Error.Code != "no_plus_account" {
			t.Fatalf("only/pro = %+v", env.Error)
		}
		f.assertNoHTTP(t)
	})
}

func TestFreeModelsOnlyListedUsesFreeAccount(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenB, "free", nil)
	mustConfigure(t, `
enabled: true
plus-preference: only
request-retry: 0
free-models: [gpt-6-luna]
models:
  - {name: gpt-6-luna, alias: luna}
`)
	mustExec(t, "openai", "luna", []byte(`{"model":"luna"}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenB || requestModel(t, calls[0]) != "gpt-6-luna" {
		t.Fatalf("listed free model calls = %#v", calls)
	}
}

func TestFreeModelsOnlyUnlistedStaysPlusOnly(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "free", nil)
	mustConfigure(t, `
enabled: true
plus-preference: only
request-retry: 0
free-models: [gpt-6-luna]
models:
  - {name: gpt-6-sol, alias: sol}
  - {name: gpt-6-luna, alias: luna}
`)
	mustExec(t, "openai", "sol", []byte(`{"model":"sol"}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA || requestModel(t, calls[0]) != "gpt-6-sol" {
		t.Fatalf("unlisted model calls = %#v", calls)
	}
	f.files[0].Unavailable = true
	env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "sol", []byte(`{"model":"sol"}`)))
	if env.OK || env.Error == nil || env.Error.Code != "no_plus_account" {
		t.Fatalf("unlisted model without plus = %+v", env.Error)
	}
	if calls := f.httpCalls(t); len(calls) != 1 {
		t.Fatalf("unlisted model called http again: %d", len(calls))
	}
}

func TestFreeModelsPreferCoolingPlusListedUsesFree(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.NextRetryAfter = now.Add(time.Hour) })
	f.codex("2", tokenB, "free", nil)
	mustConfigure(t, `
enabled: true
plus-preference: prefer
request-retry: 0
free-models: [gpt-6-luna]
models:
  - {name: gpt-6-luna, alias: luna}
`)
	mustExec(t, "openai", "luna", []byte(`{"model":"luna"}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenB || requestModel(t, calls[0]) != "gpt-6-luna" {
		t.Fatalf("cooling plus listed calls = %#v", calls)
	}
}

func TestFreeModelsPreferCoolingPlusUnlistedFails(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.NextRetryAfter = now.Add(time.Hour) })
	f.codex("2", tokenB, "free", nil)
	mustConfigure(t, `
enabled: true
plus-preference: prefer
request-retry: 0
free-models: [gpt-6-luna]
models:
  - {name: gpt-6-sol, alias: sol}
  - {name: gpt-6-luna, alias: luna}
`)
	env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "sol", []byte(`{"model":"sol"}`)))
	if env.OK || env.Error == nil || env.Error.Code != "no_plus_account" {
		t.Fatalf("cooling plus unlisted = %+v", env.Error)
	}
	f.assertNoHTTP(t)
}

func TestFreeModelsListedFallbackUsesFreeAccount(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "free", nil)
	refusal := `{"detail":"The 'requested-model' MODEL IS NOT SUPPORTED when using Codex with a ChatGPT account."}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(refusal)},
		{status: http.StatusBadRequest, body: []byte(refusal)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: prefer
request-retry: 0
fallback-retry: 2
free-models: [gpt-6-luna]
models:
  - {name: gpt-6-sol, alias: sol, fallback: [gpt-6-luna]}
`)
	mustExec(t, "openai", "sol", []byte(`{"model":"sol"}`))
	calls := f.httpCalls(t)
	if len(calls) != 3 {
		t.Fatalf("fallback calls = %d", len(calls))
	}
	if bearer(t, calls[0]) != "Bearer "+tokenA || requestModel(t, calls[0]) != "gpt-6-sol" {
		t.Fatalf("primary attempt = %s %s", bearer(t, calls[0]), requestModel(t, calls[0]))
	}
	if bearer(t, calls[1]) != "Bearer "+tokenA || requestModel(t, calls[1]) != "gpt-6-luna" {
		t.Fatalf("plus fallback attempt = %s %s", bearer(t, calls[1]), requestModel(t, calls[1]))
	}
	if bearer(t, calls[2]) != "Bearer "+tokenB || requestModel(t, calls[2]) != "gpt-6-luna" {
		t.Fatalf("free fallback attempt = %s %s", bearer(t, calls[2]), requestModel(t, calls[2]))
	}
}

func TestFreeModelsNormalize(t *testing.T) {
	cfg, errDecode := decodeConfig([]byte("free-models: [\" gpt-6-luna \", \"\", \"GPT-6-LUNA\", \"gpt-6-sol\", \"  \"]\n"))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if got := strings.Join(cfg.FreeModels, ","); got != "gpt-6-luna,gpt-6-sol" {
		t.Fatalf("normalized free-models = %q", got)
	}
	cfg, errDecode = decodeConfig(nil)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if cfg.FreeModels != nil {
		t.Fatalf("default free-models = %#v", cfg.FreeModels)
	}
	cfg, errDecode = decodeConfig([]byte("enabled: true\nmodels:\n  - {name: gpt-6-luna, alias: luna}\n"))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if cfg.FreeModels != nil || len(cfg.Models) != 1 {
		t.Fatalf("old config changed: free=%#v models=%d", cfg.FreeModels, len(cfg.Models))
	}
}

func TestFreeModelsMatchesUpstreamNameNotAlias(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenB, "free", nil)
	const models = "models:\n  - {name: gpt-6-luna, alias: luna}\n"
	mustConfigure(t, "enabled: true\nplus-preference: only\nrequest-retry: 0\nfree-models: [luna]\n"+models)
	env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "luna", []byte(`{"model":"luna"}`)))
	if env.OK || env.Error == nil || env.Error.Code != "no_plus_account" {
		t.Fatalf("alias listed = %+v", env.Error)
	}
	f.assertNoHTTP(t)
	mustConfigure(t, "enabled: true\nplus-preference: only\nrequest-retry: 0\nfree-models: [GPT-6-LUNA]\n"+models)
	mustExec(t, "openai", "luna", []byte(`{"model":"luna"}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenB || requestModel(t, calls[0]) != "gpt-6-luna" {
		t.Fatalf("upstream name listed = %#v", calls)
	}
}

func TestHTTPHeadersRetryAndURLs(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	mustConfigure(t, `
enabled: true
request-retry: 0
headers:
  X-Claude-Code-Session-Id: "$ABC"
  X-Missing: "$NONEXISTENT"
  X-Static: static-value
  X-Session: "$CPA-SESSION-ID"
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	req.Headers = http.Header{
		"Abc":           []string{"session-abc-value"},
		"Authorization": []string{"Bearer client-secret"},
	}
	req.Metadata = map[string]any{"execution_session_id": "sess-1"}
	callOK(t, pluginabi.MethodExecutorExecute, req)
	calls := f.httpCalls(t)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	call := calls[0]
	if call.Method != http.MethodPost || call.URL != defaultBaseURL+"/chat/completions" {
		t.Fatalf("request = %s %s", call.Method, call.URL)
	}
	if call.HostCallbackID != "cb-test" {
		t.Fatalf("host_callback_id = %q", call.HostCallbackID)
	}
	if bearer(t, call) != "Bearer "+tokenA {
		t.Fatalf("authorization = %s", bearer(t, call))
	}
	if strings.Contains(bearer(t, call), "client-secret") {
		t.Fatal("forwarded client authorization")
	}
	if call.Headers["X-Claude-Code-Session-Id"][0] != "session-abc-value" {
		t.Fatalf("$ABC = %#v", call.Headers["X-Claude-Code-Session-Id"])
	}
	if _, ok := call.Headers["X-Missing"]; ok {
		t.Fatalf("missing header was sent: %#v", call.Headers["X-Missing"])
	}
	if call.Headers["X-Static"][0] != "static-value" {
		t.Fatalf("static = %#v", call.Headers["X-Static"])
	}
	if call.Headers["X-Session"][0] != "sess-1" {
		t.Fatalf("session = %#v", call.Headers["X-Session"])
	}
	if call.Headers["Content-Type"][0] != "application/json" {
		t.Fatalf("content-type = %#v", call.Headers["Content-Type"])
	}
	streamReq := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	streamReq.StreamID = "exec-headers"
	f.scriptUsed = 0
	f.scripts = []httpScript{{status: http.StatusOK, streamID: "http-headers", chunks: []readChunk{{payload: []byte("data: {\"ok\":true}\n"), done: true}}}}
	f.streamFinished = make(chan struct{})
	callOK(t, pluginabi.MethodExecutorExecuteStream, streamReq)
	waitCh(t, f.streamFinished, "header stream")
	streamCall := f.httpCalls(t)[len(f.httpCalls(t))-1]
	if streamCall.Headers["Accept"][0] != "text/event-stream" || streamCall.Headers["Cache-Control"][0] != "no-cache" {
		t.Fatalf("stream headers = %#v", streamCall.Headers)
	}
	if bearer(t, streamCall) != "Bearer "+tokenA {
		t.Fatalf("stream authorization = %s", bearer(t, streamCall))
	}

	t.Run("session header and spoofed CPA-SESSION-ID", func(t *testing.T) {
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.Headers = http.Header{"Session-Id": []string{"from-header"}, "CPA-SESSION-ID": []string{"spoof"}}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		got := f.httpCalls(t)
		last := got[len(got)-1]
		if last.Headers["X-Session"][0] != "from-header" {
			t.Fatalf("header session = %#v", last.Headers["X-Session"])
		}

		req = execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.Headers = http.Header{"CPA-SESSION-ID": []string{"spoof"}}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		got = f.httpCalls(t)
		last = got[len(got)-1]
		if _, ok := last.Headers["X-Session"]; ok {
			t.Fatalf("spoofed CPA-SESSION-ID was copied: %#v", last.Headers["X-Session"])
		}
	})

	t.Run("responses url", func(t *testing.T) {
		before := len(f.httpCalls(t))
		mustExec(t, "openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		got := f.httpCalls(t)
		if got[len(got)-1].URL != defaultBaseURL+"/responses" || len(got) != before+1 {
			t.Fatalf("responses url = %s", got[len(got)-1].URL)
		}
	})

	t.Run("retry 0 stops after one 429", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nrequest-retry: 0\nplus-preference: off\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusTooManyRequests {
			t.Fatalf("429 = %+v", env.Error)
		}
		if len(f.httpCalls(t)) != 1 {
			t.Fatalf("http calls = %d", len(f.httpCalls(t)))
		}
	})

	t.Run("retry 1 moves to the next account after 429", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nrequest-retry: 1\nplus-preference: off\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("429 failover bearers mismatch, calls=%d", len(calls))
		}
	})

	t.Run("401 switches account and does not refresh", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{{status: http.StatusUnauthorized, body: []byte(`{"error":"unauthorized"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nrequest-retry: 1\nplus-preference: off\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatal("401 did not switch accounts")
		}
		if f.saw(pluginabi.MethodHostAuthSave) || f.saw("auth.refresh") || f.saw(pluginabi.MethodAuthRefresh) {
			t.Fatal("401 refreshed or saved auth")
		}
	})
}

func TestHTTP200ModelRefusalDoesNotPin(t *testing.T) {
	const unsupported = `{"detail":"The 'gpt-5.4' model is not supported when using Codex with a ChatGPT account."}`
	f := setup(t)
	f.codex("1", tokenA, "free", nil)
	f.codex("2", tokenB, "free", nil)
	f.scripts = []httpScript{
		{status: http.StatusOK, body: []byte(unsupported)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: round-robin
request-retry: 0
model-cooldown-minutes: 0
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	calls := f.httpCalls(t)
	if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
		t.Fatalf("next bearer stayed, calls=%d", len(calls))
	}

	f2 := setup(t)
	f2.codex("1", tokenA, "free", nil)
	f2.codex("2", tokenB, "free", nil)
	f2.scripts = []httpScript{{status: http.StatusOK, body: []byte(unsupported)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: sticky
sticky-seconds: 300
request-retry: 0
model-cooldown-minutes: 0
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	req.Metadata = map[string]any{"execution_session_id": "sess-refuse"}
	callOK(t, pluginabi.MethodExecutorExecute, req)
	runtimeMu.Lock()
	_, bound := runtime.sticky["sess-refuse"]
	runtimeMu.Unlock()
	if bound {
		t.Fatal("refused account was bound sticky")
	}
}

func TestPlusFileCacheDoesNotReuseToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	write := func(token string) {
		t.Helper()
		body := []byte(`{"access_token":"` + token + `","plan_type":"plus"}`)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	const first = "tok-account-a"
	const second = "tok-account-c"
	write(first)
	f := setup(t)
	f.codex("1", second, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Path = path })
	mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	write(second)
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	calls := f.httpCalls(t)
	gets := 0
	for _, call := range f.snapshot() {
		if call.method == pluginabi.MethodHostAuthGet {
			gets++
		}
	}
	// The file is read on every request, so a rewrite with the same mtime and
	// size is seen without the per-account host auth.get round trip.
	if gets != 0 || len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+first || bearer(t, calls[1]) != "Bearer "+second {
		t.Fatalf("gets=%d second bearer = %s", gets, bearer(t, calls[1]))
	}
}

func TestChatGPTModelCooldown(t *testing.T) {
	f := setup(t)
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	now := start
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	const unsupported = `{"detail":"The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account."}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(unsupported)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
model-cooldown-minutes: 60
models:
  - name: primary-model
    alias: primary-model
`)
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	calls := f.httpCalls(t)
	if len(calls) != 3 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB || bearer(t, calls[2]) != "Bearer "+tokenB {
		t.Fatalf("cooled bearers, calls=%d", len(calls))
	}
	now = start.Add(61 * time.Minute)
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	calls = f.httpCalls(t)
	if len(calls) != 4 || bearer(t, calls[3]) != "Bearer "+tokenA {
		t.Fatalf("expired cooldown bearer, calls=%d", len(calls))
	}
}

func TestUpstreamCacheKeepsStatsAndModels(t *testing.T) {
	setup(t)
	path := filepath.Join(t.TempDir(), "stats.json")
	raw := `{"requests":9,"success":8,"failed":1,"since":"2026-10-09T00:00:00Z","buckets":[{"start":"2026-10-16T16:00:00Z","requests":9,"success":8,"failed":1}],"usage_by_email":[{"email":"a@b.com","success":8,"failed":1}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	mustConfigure(t, `
enabled: true
base-url: http://127.0.0.1:11500/v1
prefix: vippro
stats-file: `+strconv.Quote(path)+`
upstream-cache: false
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	cfg := currentConfig()
	if cfg.Prefix != "vippro" || cfg.BaseURL != "http://127.0.0.1:11500/v1" || len(cfg.Models) != 1 {
		t.Fatalf("live config dropped: prefix=%q base=%q models=%d", cfg.Prefix, cfg.BaseURL, len(cfg.Models))
	}
	snap := statsSnapshot()
	if snap.Requests != 9 || len(snap.UsageByEmail) != 1 {
		t.Fatalf("stats not loaded requests=%d emails=%d", snap.Requests, len(snap.UsageByEmail))
	}
}

func TestChatGPTModelCooldownStreamKeepsRawBody(t *testing.T) {
	const unsupported = `{"detail":"The 'gpt-6-astra' model is not supported when using Codex with a ChatGPT account."}`
	f := setup(t)
	streamClosed := make(chan struct{}, 2)
	previousHost := hostCallOverride
	hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
		raw, errCall := previousHost(method, payload)
		if method == pluginabi.MethodHostStreamClose {
			streamClosed <- struct{}{}
		}
		return raw, errCall
	}
	start := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return start }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, streamID: "http-err-1", chunks: []readChunk{{payload: []byte(unsupported), done: true}}},
		{status: http.StatusOK, streamID: "http-ok-1", chunks: []readChunk{{payload: []byte("data: {\"ok\":true}\n\n"), done: true}}},
		{status: http.StatusOK, streamID: "http-ok-2", chunks: []readChunk{{payload: []byte("data: {\"ok\":true}\n\n"), done: true}}},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
model-cooldown-minutes: 1440
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	req := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	req.StreamID = "exec-1"
	callOK(t, pluginabi.MethodExecutorExecuteStream, req)
	req2 := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	req2.StreamID = "exec-2"
	callOK(t, pluginabi.MethodExecutorExecuteStream, req2)
	calls := f.httpCalls(t)
	if len(calls) != 3 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB || bearer(t, calls[2]) != "Bearer "+tokenB {
		t.Fatalf("stream cooldown bearers, calls=%d", len(calls))
	}
	waitCh(t, streamClosed, "first success stream close")
	waitCh(t, streamClosed, "second success stream close")

	f2 := setup(t)
	nowFn = func() time.Time { return start }
	f2.codex("1", tokenA, "plus", nil)
	f2.scripts = []httpScript{
		{status: http.StatusBadRequest, streamID: "http-err-raw", chunks: []readChunk{{payload: []byte(unsupported), done: true}}},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
model-cooldown-minutes: 1440
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	fail := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	fail.StreamID = "exec-fail"
	env, raw := callEnv(t, pluginabi.MethodExecutorExecuteStream, fail)
	if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadRequest || env.Error.Message != unsupported {
		t.Fatalf("raw stream error = %s", raw)
	}
}

func TestChatGPTModelCooldownSurvivesRestart(t *testing.T) {
	const unsupported = `{"detail":"The 'gpt-6-astra' model is not supported when using Codex with a ChatGPT account."}`
	stats := filepath.Join(t.TempDir(), "stats.json")
	cfg := `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
model-cooldown-minutes: 1440
stats-file: ` + strconv.Quote(stats) + `
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`
	f := setup(t)
	start := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return start }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(unsupported)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, cfg)
	mustExec(t, "openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	raw, errRead := os.ReadFile(stats + ".cooldowns.json")
	if errRead != nil {
		t.Fatal(errRead)
	}
	if strings.Contains(string(raw), tokenA) || strings.Contains(string(raw), tokenB) {
		t.Fatalf("cooldown file stored a token: %s", raw)
	}
	if !strings.Contains(string(raw), "chatgpt_model_unsupported") {
		t.Fatalf("cooldown file = %s", raw)
	}

	f2 := setup(t)
	nowFn = func() time.Time { return start.Add(time.Hour) }
	f2.codex("1", tokenA, "plus", nil)
	f2.codex("2", tokenB, "plus", nil)
	f2.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, cfg)
	mustExec(t, "openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	calls := f2.httpCalls(t)
	if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenB {
		t.Fatalf("restarted cooldown bearer, calls=%d", len(calls))
	}

	f3 := setup(t)
	nowFn = func() time.Time { return start.Add(time.Hour) }
	f3.codex("1", tokenA, "plus", nil)
	mustConfigure(t, "enabled: true\nstats-file: \"\"\nmodels:\n  - {name: gpt-6-astra, alias: gpt-6-astra}\n")
	if cooled("1", tokenA, "gpt-6-astra", nowFn()) {
		t.Fatal("empty stats-file kept the previous cooldown")
	}

	other := filepath.Join(t.TempDir(), "other-stats.json")
	mustConfigure(t, "enabled: true\nstats-file: "+strconv.Quote(other)+"\nmodels:\n  - {name: gpt-6-astra, alias: gpt-6-astra}\n")
	if cooled("1", tokenA, "gpt-6-astra", nowFn()) {
		t.Fatal("missing cooldown sidecar kept the previous cooldown")
	}
}

func TestChatGPTModelCooldownHTTP200Stream(t *testing.T) {
	const unsupported = `{"detail":"The 'gpt-6-astra' model is not supported when using Codex with a ChatGPT account."}`
	f := setup(t)
	closed := make(chan struct{}, 1)
	previousHost := hostCallOverride
	hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
		raw, errCall := previousHost(method, payload)
		if method == pluginabi.MethodHostStreamClose {
			closed <- struct{}{}
		}
		return raw, errCall
	}
	nowFn = func() time.Time { return time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC) }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusOK, streamID: "http-200-err", chunks: []readChunk{
			{payload: []byte(`data: {"detail":"The 'gpt-6-astra' model is not sup`)},
			{payload: []byte(`ported when using Codex with a ChatGPT account."}` + "\n\n"), done: true},
		}},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 0
model-cooldown-minutes: 1440
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	req := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	req.StreamID = "exec-200-err"
	callOK(t, pluginabi.MethodExecutorExecuteStream, req)
	waitCh(t, closed, "200 stream close")
	mustExec(t, "openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	calls := f.httpCalls(t)
	if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
		t.Fatalf("200 stream cooldown bearers, calls=%d", len(calls))
	}
}

func TestOpenAICompatPayloads(t *testing.T) {
	var pending []byte
	var param any
	raw := "data: {\"a\":1}\ndata: {\"b\":2}\ndata: [DONE]\n"
	got := openAICompatPayloads(&pending, []byte(raw), true, &param)
	if len(got) != 2 || string(got[0]) != `{"a":1}` || string(got[1]) != `{"b":2}` || len(pending) != 0 {
		t.Fatalf("frames = %d pending=%d", len(got), len(pending))
	}
	pending = nil
	param = nil
	if parts := openAICompatPayloads(&pending, []byte(`data: {"a":`), false, &param); len(parts) != 0 {
		t.Fatalf("partial = %#v", parts)
	}
	got = openAICompatPayloads(&pending, []byte("1}\n"), false, &param)
	if len(got) != 1 || string(got[0]) != `{"a":1}` || len(pending) != 0 {
		t.Fatalf("joined = %#v pending=%q", got, pending)
	}
	pending = nil
	param = nil
	got = openAICompatPayloads(&pending, []byte(": keep-alive\n\ndata: {\"c\":3}\n"), true, &param)
	if len(got) != 1 || string(got[0]) != `{"c":3}` {
		t.Fatalf("keepalive = %#v", got)
	}
}

func TestStream(t *testing.T) {
	t.Run("429 at open is an rpc error", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}}
		mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-stream-1"
		env, raw := callEnv(t, pluginabi.MethodExecutorExecuteStream, req)
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusTooManyRequests {
			t.Fatalf("stream 429 = %+v", env.Error)
		}
		if strings.Contains(string(raw), "text/event-stream") {
			t.Fatalf("429 returned sse: %s", raw)
		}
		calls := f.httpCalls(t)
		if len(calls) != 1 || calls[0].RPC != pluginabi.MethodHostHTTPDoStream {
			t.Fatalf("stream open calls = %#v", calls)
		}
		if f.saw(pluginabi.MethodHostStreamEmit) || f.saw(pluginabi.MethodHostHTTPStreamRead) {
			t.Fatal("failed open emitted or read a stream")
		}
	})

	t.Run("headers return before emit and a mid-stream error closes both ids", func(t *testing.T) {
		f := setup(t)
		f.codex("1", "tok-secret-visible", "plus", nil)
		f.holdFirstRead = true
		f.readStarted = make(chan struct{})
		f.releaseRead = make(chan struct{})
		f.streamFinished = make(chan struct{})
		f.scripts = []httpScript{
			{status: http.StatusOK, streamID: "http-stream-1", chunks: []readChunk{
				{payload: []byte("data: tok-secret-visible\n\n")},
				{err: "boom", done: true},
			}},
			{status: http.StatusOK, streamID: "http-stream-2", chunks: []readChunk{{payload: []byte("data: next\n\n"), done: true}}},
		}
		mustConfigure(t, "enabled: true\nrequest-retry: 3\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-stream-1"
		req.HostCallbackID = "cb-stream"
		type asyncResult struct {
			env rpcEnv
			raw []byte
			err error
		}
		done := make(chan asyncResult, 1)
		go func() {
			rawReq, errMarshal := json.Marshal(req)
			if errMarshal != nil {
				done <- asyncResult{err: errMarshal}
				return
			}
			raw, errCall := handleMethod(pluginabi.MethodExecutorExecuteStream, rawReq)
			result := asyncResult{raw: raw, err: errCall}
			if errCall == nil {
				_ = json.Unmarshal(raw, &result.env)
			}
			done <- result
		}()
		waitCh(t, f.readStarted, "upstream read")
		for _, call := range f.snapshot() {
			if call.method != pluginabi.MethodHostStreamEmit {
				continue
			}
			var ref struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			if string(ref.Payload) != string(chatStreamKeepAlive) {
				t.Fatalf("emit before upstream data = %s", ref.Payload)
			}
		}
		f.release()
		var got asyncResult
		select {
		case got = <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("execute_stream did not return headers after the first chunk")
		}
		if got.err != nil {
			t.Fatalf("execute_stream: %v", got.err)
		}
		if !got.env.OK || !strings.Contains(string(got.raw), "text/event-stream") {
			t.Fatalf("stream headers = %s", got.raw)
		}
		waitCh(t, f.streamFinished, "stream close")

		var readID, emitID, httpCloseID, execCloseID string
		var emitPayload []byte
		for _, call := range f.snapshot() {
			var ref struct {
				StreamID       string `json:"stream_id"`
				HostCallbackID string `json:"host_callback_id"`
				Payload        []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			switch call.method {
			case pluginabi.MethodHostHTTPStreamRead:
				readID = ref.StreamID
				if ref.HostCallbackID != "cb-stream" {
					t.Fatalf("read callback = %q", ref.HostCallbackID)
				}
			case pluginabi.MethodHostStreamEmit:
				emitID = ref.StreamID
				emitPayload = ref.Payload
			case pluginabi.MethodHostHTTPStreamClose:
				httpCloseID = ref.StreamID
			case pluginabi.MethodHostStreamClose:
				execCloseID = ref.StreamID
			}
		}
		if readID != "http-stream-1" || emitID != "exec-stream-1" || httpCloseID != "http-stream-1" || execCloseID != "exec-stream-1" {
			t.Fatalf("ids read=%s emit=%s httpClose=%s execClose=%s", readID, emitID, httpCloseID, execCloseID)
		}
		if !strings.Contains(string(emitPayload), "tok-secret-visible") || strings.Contains(string(emitPayload), "data:") {
			t.Fatalf("emit payload = %s", emitPayload)
		}
		opens := 0
		for _, call := range f.httpCalls(t) {
			if call.RPC == pluginabi.MethodHostHTTPDoStream {
				opens++
				if call.HostCallbackID != "cb-stream" {
					t.Fatalf("do_stream callback = %q", call.HostCallbackID)
				}
			}
		}
		if opens != 1 {
			t.Fatalf("do_stream calls = %d", opens)
		}
	})
}

func TestCountTokensDoesNotCallHTTP(t *testing.T) {
	f := setup(t)
	called := false
	hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
		called = true
		t.Errorf("count_tokens called %s", method)
		return json.RawMessage(`{}`), nil
	}
	_ = f
	result := callOK(t, pluginabi.MethodExecutorCountTokens, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
	var resp pluginapi.ExecutorResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatal(err)
	}
	if string(resp.Payload) != `{"input_tokens":0}` {
		t.Fatalf("payload = %s", resp.Payload)
	}
	if called {
		t.Fatal("host was called")
	}
}

func TestStatusAndRedaction(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("0", "tok-gemini", "plus", func(e *pluginapi.HostAuthFileEntry) {
		e.Provider = "gemini"
		e.Email = "gemini@example.com"
		e.Path = "/secret/gemini.json"
	})
	f.add(pluginapi.HostAuthFileEntry{
		AuthIndex: "1", Provider: "codex", Email: "secret@example.com", Path: "/secret/codex/a.json",
	}, map[string]any{"access_token": "tok-secret-visible", "plan_type": "plus", "email": "secret@example.com"})
	f.codex("2", "tok-disabled", "plus", func(e *pluginapi.HostAuthFileEntry) {
		e.Disabled = true
		e.Email = "secret@example.com"
		e.Path = "/secret/codex/a.json"
	})
	f.codex("3", "tok-cooling", "plus", func(e *pluginapi.HostAuthFileEntry) {
		e.NextRetryAfter = now.Add(time.Hour)
		e.Path = "/secret/codex/a.json"
	})
	f.codex("4", "tok-down", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Unavailable = true })
	mustConfigure(t, "enabled: true\nrotation-mode: round-robin\nplus-preference: prefer\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")

	before := len(f.calls)
	var page pluginapi.ManagementResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodManagementHandle, managementCall{ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/ch-relay-vippro/stats"}}), &page); err != nil {
		t.Fatal(err)
	}
	if page.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d", page.StatusCode)
	}
	for _, call := range f.calls[before:] {
		if call.method == pluginabi.MethodHostAuthList || call.method == pluginabi.MethodHostAuthGet {
			t.Fatalf("stats scanned auth via %s", call.method)
		}
	}
	for _, secret := range []string{"tok-secret-visible", "secret@example.com", "/secret/codex/a.json"} {
		if strings.Contains(string(page.Body), secret) {
			t.Fatalf("stats leaked %q", secret)
		}
	}

	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"echo":"tok-secret-visible"}`)}}
	resp := mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	if string(resp.Payload) != `{"echo":"tok-secret-visible"}` {
		t.Fatalf("success payload = %s", resp.Payload)
	}

	f2 := setup(t)
	f2.codex("1", "tok-secret-visible", "plus", nil)
	f2.scripts = []httpScript{{status: http.StatusBadGateway, body: []byte(`{"error":"tok-secret-visible"}`)}}
	mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	env, raw := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
	if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("upstream error = %+v", env.Error)
	}
	if strings.Contains(string(raw), "tok-secret-visible") || !strings.Contains(env.Error.Message, "[REDACTED]") {
		t.Fatalf("error body leaked token: %s", raw)
	}
}

func TestRegistrationAndQuiesce(t *testing.T) {
	setup(t)
	result := callOK(t, pluginabi.MethodPluginRegister, map[string]any{"schema_version": pluginabi.SchemaVersion})
	var reg registration
	if err := json.Unmarshal(result, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.SchemaVersion != pluginabi.SchemaVersion || reg.Metadata.Name != pluginID {
		t.Fatalf("registration = %+v", reg)
	}
	if !reg.Capabilities.ModelRegistrar || !reg.Capabilities.ModelProvider || !reg.Capabilities.ModelRouter || !reg.Capabilities.Executor || !reg.Capabilities.ManagementAPI {
		t.Fatalf("capabilities = %+v", reg.Capabilities)
	}
	if strings.Join(reg.Capabilities.ExecutorInputFormats, ",") != "chat-completions,openai-responses" || strings.Join(reg.Capabilities.ExecutorOutputFormats, ",") != "chat-completions,openai-responses" {
		t.Fatalf("formats = %#v %#v", reg.Capabilities.ExecutorInputFormats, reg.Capabilities.ExecutorOutputFormats)
	}
	if reg.Capabilities.ExecutorModelScope != string(pluginapi.ExecutorModelScopeStatic) {
		t.Fatalf("scope = %s", reg.Capabilities.ExecutorModelScope)
	}
	quiesce := callOK(t, pluginabi.MethodPluginQuiesce, map[string]any{})
	var obj map[string]any
	if err := json.Unmarshal(quiesce, &obj); err != nil {
		t.Fatal(err)
	}
	if len(obj) != 0 {
		t.Fatalf("quiesce = %s", quiesce)
	}
}

func TestStatsCountCompletedUsage(t *testing.T) {
	statsMu.Lock()
	stats = usageCounters{}
	statsMu.Unlock()
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)}}
	mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	got := statsSnapshot()
	if got.Requests != 1 || got.Success != 1 || got.Failed != 0 || got.InputTokens != 3 || got.OutputTokens != 4 || got.TotalTokens != 7 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestEmailUsageReport(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Email = "a@gmail.com" })
	f.codex("2", tokenB, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Email = "b@gmail.com" })
	f.scripts = []httpScript{
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)},
		{status: http.StatusBadGateway, body: []byte(`{"error":"bad"}`)},
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`)},
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`)},
	}
	mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	rows := statsSnapshot().UsageByEmail
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Email != "b@gmail.com" || rows[0].Success != 2 || rows[0].Failed != 0 || rows[0].TotalTokens != 22 {
		t.Fatalf("first = %+v", rows[0])
	}
	if rows[1].Email != "a@gmail.com" || rows[1].Success != 1 || rows[1].Failed != 1 || rows[1].TotalTokens != 7 {
		t.Fatalf("second = %+v", rows[1])
	}
}

func TestUsagePageMasksEmailAndCopiesFullAddress(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Email = "asd@gmail.com" })
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"usage":{"total_tokens":7}}`)}}
	mustConfigure(t, "enabled: true\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))

	registered := callOK(t, pluginabi.MethodManagementRegister, map[string]any{})
	if !strings.Contains(string(registered), `"/dashboard"`) || !strings.Contains(string(registered), "Dashboard") {
		t.Fatalf("dashboard menu = %s", registered)
	}
	if strings.Contains(string(registered), "/v0/resource/plugins/ch-relay-vippro/status") {
		t.Fatalf("registered a status resource: %s", registered)
	}

	var page pluginapi.ManagementResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodManagementHandle, managementCall{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/ch-relay-vippro/dashboard"},
	}), &page); err != nil {
		t.Fatal(err)
	}
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("page = %#v", page.Headers)
	}
	body := string(page.Body)
	if !strings.Contains(body, `id="root"`) || strings.Contains(body, "asd@gmail.com") {
		t.Fatalf("shell = %s", body[:min(200, len(body))])
	}
	var data struct {
		Emails []struct {
			Email  string `json:"email"`
			Masked string `json:"masked"`
		} `json:"emails"`
	}
	raw := callOK(t, pluginabi.MethodManagementHandle, managementCall{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/resource/plugins/ch-relay-vippro/dashboard",
			Query:  url.Values{"format": []string{"json"}},
		},
	})
	var jsonPage pluginapi.ManagementResponse
	if err := json.Unmarshal(raw, &jsonPage); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jsonPage.Body, &data); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(data.Emails) != 1 || data.Emails[0].Email != "asd@gmail.com" || data.Emails[0].Masked != "asd***@****.com" {
		t.Fatalf("emails = %+v", data.Emails)
	}
}

func managementBody(t *testing.T, method, path string) (pluginapi.ManagementResponse, statsPayload) {
	t.Helper()
	result := callOK(t, pluginabi.MethodManagementHandle, managementCall{
		ManagementRequest: pluginapi.ManagementRequest{Method: method, Path: path},
		HostCallbackID:    "cb-mgmt",
	})
	var page pluginapi.ManagementResponse
	if err := json.Unmarshal(result, &page); err != nil {
		t.Fatal(err)
	}
	var body statsPayload
	if len(page.Body) > 0 {
		if err := json.Unmarshal(page.Body, &body); err != nil {
			t.Fatalf("stats json: %v body=%s", err, page.Body)
		}
	}
	return page, body
}

func waitAccountCheck(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		checkWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for account check")
	}
}

func TestResponsesUsageAndSSE(t *testing.T) {
	const models = "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n"
	t.Run("response.usage", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":6,"total_tokens":11}}}`)}}
		mustConfigure(t, models)
		mustExec(t, "openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		got := statsSnapshot()
		if got.Requests != 1 || got.Success != 1 || got.Failed != 0 || got.InputTokens != 5 || got.OutputTokens != 6 || got.TotalTokens != 11 {
			t.Fatalf("stats = %+v", got)
		}
		if len(got.Buckets) != 1 || got.Buckets[0].InputTokens != 5 || got.Buckets[0].TotalTokens != 11 {
			t.Fatalf("buckets = %+v", got.Buckets)
		}
	})

	t.Run("sse data lines", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.streamFinished = make(chan struct{})
		f.scripts = []httpScript{{
			status:   http.StatusOK,
			streamID: "http-usage",
			chunks: []readChunk{
				{payload: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")},
				{payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":9,\"total_tokens\":17}}}\n\n"), done: true},
			},
		}}
		mustConfigure(t, models)
		req := execReq("openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-usage"
		env, raw := callEnv(t, pluginabi.MethodExecutorExecuteStream, req)
		if !env.OK {
			t.Fatalf("stream = %s", raw)
		}
		waitCh(t, f.streamFinished, "usage stream")
		got := statsSnapshot()
		if got.Requests != 1 || got.Success != 1 || got.InputTokens != 8 || got.OutputTokens != 9 || got.TotalTokens != 17 {
			t.Fatalf("stats = %+v", got)
		}
	})
}

func TestStatsGetDoesNotListAuth(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", "tok-disabled", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Disabled = true })
	mustConfigure(t, "enabled: true\n")
	before := len(f.snapshot())
	page, body := managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", page.StatusCode)
	}
	for _, call := range f.snapshot()[before:] {
		if call.method == pluginabi.MethodHostAuthList || call.method == pluginabi.MethodHostAuthGet {
			t.Fatalf("GET stats called %s", call.method)
		}
	}
	if body.Accounts != nil {
		t.Fatalf("accounts = %#v, want null", body.Accounts)
	}
	if body.Buckets == nil {
		t.Fatal("buckets is null")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(page.Body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"requests", "success", "failed", "input_tokens", "output_tokens", "total_tokens", "since", "buckets", "accounts"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("missing %s in %s", key, page.Body)
		}
	}
	if string(keys["accounts"]) != "null" || string(keys["buckets"]) != "[]" {
		t.Fatalf("body = %s", page.Body)
	}
	if _, err := time.Parse(time.RFC3339, body.Since.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	registered := callOK(t, pluginabi.MethodManagementRegister, map[string]any{})
	if strings.Contains(string(registered), "/v0/resource/plugins/ch-relay-vippro/status") {
		t.Fatalf("registered a status resource: %s", registered)
	}
}

func TestAccountCheckSkipsDisabled(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("gem", "tok-gem", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Provider = "gemini" })
	f.codex("off", "tok-off", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Disabled = true })
	f.codex("plus", "tok-plus", "plus", nil)
	f.codex("free", "tok-free", "free", nil)
	f.codex("cool", "tok-cool", "plus", func(e *pluginapi.HostAuthFileEntry) {
		e.NextRetryAfter = now.Add(time.Minute)
	})
	f.codex("down", "tok-down", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Unavailable = true })
	f.codex("empty", "", "plus", nil)
	mustConfigure(t, "enabled: true\n")

	if _, body := managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats"); body.Accounts != nil || f.count(pluginabi.MethodHostAuthList) != 0 {
		t.Fatal("GET stats scanned auth before the first check")
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFn)
	inner := f.call
	entered := make(chan struct{}, 1)
	hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
		if method == pluginabi.MethodHostAuthGet {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
		return inner(method, payload)
	}

	page, body := managementBody(t, http.MethodPost, "/v0/management/ch-relay-vippro/accounts/check")
	if page.StatusCode != http.StatusOK || body.Accounts == nil || !body.Accounts.Running {
		t.Fatalf("post check status=%d accounts=%#v", page.StatusCode, body.Accounts)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("account check did not reach auth.get")
	}
	if f.count(pluginabi.MethodHostAuthList) != 1 {
		t.Fatalf("auth.list = %d", f.count(pluginabi.MethodHostAuthList))
	}
	progress := statsSnapshot()
	if progress.Accounts == nil || !progress.Accounts.Running || progress.Accounts.Total != 5 || progress.Accounts.DisabledSkipped != 1 {
		t.Fatalf("progress = %#v", progress.Accounts)
	}
	managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats")
	managementBody(t, http.MethodPost, "/v0/management/ch-relay-vippro/accounts/check")
	if f.count(pluginabi.MethodHostAuthList) != 1 {
		t.Fatalf("auth.list after GET and overlapping POST = %d", f.count(pluginabi.MethodHostAuthList))
	}

	releaseFn()
	waitAccountCheck(t)
	_, done := managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats")
	if done.Accounts == nil || done.Accounts.Running {
		t.Fatalf("finished = %#v", done.Accounts)
	}
	acc := done.Accounts
	if acc.Checked != 5 || acc.Total != 5 || acc.Eligible != 2 || acc.Plus != 1 || acc.Cooling != 1 || acc.Unavailable != 2 || acc.DisabledSkipped != 1 {
		t.Fatalf("accounts = %+v", *acc)
	}
	if _, err := time.Parse(time.RFC3339, acc.CheckedAt); err != nil {
		t.Fatalf("checked_at %q: %v", acc.CheckedAt, err)
	}
	gets := map[string]int{}
	for _, call := range f.snapshot() {
		if call.method != pluginabi.MethodHostAuthGet {
			continue
		}
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		if err := json.Unmarshal(call.payload, &req); err != nil {
			t.Fatal(err)
		}
		gets[req.AuthIndex]++
	}
	for _, index := range []string{"plus", "free", "cool", "down", "empty"} {
		if gets[index] != 1 {
			t.Fatalf("auth.get %s = %d in %#v", index, gets[index], gets)
		}
	}
	if gets["off"] != 0 || gets["gem"] != 0 {
		t.Fatalf("skipped accounts were fetched: %#v", gets)
	}
	if f.count(pluginabi.MethodHostAuthList) != 1 {
		t.Fatalf("auth.list = %d", f.count(pluginabi.MethodHostAuthList))
	}
}

func TestAccountCheckPlusFollowsTokenPlan(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }

	// Two stored-plus files whose tokens say free, so a plan_type counter would report plus=2.
	staleBoth := pluginapi.HostAuthFileEntry{AuthIndex: "stale-both", Provider: "codex", Type: "codex"}
	f.add(staleBoth, map[string]any{
		"access_token":      unsignedPlanJWT(t, "free"),
		"plan_type":         "plus",
		"chatgpt_plan_type": "plus",
		"id_token":          unsignedPlanJWT(t, "free"),
	})
	staleType := pluginapi.HostAuthFileEntry{AuthIndex: "stale-type", Provider: "codex", Type: "codex"}
	f.add(staleType, map[string]any{
		"access_token": unsignedPlanJWT(t, "free"),
		"plan_type":    "plus",
		"id_token":     unsignedPlanJWT(t, "plus"),
	})
	staleTop := pluginapi.HostAuthFileEntry{AuthIndex: "stale-top", Provider: "codex", Type: "codex"}
	f.add(staleTop, map[string]any{
		"access_token":      "tok-top",
		"plan_type":         "free",
		"chatgpt_plan_type": "plus",
		"id_token":          unsignedPlanJWT(t, "free"),
	})
	liveID := pluginapi.HostAuthFileEntry{AuthIndex: "live-id", Provider: "codex", Type: "codex"}
	f.add(liveID, map[string]any{
		"access_token": unsignedPlanJWT(t, "plus"),
		"plan_type":    "free",
		"id_token":     unsignedPlanJWT(t, "free"),
	})
	liveAccess := pluginapi.HostAuthFileEntry{AuthIndex: "live-access", Provider: "codex", Type: "codex"}
	rawAccess, errMarshal := json.Marshal(map[string]any{
		"access_token": unsignedPlanJWT(t, "plus"),
		"plan_type":    "free",
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	f.authJSON[liveAccess.AuthIndex] = rawAccess

	for _, entry := range []pluginapi.HostAuthFileEntry{staleBoth, staleType, staleTop} {
		kind, plus := classifyEnabled(entry, "cb", now)
		if kind != kindEligible || plus {
			t.Fatalf("%s stored plus with free token: kind=%v plus=%v", entry.AuthIndex, kind, plus)
		}
	}
	kind, plus := classifyEnabled(liveID, "cb", now)
	if kind != kindEligible || !plus {
		t.Fatalf("access token plus with free id_token: kind=%v plus=%v", kind, plus)
	}
	kind, plus = classifyEnabled(liveAccess, "cb", now)
	if kind != kindEligible || !plus {
		t.Fatalf("access_token jwt plus with free plan_type: kind=%v plus=%v", kind, plus)
	}

	mustConfigure(t, "enabled: true\n")
	page, _ := managementBody(t, http.MethodPost, "/v0/management/ch-relay-vippro/accounts/check")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", page.StatusCode)
	}
	waitAccountCheck(t)
	before := len(f.snapshot())
	_, done := managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats")
	for _, call := range f.snapshot()[before:] {
		if call.method == pluginabi.MethodHostAuthList || call.method == pluginabi.MethodHostAuthGet {
			t.Fatalf("GET stats called %s", call.method)
		}
	}
	acc := done.Accounts
	if acc == nil || acc.Running || acc.Checked != 4 || acc.Total != 4 || acc.Eligible != 4 || acc.Plus != 1 {
		t.Fatalf("accounts = %#v", acc)
	}
}

func TestHourBucketsAggregate(t *testing.T) {
	f := setup(t)
	path := filepath.Join(t.TempDir(), "stats.json")
	current := time.Date(2026, 4, 1, 23, 10, 0, 0, time.UTC)
	nowFn = func() time.Time { return current }
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)},
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}`)},
		{status: http.StatusBadGateway, body: []byte(`{"error":"down"}`)},
		{status: http.StatusOK, body: []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}}`)},
	}
	mustConfigure(t, "enabled: true\nrequest-retry: 0\nstats-file: "+strconv.Quote(path)+"\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
	if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("failed request = %+v", env.Error)
	}
	current = time.Date(2026, 4, 2, 0, 5, 0, 0, time.UTC)
	mustExec(t, "openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))

	got := statsSnapshot()
	if got.Requests != 4 || got.Success != 3 || got.Failed != 1 || got.InputTokens != 15 || got.OutputTokens != 27 || got.TotalTokens != 42 {
		t.Fatalf("totals = %+v", got)
	}
	if len(got.Buckets) != 2 {
		t.Fatalf("buckets = %+v", got.Buckets)
	}
	first := time.Date(2026, 4, 1, 23, 0, 0, 0, time.UTC)
	second := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	if !got.Buckets[0].Start.Equal(first) || got.Buckets[0].Requests != 3 || got.Buckets[0].Success != 2 || got.Buckets[0].Failed != 1 || got.Buckets[0].InputTokens != 5 || got.Buckets[0].OutputTokens != 7 || got.Buckets[0].TotalTokens != 12 {
		t.Fatalf("hour 1 = %+v", got.Buckets[0])
	}
	if !got.Buckets[1].Start.Equal(second) || got.Buckets[1].Requests != 1 || got.Buckets[1].Success != 1 || got.Buckets[1].Failed != 0 || got.Buckets[1].InputTokens != 10 || got.Buckets[1].OutputTokens != 20 || got.Buckets[1].TotalTokens != 30 {
		t.Fatalf("hour 2 = %+v", got.Buckets[1])
	}
	rawFile, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved statsPersist
	if err := json.Unmarshal(rawFile, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Requests != 4 || len(saved.Buckets) != 2 || !saved.Buckets[0].Start.Equal(first) || !saved.Buckets[1].Start.Equal(second) {
		t.Fatalf("file = %+v", saved)
	}

	statsMu.Lock()
	stats = usageCounters{}
	hourBuckets = nil
	statsMu.Unlock()
	loadStats(path)
	reloaded := statsSnapshot()
	if reloaded.Requests != got.Requests || reloaded.InputTokens != got.InputTokens || len(reloaded.Buckets) != 2 || !reloaded.Buckets[0].Start.Equal(first) || reloaded.Buckets[1].TotalTokens != 30 {
		t.Fatalf("reloaded = %+v", reloaded)
	}

	f.scripts = append(f.scripts, httpScript{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)})
	current = second.Add(91 * 24 * time.Hour)
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	pruned := statsSnapshot()
	if len(pruned.Buckets) != 1 || pruned.Requests != 1 || pruned.Success != 1 || pruned.InputTokens != 1 || pruned.OutputTokens != 1 || pruned.TotalTokens != 2 {
		t.Fatalf("pruned = %+v buckets=%+v", pruned, pruned.Buckets)
	}
	if !pruned.Buckets[0].Start.Equal(hourStart(current)) {
		t.Fatalf("pruned start = %s", pruned.Buckets[0].Start)
	}

	kept, errKept := os.ReadFile(path)
	if errKept != nil {
		t.Fatal(errKept)
	}
	mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	after, errAfter := os.ReadFile(path)
	if errAfter != nil {
		t.Fatal(errAfter)
	}
	if string(after) != string(kept) {
		t.Fatal("empty stats-file still rewrote the file")
	}
}

func requestModel(t *testing.T, call decodedHTTP) string {
	t.Helper()
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(call.Body, &payload); err != nil {
		t.Fatalf("model json: %v body=%s", err, call.Body)
	}
	return payload.Model
}

func TestModelFallback(t *testing.T) {
	const unsupported = `{"detail":"The 'requested-model' MODEL IS NOT SUPPORTED when using Codex with a ChatGPT account."}`
	const primaryBody = `{"model":"primary-model"}`

	t.Run("zero ignores the fallback list", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusBadRequest, body: []byte(unsupported)}}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
fallback-retry: 0
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "primary-model", []byte(primaryBody)))
		calls := f.httpCalls(t)
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadRequest || len(calls) != 1 || requestModel(t, calls[0]) != "primary-model" {
			t.Fatalf("status=%v calls=%d", env.Error, len(calls))
		}
	})

	t.Run("400 keeps the model and the next account uses it too", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadRequest, body: []byte(unsupported)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 1
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("calls=%d", len(calls))
		}
		if requestModel(t, calls[0]) != "primary-model" || requestModel(t, calls[1]) != "primary-model" {
			t.Fatalf("models = %s %s", calls[0].Body, calls[1].Body)
		}
	})

	t.Run("429 switches account and keeps the model", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{
			{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 1
fallback-retry: 2
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("calls=%d", len(calls))
		}
		if requestModel(t, calls[0]) != "primary-model" || requestModel(t, calls[1]) != "primary-model" {
			t.Fatalf("models = %s %s", calls[0].Body, calls[1].Body)
		}
	})

	t.Run("http 200 overload is returned unchanged", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		const overload = `{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`
		f.scripts = []httpScript{
			{status: http.StatusOK, body: []byte(overload)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 1
fallback-retry: 2
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		resp := mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA || requestModel(t, calls[0]) != "primary-model" {
			t.Fatalf("overload calls=%d", len(calls))
		}
		if string(resp.Payload) != overload {
			t.Fatalf("payload = %s", resp.Payload)
		}
	})

	t.Run("stream forwards the json inside the sse line", func(t *testing.T) {
		f := setup(t)
		f.streamFinished = make(chan struct{})
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusOK, streamID: "http-over", chunks: []readChunk{{payload: []byte("data: {\"code\":\"server_is_overloaded\"}\n\n"), done: true}}},
		}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-over"
		callOK(t, pluginabi.MethodExecutorExecuteStream, req)
		waitCh(t, f.streamFinished, "overload stream")
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA {
			t.Fatalf("stream calls=%d", len(calls))
		}
		var emitted string
		for _, call := range f.snapshot() {
			if call.method != pluginabi.MethodHostStreamEmit {
				continue
			}
			var ref struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			emitted = string(ref.Payload)
		}
		if emitted != `{"code":"server_is_overloaded"}` {
			t.Fatalf("emit = %s", emitted)
		}
	})

	t.Run("responses stream forwards original bytes", func(t *testing.T) {
		f := setup(t)
		f.streamFinished = make(chan struct{})
		f.codex("1", tokenA, "plus", nil)
		const raw = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n"
		f.scripts = []httpScript{{
			status:   http.StatusOK,
			streamID: "http-responses",
			chunks:   []readChunk{{payload: []byte(raw), done: true}},
		}}
		mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		req := execReq("openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-responses"
		callOK(t, pluginabi.MethodExecutorExecuteStream, req)
		waitCh(t, f.streamFinished, "responses stream")
		var emitted string
		for _, call := range f.snapshot() {
			if call.method != pluginabi.MethodHostStreamEmit {
				continue
			}
			var ref struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			emitted = string(ref.Payload)
		}
		if emitted != raw {
			t.Fatalf("emit = %s", emitted)
		}
	})

	t.Run("a missing response id does not start fallback", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusBadRequest, body: []byte(`{"error":"response id does not exist"}`)}}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "primary-model", []byte(primaryBody)))
		calls := f.httpCalls(t)
		if env.OK || len(calls) != 1 || requestModel(t, calls[0]) != "primary-model" {
			t.Fatalf("calls=%d", len(calls))
		}
	})

	t.Run("fallback retry walks later names after the first", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadRequest, body: []byte(`{"error":{"code":"model_not_found"}}`)},
			{status: http.StatusBadRequest, body: []byte(`{"error":{"code":"model_not_found"}}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
fallback-retry: 3
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model", "third-model"]
`)
		mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 3 || requestModel(t, calls[1]) != "other-model" || requestModel(t, calls[2]) != "third-model" {
			t.Fatalf("calls=%d bodies=%s %s", len(calls), calls[1].Body, calls[len(calls)-1].Body)
		}
	})

	t.Run("model does not exist starts fallback", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadRequest, body: []byte(`{"error":"model gpt-x does not exist"}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 2 || requestModel(t, calls[1]) != "other-model" {
			t.Fatalf("calls=%d", len(calls))
		}
	})

	t.Run("model error switches to fallback after primary accounts", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		const notFound = `{"error":{"message":"The model does not exist","code":"model_not_found"}}`
		f.scripts = []httpScript{
			{status: http.StatusBadRequest, body: []byte(unsupported)},
			{status: http.StatusNotFound, body: []byte(notFound)},
			{status: http.StatusOK, body: []byte(`{"ok":true,"model":"other-model"}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		resp := mustExec(t, "openai", "primary-model(high)", []byte(`{"model":"primary-model(high)"}`))
		calls := f.httpCalls(t)
		if len(calls) != 3 || bearer(t, calls[2]) != "Bearer "+tokenA {
			t.Fatalf("calls=%d", len(calls))
		}
		if requestModel(t, calls[0]) != "primary-model" || requestModel(t, calls[1]) != "primary-model" || requestModel(t, calls[2]) != "other-model" {
			t.Fatalf("models = %s %s %s", calls[0].Body, calls[1].Body, calls[2].Body)
		}
		if string(resp.Payload) != `{"ok":true,"model":"other-model"}` {
			t.Fatalf("payload = %s", resp.Payload)
		}
	})

	t.Run("cooled accounts serve the fallback", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		markModelCooldown("1", tokenA, "primary-model", time.Hour, "")
		mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA || requestModel(t, calls[0]) != "other-model" {
			t.Fatalf("calls=%d", len(calls))
		}
	})

	t.Run("cooled accounts without fallback keep accounts_cooling", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		mustConfigure(t, `
enabled: true
plus-preference: off
fallback-retry: 0
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		markModelCooldown("1", tokenA, "primary-model", time.Hour, "")
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "primary-model", []byte(primaryBody)))
		if env.OK || env.Error == nil || env.Error.Code != "accounts_cooling" || len(f.httpCalls(t)) != 0 {
			t.Fatalf("env=%+v", env.Error)
		}
	})

	t.Run("stream fallback forwards upstream bytes unchanged", func(t *testing.T) {
		f := setup(t)
		f.streamFinished = make(chan struct{})
		f.codex("1", tokenA, "plus", nil)
		const raw = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n"
		f.scripts = []httpScript{
			{status: http.StatusBadRequest, streamID: "http-miss", chunks: []readChunk{{payload: []byte(unsupported), done: true}}},
			{status: http.StatusOK, streamID: "http-fallback", chunks: []readChunk{{payload: []byte(raw), done: true}}},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		req := execReq("openai-response", "primary-model", []byte(primaryBody))
		req.StreamID = "exec-fallback"
		callOK(t, pluginabi.MethodExecutorExecuteStream, req)
		waitCh(t, f.streamFinished, "fallback stream")
		calls := f.httpCalls(t)
		if len(calls) != 2 || requestModel(t, calls[0]) != "primary-model" || requestModel(t, calls[1]) != "other-model" {
			t.Fatalf("stream calls=%d", len(calls))
		}
		var emitted []string
		for _, call := range f.snapshot() {
			if call.method != pluginabi.MethodHostStreamEmit {
				continue
			}
			var ref struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			emitted = append(emitted, string(ref.Payload))
		}
		if len(emitted) != 1 || emitted[0] != raw {
			t.Fatalf("emit = %q", emitted)
		}
	})

	t.Run("duplicate name and alias keeps the first fallback", func(t *testing.T) {
		setup(t)
		mustConfigure(t, `
enabled: true
models:
  - {name: primary-model, alias: primary-model, fallback: ["primary-model", "kept-model", "kept-model"]}
  - {name: primary-model, alias: primary-model, fallback: ["dropped-model"]}
`)
		models := currentConfig().Models
		if len(models) != 1 || len(models[0].Fallback) != 1 || models[0].Fallback[0] != "kept-model" {
			t.Fatalf("models = %+v", models)
		}
	})
}

func TestForwardStreamClosesAfterResponsesTerminalEvent(t *testing.T) {
	f := setup(t)
	streamClosed := make(chan struct{}, 1)
	previousHost := hostCallOverride
	hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
		raw, errCall := previousHost(method, payload)
		if method == pluginabi.MethodHostStreamClose {
			streamClosed <- struct{}{}
		}
		return raw, errCall
	}
	f.codex("1", tokenA, "plus", nil)
	completed := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"
	f.scripts = []httpScript{{status: http.StatusOK, streamID: "http-terminal", chunks: []readChunk{
		{payload: []byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n")},
		// Terminal frame split across two reads: the marker alone must not close.
		{payload: []byte(completed[:40])},
		{payload: []byte(completed[40:])},
		// Upstream keeps the socket open with comments; these must never be read.
		{payload: []byte(": keep-alive\n\n")},
		{payload: []byte(": keep-alive\n\n"), done: true},
	}}}
	mustConfigure(t, `
enabled: true
plus-preference: off
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	req := execReq("openai-response", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	req.StreamID = "exec-terminal"
	callOK(t, pluginabi.MethodExecutorExecuteStream, req)
	waitCh(t, streamClosed, "stream close after response.completed")

	f.mu.Lock()
	defer f.mu.Unlock()
	reads, emits := 0, 0
	closeErr := "unset"
	for _, call := range f.calls {
		switch call.method {
		case pluginabi.MethodHostHTTPStreamRead:
			reads++
		case pluginabi.MethodHostStreamEmit:
			emits++
		case pluginabi.MethodHostStreamClose:
			var payload struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(call.payload, &payload)
			closeErr = payload.Error
		}
	}
	if reads != 3 {
		t.Fatalf("http stream reads = %d, want 3 (stop after the terminal frame)", reads)
	}
	if emits != 3 {
		t.Fatalf("stream emits = %d, want 3", emits)
	}
	if closeErr != "" {
		t.Fatalf("stream close error = %q, want empty", closeErr)
	}
}

func TestResponsesTerminalEventMarkers(t *testing.T) {
	cases := map[string]bool{
		"event: response.completed\n":                                true,
		"data: {\"type\":\"response.failed\",\"x\":1}\n":             true,
		"data: {\"type\":\"response.incomplete\"}\n":                 true,
		"data: {\"type\":\"response.output_text.delta\"}\n":          false,
		"data: {\"delta\":\"event: response.completed\"}\n":          false,
		"data: {\"delta\":\"{\\\"type\\\":\\\"response.completed\"}": false,
	}
	for input, want := range cases {
		if got := responsesTerminalEvent([]byte(input)); got != want {
			t.Fatalf("responsesTerminalEvent(%q) = %v, want %v", input, got, want)
		}
	}
	if !sseFrameBoundary([]byte("x\n\n")) || sseFrameBoundary([]byte("x\n")) {
		t.Fatal("sseFrameBoundary mismatch")
	}
}

const twoModels = "models:\n  - {name: model-a, alias: model-a}\n  - {name: model-b, alias: model-b}\n"

func execModel(t *testing.T, model string) pluginapi.ExecutorResponse {
	t.Helper()
	return mustExec(t, "openai", model, []byte(`{"model":"`+model+`"}`))
}

func lastBearer(t *testing.T, f *fakeHost) string {
	t.Helper()
	calls := f.httpCalls(t)
	return bearer(t, calls[len(calls)-1])
}

func penaltyCounts() (index, token int) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	return len(runtime.penaltyIndex), len(runtime.penaltyToken)
}

func TestModelCooldownIsPerModel(t *testing.T) {
	const refusal = `{"detail":"The 'model-a' model is not supported when using Codex with a ChatGPT account."}`
	t.Run("a refusal for one model keeps the account for other models", func(t *testing.T) {
		f := setup(t)
		now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusBadRequest, body: []byte(refusal)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: fill-first\nrequest-retry: 1\n"+twoModels)
		execModel(t, "model-a")
		execModel(t, "model-b")
		execModel(t, "model-a")
		calls := f.httpCalls(t)
		want := []string{tokenA, tokenB, tokenA, tokenB}
		if len(calls) != len(want) {
			t.Fatalf("calls = %d", len(calls))
		}
		for i, token := range want {
			if bearer(t, calls[i]) != "Bearer "+token {
				t.Fatalf("call %d bearer = %s, want %s", i, bearer(t, calls[i]), token)
			}
		}
		if !cooled("1", tokenA, "model-a", now) || cooled("1", tokenA, "model-b", now) {
			t.Fatal("cooldown is not scoped to model-a")
		}
		if !cooledAny("1", tokenA, now) || cooledAny("2", tokenB, now) {
			t.Fatal("cooledAny mismatch")
		}
	})

	t.Run("thinking suffix shares the base model cooldown", func(t *testing.T) {
		f := setup(t)
		now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusBadRequest, body: []byte(refusal)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: fill-first\nrequest-retry: 1\n"+twoModels)
		execModel(t, "model-a(high)")
		execModel(t, "model-a(low)")
		calls := f.httpCalls(t)
		if len(calls) != 3 || bearer(t, calls[2]) != "Bearer "+tokenB || requestModel(t, calls[2]) != "model-a" {
			t.Fatalf("suffix cooldown calls = %d", len(calls))
		}
		if !cooled("1", tokenA, "model-a(medium)", now) {
			t.Fatal("suffix changed the cooldown key")
		}
	})

	t.Run("cooldown file records the model and drops legacy rows", func(t *testing.T) {
		stats := filepath.Join(t.TempDir(), "stats.json")
		path := stats + ".cooldowns.json"
		f := setup(t)
		now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		mustConfigure(t, "enabled: true\nstats-file: "+strconv.Quote(stats)+"\n"+twoModels)
		markModelCooldown("1", tokenA, "model-a(high)", time.Hour, stats)
		raw, errRead := os.ReadFile(path)
		if errRead != nil || !strings.Contains(string(raw), `"model":"model-a"`) {
			t.Fatalf("cooldown file = %s err=%v", raw, errRead)
		}
		legacy := `[{"auth_index":"1","until":"2026-10-26T00:00:00Z","reason":"chatgpt_model_unsupported"},{"token_hash":"` + tokenKey(tokenA) + `","until":"2026-10-26T00:00:00Z"}]`
		if errWrite := os.WriteFile(path, []byte(legacy), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		mustConfigure(t, "enabled: true\nstats-file: "+strconv.Quote(stats)+"\n"+twoModels)
		if cooledAny("1", tokenA, now) {
			t.Fatal("legacy cooldown record survived load")
		}
		markModelCooldown("1", tokenA, "model-b", time.Hour, stats)
		mustConfigure(t, "enabled: true\nstats-file: "+strconv.Quote(stats)+"\n"+twoModels)
		if !cooled("1", tokenA, "model-b", now) || cooled("1", tokenA, "model-a", now) {
			t.Fatal("model rows did not round-trip")
		}
	})

	t.Run("fallback attempts skip an account cooled for the fallback model", func(t *testing.T) {
		f := setup(t)
		now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusNotFound, body: []byte(`{"error":{"code":"model_not_found"}}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: fill-first\nrequest-retry: 0\nfallback-retry: 2\nmodels:\n  - {name: primary-model, alias: primary-model, fallback: [other-model]}\n")
		markModelCooldown("1", tokenA, "other-model", time.Hour, "")
		execModel(t, "primary-model")
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || requestModel(t, calls[0]) != "primary-model" ||
			bearer(t, calls[1]) != "Bearer "+tokenB || requestModel(t, calls[1]) != "other-model" {
			t.Fatalf("fallback skip calls = %d", len(calls))
		}
	})

	t.Run("in-stream refusal cools only the streamed model", func(t *testing.T) {
		f := setup(t)
		closed := make(chan struct{}, 1)
		previousHost := hostCallOverride
		hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
			raw, errCall := previousHost(method, payload)
			if method == pluginabi.MethodHostStreamClose {
				closed <- struct{}{}
			}
			return raw, errCall
		}
		now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, streamID: "http-200-err", chunks: []readChunk{{payload: []byte("data: " + refusal + "\n\n"), done: true}}}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 0\n"+twoModels)
		req := execReq("openai", "model-a(high)", []byte(`{"model":"model-a(high)"}`))
		req.StreamID = "exec-model-a"
		callOK(t, pluginabi.MethodExecutorExecuteStream, req)
		waitCh(t, closed, "stream close")
		if !cooled("1", tokenA, "model-a", now) || cooled("1", tokenA, "model-b", now) {
			t.Fatal("stream refusal cooled the wrong model")
		}
	})
}

func TestRefusalOnEveryAccountUsesOutageCooldown(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 10, 31, 11, 10, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	const refusal = `{"detail":"The 'primary-model' model is not supported when using Codex with a ChatGPT account."}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(refusal)},
		{status: http.StatusBadRequest, body: []byte(refusal)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["fallback-model"]
`)
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	calls := f.httpCalls(t)
	if len(calls) != 3 || requestModel(t, calls[2]) != "fallback-model" {
		t.Fatalf("first request calls=%d", len(calls))
	}
	want := now.Add(5 * time.Minute)
	for _, index := range []string{"1", "2"} {
		runtimeMu.Lock()
		until := runtime.cooldownIndex[cooldownKey(index, "primary-model")]
		runtimeMu.Unlock()
		if !until.Equal(want) {
			t.Fatalf("account %s cooldown until %v, want %v", index, until, want)
		}
	}
	now = now.Add(6 * time.Minute)
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	calls = f.httpCalls(t)
	last := calls[len(calls)-1]
	if requestModel(t, last) != "primary-model" || bearer(t, last) != "Bearer "+tokenA {
		t.Fatalf("after outage cooldown model=%s bearer=%s", requestModel(t, last), bearer(t, last))
	}
}

func TestRefusalWithSuccessElsewhereUsesFullCooldown(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 10, 31, 11, 30, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	const refusal = `{"detail":"The 'primary-model' model is not supported when using Codex with a ChatGPT account."}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(refusal)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
model-cooldown-minutes: 60
models:
  - name: primary-model
    alias: primary-model
`)
	mustExec(t, "openai", "primary-model(high)", []byte(`{"model":"primary-model(high)"}`))
	calls := f.httpCalls(t)
	if len(calls) != 2 || requestModel(t, calls[0]) != "primary-model" || requestModel(t, calls[1]) != "primary-model" {
		t.Fatalf("calls=%d", len(calls))
	}
	runtimeMu.Lock()
	until := runtime.cooldownIndex[cooldownKey("1", "primary-model")]
	_, tokenCooled := runtime.cooldownToken[cooldownKey(tokenKey(tokenA), "primary-model")]
	runtimeMu.Unlock()
	if want := now.Add(60 * time.Minute); !until.Equal(want) {
		t.Fatalf("proven cooldown until %v, want %v", until, want)
	}
	if !tokenCooled {
		t.Fatal("proven cooldown missed the token")
	}
	if cooled("2", tokenB, "primary-model", now) {
		t.Fatal("successful account was cooled")
	}
	if !cooled("1", tokenA, "primary-model(low)", now) {
		t.Fatal("cooldown missed the thinking suffix")
	}
	now = now.Add(61 * time.Minute)
	if cooled("1", tokenA, "primary-model", now) {
		t.Fatal("proven cooldown outlived model-cooldown-minutes")
	}
}

func TestStreamRefusalUsesOutageCooldown(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.streamFinished = make(chan struct{})
	f.codex("1", tokenA, "plus", nil)
	const refusal = `{"detail":"The 'gpt-6-astra' model is not supported when using Codex with a ChatGPT account."}`
	f.scripts = []httpScript{{status: http.StatusOK, streamID: "http-stream-refusal", chunks: []readChunk{{payload: []byte("data: " + refusal + "\n\n"), done: true}}}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
model-cooldown-minutes: 1440
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	req := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	req.StreamID = "exec-stream-refusal"
	callOK(t, pluginabi.MethodExecutorExecuteStream, req)
	waitCh(t, f.streamFinished, "stream refusal close")
	runtimeMu.Lock()
	until := runtime.cooldownIndex[cooldownKey("1", "gpt-6-astra")]
	runtimeMu.Unlock()
	if want := now.Add(5 * time.Minute); !until.Equal(want) {
		t.Fatalf("stream refusal cooldown until %v, want %v", until, want)
	}
}

func TestRefusalTextInSuccessfulReplyDoesNotCool(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)
	const phrase = "The 'gpt-6-astra' model is not supported when using Codex with a ChatGPT account."
	const yml = `
enabled: true
plus-preference: off
request-retry: 0
model-cooldown-minutes: 60
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`
	t.Run("json reply content", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		body := `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"` + phrase + `"},"finish_reason":"stop"}]}`
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(body)}}
		mustConfigure(t, yml)
		mustExec(t, "openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
		if cooled("1", tokenA, "gpt-6-astra", now) {
			t.Fatal("reply content cooled the account")
		}
	})
	t.Run("stream delta content", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return now }
		f.streamFinished = make(chan struct{})
		f.codex("1", tokenA, "plus", nil)
		chunk := `data: {"choices":[{"index":0,"delta":{"content":"` + phrase + `"}}]}` + "\n\n"
		f.scripts = []httpScript{{status: http.StatusOK, streamID: "http-reply", chunks: []readChunk{{payload: []byte(chunk), done: true}}}}
		mustConfigure(t, yml)
		req := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
		req.StreamID = "exec-reply"
		callOK(t, pluginabi.MethodExecutorExecuteStream, req)
		waitCh(t, f.streamFinished, "reply stream close")
		if cooled("1", tokenA, "gpt-6-astra", now) {
			t.Fatal("stream reply content cooled the account")
		}
	})
	t.Run("error field still detected", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"detail":"` + phrase + `"}`)}}
		mustConfigure(t, yml)
		mustExec(t, "openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
		if !cooled("1", tokenA, "gpt-6-astra", now) {
			t.Fatal("error field refusal was not detected")
		}
	})
}

func TestOutageCooldownExpiryRetriesPrimary(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	const refusal = `{"detail":"The 'primary-model' model is not supported when using Codex with a ChatGPT account."}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(refusal)},
		{status: http.StatusBadRequest, body: []byte(refusal)},
		{status: http.StatusOK, body: []byte(`{"ok":true,"model":"fallback-model"}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["fallback-model"]
`)
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	calls := f.httpCalls(t)
	if len(calls) != 3 || requestModel(t, calls[2]) != "fallback-model" {
		t.Fatalf("first request calls=%d", len(calls))
	}
	now = now.Add(6 * time.Minute)
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	calls = f.httpCalls(t)
	if len(calls) != 4 {
		t.Fatalf("second request calls=%d", len(calls))
	}
	last := calls[len(calls)-1]
	if requestModel(t, last) != "primary-model" || bearer(t, last) != "Bearer "+tokenA {
		t.Fatalf("after expiry model=%s bearer=%s", requestModel(t, last), bearer(t, last))
	}
}

func TestOutageCooldownConfig(t *testing.T) {
	const refusal = `{"detail":"The 'gpt-6-astra' model is not supported when using Codex with a ChatGPT account."}`
	const modelYML = "models:\n  - {name: gpt-6-astra, alias: gpt-6-astra}\n"
	now := time.Date(2026, 10, 31, 14, 0, 0, 0, time.UTC)

	t.Run("unset defaults to five minutes", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusBadRequest, body: []byte(refusal)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 0\n"+modelYML)
		cfg := currentConfig()
		if cfg.ModelOutageCooldownMinutes == nil || *cfg.ModelOutageCooldownMinutes != 5 {
			t.Fatalf("model-outage-cooldown-minutes = %v", cfg.ModelOutageCooldownMinutes)
		}
		if got := cfg.modelOutageCooldown(); got != 5*time.Minute {
			t.Fatalf("outage cooldown = %s", got)
		}
		callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`)))
		if !cooled("1", tokenA, "gpt-6-astra", now) {
			t.Fatal("default outage cooldown did not apply")
		}
	})
	t.Run("zero remembers nothing", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusBadRequest, body: []byte(refusal)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 0\nmodel-outage-cooldown-minutes: 0\n"+modelYML)
		callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`)))
		if cooled("1", tokenA, "gpt-6-astra", now) {
			t.Fatal("outage 0 remembered the refusal")
		}
	})
	t.Run("above the maximum is clamped", func(t *testing.T) {
		setup(t)
		mustConfigure(t, "enabled: true\nmodel-outage-cooldown-minutes: 20000\n")
		cfg := currentConfig()
		if cfg.ModelOutageCooldownMinutes == nil || *cfg.ModelOutageCooldownMinutes != maxModelCooldownMinutes {
			t.Fatalf("clamped outage = %v", cfg.ModelOutageCooldownMinutes)
		}
	})
	t.Run("full cooldown zero disables the outage", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusBadRequest, body: []byte(refusal)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 0\nmodel-cooldown-minutes: 0\n"+modelYML)
		if got := currentConfig().modelOutageCooldown(); got != 0 {
			t.Fatalf("outage cooldown with full 0 = %s", got)
		}
		callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`)))
		if cooled("1", tokenA, "gpt-6-astra", now) {
			t.Fatal("full cooldown 0 remembered the refusal")
		}
	})
	t.Run("schema lists the field", func(t *testing.T) {
		setup(t)
		for _, field := range configFields() {
			if field.Name != "model-outage-cooldown-minutes" {
				continue
			}
			if field.Type != pluginapi.ConfigFieldTypeInteger || strings.TrimSpace(field.Description) == "" {
				t.Fatalf("field = %+v", field)
			}
			return
		}
		t.Fatal("model-outage-cooldown-minutes missing from the schema")
	})
}

func TestThinkingSuffixChatSetsReasoningEffort(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: primary-model
    alias: primary-model
`)
	mustExec(t, "openai", "primary-model(high)", []byte(`{"model":"primary-model(high)","messages":[{"role":"user","content":"hi"}]}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 || calls[0].URL != defaultBaseURL+"/chat/completions" {
		t.Fatalf("calls = %#v", calls)
	}
	var body struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "primary-model" || body.ReasoningEffort != "high" {
		t.Fatalf("body = %s", calls[0].Body)
	}
}

func TestThinkingSuffixResponsesSetsReasoningEffort(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: primary-model
    alias: primary-model
`)
	mustExec(t, "openai-response", "primary-model(high)", []byte(`{"model":"primary-model(high)","input":"hi"}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 || calls[0].URL != defaultBaseURL+"/responses" {
		t.Fatalf("calls = %#v", calls)
	}
	var body struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "primary-model" || body.Reasoning.Effort != "high" {
		t.Fatalf("body = %s", calls[0].Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(calls[0].Body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["reasoning_effort"]; ok {
		t.Fatalf("top-level reasoning_effort = %s", calls[0].Body)
	}
}

func TestThinkingSuffixOverridesBodyEffort(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: primary-model
    alias: primary-model
`)
	mustExec(t, "openai", "primary-model(high)", []byte(`{"model":"primary-model(high)","reasoning_effort":"low","messages":[]}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	var body struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "primary-model" || body.ReasoningEffort != "high" {
		t.Fatalf("body = %s", calls[0].Body)
	}
}

func TestThinkingBudgetSuffixUsesHostConversion(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: primary-model
    alias: primary-model
`)
	want, ok := thinking.ConvertBudgetToLevel(8192)
	if !ok {
		t.Fatal("thinking.ConvertBudgetToLevel(8192) rejected the budget")
	}
	mustExec(t, "openai", "primary-model(8192)", []byte(`{"model":"primary-model(8192)","messages":[]}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	var body struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "primary-model" || body.ReasoningEffort != want {
		t.Fatalf("body = %s, want reasoning_effort %q", calls[0].Body, want)
	}
}

func TestNoThinkingSuffixLeavesBodyUnchanged(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 0
models:
  - name: upstream-model
    alias: client-model
`)
	chat := []byte(`{"model":"client-model","reasoning_effort":"low","temperature":0.3,"messages":[{"role":"user","content":"hi"}]}`)
	_, start, end, errModel := extractModel(chat)
	if errModel != nil {
		t.Fatal(errModel)
	}
	wantChat, errRewrite := rewriteModel(chat, start, end, "upstream-model")
	if errRewrite != nil {
		t.Fatal(errRewrite)
	}
	mustExec(t, "openai", "client-model", chat)
	responses := []byte(`{"model":"client-model","reasoning":{"effort":"low"},"input":"hi","temperature":0.3}`)
	_, start, end, errModel = extractModel(responses)
	if errModel != nil {
		t.Fatal(errModel)
	}
	wantResponses, errRewrite := rewriteModel(responses, start, end, "upstream-model")
	if errRewrite != nil {
		t.Fatal(errRewrite)
	}
	mustExec(t, "openai-response", "client-model", responses)
	calls := f.httpCalls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if string(calls[0].Body) != string(wantChat) {
		t.Fatalf("chat body = %s, want %s", calls[0].Body, wantChat)
	}
	if string(calls[1].Body) != string(wantResponses) {
		t.Fatalf("responses body = %s, want %s", calls[1].Body, wantResponses)
	}
}

func TestThinkingSuffixFallbackSendsBareName(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusNotFound, body: []byte(`{"error":{"code":"model_not_found"}}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 0
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
	mustExec(t, "openai", "primary-model(high)", []byte(`{"model":"primary-model(high)","messages":[]}`))
	calls := f.httpCalls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	var body struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(calls[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "other-model" || body.ReasoningEffort != "high" {
		t.Fatalf("fallback body = %s", calls[1].Body)
	}
}

func TestSuffixLevelRejectedLocallyStillSent(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	const model = "gpt-6-astra(minimal)"
	payload := []byte(`{"model":"gpt-6-astra(minimal)","messages":[]}`)
	mustExec(t, "openai", model, payload)
	calls := f.httpCalls(t)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	bare, _ := splitSuffix(model)
	_, start, end, errModel := extractModel(payload)
	if errModel != nil {
		t.Fatal(errModel)
	}
	rewritten, errRewrite := rewriteModel(payload, start, end, bare)
	if errRewrite != nil {
		t.Fatal(errRewrite)
	}
	want, errApply := thinking.ApplyThinkingWithModelInfo(rewritten, nil, model, "openai", "openai", "openai", &registry.ModelInfo{ID: bare, UserDefined: true})
	if errApply != nil {
		t.Fatal(errApply)
	}
	if string(calls[0].Body) != string(want) {
		t.Fatalf("body = %s, want %s", calls[0].Body, want)
	}
}

func TestUpstreamUnsupportedEffortRetriesClamped(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	const unsupported = `{"error":{"message":"Unsupported value: 'minimal' is not supported with the 'gpt-6-astra' model. Supported values are: 'low', 'medium', 'high', 'xhigh', and 'max'.","type":"invalid_request_error","param":"reasoning.effort","code":"unsupported_value"}}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(unsupported)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: primary-model
    alias: primary-model
`)
	payload := []byte(`{"model":"primary-model","reasoning_effort":"minimal","messages":[{"role":"user","content":"hi"}]}`)
	resp := mustExec(t, "openai", "primary-model", payload)
	calls := f.httpCalls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenA {
		t.Fatalf("bearers = %s %s", bearer(t, calls[0]), bearer(t, calls[1]))
	}
	_, start, end, errModel := extractModel(payload)
	if errModel != nil {
		t.Fatal(errModel)
	}
	today, errRewrite := rewriteModel(payload, start, end, "primary-model")
	if errRewrite != nil {
		t.Fatal(errRewrite)
	}
	if string(calls[0].Body) != string(today) {
		t.Fatalf("first body = %s, want %s", calls[0].Body, today)
	}
	var retry struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(calls[1].Body, &retry); err != nil {
		t.Fatal(err)
	}
	if retry.Model != "primary-model" || retry.ReasoningEffort != "low" {
		t.Fatalf("retry body = %s", calls[1].Body)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("payload = %s", resp.Payload)
	}
	if gotIndex, gotToken := penaltyCounts(); gotIndex+gotToken != 0 {
		t.Fatalf("penalties = %d/%d", gotIndex, gotToken)
	}
	if cooledAny("1", tokenA, now) {
		t.Fatal("effort retry cooled the account")
	}
}

func TestUpstreamInvalidEffortRetriesMedium(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	const invalid = `{
  "error": {
    "message": "Invalid value: 'auto'. Supported values are: 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', and 'max'.",
    "type": "invalid_request_error",
    "param": "reasoning.effort",
    "code": "invalid_value"
  }
}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(invalid)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: primary-model
    alias: primary-model
`)
	mustExec(t, "openai-response", "primary-model", []byte(`{"model":"primary-model","reasoning":{"effort":"auto","summary":"auto"},"input":"hi"}`))
	calls := f.httpCalls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	var retry struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort  string `json:"effort"`
			Summary string `json:"summary"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(calls[1].Body, &retry); err != nil {
		t.Fatal(err)
	}
	if retry.Model != "primary-model" || retry.Reasoning.Effort != "medium" || retry.Reasoning.Summary != "auto" {
		t.Fatalf("retry body = %s", calls[1].Body)
	}
}

func TestEffortRetryOnlyOnce(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	const unsupported = `{"error":{"message":"Unsupported value: 'minimal' is not supported with the 'primary-model' model. Supported values are: 'low', 'medium', 'high', 'xhigh', and 'max'.","type":"invalid_request_error","param":"reasoning.effort","code":"unsupported_value"}}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(unsupported)},
		{status: http.StatusBadRequest, body: []byte(unsupported)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
	env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "primary-model", []byte(`{"model":"primary-model","reasoning_effort":"minimal","messages":[]}`)))
	calls := f.httpCalls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadRequest || env.Error.Message != unsupported {
		t.Fatalf("client error = %+v", env.Error)
	}
	for _, call := range calls {
		if requestModel(t, call) != "primary-model" {
			t.Fatalf("fallback ran: %s", call.Body)
		}
	}
	if gotIndex, gotToken := penaltyCounts(); gotIndex+gotToken != 0 {
		t.Fatalf("penalties = %d/%d", gotIndex, gotToken)
	}
	if cooledAny("1", tokenA, now) {
		t.Fatal("rejected effort cooled the account")
	}
}

func TestThinkingSuffixStreamPath(t *testing.T) {
	f := setup(t)
	f.streamFinished = make(chan struct{})
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, streamID: "http-stream", chunks: []readChunk{{payload: []byte("data: {\"ok\":true}\n"), done: true}}}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: primary-model
    alias: primary-model
`)
	req := execReq("openai", "primary-model(high)", []byte(`{"model":"primary-model(high)","stream":true,"messages":[]}`))
	req.StreamID = "exec-stream"
	callOK(t, pluginabi.MethodExecutorExecuteStream, req)
	waitCh(t, f.streamFinished, "thinking stream close")
	calls := f.httpCalls(t)
	if len(calls) != 1 || calls[0].URL != defaultBaseURL+"/chat/completions" {
		t.Fatalf("calls = %#v", calls)
	}
	var body struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
		Stream          bool   `json:"stream"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "primary-model" || body.ReasoningEffort != "high" || !body.Stream {
		t.Fatalf("body = %s", calls[0].Body)
	}
}

func TestThinkingSuffixAutoAndNoneUseHostOutput(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	expected := func(payload []byte, model, format string) []byte {
		t.Helper()
		bare, suffix := splitSuffix(model)
		_, start, end, errModel := extractModel(payload)
		if errModel != nil {
			t.Fatal(errModel)
		}
		body, errRewrite := rewriteModel(payload, start, end, bare)
		if errRewrite != nil {
			t.Fatal(errRewrite)
		}
		if suffix == "" {
			return body
		}
		applied, errApply := thinking.ApplyThinking(body, model, format, format, format)
		if errApply != nil {
			applied, errApply = thinking.ApplyThinkingWithModelInfo(body, nil, model, format, format, format, &registry.ModelInfo{ID: bare, UserDefined: true})
			if errApply != nil {
				t.Fatal(errApply)
			}
		}
		return applied
	}
	cases := []struct {
		format  string
		model   string
		payload []byte
	}{
		{"openai", "gpt-6-astra(auto)", []byte(`{"model":"gpt-6-astra(auto)","messages":[]}`)},
		{"openai", "gpt-6-astra(none)", []byte(`{"model":"gpt-6-astra(none)","messages":[]}`)},
		{"openai-response", "gpt-6-astra(auto)", []byte(`{"model":"gpt-6-astra(auto)","input":"hi"}`)},
		{"openai-response", "gpt-6-astra(none)", []byte(`{"model":"gpt-6-astra(none)","input":"hi"}`)},
	}
	for i, tc := range cases {
		mustExec(t, tc.format, tc.model, tc.payload)
		calls := f.httpCalls(t)
		if len(calls) != i+1 {
			t.Fatalf("case %d calls = %d", i, len(calls))
		}
		format := "openai"
		if tc.format == "openai-response" {
			format = "codex"
		}
		want := expected(tc.payload, tc.model, format)
		if string(calls[i].Body) != string(want) {
			t.Fatalf("case %d body = %s, want %s", i, calls[i].Body, want)
		}
	}
}

func TestRateLimitDelay(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	header := func(value string) http.Header { return http.Header{"Retry-After": {value}} }
	cases := []struct {
		name   string
		header http.Header
		body   string
		want   time.Duration
	}{
		{"resets_in_seconds", nil, `{"error":{"resets_in_seconds":120,"resets_at":1}}`, 2 * time.Minute},
		{"resets_at", nil, `{"error":{"resets_at":` + strconv.FormatInt(now.Add(5*time.Minute).Unix(), 10) + `}}`, 5 * time.Minute},
		{"retry-after seconds", header("90"), `{"error":"rate_limit"}`, 90 * time.Second},
		{"retry-after date", header(now.Add(time.Hour).Format(http.TimeFormat)), ``, time.Hour},
		{"body wins over header", header("90"), `{"error":{"resets_in_seconds":300}}`, 5 * time.Minute},
		{"default", nil, `{"error":"rate_limit"}`, 10 * time.Minute},
		{"clamp low", nil, `{"error":{"resets_in_seconds":5}}`, 30 * time.Second},
		{"clamp high", nil, `{"error":{"resets_in_seconds":99999999}}`, 7 * 24 * time.Hour},
		{"past reset clamps low", nil, `{"error":{"resets_at":` + strconv.FormatInt(now.Add(-time.Hour).Unix(), 10) + `}}`, 30 * time.Second},
	}
	for _, tc := range cases {
		if got := rateLimitDelay(tc.header, []byte(tc.body), now); got != tc.want {
			t.Fatalf("%s: delay = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSoftPenalty(t *testing.T) {
	start := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	const fill = "enabled: true\nplus-preference: off\nrotation-mode: fill-first\n"
	const model = "models:\n  - {name: gpt-5.4, alias: gpt-5.4}\n"

	t.Run("429 demotes the account until the reset", func(t *testing.T) {
		f := setup(t)
		now := start
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":{"resets_in_seconds":120}}`)}}
		mustConfigure(t, fill+"request-retry: 1\n"+model)
		execModel(t, "gpt-5.4")
		execModel(t, "gpt-5.4")
		if got := lastBearer(t, f); got != "Bearer "+tokenB {
			t.Fatalf("demoted account was first again: %s", got)
		}
		now = start.Add(121 * time.Second)
		execModel(t, "gpt-5.4")
		if got := lastBearer(t, f); got != "Bearer "+tokenA {
			t.Fatalf("penalty did not expire: %s", got)
		}
	})

	t.Run("401 penalizes the token and 402 403 the account", func(t *testing.T) {
		for _, tc := range []struct {
			status    int
			wantIndex int
			wantToken int
		}{{http.StatusUnauthorized, 0, 1}, {http.StatusPaymentRequired, 1, 0}, {http.StatusForbidden, 1, 0}} {
			f := setup(t)
			nowFn = func() time.Time { return start }
			f.codex("1", tokenA, "plus", nil)
			f.codex("2", tokenB, "plus", nil)
			f.scripts = []httpScript{{status: tc.status, body: []byte(`{"error":"denied"}`)}}
			mustConfigure(t, fill+"request-retry: 1\n"+model)
			execModel(t, "gpt-5.4")
			if gotIndex, gotToken := penaltyCounts(); gotIndex != tc.wantIndex || gotToken != tc.wantToken {
				t.Fatalf("status %d penalties = %d/%d", tc.status, gotIndex, gotToken)
			}
			execModel(t, "gpt-5.4")
			if got := lastBearer(t, f); got != "Bearer "+tokenB {
				t.Fatalf("status %d: demoted account came first: %s", tc.status, got)
			}
			runtimeMu.Lock()
			var until time.Time
			for _, p := range runtime.penaltyIndex {
				until = p.until
			}
			for _, p := range runtime.penaltyToken {
				until = p.until
			}
			runtimeMu.Unlock()
			if want := start.Add(10 * time.Minute); !until.Equal(want) {
				t.Fatalf("status %d penalty until %v, want %v", tc.status, until, want)
			}
		}
	})

	t.Run("5xx and transport errors carry no penalty", func(t *testing.T) {
		for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusRequestTimeout} {
			f := setup(t)
			f.codex("1", tokenA, "plus", nil)
			f.scripts = []httpScript{{status: status, body: []byte(`{"error":"down"}`)}}
			mustConfigure(t, fill+"request-retry: 0\n"+model)
			callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
			if gotIndex, gotToken := penaltyCounts(); gotIndex+gotToken != 0 {
				t.Fatalf("status %d left penalties %d/%d", status, gotIndex, gotToken)
			}
		}
	})

	t.Run("penalized accounts stay as a tail sorted by expiry", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.codex("3", "tok-c", "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusTooManyRequests, body: []byte(`{"error":{"resets_in_seconds":600}}`)},
			{status: http.StatusTooManyRequests, body: []byte(`{"error":{"resets_in_seconds":60}}`)},
			{status: http.StatusTooManyRequests, body: []byte(`{"error":{"resets_in_seconds":300}}`)},
		}
		mustConfigure(t, fill+"request-retry: 2\n"+model)
		callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if len(f.httpCalls(t)) != 3 {
			t.Fatalf("setup calls = %d", len(f.httpCalls(t)))
		}
		// Everything is penalized, so the next request still tries the soonest expiry first.
		execModel(t, "gpt-5.4")
		if got := lastBearer(t, f); got != "Bearer "+tokenB {
			t.Fatalf("tail order first = %s", got)
		}
	})

	t.Run("a success clears an older penalty", func(t *testing.T) {
		f := setup(t)
		now := start
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		mustConfigure(t, fill+"request-retry: 0\n"+model)
		penalizeAttempt(attempt{index: "1", token: tokenA}, http.StatusTooManyRequests, nil, nil, now)
		now = now.Add(time.Second)
		execModel(t, "gpt-5.4")
		if gotIndex, gotToken := penaltyCounts(); gotIndex+gotToken != 0 {
			t.Fatalf("penalty survived a success: %d/%d", gotIndex, gotToken)
		}
	})

	t.Run("a penalty set during the attempt survives and does not pin", func(t *testing.T) {
		f := setup(t)
		now := start
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", nil)
		previousHost := hostCallOverride
		hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
			if method == pluginabi.MethodHostHTTPDo {
				now = now.Add(time.Second)
				penalizeAttempt(attempt{index: "1", token: tokenA}, http.StatusTooManyRequests, nil, nil, now)
			}
			return previousHost(method, payload)
		}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 0\n"+model)
		execModel(t, "gpt-5.4")
		if gotIndex, _ := penaltyCounts(); gotIndex != 1 {
			t.Fatalf("newer penalty was cleared: %d", gotIndex)
		}
		runtimeMu.Lock()
		pinned := runtime.pinnedIndex
		runtimeMu.Unlock()
		if pinned != "" {
			t.Fatalf("penalized account was pinned: %q", pinned)
		}
	})

	t.Run("expired entries are pruned on write", func(t *testing.T) {
		setup(t)
		penalizeAttempt(attempt{index: "1", token: tokenA}, http.StatusForbidden, nil, nil, start)
		penalizeAttempt(attempt{index: "2", token: tokenB}, http.StatusUnauthorized, nil, nil, start)
		penalizeAttempt(attempt{index: "3", token: "tok-c"}, http.StatusForbidden, nil, nil, start.Add(time.Hour))
		runtimeMu.Lock()
		_, staleIndex := runtime.penaltyIndex["1"]
		_, staleToken := runtime.penaltyToken[tokenKey(tokenB)]
		runtimeMu.Unlock()
		if staleIndex || staleToken {
			t.Fatalf("expired penalties kept: index=%v token=%v", staleIndex, staleToken)
		}
	})

	t.Run("priority tiers apply after the penalty split", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 2 })
		f.codex("2", tokenB, "plus", nil)
		mustConfigure(t, fill+"request-retry: 0\n"+model)
		penalizeAttempt(attempt{index: "1", token: tokenA}, http.StatusTooManyRequests, nil, nil, start)
		execModel(t, "gpt-5.4")
		if got := lastBearer(t, f); got != "Bearer "+tokenB {
			t.Fatalf("penalized top tier was used first: %s", got)
		}
		// With both penalized, the tail keeps only the highest priority.
		penalizeAttempt(attempt{index: "2", token: tokenB}, http.StatusTooManyRequests, header429(30), nil, start)
		execModel(t, "gpt-5.4")
		if got := lastBearer(t, f); got != "Bearer "+tokenA {
			t.Fatalf("tail priority = %s", got)
		}
	})

	t.Run("priority tiers apply after the cooldown split", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 2 })
		f.codex("2", tokenB, "plus", nil)
		mustConfigure(t, fill+"request-retry: 0\n"+twoModels)
		markModelCooldown("1", tokenA, "model-a", time.Hour, "")
		execModel(t, "model-a")
		if got := lastBearer(t, f); got != "Bearer "+tokenB {
			t.Fatalf("cooled top tier was used: %s", got)
		}
		execModel(t, "model-b")
		if got := lastBearer(t, f); got != "Bearer "+tokenA {
			t.Fatalf("top tier unavailable for another model: %s", got)
		}
	})
}

func header429(seconds int) http.Header {
	return http.Header{"Retry-After": {strconv.Itoa(seconds)}}
}

func TestClientErrorsDoNotRotate(t *testing.T) {
	const model = "models:\n  - {name: gpt-5.4, alias: gpt-5.4}\n"
	for _, status := range []int{400, 404, 409, 413, 415, 422} {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{{status: status, body: []byte(`{"error":"bad request"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 3\n"+model)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || env.Error == nil || env.Error.HTTPStatus != status || len(f.httpCalls(t)) != 1 {
			t.Fatalf("status %d rotated: err=%+v calls=%d", status, env.Error, len(f.httpCalls(t)))
		}
		if gotIndex, gotToken := penaltyCounts(); gotIndex+gotToken != 0 {
			t.Fatalf("status %d penalized: %d/%d", status, gotIndex, gotToken)
		}
		runtimeMu.Lock()
		pinned, failed := runtime.pinnedIndex, runtime.pinFailed
		runtimeMu.Unlock()
		if pinned != "" || failed {
			t.Fatalf("status %d moved the pin: %q %v", status, pinned, failed)
		}
	}

	t.Run("model refusal bodies still rotate", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusNotFound, body: []byte(`{"error":{"code":"model_not_found"}}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: fill-first\nrequest-retry: 1\n"+model)
		execModel(t, "gpt-5.4")
		if len(f.httpCalls(t)) != 2 {
			t.Fatalf("model_not_found did not rotate: %d", len(f.httpCalls(t)))
		}
	})
}

func TestNoRepeatAfterAccountFailure(t *testing.T) {
	const model = "models:\n  - {name: gpt-5.4, alias: gpt-5.4}\n"
	const unsupported = `{"detail":"The 'gpt-5.4' model is not supported when using Codex with a ChatGPT account."}`
	run := func(t *testing.T, status int, body, extra string) int {
		t.Helper()
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: status, body: []byte(body)}, {status: status, body: []byte(body)}, {status: status, body: []byte(body)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 2\n"+extra+model)
		callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		return len(f.httpCalls(t))
	}
	for _, status := range []int{401, 402, 403, 429} {
		if got := run(t, status, `{"error":"denied"}`, ""); got != 1 {
			t.Fatalf("status %d repeated the account: %d calls", status, got)
		}
	}
	if got := run(t, 400, unsupported, "model-cooldown-minutes: 0\n"); got != 1 {
		t.Fatalf("model refusal repeated the account: %d calls", got)
	}
	for _, status := range []int{500, 502, 503} {
		if got := run(t, status, `{"error":"down"}`, ""); got != 3 {
			t.Fatalf("status %d should repeat the account: %d calls", status, got)
		}
	}
}

func TestRoundRobinSuccessor(t *testing.T) {
	const yml = "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n"
	pin := func(index string, failed bool) {
		runtimeMu.Lock()
		runtime.pinnedIndex, runtime.pinFailed = index, failed
		runtimeMu.Unlock()
	}
	setupThree := func(t *testing.T) *fakeHost {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.codex("3", "tok-c", "plus", nil)
		mustConfigure(t, yml)
		return f
	}
	t.Run("a missing pinned account hands over to the next index", func(t *testing.T) {
		f := setupThree(t)
		now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		penalizeAttempt(attempt{index: "2", token: tokenB}, http.StatusForbidden, nil, nil, now)
		pin("2", false)
		execModel(t, "gpt-5.4")
		if got := lastBearer(t, f); got != "Bearer tok-c" {
			t.Fatalf("successor = %s", got)
		}
	})
	t.Run("the successor wraps to the first account", func(t *testing.T) {
		f := setupThree(t)
		now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		penalizeAttempt(attempt{index: "3", token: "tok-c"}, http.StatusForbidden, nil, nil, now)
		pin("3", true)
		execModel(t, "gpt-5.4")
		if got := lastBearer(t, f); got != "Bearer "+tokenA {
			t.Fatalf("wrap = %s", got)
		}
	})
	t.Run("a failed pin in the pool moves to the next account", func(t *testing.T) {
		f := setupThree(t)
		pin("1", true)
		execModel(t, "gpt-5.4")
		if got := lastBearer(t, f); got != "Bearer "+tokenB {
			t.Fatalf("failed pin successor = %s", got)
		}
	})
}

func TestLeastRecentIsLRU(t *testing.T) {
	f := setup(t)
	tick := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}
	f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) })
	f.codex("2", tokenB, "plus", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) })
	f.codex("3", "tok-c", "plus", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
	mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: least-recent\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	var got []string
	for i := 0; i < 5; i++ {
		execModel(t, "gpt-5.4")
		got = append(got, strings.TrimPrefix(lastBearer(t, f), "Bearer "))
	}
	// Never-used accounts go first by UpdatedAt, then the oldest use.
	want := []string{"tok-c", tokenB, tokenA, "tok-c", tokenB}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lru order = %v, want %v", got, want)
	}
}

func TestLeastRecentSpreadsConcurrentPlans(t *testing.T) {
	setup(t)
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	accounts := []account{{index: "1"}, {index: "2"}, {index: "3"}}
	cfg := defaultPluginConfig()
	cfg.RotationMode = "least-recent"
	runtimeMu.Lock()
	first := planAccountsLocked(cfg, accounts, nil, "", now)
	second := planAccountsLocked(cfg, accounts, nil, "", now)
	third := planAccountsLocked(cfg, accounts, nil, "", now)
	runtimeMu.Unlock()
	if first[0].index == second[0].index || second[0].index == third[0].index || first[0].index == third[0].index {
		t.Fatalf("plans did not spread: %s %s %s", first[0].index, second[0].index, third[0].index)
	}
}

func TestStickyPrunesExpiredBinds(t *testing.T) {
	setup(t)
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	bindSticky("old-1", "1", 60)
	bindSticky("old-2", "2", 60)
	now = now.Add(2 * time.Minute)
	bindSticky("fresh", "1", 60)
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if len(runtime.sticky) != 1 {
		t.Fatalf("sticky binds = %d, want 1", len(runtime.sticky))
	}
	if _, ok := runtime.sticky["fresh"]; !ok {
		t.Fatal("fresh bind missing")
	}
}

func TestPriorityTiersDoNotLeakIntoTails(t *testing.T) {
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	const fill = "enabled: true\nplus-preference: off\nrotation-mode: fill-first\n"

	t.Run("penalized tail stays at or above the healthy tier", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("a", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 10 })
		f.codex("b", tokenB, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 1 })
		f.codex("c", "tok-c", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 1 })
		down := httpScript{status: http.StatusBadGateway, body: []byte(`{"error":"down"}`)}
		f.scripts = []httpScript{down, down, down, down}
		mustConfigure(t, fill+"request-retry: 3\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		penalizeAttempt(attempt{index: "b", token: tokenB}, http.StatusForbidden, nil, nil, start)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK {
			t.Fatal("expected failure after 5xx")
		}
		for _, call := range f.httpCalls(t) {
			if got := bearer(t, call); got != "Bearer "+tokenA {
				t.Fatalf("lower tier leaked into the order: %s", got)
			}
		}
	})

	t.Run("fallback cooling accounts stay at or above the ordered tier", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("a", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 10 })
		f.codex("b", tokenB, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 1 })
		f.scripts = []httpScript{
			{status: http.StatusNotFound, body: []byte(`{"error":{"code":"model_not_found"}}`)},
			{status: http.StatusNotFound, body: []byte(`{"error":{"code":"model_not_found"}}`)},
		}
		mustConfigure(t, fill+"request-retry: 0\nfallback-retry: 3\nmodels:\n  - {name: primary-model, alias: primary-model, fallback: [other-model]}\n")
		// Account b is cooled for the primary model; a serves it and sits in a higher tier.
		markModelCooldown("b", tokenB, "primary-model", time.Hour, "")
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "primary-model", []byte(`{"model":"primary-model"}`)))
		if env.OK {
			t.Fatal("expected failure")
		}
		for _, call := range f.httpCalls(t) {
			if got := bearer(t, call); got != "Bearer "+tokenA {
				t.Fatalf("lower-tier cooling account joined the fallback pool: %s", got)
			}
		}
	})

	t.Run("an all-penalized or all-cooled pool keeps its own top tier", func(t *testing.T) {
		pool := []account{{index: "a", priority: 3}, {index: "b", priority: 1}}
		if got := priorityAtLeast(pool, nil); len(got) != 1 || got[0].index != "a" {
			t.Fatalf("empty anchor = %+v", got)
		}
		if got := priorityAtLeast(pool, []account{{index: "x", priority: 1}}); len(got) != 2 {
			t.Fatalf("anchor at tier 1 = %+v", got)
		}
	})
}

func promptCacheKeyOK(t *testing.T, key string) {
	t.Helper()
	raw, errDecode := hex.DecodeString(key)
	if errDecode != nil || len(raw) != 16 || key != strings.ToLower(key) {
		t.Fatalf("prompt_cache_key = %q, want 32 lowercase hex characters", key)
	}
}

func TestDisablePromptCacheOffKeepsBody(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: upstream-model
    alias: client-model
`)
	chat := []byte(`{"model":"client-model","prompt_cache_key":"client-key","messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`)
	_, start, end, errModel := extractModel(chat)
	if errModel != nil {
		t.Fatal(errModel)
	}
	wantChat, errRewrite := rewriteModel(chat, start, end, "upstream-model")
	if errRewrite != nil {
		t.Fatal(errRewrite)
	}
	mustExec(t, "openai", "client-model", chat)
	responses := []byte(`{"model":"client-model","prompt_cache_key":"client-key","instructions":"base","input":"hi"}`)
	_, start, end, errModel = extractModel(responses)
	if errModel != nil {
		t.Fatal(errModel)
	}
	wantResponses, errRewrite := rewriteModel(responses, start, end, "upstream-model")
	if errRewrite != nil {
		t.Fatal(errRewrite)
	}
	mustExec(t, "openai-response", "client-model", responses)
	calls := f.httpCalls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if !bytes.Equal(calls[0].Body, wantChat) {
		t.Fatalf("chat body = %s, want %s", calls[0].Body, wantChat)
	}
	if !bytes.Equal(calls[1].Body, wantResponses) {
		t.Fatalf("responses body = %s, want %s", calls[1].Body, wantResponses)
	}
	if got := gjson.GetBytes(calls[0].Body, "prompt_cache_key").String(); got != "client-key" {
		t.Fatalf("client key = %q, want client-key", got)
	}
}

func TestDisablePromptCacheResponsesOverwritesKey(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 0
disable-prompt-cache: true
models:
  - name: upstream-model
    alias: upstream-model
`)
	const instructions = "You are a helpful assistant."
	payload := []byte(`{"model":"upstream-model","prompt_cache_key":"client-key","instructions":"You are a helpful assistant.","input":"hi"}`)
	mustExec(t, "openai-response", "upstream-model", payload)
	mustExec(t, "openai-response", "upstream-model", payload)
	calls := f.httpCalls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	keys := make([]string, 0, len(calls))
	for _, call := range calls {
		var body struct {
			PromptCacheKey string `json:"prompt_cache_key"`
			Instructions   string `json:"instructions"`
		}
		if err := json.Unmarshal(call.Body, &body); err != nil {
			t.Fatal(err)
		}
		promptCacheKeyOK(t, body.PromptCacheKey)
		if body.PromptCacheKey == "client-key" {
			t.Fatalf("client key kept: %s", call.Body)
		}
		if want := instructions + "\n\n[request " + body.PromptCacheKey + "]"; body.Instructions != want {
			t.Fatalf("instructions = %q, want %q", body.Instructions, want)
		}
		keys = append(keys, body.PromptCacheKey)
	}
	if keys[0] == keys[1] {
		t.Fatalf("both attempts share one key: %s", keys[0])
	}
}

func TestDisablePromptCacheResponsesWithoutInstructions(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
disable-prompt-cache: true
models:
  - name: upstream-model
    alias: upstream-model
`)
		mustExec(t, "openai-response", "upstream-model", []byte(`{"model":"upstream-model","input":"hi"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		key := gjson.GetBytes(calls[0].Body, "prompt_cache_key").String()
		promptCacheKeyOK(t, key)
		if want := "[request " + key + "]"; gjson.GetBytes(calls[0].Body, "instructions").String() != want {
			t.Fatalf("body = %s, want instructions %q", calls[0].Body, want)
		}
	})
	t.Run("empty string", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
disable-prompt-cache: true
models:
  - name: upstream-model
    alias: upstream-model
`)
		mustExec(t, "openai-response", "upstream-model", []byte(`{"model":"upstream-model","instructions":"","input":"hi"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		key := gjson.GetBytes(calls[0].Body, "prompt_cache_key").String()
		promptCacheKeyOK(t, key)
		if want := "[request " + key + "]"; gjson.GetBytes(calls[0].Body, "instructions").String() != want {
			t.Fatalf("body = %s, want instructions %q", calls[0].Body, want)
		}
	})
	t.Run("non-string instructions are left alone", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
disable-prompt-cache: true
models:
  - name: upstream-model
    alias: upstream-model
`)
		mustExec(t, "openai-response", "upstream-model", []byte(`{"model":"upstream-model","instructions":["a","b"],"input":"hi"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		promptCacheKeyOK(t, gjson.GetBytes(calls[0].Body, "prompt_cache_key").String())
		if got := gjson.GetBytes(calls[0].Body, "instructions").Raw; got != `["a","b"]` {
			t.Fatalf("instructions = %s", got)
		}
	})
}

func TestDisablePromptCacheChatSystemMessage(t *testing.T) {
	config := `
enabled: true
plus-preference: off
request-retry: 0
disable-prompt-cache: true
models:
  - name: upstream-model
    alias: upstream-model
`
	t.Run("system message gets the tag", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, config)
		mustExec(t, "openai", "upstream-model", []byte(`{"model":"upstream-model","messages":[{"role":"user","content":"first"},{"role":"system","content":"base"},{"role":"user","content":"last"}]}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		key := gjson.GetBytes(calls[0].Body, "prompt_cache_key").String()
		promptCacheKeyOK(t, key)
		want := "base\n\n[request " + key + "]"
		if got := gjson.GetBytes(calls[0].Body, "messages.1.content").String(); got != want {
			t.Fatalf("system content = %q, want %q", got, want)
		}
		if got := gjson.GetBytes(calls[0].Body, "messages.0.content").String(); got != "first" {
			t.Fatalf("user content = %q", got)
		}
		if got := gjson.GetBytes(calls[0].Body, "messages.2.content").String(); got != "last" {
			t.Fatalf("user content = %q", got)
		}
	})
	t.Run("developer message gets the tag", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, config)
		mustExec(t, "openai", "upstream-model", []byte(`{"model":"upstream-model","messages":[{"role":"developer","content":"base"}]}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		key := gjson.GetBytes(calls[0].Body, "prompt_cache_key").String()
		promptCacheKeyOK(t, key)
		if want := "base\n\n[request " + key + "]"; gjson.GetBytes(calls[0].Body, "messages.0.content").String() != want {
			t.Fatalf("body = %s, want %q", calls[0].Body, want)
		}
	})
	t.Run("no system message keeps messages", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, config)
		mustExec(t, "openai", "upstream-model", []byte(`{"model":"upstream-model","messages":[{"role":"user","content":"hi"}]}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		key := gjson.GetBytes(calls[0].Body, "prompt_cache_key").String()
		promptCacheKeyOK(t, key)
		if got := gjson.GetBytes(calls[0].Body, "messages").Raw; got != `[{"role":"user","content":"hi"}]` {
			t.Fatalf("messages = %s", got)
		}
	})
	t.Run("array system content is left alone", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, config)
		mustExec(t, "openai", "upstream-model", []byte(`{"model":"upstream-model","messages":[{"role":"system","content":[{"type":"text","text":"base"}]}]}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		promptCacheKeyOK(t, gjson.GetBytes(calls[0].Body, "prompt_cache_key").String())
		if got := gjson.GetBytes(calls[0].Body, "messages.0.content.#").Int(); got != 1 {
			t.Fatalf("array content changed: %s", calls[0].Body)
		}
	})
}

func TestDisablePromptCacheConfig(t *testing.T) {
	t.Run("default is false", func(t *testing.T) {
		setup(t)
		if currentConfig().DisablePromptCache {
			t.Fatal("default enabled the prompt cache miss")
		}
	})
	t.Run("yaml decodes and reloads", func(t *testing.T) {
		setup(t)
		mustConfigure(t, "enabled: true\ndisable-prompt-cache: true\nmodels:\n  - {name: upstream-model, alias: upstream-model}\n")
		if !currentConfig().DisablePromptCache {
			t.Fatal("disable-prompt-cache: true did not apply")
		}
		mustConfigure(t, "enabled: true\ndisable-prompt-cache: false\nmodels:\n  - {name: upstream-model, alias: upstream-model}\n")
		if currentConfig().DisablePromptCache {
			t.Fatal("disable-prompt-cache: false did not apply")
		}
	})
	t.Run("schema lists the field", func(t *testing.T) {
		for _, field := range configFields() {
			if field.Name != "disable-prompt-cache" {
				continue
			}
			if field.Type != pluginapi.ConfigFieldTypeBoolean || strings.TrimSpace(field.Description) == "" {
				t.Fatalf("field = %+v", field)
			}
			return
		}
		t.Fatal("disable-prompt-cache missing from the schema")
	})
}

func TestDisablePromptCacheThinkingSuffix(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
disable-prompt-cache: true
models:
  - name: primary-model
    alias: primary-model
`)
	mustExec(t, "openai", "primary-model(high)", []byte(`{"model":"primary-model(high)","messages":[{"role":"user","content":"hi"}]}`))
	calls := f.httpCalls(t)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	var body struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
		PromptCacheKey  string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	promptCacheKeyOK(t, body.PromptCacheKey)
	if body.Model != "primary-model" || body.ReasoningEffort != "high" {
		t.Fatalf("body = %s", calls[0].Body)
	}
}

func TestDisablePromptCacheFreshPerAttempt(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusInternalServerError, body: []byte(`{"error":{"message":"boom"}}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
disable-prompt-cache: true
models:
  - name: upstream-model
    alias: upstream-model
`)
	mustExec(t, "openai", "upstream-model", []byte(`{"model":"upstream-model","messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`))
	calls := f.httpCalls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if bearer(t, calls[0]) == bearer(t, calls[1]) {
		t.Fatalf("both attempts used one account: %s", bearer(t, calls[0]))
	}
	first := gjson.GetBytes(calls[0].Body, "prompt_cache_key").String()
	second := gjson.GetBytes(calls[1].Body, "prompt_cache_key").String()
	promptCacheKeyOK(t, first)
	promptCacheKeyOK(t, second)
	if first == second {
		t.Fatalf("both attempts share one key: %s", first)
	}
	if want := "base\n\n[request " + first + "]"; gjson.GetBytes(calls[0].Body, "messages.0.content").String() != want {
		t.Fatalf("first body = %s, want %q", calls[0].Body, want)
	}
	if want := "base\n\n[request " + second + "]"; gjson.GetBytes(calls[1].Body, "messages.0.content").String() != want {
		t.Fatalf("second body = %s, want %q", calls[1].Body, want)
	}
}

func TestServiceTierForced(t *testing.T) {
	const cfgYAML = `
enabled: true
plus-preference: off
request-retry: 0
service-tier: priority
models:
  - name: upstream-model
    alias: client-model
`
	cases := []struct {
		name    string
		format  string
		payload string
		want    string
	}{
		{"responses without tier", "openai-response", `{"model":"client-model","input":"hi"}`, "priority"},
		{"chat without tier", "openai", `{"model":"client-model","messages":[{"role":"user","content":"hi"}]}`, "priority"},
		{"auto is replaced", "openai-response", `{"model":"client-model","service_tier":"auto","input":"hi"}`, "priority"},
		{"default is replaced", "openai-response", `{"model":"client-model","service_tier":"default","input":"hi"}`, "priority"},
		{"null is replaced", "openai-response", `{"model":"client-model","service_tier":null,"input":"hi"}`, "priority"},
		{"client ultrafast is kept", "openai-response", `{"model":"client-model","service_tier":"ultrafast","input":"hi"}`, "ultrafast"},
		{"client flex is kept", "openai", `{"model":"client-model","service_tier":"flex","messages":[{"role":"user","content":"hi"}]}`, "flex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.codex("1", tokenA, "plus", nil)
			f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
			mustConfigure(t, cfgYAML)
			mustExec(t, tc.format, "client-model", []byte(tc.payload))
			calls := f.httpCalls(t)
			if len(calls) != 1 {
				t.Fatalf("calls = %d", len(calls))
			}
			if got := gjson.GetBytes(calls[0].Body, "service_tier").String(); got != tc.want {
				t.Fatalf("service_tier = %q, want %q; body %s", got, tc.want, calls[0].Body)
			}
			if requestModel(t, calls[0]) != "upstream-model" {
				t.Fatalf("model not rewritten: %s", calls[0].Body)
			}
		})
	}
}

func TestServiceTierUnsetKeepsBody(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"ok":true}`)}}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
models:
  - name: upstream-model
    alias: upstream-model
`)
	payload := []byte(`{"model":"upstream-model","input":"hi"}`)
	mustExec(t, "openai-response", "upstream-model", payload)
	calls := f.httpCalls(t)
	if len(calls) != 1 || !bytes.Equal(calls[0].Body, payload) {
		t.Fatalf("body changed without service-tier: %s", calls[0].Body)
	}
}

func TestServiceTierPerModel(t *testing.T) {
	const unsupported = `{"detail":"The 'requested-model' MODEL IS NOT SUPPORTED when using Codex with a ChatGPT account."}`
	t.Run("model tier overrides and off disables", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
service-tier: priority
models:
  - {name: fast-model, alias: fast-model, service-tier: ultrafast}
  - {name: slow-model, alias: slow-model, service-tier: "off"}
  - {name: plain-model, alias: plain-model}
`)
		mustExec(t, "openai-response", "fast-model", []byte(`{"model":"fast-model","input":"hi"}`))
		mustExec(t, "openai-response", "slow-model", []byte(`{"model":"slow-model","input":"hi"}`))
		mustExec(t, "openai-response", "plain-model(high)", []byte(`{"model":"plain-model(high)","input":"hi"}`))
		calls := f.httpCalls(t)
		if len(calls) != 3 {
			t.Fatalf("calls = %d", len(calls))
		}
		if got := gjson.GetBytes(calls[0].Body, "service_tier").String(); got != "ultrafast" {
			t.Fatalf("fast-model tier = %q", got)
		}
		if gjson.GetBytes(calls[1].Body, "service_tier").Exists() {
			t.Fatalf("slow-model tier set: %s", calls[1].Body)
		}
		if got := gjson.GetBytes(calls[2].Body, "service_tier").String(); got != "priority" {
			t.Fatalf("suffixed plain-model tier = %q; body %s", got, calls[2].Body)
		}
	})
	t.Run("fallback model uses its own tier", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadRequest, body: []byte(unsupported)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
fallback-retry: 1
models:
  - {name: primary-model, alias: primary-model, service-tier: ultrafast, fallback: [backup-model]}
  - {name: backup-model, alias: backup-model, service-tier: priority}
`)
		mustExec(t, "openai-response", "primary-model", []byte(`{"model":"primary-model","input":"hi"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 {
			t.Fatalf("calls = %d", len(calls))
		}
		if requestModel(t, calls[0]) != "primary-model" || gjson.GetBytes(calls[0].Body, "service_tier").String() != "ultrafast" {
			t.Fatalf("primary body = %s", calls[0].Body)
		}
		if requestModel(t, calls[1]) != "backup-model" || gjson.GetBytes(calls[1].Body, "service_tier").String() != "priority" {
			t.Fatalf("fallback body = %s", calls[1].Body)
		}
	})
}

func TestServiceTierConfig(t *testing.T) {
	t.Run("fast is stored as priority", func(t *testing.T) {
		setup(t)
		mustConfigure(t, "enabled: true\nservice-tier: FAST\nmodels:\n  - {name: m, alias: m, service-tier: Ultrafast}\n")
		cfg := currentConfig()
		if cfg.ServiceTier != "priority" || cfg.Models[0].ServiceTier != "ultrafast" {
			t.Fatalf("tiers = %q %q", cfg.ServiceTier, cfg.Models[0].ServiceTier)
		}
	})
	t.Run("invalid values are rejected", func(t *testing.T) {
		for _, yml := range []string{
			"enabled: true\nservice-tier: turbo\n",
			"enabled: true\nservice-tier: \"off\"\n",
			"enabled: true\nmodels:\n  - {name: m, alias: m, service-tier: turbo}\n",
		} {
			if _, err := decodeConfig([]byte(yml)); err == nil {
				t.Fatalf("accepted %q", yml)
			}
		}
	})
	t.Run("schema lists the field", func(t *testing.T) {
		for _, field := range configFields() {
			if field.Name == "service-tier" {
				if field.Type != pluginapi.ConfigFieldTypeEnum || len(field.EnumValues) != 3 {
					t.Fatalf("field = %+v", field)
				}
				return
			}
		}
		t.Fatal("service-tier missing from the schema")
	})
}
